package application_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/agent/einoadapter"
	"github.com/imbrooklyn/kupilot/internal/application"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/security"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
	"github.com/imbrooklyn/kupilot/internal/tools"
)

const (
	integrationEvidenceID1 domain.EvidenceID = "00000000-0000-7000-8000-000000000701"
	integrationEvidenceID2 domain.EvidenceID = "00000000-0000-7000-8000-000000000702"
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
	agentAdapter, err := einoadapter.New(einoadapter.Config{
		Model: model, Tools: toolHandlers, ScopeGuard: scope, Identifiers: identifiers, Now: clock.Now,
	})
	if err != nil {
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
		}
	}
	if terminalCount != 1 {
		t.Fatalf("UI terminal count = %d, want 1", terminalCount)
	}
	if strings.Contains(fmt.Sprintf("%#v", observer.Values()), canary) {
		t.Fatal("lifecycle log observations contain the sensitive canary")
	}

	if err := database.Close(); err != nil {
		t.Fatalf("database.Close() error = %v", err)
	}
	databaseClosed = true
	entries, err := os.ReadDir(stateDirectory)
	if err != nil {
		t.Fatalf("os.ReadDir(state directory) error = %v", err)
	}
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
	}
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
		Context: "test-context", Namespace: "team-a", Generation: 7,
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

type integrationModel struct {
	mu        sync.Mutex
	requests  []domain.ModelRequest
	diagnosis string
}

func (model *integrationModel) Stream(
	ctx context.Context,
	request domain.ModelRequest,
	consume agent.ModelStreamConsumer,
) *domain.ModelError {
	if ctx == nil || request.Validate() != nil {
		return domain.NewModelError(domain.ModelErrorCodeInvalidRequest, domain.ModelOperationStream, "integration-model")
	}
	model.mu.Lock()
	model.requests = append(model.requests, request)
	call := len(model.requests)
	diagnosis := model.diagnosis
	model.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.NewModelError(domain.ModelErrorCodeCancelled, domain.ModelOperationStream, string(request.ID))
	}
	switch call {
	case 1:
		consume(domain.ModelStreamEvent{
			Sequence: 1, Kind: domain.ModelStreamEventToolCallFragment,
			ToolCallFragment: &domain.ModelToolCallFragment{
				Index: 0, IDFragment: "call-1", NameFragment: string(domain.ToolNameGetResource),
				ArgumentsFragment: `{"purpose":"Inspect the selected Pod.","resource":{"kind":"Pod","name":"sample-pod"}}`,
			},
		})
		consume(domain.ModelStreamEvent{
			Sequence: 2, Kind: domain.ModelStreamEventCompleted,
			Completion: &domain.ModelCompletion{FinishReason: domain.ModelFinishReasonToolCalls},
		})
	case 2:
		consume(domain.ModelStreamEvent{Sequence: 1, Kind: domain.ModelStreamEventTextDelta, TextDelta: diagnosis})
		consume(domain.ModelStreamEvent{
			Sequence: 2, Kind: domain.ModelStreamEventCompleted,
			Completion: &domain.ModelCompletion{FinishReason: domain.ModelFinishReasonStop},
		})
	default:
		return domain.NewModelError(domain.ModelErrorCodeInternal, domain.ModelOperationStream, string(request.ID))
	}
	return nil
}

func (model *integrationModel) Requests() []domain.ModelRequest {
	model.mu.Lock()
	defer model.mu.Unlock()
	return append([]domain.ModelRequest(nil), model.requests...)
}

func integrationDiagnosisJSON(evidenceID domain.EvidenceID) string {
	return fmt.Sprintf(
		`{"confirmed_facts":[{"statement":"The Pod is not Ready.","evidence_ids":[%q]}],"hypotheses":[],"missing_information":[],"recommended_actions":[{"action":"Review the readiness probe configuration.","risk":"Read-only recommendation; not executed.","prerequisites":[],"executed":false}]}`,
		evidenceID,
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
