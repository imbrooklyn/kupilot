package main

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/kube"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/security"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestSessionApplicationAdapterUsesRealSQLiteResumeEligibility(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 10, 6, 30, 0, 0, time.UTC)
	stateRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks() error = %v", err)
	}
	database, err := sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: filepath.Join(stateRoot, "state"), ApplicationVersion: "test", CorrelationID: "session-adapter",
	})
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	defer database.Close()
	sessions := sqlite.NewSessionRepository(database)
	messages := sqlite.NewMessageRepository(database)
	runs := sqlite.NewAgentRunRepository(database)
	seedIntegrationResumeHistory(t, ctx, sessions, messages, now)
	minimalID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2b03")
	if err := sessions.Create(ctx, domain.Session{
		ID: minimalID, Status: domain.SessionStatusActive, PrivacyMode: domain.PrivacyModeMinimal,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("minimal Session Create() error = %v", err)
	}
	adapter := sessionServiceForIntegration(sessions, messages, runs, database)

	candidates, err := adapter.ListResumable(ctx, application.MaxUIQueryCandidates)
	if err != nil || len(candidates) != 1 || candidates[0].ID != integrationSessionID {
		t.Fatalf("ListResumable() = %#v, %v", candidates, err)
	}
	latest, err := adapter.ResumeLatest(ctx)
	if err != nil || latest.Session.ID != integrationSessionID || len(latest.Messages) != 1 {
		t.Fatalf("ResumeLatest() = %#v, %v", latest, err)
	}
	if _, err := adapter.ResumeByID(ctx, minimalID); !errors.Is(err, application.ErrSessionNotResumable) {
		t.Fatalf("minimal ResumeByID() error = %v", err)
	}
	missingID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2b04")
	if _, err := adapter.ResumeByID(ctx, missingID); !errors.Is(err, application.ErrSessionResumeUnavailable) {
		t.Fatalf("missing ResumeByID() error = %v", err)
	}
	detailAdapter := &applicationEvidenceDetailAdapter{
		evidence: sqlite.NewEvidenceRepository(database), diagnoses: sqlite.NewDiagnosisRepository(database),
	}
	missingRunID := domain.AgentRunID("0198a46e-7d2a-7d34-9b6f-2df5f45a2b05")
	if _, found, readErr := detailAdapter.ReadDiagnosis(ctx, missingRunID); readErr != nil || found {
		t.Fatalf("missing Diagnosis detail = found %v, error %v", found, readErr)
	}
	missingEvidenceID := domain.EvidenceID("0198a46e-7d2a-7d34-9b6f-2df5f45a2b06")
	if _, found, readErr := detailAdapter.ReadEvidence(ctx, missingEvidenceID); readErr != nil || found {
		t.Fatalf("missing Evidence detail = found %v, error %v", found, readErr)
	}

	emptyRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("empty filepath.EvalSymlinks() error = %v", err)
	}
	emptyDatabase, err := sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: filepath.Join(emptyRoot, "state"), ApplicationVersion: "test", CorrelationID: "empty-session-adapter",
	})
	if err != nil {
		t.Fatalf("empty sqlite.Open() error = %v", err)
	}
	defer emptyDatabase.Close()
	emptySessions := sqlite.NewSessionRepository(emptyDatabase)
	emptyMessages := sqlite.NewMessageRepository(emptyDatabase)
	emptyRuns := sqlite.NewAgentRunRepository(emptyDatabase)
	emptyAdapter := sessionServiceForIntegration(emptySessions, emptyMessages, emptyRuns, emptyDatabase)
	emptyCandidates, err := emptyAdapter.ListResumable(ctx, application.MaxUIQueryCandidates)
	if err != nil || len(emptyCandidates) != 0 {
		t.Fatalf("empty ListResumable() = %#v, %v", emptyCandidates, err)
	}
	if _, err := emptyAdapter.ResumeLatest(ctx); !errors.Is(err, application.ErrNoResumableSession) {
		t.Fatalf("empty ResumeLatest() error = %v", err)
	}
}

