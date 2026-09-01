package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/agent/einoadapter"
	"github.com/imbrooklyn/kupilot/internal/application"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/security"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
	"github.com/imbrooklyn/kupilot/internal/tools"
	"github.com/imbrooklyn/kupilot/internal/tui"
)

const (
	integrationEvidenceID1 domain.EvidenceID = "00000000-0000-7000-8000-000000000701"
	integrationEvidenceID2 domain.EvidenceID = "00000000-0000-7000-8000-000000000702"
	integrationEvidenceID3 domain.EvidenceID = "00000000-0000-7000-8000-000000000703"
)

func TestNewSessionQuestionPersistsToolEvidenceAndDiagnosis(t *testing.T) {
	canary := "integration-canary-value-1234567890"
	temporaryRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks() error = %v", err)
	}
	stateDirectory := filepath.Join(temporaryRoot, "state")
	database, err := sqlite.Open(context.Background(), sqlite.OpenOptions{
		StateDir: stateDirectory, ApplicationVersion: "integration-test", CorrelationID: "integration",
	})
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	databaseClosed := false
	defer func() {
		if !databaseClosed {
			_ = database.Close()
		}
	}()

	sessionRepository := sqlite.NewSessionRepository(database)
	runRepository := sqlite.NewAgentRunRepository(database)
	messageRepository := sqlite.NewMessageRepository(database)
	toolRepository := sqlite.NewToolInvocationRepository(database)
	evidenceRepository := sqlite.NewEvidenceRepository(database)
	diagnosisRepository := sqlite.NewDiagnosisRepository(database)
	auditRepository := sqlite.NewAuditRepository(database)

	clock := newIntegrationClock()
	privacyManager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: sqlite.NewPrivacyRepository(database), Origin: "https://model.example", Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	identifiers := newIntegrationIDs()
	scope := newIntegrationScope()
	kubernetes := &integrationKube{canary: canary}
	redactor := security.NewRedactor()
	toolHandlers, err := tools.NewReadOnlyToolCatalog(tools.ReadOnlyToolCatalogDependencies{
		Resources: tools.ResourceToolDependencies{
			Reader: kubernetes, ScopeGuard: scope, EvidenceIDs: identifiers, Text: redactor, Now: clock.Now,
		},
		Events: tools.EventToolDependencies{
			Reader: kubernetes, ScopeGuard: scope, EvidenceIDs: identifiers, Text: redactor, Now: clock.Now,
		},
		Logs: tools.LogToolDependencies{
			Reader: kubernetes, ScopeGuard: scope, EvidenceIDs: identifiers, Text: redactor,
			Policy: deniedIntegrationLogPolicy{}, Now: clock.Now,
		},
		Related: tools.RelatedToolDependencies{
			Reader: kubernetes, ScopeGuard: scope, EvidenceIDs: identifiers, Text: redactor, Now: clock.Now,
		},
	})
	if err != nil {
		t.Fatalf("NewReadOnlyToolCatalog() error = %v", err)
	}
	model := &integrationModel{diagnosis: integrationDiagnosisJSON(integrationEvidenceID1)}
	modelServer := httptest.NewServer(model)
	defer modelServer.Close()
	modelCredential, err := config.NewSecretValue("integration-model-credential-8401")
	if err != nil {
		t.Fatalf("config.NewSecretValue() error = %v", err)
	}
	agentAdapter, err := einoadapter.New(einoadapter.Config{
		ModelConfiguration: domain.ModelConfiguration{
			ProviderKind:        domain.ModelProviderOpenAICompatible,
			Endpoint:            modelServer.URL + "/v1",
			Origin:              modelServer.URL,
			Model:               "integration-model",
			APIKeySource:        domain.ModelAPIKeySourceRuntime,
			Temperature:         0.1,
			MaxOutputTokens:     2048,
			RequestTimeout:      time.Second,
			StreamingRequired:   true,
			ToolCallingRequired: true,
			TransportPolicy:     domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
		},
		Credential:  &modelCredential,
		Tools:       toolHandlers,
		ScopeGuard:  scope,
		Identifiers: identifiers,
		Now:         clock.Now,
	})
	if err != nil {
		modelCredential.Destroy()
		t.Fatalf("einoadapter.New() error = %v", err)
	}
	defer agentAdapter.Close()
	uiEvents := newIntegrationUIEvents()
	observer := newIntegrationObserver()
	coordinator, err := application.NewCoordinator(application.CoordinatorConfig{
		Sessions: sessionRepository, Runs: runRepository, Tools: toolRepository,
		Audits: auditRepository, Scope: scope,
		Runner: agentAdapter, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: redactor, Privacy: privacyManager, UIEvents: uiEvents, Observer: observer, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	defer func() { _ = coordinator.Shutdown(context.Background()) }()

	session, err := coordinator.CreateSession(context.Background(), application.CreateSessionCommand{
		PrivacyMode: domain.PrivacyModeStandard,
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	blockedCommand := application.StartRunCommand{
		SessionID: session.ID, Question: "Why is the selected Pod not Ready? token=" + canary,
		Resource: &domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
		},
	}
	if _, err := coordinator.StartRun(context.Background(), blockedCommand); !errors.Is(err, application.ErrConsentRequired) {
		t.Fatalf("StartRun(before consent) error = %v", err)
	}
	if len(model.Requests()) != 0 || kubernetes.resourceCalls() != 0 {
		t.Fatalf("pre-consent model/Kubernetes calls = %d/%d", len(model.Requests()), kubernetes.resourceCalls())
	}
	privacyOutcome, err := coordinator.ExecuteUICommand(context.Background(), application.UICommand{
		Kind: application.UICommandShowPrivacy, RequestID: 41,
	})
	if err != nil || privacyOutcome.Privacy == nil || privacyOutcome.Privacy.Origin != "https://model.example" ||
		privacyOutcome.Privacy.PolicyVersion != application.PrivacyPolicyVersion {
		t.Fatalf("privacy review = %#v, %v", privacyOutcome, err)
	}
	if _, err := coordinator.ExecuteUICommand(context.Background(), application.UICommand{
		Kind: application.UICommandAcceptPrivacy, RequestID: 41,
		PrivacyRevision: privacyOutcome.Privacy.Revision,
	}); err != nil {
		t.Fatalf("accept privacy error = %v", err)
	}
	runID, err := coordinator.StartRun(context.Background(), application.StartRunCommand{
		SessionID: session.ID,
		Question:  "Why is the selected Pod not Ready? token=" + canary,
		Resource: &domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
		},
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	result, err := coordinator.WaitRun(waitContext, runID)
	if err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if result.Status != domain.AgentRunStatusCompleted || result.PersistenceDegraded {
		t.Fatalf("RunResult = %#v", result)
	}

	requests := model.Requests()
	if len(requests) != 2 || kubernetes.resourceCalls() != 1 {
		t.Fatalf("model/Kubernetes calls = %d/%d, want 2/1", len(requests), kubernetes.resourceCalls())
	}
	for index, request := range requests {
		if strings.Contains(fmt.Sprintf("%#v", request), canary) {
			t.Fatalf("ModelRequest[%d] contains the sensitive canary", index)
		}
	}
	if !strings.Contains(requests[0].Messages[len(requests[0].Messages)-1].Content, "[REDACTED]") {
		t.Fatalf("safe question = %q", requests[0].Messages[len(requests[0].Messages)-1].Content)
	}

	persistedRun, err := runRepository.GetByID(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetByID(run) error = %v", err)
	}
	if persistedRun.Status != domain.AgentRunStatusCompleted || persistedRun.ToolCallCount != 1 ||
		persistedRun.ModelRequestCount != 2 || persistedRun.PersistenceDegraded {
		t.Fatalf("persisted AgentRun = %#v", persistedRun)
	}
	invocations, err := toolRepository.ListByRun(context.Background(), runID)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("ListByRun() = %#v/%v", invocations, err)
	}
	evidence, err := evidenceRepository.ListByInvocation(context.Background(), invocations[0].ID)
	if err != nil || len(evidence) < 2 {
		t.Fatalf("ListByInvocation() = %#v/%v", evidence, err)
	}
	for index, item := range evidence {
		if item.RunID != runID || item.InvocationID != invocations[0].ID || strings.Contains(fmt.Sprintf("%#v", item), canary) {
			t.Fatalf("Evidence[%d] = %#v", index, item)
		}
	}
	diagnosis, err := diagnosisRepository.GetByRunID(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetByRunID(diagnosis) error = %v", err)
	}
	references := diagnosis.ReferencedEvidenceIDs()
	if len(references) != 1 || references[0] != integrationEvidenceID1 || strings.Contains(fmt.Sprintf("%#v", diagnosis), canary) {
		t.Fatalf("persisted Diagnosis = %#v", diagnosis)
	}
	messages, err := messageRepository.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{
		SessionID: session.ID, Limit: sessioncontract.MaxMessagePageSize,
	})
	if err != nil || len(messages.Messages) != 2 {
		t.Fatalf("ListCommittedBySession() = %#v/%v", messages, err)
	}
	for index, message := range messages.Messages {
		if strings.Contains(message.Content, canary) {
			t.Fatalf("Message[%d] contains the sensitive canary", index)
		}
	}
	auditPage, err := auditRepository.ListBySession(context.Background(), auditcontract.PageRequest{
		SessionID: session.ID, Limit: auditcontract.MaxPageSize,
	})
	if err != nil || len(auditPage.Events) < 7 {
		t.Fatalf("ListBySession(audit) = %#v/%v", auditPage, err)
	}
	if strings.Contains(fmt.Sprintf("%#v", auditPage.Events), canary) {
		t.Fatal("structured audit events contain the sensitive canary")
	}

	ui := uiEvents.Events()
	terminalCount := 0
	for index, event := range ui {
		if event.Sequence != int64(index+1) || event.RunID != runID || event.ScopeGeneration != 7 || event.Validate() != nil {
			t.Fatalf("UIEvent[%d] = %#v", index, event)
		}
		if strings.Contains(fmt.Sprintf("%#v", event), canary) {
			t.Fatalf("UIEvent[%d] contains the sensitive canary", index)
		}
		if event.Terminal() {
			terminalCount++
			if event.Kind != application.UIEventRunCompleted {
				t.Fatalf("terminal UI event = %#v", event)
			}
			if len(event.EvidenceReferences) != 1 || event.EvidenceReferences[0].EvidenceID != integrationEvidenceID1 ||
				event.EvidenceReferences[0].RunID != runID || event.EvidenceReferences[0].Scope.Generation != 7 ||
				event.EvidenceReferences[0].Sequence != event.Sequence ||
				event.EvidenceReferences[0].State != application.UIEvidenceDetailAvailable {
				t.Fatalf("terminal Evidence references = %#v", event.EvidenceReferences)
			}
		}
	}
	if terminalCount != 1 {
		t.Fatalf("UI terminal count = %d, want 1", terminalCount)
	}
	if frame := renderIntegrationUI(ui); strings.Contains(frame, canary) {
		t.Fatal("rendered TUI frame contains the sensitive canary")
	}
	if strings.Contains(fmt.Sprintf("%#v", observer.Values()), canary) {
		t.Fatal("lifecycle log observations contain the sensitive canary")
	}

	toolPurposeCanary := strings.Join([]string{"synthetic", "model", "tool", "purpose", "canary", "8201"}, "-")
	diagnosisCanary := strings.Join([]string{"synthetic", "model", "diagnosis", "canary", "8202"}, "-")
	blockedModelCanary := strings.Join([]string{"synthetic", "blocked", "model", "canary", "8204"}, "-")
	blockedModelPurpose := strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\n" +
		blockedModelCanary + "\n" + strings.Join([]string{"-----END", "PRIVATE", "KEY-----"}, " ")
	safeEventCount := len(ui)
	t.Run("security review finding SR-001 blocks model text from actions and sinks", func(t *testing.T) {
		model.SetReviewPayloads(
			"Inspect the selected Pod; token="+toolPurposeCanary,
			integrationSensitiveDiagnosisJSON(integrationEvidenceID3, diagnosisCanary),
		)
		findingRunID, err := coordinator.StartRun(context.Background(), application.StartRunCommand{
			SessionID: session.ID,
			Question:  "Re-check the selected Pod.",
			Resource: &domain.ResourceRef{
				APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
			},
		})
		if err != nil {
			t.Fatalf("StartRun(review finding) error = %v", err)
		}
		findingWaitContext, cancelFindingWait := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelFindingWait()
		findingResult, err := coordinator.WaitRun(findingWaitContext, findingRunID)
		if err != nil {
			t.Fatalf("WaitRun(review finding) error = %v", err)
		}
		if findingResult.Status != domain.AgentRunStatusCompleted || kubernetes.resourceCalls() != 2 {
			t.Fatalf("review finding result/Kubernetes calls = %#v/%d", findingResult, kubernetes.resourceCalls())
		}

		findingRequests := model.Requests()
		if len(findingRequests) != 4 {
			t.Fatalf("model requests after sanitized run = %d, want 4", len(findingRequests))
		}
		if encoded := fmt.Sprintf("%#v", findingRequests); strings.Contains(encoded, toolPurposeCanary) ||
			strings.Contains(encoded, diagnosisCanary) || !strings.Contains(encoded, "[REDACTED]") {
			t.Fatalf("model requests did not contain only the sanitized derivatives: %#v", findingRequests)
		}
		findingInvocations, err := toolRepository.ListByRun(context.Background(), findingRunID)
		if err != nil || len(findingInvocations) != 1 {
			t.Fatalf("persisted sanitized ToolInvocation = %#v/%v", findingInvocations, err)
		}
		if encoded := fmt.Sprintf("%#v", findingInvocations[0]); strings.Contains(encoded, toolPurposeCanary) ||
			!strings.Contains(encoded, "[REDACTED]") {
			t.Fatalf("persisted ToolInvocation did not contain only the sanitized purpose: %#v", findingInvocations[0])
		}
		findingDiagnosis, err := diagnosisRepository.GetByRunID(context.Background(), findingRunID)
		if err != nil {
			t.Fatalf("persisted sanitized Diagnosis = %#v/%v", findingDiagnosis, err)
		}
		if encoded := fmt.Sprintf("%#v", findingDiagnosis); strings.Contains(encoded, diagnosisCanary) ||
			!strings.Contains(encoded, "[REDACTED]") {
			t.Fatalf("persisted Diagnosis did not contain only sanitized text: %#v", findingDiagnosis)
		}
		findingEvents := uiEvents.Events()
		if safeEventCount > len(findingEvents) {
			t.Fatalf("finding UI event count = %d, before finding = %d", len(findingEvents), safeEventCount)
		}
		var findingEventText strings.Builder
		for _, event := range findingEvents[safeEventCount:] {
			findingEventText.WriteString(event.Text)
			if event.ToolStep != nil {
				findingEventText.WriteString(event.ToolStep.Purpose)
				findingEventText.WriteString(event.ToolStep.Summary)
			}
		}
		if strings.Contains(findingEventText.String(), toolPurposeCanary) ||
			strings.Contains(findingEventText.String(), diagnosisCanary) ||
			!strings.Contains(findingEventText.String(), "[REDACTED]") {
			t.Fatalf("finding UI events did not contain only sanitized model text: %s", findingEventText.String())
		}
		findingFrame := renderIntegrationUI(findingEvents[safeEventCount:])
		if strings.Contains(findingFrame, toolPurposeCanary) || strings.Contains(findingFrame, diagnosisCanary) ||
			!strings.Contains(findingFrame, "[REDACTED]") {
			t.Fatalf("rendered finding TUI frame did not contain only sanitized model text: %s", findingFrame)
		}
		if strings.Contains(fmt.Sprintf("%#v", observer.Values()), toolPurposeCanary) ||
			strings.Contains(fmt.Sprintf("%#v", observer.Values()), diagnosisCanary) {
			t.Fatal("text-free lifecycle observations contain a model-text canary")
		}
		findingAuditPage, err := auditRepository.ListBySession(context.Background(), auditcontract.PageRequest{
			SessionID: session.ID, Limit: auditcontract.MaxPageSize,
		})
		if err != nil {
			t.Fatalf("ListBySession(sanitized model text) error = %v", err)
		}
		if encoded := fmt.Sprintf("%#v", findingAuditPage.Events); strings.Contains(encoded, toolPurposeCanary) ||
			strings.Contains(encoded, diagnosisCanary) {
			t.Fatal("structured audit events contain a model-text canary")
		}
	})

	t.Run("security review finding SR-001 blocks high-risk model text before Tool action", func(t *testing.T) {
		model.SetReviewPayloads(blockedModelPurpose, integrationDiagnosisJSON(integrationEvidenceID3))
		requestsBefore := len(model.Requests())
		kubernetesBefore := kubernetes.resourceCalls()
		eventsBefore := len(uiEvents.Events())
		blockedRunID, err := coordinator.StartRun(context.Background(), application.StartRunCommand{
			SessionID: session.ID,
			Question:  "Re-check the selected Pod with a blocked model response.",
			Resource: &domain.ResourceRef{
				APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
			},
		})
		if err != nil {
			t.Fatalf("StartRun(blocked model text) error = %v", err)
		}
		blockedWaitContext, cancelBlockedWait := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelBlockedWait()
		blockedResult, err := coordinator.WaitRun(blockedWaitContext, blockedRunID)
		if err != nil {
			t.Fatalf("WaitRun(blocked model text) error = %v", err)
		}
		if blockedResult.Status != domain.AgentRunStatusFailed || blockedResult.PersistenceDegraded {
			t.Fatalf("blocked model-text result = %#v", blockedResult)
		}
		if len(model.Requests()) != requestsBefore+1 || kubernetes.resourceCalls() != kubernetesBefore {
			t.Fatalf("blocked model-text model/Kubernetes calls changed from %d/%d to %d/%d",
				requestsBefore, kubernetesBefore, len(model.Requests()), kubernetes.resourceCalls())
		}
		blockedInvocations, err := toolRepository.ListByRun(context.Background(), blockedRunID)
		if err != nil || len(blockedInvocations) != 0 {
			t.Fatalf("blocked model-text ToolInvocations = %#v/%v", blockedInvocations, err)
		}
		if _, err := diagnosisRepository.GetByRunID(context.Background(), blockedRunID); !errors.Is(err, sqlite.ErrDiagnosisNotFound) {
			t.Fatalf("blocked model-text Diagnosis error = %v, want ErrDiagnosisNotFound", err)
		}
		blockedEvents := fmt.Sprintf("%#v", uiEvents.Events()[eventsBefore:])
		if strings.Contains(blockedEvents, blockedModelCanary) || strings.Contains(blockedEvents, blockedModelPurpose) ||
			!strings.Contains(strings.ToLower(blockedEvents), "sensitive") {
			t.Fatalf("blocked model-text UI events = %s", blockedEvents)
		}
		blockedAuditPage, err := auditRepository.ListBySession(context.Background(), auditcontract.PageRequest{
			SessionID: session.ID, Limit: auditcontract.MaxPageSize,
		})
		if err != nil {
			t.Fatalf("ListBySession(blocked model text) error = %v", err)
		}
		if encoded := fmt.Sprintf("%#v", blockedAuditPage.Events); strings.Contains(encoded, blockedModelCanary) ||
			strings.Contains(encoded, blockedModelPurpose) || strings.Contains(fmt.Sprintf("%#v", observer.Values()), blockedModelCanary) {
			t.Fatal("audit or lifecycle observations contain blocked model text")
		}
	})

	unreferencedCanary := strings.Join([]string{"synthetic", "unreferenced", "diagnosis", "canary", "8203"}, "-")
	t.Run("security review finding SR-002 persists valid unreferenced Diagnosis without degradation", func(t *testing.T) {
		model.SetReviewPayloads(
			"Inspect the selected Pod.",
			integrationUnreferencedDiagnosisJSON(unreferencedCanary),
		)
		requestsBefore := len(model.Requests())
		kubernetesBefore := kubernetes.resourceCalls()
		eventsBefore := len(uiEvents.Events())
		unreferencedRunID, err := coordinator.StartRun(context.Background(), application.StartRunCommand{
			SessionID: session.ID,
			Question:  "Check the selected Pod without asserting a confirmed fact.",
			Resource: &domain.ResourceRef{
				APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
			},
		})
		if err != nil {
			t.Fatalf("StartRun(unreferenced Diagnosis) error = %v", err)
		}
		unreferencedWaitContext, cancelUnreferencedWait := context.WithTimeout(context.Background(), 5*time.Second)
		unreferencedResult, err := coordinator.WaitRun(unreferencedWaitContext, unreferencedRunID)
		cancelUnreferencedWait()
		if err != nil {
			t.Fatalf("WaitRun(unreferenced Diagnosis) error = %v", err)
		}
		if unreferencedResult.Status != domain.AgentRunStatusCompleted || unreferencedResult.PersistenceDegraded {
			t.Fatalf("unreferenced Diagnosis result = %#v", unreferencedResult)
		}
		if len(model.Requests()) != requestsBefore+2 || kubernetes.resourceCalls() != kubernetesBefore+1 {
			t.Fatalf("unreferenced Diagnosis model/Kubernetes calls changed from %d/%d to %d/%d",
				requestsBefore, kubernetesBefore, len(model.Requests()), kubernetes.resourceCalls())
		}
		unreferencedDiagnosis, err := diagnosisRepository.GetByRunID(context.Background(), unreferencedRunID)
		if err != nil {
			t.Fatalf("GetByRunID(unreferenced Diagnosis) error = %v", err)
		}
		if references := unreferencedDiagnosis.ReferencedEvidenceIDs(); len(references) != 0 ||
			unreferencedDiagnosis.ObservedFrom == nil || unreferencedDiagnosis.ObservedTo == nil ||
			unreferencedDiagnosis.EvidenceDetailsState != domain.EvidenceDetailAvailable ||
			!strings.Contains(fmt.Sprintf("%#v", unreferencedDiagnosis), unreferencedCanary) {
			t.Fatalf("durable unreferenced Diagnosis = %#v", unreferencedDiagnosis)
		}
		unreferencedInvocations, err := toolRepository.ListByRun(context.Background(), unreferencedRunID)
		if err != nil || len(unreferencedInvocations) != 1 {
			t.Fatalf("ListByRun(unreferenced Diagnosis) = %#v/%v", unreferencedInvocations, err)
		}
		unreferencedEvidence, err := evidenceRepository.ListByInvocation(context.Background(), unreferencedInvocations[0].ID)
		if err != nil || len(unreferencedEvidence) == 0 {
			t.Fatalf("ListByInvocation(unreferenced Diagnosis) = %#v/%v", unreferencedEvidence, err)
		}
		observedFrom, observedTo := unreferencedEvidence[0].ObservedAt, unreferencedEvidence[0].ObservedAt
		for _, item := range unreferencedEvidence[1:] {
			if item.ObservedAt.Before(observedFrom) {
				observedFrom = item.ObservedAt
			}
			if item.ObservedAt.After(observedTo) {
				observedTo = item.ObservedAt
			}
		}
		if !unreferencedDiagnosis.ObservedFrom.Equal(observedFrom) || !unreferencedDiagnosis.ObservedTo.Equal(observedTo) {
			t.Fatalf("Diagnosis/Evidence observation windows = %v..%v/%v..%v",
				unreferencedDiagnosis.ObservedFrom, unreferencedDiagnosis.ObservedTo, observedFrom, observedTo)
		}
		unreferencedEvents := fmt.Sprintf("%#v", uiEvents.Events()[eventsBefore:])
		if !strings.Contains(unreferencedEvents, unreferencedCanary) ||
			strings.Contains(unreferencedEvents, string(application.UIEventPersistenceDegraded)) {
			t.Fatalf("unreferenced Diagnosis UI events = %s", unreferencedEvents)
		}

		model.SetReviewPayloads("Inspect the selected Pod.", integrationUnreferencedDiagnosisJSON(unreferencedCanary))
		requestsBeforeFollowUp := len(model.Requests())
		kubernetesBeforeFollowUp := kubernetes.resourceCalls()
		followUpRunID, err := coordinator.StartRun(context.Background(), application.StartRunCommand{
			SessionID: session.ID,
			Question:  "Confirm that a later run remains available.",
			Resource: &domain.ResourceRef{
				APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
			},
		})
		if err != nil {
			t.Fatalf("StartRun(after durable unreferenced Diagnosis) error = %v", err)
		}
		followUpWaitContext, cancelFollowUpWait := context.WithTimeout(context.Background(), 5*time.Second)
		followUpResult, err := coordinator.WaitRun(followUpWaitContext, followUpRunID)
		cancelFollowUpWait()
		if err != nil || followUpResult.Status != domain.AgentRunStatusCompleted || followUpResult.PersistenceDegraded {
			t.Fatalf("WaitRun(after durable unreferenced Diagnosis) = %#v/%v", followUpResult, err)
		}
		if len(model.Requests()) != requestsBeforeFollowUp+2 || kubernetes.resourceCalls() != kubernetesBeforeFollowUp+1 {
			t.Fatalf("follow-up model/Kubernetes calls changed from %d/%d to %d/%d",
				requestsBeforeFollowUp, kubernetesBeforeFollowUp, len(model.Requests()), kubernetes.resourceCalls())
		}
		if _, err := diagnosisRepository.GetByRunID(context.Background(), followUpRunID); err != nil {
			t.Fatalf("GetByRunID(follow-up Diagnosis) error = %v", err)
		}
	})

	if err := database.Close(); err != nil {
		t.Fatalf("database.Close() error = %v", err)
	}
	databaseClosed = true
	entries, err := os.ReadDir(stateDirectory)
	if err != nil {
		t.Fatalf("os.ReadDir(state directory) error = %v", err)
	}
	sensitiveModelValues := []string{toolPurposeCanary, diagnosisCanary, blockedModelCanary, blockedModelPurpose}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(stateDirectory, entry.Name()))
		if err != nil {
			t.Fatalf("os.ReadFile(%s) error = %v", entry.Name(), err)
		}
		if strings.Contains(string(content), canary) {
			t.Fatalf("SQLite file %s contains the sensitive canary", entry.Name())
		}
		for _, value := range sensitiveModelValues {
			if strings.Contains(string(content), value) {
				t.Fatalf("SQLite file %s contains model-originated sensitive text", entry.Name())
			}
		}
	}
}