func TestResumeIntegrationSeparatesExplicitScopeActivationFromZeroIOAcceptance(t *testing.T) {
	tests := []struct {
		name                   string
		initialContext         string
		initialNamespace       string
		savedScope             domain.ScopeCandidate
		savedUID               string
		actualUID              string
		forbiddenNamespace     string
		wantActions            []string
		wantScopeFailure       application.UIQueryFailureCode
		wantContextAfterward   string
		wantNamespaceAfterward string
	}{
		{
			name: "same scope", initialContext: "saved-context", initialNamespace: "payments",
			savedScope: domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"},
			savedUID:   "deployment-uid", actualUID: "deployment-uid",
			wantActions:          nil,
			wantContextAfterward: "saved-context", wantNamespaceAfterward: "payments",
		},
		{
			name: "different scope", initialContext: "current-context", initialNamespace: "default",
			savedScope: domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"},
			savedUID:   "deployment-uid", actualUID: "deployment-uid",
			wantActions:          []string{"GET /api/v1/namespaces/payments"},
			wantContextAfterward: "saved-context", wantNamespaceAfterward: "payments",
		},
		{
			name: "missing Context", initialContext: "current-context", initialNamespace: "default",
			savedScope: domain.ScopeCandidate{Context: "missing-context", Namespace: "payments"},
			savedUID:   "deployment-uid", actualUID: "deployment-uid",
			wantScopeFailure:     application.UIQueryUnavailable,
			wantContextAfterward: "current-context", wantNamespaceAfterward: "default",
		},
		{
			name: "forbidden Namespace", initialContext: "current-context", initialNamespace: "default",
			savedScope: domain.ScopeCandidate{Context: "current-context", Namespace: "forbidden"},
			savedUID:   "deployment-uid", actualUID: "deployment-uid", forbiddenNamespace: "forbidden",
			wantActions:          []string{"GET /api/v1/namespaces/forbidden"},
			wantScopeFailure:     application.UIQueryForbidden,
			wantContextAfterward: "current-context", wantNamespaceAfterward: "default",
		},
		{
			name: "Resource UID changed", initialContext: "current-context", initialNamespace: "default",
			savedScope: domain.ScopeCandidate{Context: "current-context", Namespace: "default"},
			savedUID:   "deployment-uid", actualUID: "replacement-uid",
			wantActions:          nil,
			wantContextAfterward: "current-context", wantNamespaceAfterward: "default",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC)
			recorder := newIntegrationActionRecorder(test.actualUID, test.forbiddenNamespace)
			server := httptest.NewTLSServer(http.HandlerFunc(recorder.serveHTTP))
			defer server.Close()

			kubeconfigPath := filepath.Join(t.TempDir(), "config")
			certificateAuthority := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			writeIntegrationKubeconfig(t, kubeconfigPath, server.URL, certificateAuthority, test.initialContext)
			t.Setenv(clientcmd.RecommendedConfigPathEnvVar, kubeconfigPath)

			stateRoot, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("filepath.EvalSymlinks() error = %v", err)
			}
			database, err := sqlite.Open(ctx, sqlite.OpenOptions{
				StateDir: filepath.Join(stateRoot, "state"), ApplicationVersion: "test", CorrelationID: "resume-integration",
			})
			if err != nil {
				t.Fatalf("sqlite.Open() error = %v", err)
			}
			defer database.Close()
			sessions := sqlite.NewSessionRepository(database)
			messages := sqlite.NewMessageRepository(database)
			runs := sqlite.NewAgentRunRepository(database)
			tools := sqlite.NewToolInvocationRepository(database)
			audits := sqlite.NewAuditRepository(database)
			seedIntegrationResumeHistoryForScope(
				t, ctx, sessions, messages, now, test.savedScope, test.savedUID,
			)
			seedIntegrationCompletedTurn(t, ctx, runs, now, test.savedScope)
			service := sessionServiceForIntegration(sessions, messages, runs, database)

			loader := kube.NewConfigLoader()
			factory, err := kube.NewClientFactory(loader, kube.ExecCredentialsDeny)
			if err != nil {
				t.Fatalf("kube.NewClientFactory() error = %v", err)
			}
			gateway, err := kube.NewGateway(factory)
			if err != nil {
				t.Fatalf("kube.NewGateway() error = %v", err)
			}
			binding, err := kube.NewToolScopeBinding(gateway)
			if err != nil {
				t.Fatalf("kube.NewToolScopeBinding() error = %v", err)
			}
			scopeManager, err := application.NewScopeManager(binding, gateway, gateway, binding, func() time.Time { return now })
			if err != nil {
				t.Fatalf("application.NewScopeManager() error = %v", err)
			}
			defer scopeManager.Close()

			runner := &integrationRunner{now: func() time.Time { return now }}
			identifiers, err := application.NewIdentifierGenerator(func() time.Time { return now })
			if err != nil {
				t.Fatalf("application.NewIdentifierGenerator() error = %v", err)
			}
			privacyManager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
				Store: sqlite.NewPrivacyRepository(database), Origin: "https://model.example", Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatalf("application.NewPrivacyManager() error = %v", err)
			}
			privacyReview, err := privacyManager.Review(ctx)
			if err != nil {
				t.Fatalf("Privacy Review() error = %v", err)
			}
			if _, err := privacyManager.Decide(ctx, application.PrivacyActionAccept, privacyReview.Revision, nil); err != nil {
				t.Fatalf("Privacy Decide() error = %v", err)
			}
			coordinator, err := application.NewCoordinator(application.CoordinatorConfig{
				Sessions: sessions, Runs: runs, Tools: tools, Audits: audits, Scope: scopeManager,
				ModelContext: messages,
				Runner:       runner, Identifiers: identifiers, AuditIdentifiers: identifiers,
				Questions: security.NewRedactor(), Privacy: privacyManager, UIEvents: integrationUIEvents{},
				Observer: application.RunObserverFunc(func(context.Context, application.RunObservation) {}),
				Now:      func() time.Time { return now },
				UI: &application.CoordinatorUIConfig{
					Sessions: service, Search: sessions, Titles: service, Startup: service, Scopes: scopeManager,
					ScopePreferences: sqlite.NewScopePreferenceRepository(database),
				},
			})
			if err != nil {
				t.Fatalf("application.NewCoordinator() error = %v", err)
			}
			defer coordinator.Shutdown(context.Background())

			if _, err := coordinator.StartUI(ctx, application.UIStartIntent{
				Kind: application.UIStartResumeID, SessionID: integrationSessionID,
			}, domain.PrivacyModeStandard); err != nil {
				t.Fatalf("StartUI() error = %v", err)
			}
			contextOutcome, err := coordinator.ExecuteUICommand(ctx, application.UICommand{
				Kind: application.UICommandSelectContext, RequestID: 1, Text: test.initialContext,
			})
			if err != nil || contextOutcome.Scope == nil || contextOutcome.Scope.Failure != "" {
				t.Fatalf("initial Context outcome = %#v (scope %#v), actions = %#v, error = %v",
					contextOutcome, contextOutcome.Scope, recorder.snapshot(), err)
			}
			if test.initialNamespace != contextOutcome.Scope.Namespace {
				namespaceOutcome, namespaceErr := coordinator.ExecuteUICommand(ctx, application.UICommand{
					Kind: application.UICommandSelectNamespace, RequestID: 2, Text: test.initialNamespace,
					ExpectedScopeGeneration: contextOutcome.Scope.ScopeGeneration,
				})
				if namespaceErr != nil || namespaceOutcome.Scope == nil || namespaceOutcome.Scope.Failure != "" {
					t.Fatalf("initial Namespace outcome = %#v, %v", namespaceOutcome, namespaceErr)
				}
			}
			recorder.reset()

			resumeRequest := application.UIResumeRequest{
				RequestID: 3, Mode: application.UIResumeExact, SessionID: integrationSessionID,
			}
			resumeResult, err := coordinator.ResumeUI(ctx, resumeRequest)
			if err != nil || resumeResult.Failure != "" || resumeResult.Session == nil {
				t.Fatalf("ResumeUI() = %#v, %v", resumeResult, err)
			}
			if got := recorder.snapshot(); len(got) != 0 || runner.calls.Load() != 0 {
				t.Fatalf("pre-accept actions = %#v, model calls = %d", got, runner.calls.Load())
			}

			savedScope := *resumeResult.Session.SavedScope
			activation, err := coordinator.ExecuteUICommand(ctx, application.UICommand{
				Kind: application.UICommandActivateScope, RequestID: resumeRequest.RequestID,
				ExpectedScopeGeneration: scopeManager.View().Generation, Scope: &savedScope,
			})
			if err != nil || activation.Validate() != nil || activation.Scope == nil ||
				activation.Scope.Failure != test.wantScopeFailure {
				t.Fatalf("explicit scope activation = %#v, %v", activation, err)
			}
			if actions := recorder.snapshot(); !reflect.DeepEqual(actions, test.wantActions) || runner.calls.Load() != 0 {
				t.Fatalf("scope activation actions = %#v, model calls = %d", actions, runner.calls.Load())
			}
			recorder.reset()
			if test.wantScopeFailure != "" {
				cancelled, cancelErr := coordinator.ExecuteUICommand(ctx, application.UICommand{
					Kind: application.UICommandCancelResume, RequestID: resumeRequest.RequestID,
				})
				if cancelErr != nil || cancelled.Validate() != nil || cancelled.Resumed != nil || cancelled.Session != nil {
					t.Fatalf("resume cancellation = %#v, %v", cancelled, cancelErr)
				}
			} else {
				outcome, acceptErr := coordinator.ExecuteUICommand(ctx, application.UICommand{
					Kind: application.UICommandAcceptResume, RequestID: resumeRequest.RequestID,
					ExpectedScopeGeneration: scopeManager.View().Generation,
				})
				if acceptErr != nil || outcome.Validate() != nil || outcome.Resumed == nil || outcome.Scope == nil ||
					outcome.Scope.Failure != "" || outcome.Resource != nil {
					t.Fatalf("resume acceptance = %#v, %v", outcome, acceptErr)
				}
			}
			if actions := recorder.snapshot(); len(actions) != 0 || runner.calls.Load() != 0 {
				t.Fatalf("resume decision actions = %#v, model calls = %d", actions, runner.calls.Load())
			}
			view := scopeManager.View()
			if view.Scope == nil || view.Scope.Context != test.wantContextAfterward || view.Scope.Namespace != test.wantNamespaceAfterward {
				t.Fatalf("final scope = %#v", view)
			}
			selected, err := scopeManager.SelectedResource(*view.Scope)
			if err != nil || selected != nil {
				t.Fatalf("selected ResourceRef = %#v, %v", selected, err)
			}
			if test.wantScopeFailure == "" {
				runID, startErr := coordinator.StartRun(ctx, application.StartRunCommand{
					SessionID: integrationSessionID, Question: "Use the resumed safe conversation context.",
				})
				if startErr != nil {
					t.Fatalf("resumed StartRun() error = %v", startErr)
				}
				if _, waitErr := coordinator.WaitRun(ctx, runID); waitErr != nil {
					t.Fatalf("resumed WaitRun() error = %v", waitErr)
				}
				inputs := runner.Inputs()
				if len(inputs) != 1 {
					t.Fatalf("resumed Agent inputs = %d", len(inputs))
				}
				turns := inputs[0].Conversation().Turns()
				if len(turns) != 2 || turns[0].Role != domain.MessageRoleUser || turns[0].Content != "Durable prior question." ||
					turns[1].Role != domain.MessageRoleAssistant || turns[1].Content != "Durable prior final answer." {
					t.Fatalf("resumed model context = %#v", turns)
				}
				for _, turn := range turns {
					if turn.Content == "Historic diagnosis content." {
						t.Fatal("unpaired render history entered model context")
					}
				}
			}
		})
	}
}