func renderIntegrationUI(events []application.UIEvent) string {
	model := tui.NewModel(tui.Config{
		Width: 120, Height: 80, NoColor: true,
		Scope: tui.ScopeView{Context: "test-context", Namespace: "team-a", Generation: 7, ReadOnly: true},
	})
	for _, event := range events {
		updated, _ := model.Update(tui.ApplicationEventMsg{Event: event})
		model = updated.(tui.Model)
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	model = updated.(tui.Model)
	return model.View().Content
}

type integrationClock struct {
	mu   sync.Mutex
	next time.Time
}

func newIntegrationClock() *integrationClock {
	return &integrationClock{next: time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)}
}

func (clock *integrationClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return value
}

type integrationIDs struct {
	mu            sync.Mutex
	applicationID int
	auditID       int
	modelID       int
	invocationID  int
	diagnosisID   int
	evidenceID    int
}

func newIntegrationIDs() *integrationIDs { return &integrationIDs{} }

func (source *integrationIDs) next(counter *int, offset int) string {
	source.mu.Lock()
	defer source.mu.Unlock()
	(*counter)++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", offset+*counter)
}

func (source *integrationIDs) NewSessionID() (domain.SessionID, error) {
	return domain.SessionID(source.next(&source.applicationID, 100)), nil
}
func (source *integrationIDs) NewMessageID() (domain.MessageID, error) {
	return domain.MessageID(source.next(&source.applicationID, 100)), nil
}
func (source *integrationIDs) NewAgentRunID() (domain.AgentRunID, error) {
	return domain.AgentRunID(source.next(&source.applicationID, 100)), nil
}
func (source *integrationIDs) NewAuditEventID() (domain.AuditEventID, error) {
	return domain.AuditEventID(source.next(&source.auditID, 200)), nil
}
func (source *integrationIDs) NewModelRequestID() (domain.ModelRequestID, error) {
	return domain.ModelRequestID(source.next(&source.modelID, 300)), nil
}
func (source *integrationIDs) NewToolInvocationID() (domain.ToolInvocationID, error) {
	return domain.ToolInvocationID(source.next(&source.invocationID, 400)), nil
}
func (source *integrationIDs) NewDiagnosisID() (domain.DiagnosisID, error) {
	return domain.DiagnosisID(source.next(&source.diagnosisID, 500)), nil
}
func (source *integrationIDs) NewEvidenceID() (domain.EvidenceID, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.evidenceID++
	if source.evidenceID == 1 {
		return integrationEvidenceID1, nil
	}
	if source.evidenceID == 2 {
		return integrationEvidenceID2, nil
	}
	return domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 700+source.evidenceID)), nil
}

type integrationScope struct {
	mu     sync.Mutex
	scope  domain.ClusterScope
	runID  domain.AgentRunID
	cancel context.CancelFunc
}

func newIntegrationScope() *integrationScope {
	return &integrationScope{scope: domain.ClusterScope{
		Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7,
		ActivatedAt: time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC),
	}}
}

func (scope *integrationScope) CurrentScope() (domain.ClusterScope, bool) { return scope.scope, true }

func (scope *integrationScope) Current(ctx context.Context, value domain.ClusterScope) bool {
	return ctx != nil && ctx.Err() == nil && value == scope.scope
}

func (scope *integrationScope) BindRun(value domain.ClusterScope, runID domain.AgentRunID, cancel context.CancelFunc) error {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if value != scope.scope || scope.cancel != nil {
		return errors.New("integration scope is unavailable")
	}
	scope.runID, scope.cancel = runID, cancel
	return nil
}

func (scope *integrationScope) UnbindRun(runID domain.AgentRunID) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.runID == runID {
		scope.runID, scope.cancel = "", nil
	}
}

type integrationModelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type integrationModelRequest struct {
	Messages []integrationModelMessage `json:"messages"`
}

type integrationModel struct {
	mu          sync.Mutex
	requests    []integrationModelRequest
	toolPurpose string
	diagnosis   string
	step        int
}