const (
	integrationSessionID domain.SessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2b01"
	integrationMessageID domain.MessageID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2b02"
)

type integrationActionRecorder struct {
	mu                 sync.Mutex
	actions            []string
	resourceUID        string
	forbiddenNamespace string
}

func newIntegrationActionRecorder(resourceUID, forbiddenNamespace string) *integrationActionRecorder {
	return &integrationActionRecorder{resourceUID: resourceUID, forbiddenNamespace: forbiddenNamespace}
}

func (recorder *integrationActionRecorder) serveHTTP(response http.ResponseWriter, request *http.Request) {
	recorder.mu.Lock()
	recorder.actions = append(recorder.actions, request.Method+" "+request.URL.Path)
	recorder.mu.Unlock()
	response.Header().Set("Content-Type", "application/json")
	if request.URL.Path == "/api/v1/namespaces/"+recorder.forbiddenNamespace && recorder.forbiddenNamespace != "" {
		response.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(response, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`)
		return
	}
	switch request.URL.Path {
	case "/api/v1/namespaces/default":
		_, _ = fmt.Fprint(response, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"default"}}`)
	case "/api/v1/namespaces/payments":
		_, _ = fmt.Fprint(response, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"payments"}}`)
	case "/apis/apps/v1/namespaces/payments/deployments/payment-api":
		_, _ = fmt.Fprintf(response, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"payment-api","namespace":"payments","uid":%q,"resourceVersion":"29"},"spec":{"replicas":1},"status":{"readyReplicas":1,"availableReplicas":1}}`, recorder.resourceUID)
	case "/apis/apps/v1/namespaces/default/deployments/payment-api":
		_, _ = fmt.Fprintf(response, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"payment-api","namespace":"default","uid":%q,"resourceVersion":"29"},"spec":{"replicas":1},"status":{"readyReplicas":1,"availableReplicas":1}}`, recorder.resourceUID)
	default:
		http.NotFound(response, request)
	}
}

func (recorder *integrationActionRecorder) reset() {
	recorder.mu.Lock()
	recorder.actions = nil
	recorder.mu.Unlock()
}

func (recorder *integrationActionRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.actions...)
}

func writeIntegrationKubeconfig(t *testing.T, path, server string, certificateAuthority []byte, currentContext string) {
	t.Helper()
	config := clientcmdapi.NewConfig()
	config.CurrentContext = currentContext
	config.Clusters["local"] = &clientcmdapi.Cluster{Server: server, CertificateAuthorityData: certificateAuthority}
	config.AuthInfos["local"] = &clientcmdapi.AuthInfo{}
	config.Contexts["current-context"] = &clientcmdapi.Context{
		Cluster: "local", AuthInfo: "local", Namespace: "default",
	}
	config.Contexts["saved-context"] = &clientcmdapi.Context{
		Cluster: "local", AuthInfo: "local", Namespace: "payments",
	}
	if err := clientcmd.WriteToFile(*config, path); err != nil {
		t.Fatalf("clientcmd.WriteToFile() error = %v", err)
	}
}

func seedIntegrationResumeHistory(
	t *testing.T,
	ctx context.Context,
	sessions *sqlite.SessionRepository,
	messages *sqlite.MessageRepository,
	now time.Time,
) {
	seedIntegrationResumeHistoryForScope(
		t, ctx, sessions, messages, now,
		domain.ScopeCandidate{Context: "saved-context", Namespace: "payments"},
		"deployment-uid",
	)
}

func seedIntegrationResumeHistoryForScope(
	t *testing.T,
	ctx context.Context,
	sessions *sqlite.SessionRepository,
	messages *sqlite.MessageRepository,
	now time.Time,
	scope domain.ScopeCandidate,
	resourceUID string,
) {
	t.Helper()
	reference := domain.ResourceRef{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: scope.Namespace,
		Name: "payment-api", UID: resourceUID, ResourceVersion: "18",
	}
	session := domain.Session{
		ID: integrationSessionID, Title: "Historic diagnosis", Status: domain.SessionStatusActive,
		PrivacyMode: domain.PrivacyModeStandard, LastScope: &scope, SelectedResource: &reference,
		Version: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	if err := sessions.Create(ctx, session); err != nil {
		t.Fatalf("SessionRepository.Create() error = %v", err)
	}
	snapshot := domain.ScopeSnapshot{Context: scope.Context, Namespace: scope.Namespace, Generation: 4}
	content := "Historic diagnosis content."
	message := domain.Message{
		ID: integrationMessageID, SessionID: session.ID, Role: domain.MessageRoleAssistant,
		Content: content, Format: domain.MessageFormatMarkdown, Status: domain.MessageStatusCommitted,
		Scope: &snapshot, Resource: &reference, Hash: domain.MessageContentHash(content), CreatedAt: now,
	}
	if err := messages.Append(ctx, message); err != nil {
		t.Fatalf("MessageRepository.Append() error = %v", err)
	}
}

func seedIntegrationCompletedTurn(
	t *testing.T,
	ctx context.Context,
	runs *sqlite.AgentRunRepository,
	now time.Time,
	scopeCandidate domain.ScopeCandidate,
) {
	t.Helper()
	runID := domain.AgentRunID("0198a46e-7d2a-7d34-9b6f-2df5f45a2c01")
	requestID := domain.MessageID("0198a46e-7d2a-7d34-9b6f-2df5f45a2c02")
	answerID := domain.MessageID("0198a46e-7d2a-7d34-9b6f-2df5f45a2c03")
	startedAt := now.Add(time.Millisecond)
	finishedAt := startedAt.Add(time.Millisecond)
	scope := domain.ScopeSnapshot{Context: scopeCandidate.Context, Namespace: scopeCandidate.Namespace, Generation: 4}
	question := "Durable prior question."
	request := domain.Message{
		ID: requestID, SessionID: integrationSessionID, RunID: &runID,
		Role: domain.MessageRoleUser, Content: question, Format: domain.MessageFormatPlain,
		Status: domain.MessageStatusCommitted, Scope: &scope, Hash: domain.MessageContentHash(question), CreatedAt: startedAt,
	}
	running := domain.AgentRun{
		ID: runID, SessionID: integrationSessionID, RequestMessageID: requestID,
		Status: domain.AgentRunStatusRunning, Scope: scope,
		PromptVersion: agent.SystemPromptVersion, ToolCatalogVersion: agent.ToolCatalogVersion, StartedAt: &startedAt,
	}
	if err := runs.Begin(ctx, request, running); err != nil {
		t.Fatalf("seed AgentRun Begin() error = %v", err)
	}
	terminal := running
	terminal.Status = domain.AgentRunStatusCompleted
	terminal.FinishedAt = &finishedAt
	answer := "Durable prior final answer."
	assistant := domain.Message{
		ID: answerID, SessionID: integrationSessionID, RunID: &runID,
		Role: domain.MessageRoleAssistant, Content: answer, Format: domain.MessageFormatMarkdown,
		Status: domain.MessageStatusCommitted, Scope: &scope, Hash: domain.MessageContentHash(answer), CreatedAt: finishedAt,
	}
	if err := runs.FinishWithMessage(ctx, assistant, terminal); err != nil {
		t.Fatalf("seed AgentRun FinishWithMessage() error = %v", err)
	}
}

func sessionServiceForIntegration(
	sessions *sqlite.SessionRepository,
	messages *sqlite.MessageRepository,
	runs *sqlite.AgentRunRepository,
	database *sqlite.DB,
) *applicationSessionAdapter {
	return &applicationSessionAdapter{
		service:   sessioncontract.NewService(sessions, sessions, messages, runs, runs),
		retention: sqlite.NewRetentionRepository(database),
	}
}

type integrationRunner struct {
	calls  atomic.Int64
	mu     sync.Mutex
	inputs []agent.RunInput
	now    func() time.Time
}

func (runner *integrationRunner) Run(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
	runner.calls.Add(1)
	runner.mu.Lock()
	runner.inputs = append(runner.inputs, input)
	runner.mu.Unlock()
	publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, runner.now, sink)
	if err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	diagnosis := domain.Diagnosis{
		ID: "0198a46e-7d2a-7d34-9b6f-2df5f45a2d01", RunID: input.RunID(), Scope: input.Scope().Snapshot(),
		AnswerMarkdown: "The resumed context was observed by the scripted Agent.", CreatedAt: runner.now(),
	}
	if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis}); err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunCompleted}); err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	return agent.RunOutcome{Status: domain.AgentRunStatusCompleted, Diagnosis: &diagnosis}
}

func (runner *integrationRunner) Inputs() []agent.RunInput {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]agent.RunInput(nil), runner.inputs...)
}

type integrationUIEvents struct{}

func (integrationUIEvents) PublishUIEvent(context.Context, application.UIEvent) error { return nil }