func (model *integrationModel) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil || request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var captured integrationModelRequest
	if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	model.mu.Lock()
	model.requests = append(model.requests, captured)
	step := model.step
	model.step++
	toolPurpose := model.toolPurpose
	diagnosis := model.diagnosis
	model.mu.Unlock()
	if toolPurpose == "" {
		toolPurpose = "Inspect the selected Pod."
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	switch step % 2 {
	case 0:
		arguments, _ := json.Marshal(struct {
			Purpose  string `json:"purpose"`
			Resource struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"resource"`
		}{
			Purpose: toolPurpose,
			Resource: struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			}{Kind: "Pod", Name: "sample-pod"},
		})
		writeIntegrationModelChunk(writer, map[string]any{
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"index": 0, "id": "call-1", "type": "function",
						"function": map[string]any{
							"name": string(domain.ToolNameGetResource), "arguments": string(arguments),
						},
					}},
				},
				"finish_reason": nil,
			}},
		})
		writeIntegrationModelChunk(writer, map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
			}},
		})
	case 1:
		writeIntegrationModelChunk(writer, map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": diagnosis}, "finish_reason": nil,
			}},
		})
		writeIntegrationModelChunk(writer, map[string]any{
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
			}},
		})
	}
	_, _ = writer.Write([]byte("data: [DONE]\n\n"))
}

func writeIntegrationModelChunk(writer http.ResponseWriter, value any) {
	encoded, _ := json.Marshal(value)
	_, _ = writer.Write(append(append([]byte("data: "), encoded...), '\n', '\n'))
}

func (model *integrationModel) Requests() []integrationModelRequest {
	model.mu.Lock()
	defer model.mu.Unlock()
	return append([]integrationModelRequest(nil), model.requests...)
}

func (model *integrationModel) SetReviewPayloads(toolPurpose, diagnosis string) {
	model.mu.Lock()
	defer model.mu.Unlock()
	model.toolPurpose = toolPurpose
	model.diagnosis = diagnosis
	model.step = 0
}

func integrationDiagnosisJSON(evidenceID domain.EvidenceID) string {
	return fmt.Sprintf(
		`{"answer_markdown":"The Pod is not Ready. Review the readiness probe configuration before changing it.","evidence_citations":[{"claim":"The Pod is not Ready.","evidence_ids":[%q]}],"proposed_actions":[]}`,
		evidenceID,
	)
}

func integrationSensitiveDiagnosisJSON(evidenceID domain.EvidenceID, canary string) string {
	return fmt.Sprintf(
		`{"answer_markdown":%q,"evidence_citations":[{"claim":%q,"evidence_ids":[%q]}],"proposed_actions":[]}`,
		"The projected condition includes token="+canary,
		"The projected condition includes token="+canary,
		evidenceID,
	)
}

func integrationUnreferencedDiagnosisJSON(canary string) string {
	return fmt.Sprintf(
		`{"answer_markdown":%q,"evidence_citations":[],"proposed_actions":[]}`,
		"Review the observation without claiming it; marker="+canary,
	)
}

type integrationKube struct {
	mu       sync.Mutex
	canary   string
	getCalls int
}

func (reader *integrationKube) ReadResource(_ context.Context, request tools.ResourceReadRequest) (tools.ResourceObservation, error) {
	reader.mu.Lock()
	reader.getCalls++
	reader.mu.Unlock()
	if request.Validate() != nil || request.Scope.Namespace != "team-a" || request.Reference.Name != "sample-pod" {
		return tools.ResourceObservation{}, errors.New("unexpected integration resource request")
	}
	return tools.ResourceObservation{
		Summary: domain.ResourceSummary{
			Reference: domain.ResourceRef{
				APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod",
				UID: "generated-pod-uid", ResourceVersion: "17",
			},
			CreatedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
			Status: domain.ResourceStatus{
				Phase: "Running", Desired: domain.Count(1), Ready: domain.Count(0),
			},
		},
		Conditions: []tools.ConditionObservation{{
			Type: tools.ExternalText{Value: "Ready"}, Status: tools.ExternalText{Value: "False"},
			Reason:  tools.ExternalText{Value: "ProbeFailed"},
			Message: tools.ExternalText{Value: "token=" + reader.canary},
		}},
	}, nil
}

func (*integrationKube) ListResources(context.Context, tools.ResourceListRequest) (tools.ResourceObservationList, error) {
	return tools.ResourceObservationList{}, errors.New("unexpected list_resources call")
}

func (*integrationKube) ReadEvents(context.Context, tools.EventReadRequest) (tools.EventObservationList, error) {
	return tools.EventObservationList{}, errors.New("unexpected get_events call")
}

func (*integrationKube) ReadPodLog(context.Context, tools.PodLogReadRequest) (tools.PodLogObservation, error) {
	return tools.PodLogObservation{}, errors.New("unexpected Pod log call")
}

func (*integrationKube) ReadRelatedResources(context.Context, tools.RelatedReadRequest) (tools.RelatedObservationGraph, error) {
	return tools.RelatedObservationGraph{}, errors.New("unexpected related-resource call")
}

func (reader *integrationKube) resourceCalls() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.getCalls
}

type deniedIntegrationLogPolicy struct{}

func (deniedIntegrationLogPolicy) AuthorizeLogRead(context.Context, tools.LogPolicyRequest) tools.LogPolicyDecision {
	return tools.LogPolicyDenied
}

type integrationUIEvents struct {
	mu     sync.Mutex
	events []application.UIEvent
}

func newIntegrationUIEvents() *integrationUIEvents { return &integrationUIEvents{} }

func (sink *integrationUIEvents) PublishUIEvent(_ context.Context, event application.UIEvent) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if event.Validate() != nil {
		return application.ErrInvalidUIEvent
	}
	sink.events = append(sink.events, event)
	return nil
}

func (sink *integrationUIEvents) Events() []application.UIEvent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]application.UIEvent(nil), sink.events...)
}

type integrationObserver struct {
	mu     sync.Mutex
	values []application.RunObservation
}

func newIntegrationObserver() *integrationObserver { return &integrationObserver{} }

func (observer *integrationObserver) ObserveRun(_ context.Context, value application.RunObservation) {
	observer.mu.Lock()
	observer.values = append(observer.values, value)
	observer.mu.Unlock()
}

func (observer *integrationObserver) Values() []application.RunObservation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]application.RunObservation(nil), observer.values...)
}
