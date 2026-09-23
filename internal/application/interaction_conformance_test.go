package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/security"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const interactionGreeting = `{"answer_markdown":"Hello from the fixture.","evidence_citations":[],"proposed_actions":[],"response_schema_version":1,"outcome":"answer","limitations":[],"questions":[]}`

type interactionStep struct {
	tool      domain.ToolName
	arguments string
	final     string
	provider  string
	entered   chan struct{}
	release   <-chan struct{}
}

// interactionModel records only synthetic traffic and has no live endpoint.
type interactionModel struct {
	responses bool
	mu        sync.Mutex
	steps     []interactionStep
	requests  []integrationModelRequest
	bytes     int
}

func (model *interactionModel) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, 1024*1024))
	var captured integrationModelRequest
	if err != nil || json.Unmarshal(body, &captured) != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if model.responses {
		var native struct {
			Input []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if json.Unmarshal(body, &native) != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, item := range native.Input {
			if item.Role == "" {
				continue
			}
			var content string
			if json.Unmarshal(item.Content, &content) != nil {
				var parts []struct{ Text string }
				if json.Unmarshal(item.Content, &parts) != nil {
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				for _, part := range parts {
					content += part.Text
				}
			}
			captured.Messages = append(captured.Messages, integrationModelMessage{Role: item.Role, Content: content})
		}
	}
	model.mu.Lock()
	index := len(model.requests)
	model.requests = append(model.requests, captured)
	model.bytes += len(body)
	step := interactionStep{provider: "unexpected"}
	if index < len(model.steps) {
		step = model.steps[index]
	}
	model.mu.Unlock()
	if step.entered != nil {
		close(step.entered)
	}
	if step.release != nil {
		select {
		case <-step.release:
		case <-request.Context().Done():
			return
		}
	}
	if step.provider == "transport" || step.provider == "unexpected" {
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if step.provider == "timeout" {
		<-request.Context().Done()
		return
	}
	if model.responses {
		writer.Header().Set("Content-Type", "application/json")
		if step.provider == "malformed" {
			_, _ = io.WriteString(writer, "{invalid}")
			return
		}
		output := fmt.Sprintf(`[{"type":"message","id":"msg_fixture","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}]`, step.final)
		if step.tool != "" {
			arguments := `{"detail":"summary","name":"synthetic-ns","namespace":null,"purpose":"Inspect the synthetic Namespace.","resource_type":"namespaces"}`
			if step.tool == domain.ToolNameListResources {
				arguments = `{"filters":[],"format":"list","limit":20,"namespace":null,"purpose":"List synthetic Namespaces.","resource_type":"namespaces"}`
			}
			if step.arguments != "" {
				arguments = step.arguments
			}
			output = fmt.Sprintf(`[{"type":"function_call","id":"fc_%d","call_id":"call-%d","name":%q,"arguments":%q,"status":"completed"}]`, index, index, step.tool, arguments)
		}
		_, _ = fmt.Fprintf(writer, `{"id":"resp_fixture","status":"completed","output":%s}`, output)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	if step.provider == "malformed" {
		_, _ = io.WriteString(writer, "data: {invalid}\n\n")
		return
	}
	if step.tool != "" {
		arguments := `{"detail":"summary","name":"synthetic-ns","namespace":null,"purpose":"Inspect the synthetic Namespace.","resource_type":"namespaces"}`
		if step.tool == domain.ToolNameListResources {
			arguments = `{"filters":[],"format":"list","limit":20,"namespace":null,"purpose":"List synthetic Namespaces.","resource_type":"namespaces"}`
		}
		if step.arguments != "" {
			arguments = step.arguments
		}
		call := fmt.Sprintf(`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-%d","type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":null}]}`, index, step.tool, arguments)
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", call)
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	} else {
		for _, fragment := range integrationModelFragments(step.final, 37) {
			_, _ = fmt.Fprintf(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", fragment)
		}
		finish := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
		_, _ = io.WriteString(writer, finish)
		if step.provider == "duplicate_finish" {
			_, _ = io.WriteString(writer, finish)
		}
		if step.provider == "after_finish" {
			_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"late\"},\"finish_reason\":null}]}\n\n")
		}
	}
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
}

func (model *interactionModel) snapshot() []integrationModelRequest {
	model.mu.Lock()
	defer model.mu.Unlock()
	return append([]integrationModelRequest(nil), model.requests...)
}

// interactionTool has no Kubernetes or other external port. The production
// binder, budget, registry, Application event acceptance, and SQLite remain real.
type interactionTool struct {
	mu       sync.Mutex
	calls    []agent.BoundToolCall
	evidence []domain.Evidence
	mode     string
	clock    *integrationClock
	entered  chan struct{}
	release  <-chan struct{}
}

func (tool *interactionTool) Execute(ctx context.Context, call agent.BoundToolCall) domain.ToolResult {
	tool.mu.Lock()
	index := len(tool.calls)
	tool.calls = append(tool.calls, call)
	tool.mu.Unlock()
	if tool.entered != nil {
		close(tool.entered)
	}
	if tool.release != nil {
		select {
		case <-tool.release:
		case <-ctx.Done():
			return domain.ToolResult{}
		}
	}
	if tool.mode == "timeout" {
		<-ctx.Done()
		return domain.ToolResult{}
	}
	observed := tool.clock.Now()
	result := domain.ToolResult{InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(), ObservedAt: observed, Status: domain.ToolResultStatusSuccess, DataJSON: `{"items":[]}`}
	if tool.mode == "invalid" {
		result.InvocationID = ""
		return result
	}
	if tool.mode == "empty" {
		return result
	}
	if tool.mode == "deny_logs" && call.Name() == domain.ToolNameGetPodLogs {
		result.Status = domain.ToolResultStatusDenied
		result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassPolicyDenied, SafeMessage: "The cluster-read request was blocked by the fixed policy."}
		return result
	}
	if tool.mode == "unavailable" {
		result.Status = domain.ToolResultStatusError
		result.Error = &domain.ToolResultError{Class: domain.SafeErrorClassUnavailable, SafeMessage: "The synthetic source is unavailable."}
		return result
	}
	id := domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 9500+index))
	item := domain.Evidence{ID: id, RunID: call.RunID(), InvocationID: call.InvocationID(), Category: domain.EvidenceCategoryCondition, Scope: call.Scope().Snapshot(), PolicyVersion: domain.ResourcePolicyVersion, PolicyGeneration: call.PolicyGeneration(), Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Namespace", Name: "synthetic-ns"}, Fact: "The synthetic Namespace is Active.", Fingerprint: domain.SHA256Hex(string(id)), ObservedAt: observed}
	if tool.mode == "partial" {
		item.Partial = true
		result.Status = domain.ToolResultStatusPartial
	}
	if tool.mode == "truncated" {
		result.Status = domain.ToolResultStatusPartial
		item.Truncated = true
		result.Truncation = domain.ToolResultTruncation{Truncated: true, Reason: "item_limit", ReturnedCount: 1}
	}
	result.Evidence = []domain.Evidence{item}
	result.DataJSON = `{"items":[{"name":"synthetic-ns","phase":"Active"}]}`
	tool.mu.Lock()
	tool.evidence = append(tool.evidence, item)
	tool.mu.Unlock()
	return result
}

func (tool *interactionTool) snapshot() ([]agent.BoundToolCall, []domain.Evidence) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return append([]agent.BoundToolCall(nil), tool.calls...), append([]domain.Evidence(nil), tool.evidence...)
}

type interactionRunStore struct {
	application.RunPersistence
	failBegin    bool
	failComplete bool
}

func (store interactionRunStore) BeginWithAudit(ctx context.Context, message domain.Message, run domain.AgentRun, audit domain.AuditEvent) error {
	if store.failBegin {
		return errors.New("synthetic begin failure")
	}
	return store.RunPersistence.BeginWithAudit(ctx, message, run, audit)
}

func (store interactionRunStore) CompleteWithAudit(ctx context.Context, diagnosis domain.Diagnosis, message domain.Message, run domain.AgentRun, audit domain.AuditEvent) error {
	if store.failComplete {
		return errors.New("synthetic commit failure")
	}
	return store.RunPersistence.CompleteWithAudit(ctx, diagnosis, message, run, audit)
}

type interactionHarness struct {
	configuration application.CoordinatorConfig
	scope         *interactionScope
	policies      *interactionPolicies
	database      *sqlite.DB
	coordinator   *application.Coordinator
	session       domain.Session
	model         *interactionModel
	tool          *interactionTool
	events        *integrationUIEvents
	messages      *sqlite.MessageRepository
	runs          *sqlite.AgentRunRepository
	diagnoses     *sqlite.DiagnosisRepository
}

type interactionRuntime struct {
	*application.EinoRuntime
	origin string
}

func (runtime interactionRuntime) Origin() string { return runtime.origin }
func (interactionRuntime) ModelName() string      { return "synthetic-model" }
func (interactionRuntime) ProfileName() string    { return "agent" }

type interactionScenario struct {
	responses    bool
	name         string
	steps        []interactionStep
	toolMode     string
	wantReason   domain.RunTerminalReason
	wantFailure  domain.InteractionFailure
	wantTools    int
	wantMessages int
	plan         bool
	history      bool
	failBegin    bool
	failComplete bool
	resume       bool
}

type interactionScope struct{ *integrationScope }

func (scope *interactionScope) CurrentScope() (domain.ClusterScope, bool) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.scope, true
}

func (scope *interactionScope) Current(ctx context.Context, value domain.ClusterScope) bool {
	current, ok := scope.CurrentScope()
	return ctx.Err() == nil && ok && current == value
}

func (scope *interactionScope) invalidate() {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.scope.Generation++
}

type interactionPolicies struct{ stale atomic.Bool }

func (policies *interactionPolicies) ResourcePolicySnapshot(ctx context.Context) (domain.ResourcePolicyCatalog, domain.PolicyGeneration, bool) {
	return integrationResourcePolicies{}.ResourcePolicySnapshot(ctx)
}

func (policies *interactionPolicies) CurrentPolicyGeneration(ctx context.Context, generation domain.PolicyGeneration) bool {
	return !policies.stale.Load() && integrationResourcePolicies{}.CurrentPolicyGeneration(ctx, generation)
}

func newInteractionHarness(t *testing.T, scenario interactionScenario) *interactionHarness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(context.Background(), sqlite.OpenOptions{StateDir: filepath.Join(root, "state"), ApplicationVersion: "conformance-test", CorrelationID: "conformance"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	clock := newIntegrationClock()
	ids := newIntegrationIDs()
	scope := &interactionScope{newIntegrationScope()}
	var activeScope interface {
		application.ActiveScope
		agent.RunScopeGuard
	} = scope
	var scopeManager *application.ScopeManager
	if scenario.resume {
		ports := new(interactionScopePorts)
		scopeManager, err = application.NewScopeManager(ports, ports, ports, ports, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = scopeManager.SwitchContext(context.Background(), "test-context", 0); err != nil {
			t.Fatal(err)
		}
		activeScope = scopeManager
		t.Cleanup(func() { _ = scopeManager.Close() })
	}
	model := &interactionModel{steps: scenario.steps, responses: scenario.responses}
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)
	privacy, err := application.NewPrivacyManager(application.PrivacyManagerConfig{Store: sqlite.NewPrivacyRepository(db), Origin: server.URL, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	tool := &interactionTool{clock: clock, mode: scenario.toolMode}
	handlers := agent.ToolHandlers{GetResource: tool, ListResources: tool, GetEvents: tool, GetPodLogs: tool, GetPreviousPodLogs: tool, GetPodMetrics: tool, GetNodeMetrics: tool, QueryPrometheus: tool, QueryLoki: tool, GetRelatedResources: tool, GetClusterOverview: tool}
	credential, err := config.NewSecretValue("synthetic-conformance-credential-9500")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := application.NewEinoRuntime(application.EinoConfig{ModelConfiguration: domain.ModelConfiguration{ProfileName: "agent", Role: domain.ModelRoleAgent, ProviderKind: domain.ModelProviderOpenAI, Endpoint: server.URL + "/v1", Origin: server.URL, Model: "synthetic-model", ResponseFormat: domain.ModelResponseFormatPrompt, APIKeySource: domain.ModelAPIKeySourceRuntime, Temperature: integrationTemperature(0.1), MaxOutputTokens: 2048, RequestTimeout: time.Second, StreamingRequired: !scenario.responses, APIProtocol: interactionProtocol(scenario.responses), ToolCallingRequired: true, TransportPolicy: domain.ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP}, Credential: &credential, Tools: handlers, ScopeGuard: activeScope, Identifiers: ids, Now: clock.Now})
	if err != nil {
		credential.Destroy()
		t.Fatal(err)
	}
	t.Cleanup(adapter.Close)
	sessions := sqlite.NewSessionRepository(db)
	messages := sqlite.NewMessageRepository(db)
	runs := sqlite.NewAgentRunRepository(db)
	events := newIntegrationUIEvents()
	limits := agent.DefaultRunBudgetLimits()
	if scenario.toolMode == "timeout" {
		limits.ToolRequestTimeout = time.Nanosecond
	}
	policies := new(interactionPolicies)
	configuration := application.CoordinatorConfig{Sessions: sessions, Runs: interactionRunStore{RunPersistence: runs, failBegin: scenario.failBegin, failComplete: scenario.failComplete}, RunInputs: runs, Tools: sqlite.NewToolInvocationRepository(db), Audits: sqlite.NewAuditRepository(db), Scope: activeScope, ModelRuntime: interactionRuntime{EinoRuntime: adapter, origin: server.URL}, Identifiers: ids, AuditIdentifiers: ids, Questions: security.NewRedactor(), Privacy: privacy, RunResourcePolicies: policies, ModelContext: messages, UIEvents: events, Observer: newIntegrationObserver(), Now: clock.Now, BudgetLimits: limits}
	if scenario.resume {
		service := interactionSessionAdapter{service: sessioncontract.NewService(sessions, sessions, messages, runs, runs)}
		configuration.UI = &application.CoordinatorUIConfig{Sessions: service, Search: sessions, Titles: service, Startup: service, Scopes: scopeManager, ScopePreferences: sqlite.NewScopePreferenceRepository(db)}
	}
	coordinator, err := application.NewCoordinator(configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := coordinator.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	session, err := coordinator.CreateSession(context.Background(), application.CreateSessionCommand{PrivacyMode: domain.PrivacyModeStandard})
	if err != nil {
		t.Fatal(err)
	}
	review, err := coordinator.ExecuteUICommand(context.Background(), application.UICommand{Kind: application.UICommandShowPrivacy, RequestID: 95})
	if err != nil || review.Privacy == nil {
		t.Fatalf("privacy review = %v", err)
	}
	if _, err = coordinator.ExecuteUICommand(context.Background(), application.UICommand{Kind: application.UICommandAcceptPrivacy, RequestID: 95, PrivacyRevision: review.Privacy.Revision}); err != nil {
		t.Fatal(err)
	}
	return &interactionHarness{configuration: configuration, scope: scope, policies: policies, database: db, coordinator: coordinator, session: session, model: model, tool: tool, events: events, messages: messages, runs: runs, diagnoses: sqlite.NewDiagnosisRepository(db)}
}

func interactionFinal(claims string) string {
	return strings.Replace(strings.Replace(interactionGreeting, "Hello from the fixture.", "Synthetic Namespace observations are available.", 1), `"evidence_citations":[]`, `"evidence_citations":[`+claims+`]`, 1)
}

func interactionClaim(number int, text string) string {
	id := domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 9500+number))
	return fmt.Sprintf(`{"claim":%q,"claim_type":"current_observation","evidence_ids":[%q]}`, text, agent.ModelEvidenceReference(id))
}

func (harness *interactionHarness) run(t *testing.T, question string) (domain.AgentRunID, application.RunResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runID, err := harness.coordinator.StartRun(ctx, application.StartRunCommand{SessionID: harness.session.ID, Question: question})
	if err != nil {
		t.Fatal(err)
	}
	result, err := harness.coordinator.WaitRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return runID, result
}

func TestInteractionCompositionScenarioMatrix(t *testing.T) {
	list := interactionStep{tool: domain.ToolNameListResources}
	inspect := interactionStep{tool: domain.ToolNameGetResource}
	answer := interactionStep{final: interactionFinal(interactionClaim(0, "The Namespace is Active."))}
	clarification := strings.Replace(strings.Replace(interactionGreeting, `"outcome":"answer"`, `"outcome":"needs_user_input"`, 1), `"questions":[]`, `"questions":[{"kind":"choice","prompt":"Which namespace?","choices":[{"label":"Working namespace"},{"label":"Another namespace"}]}]`, 1)
	cases := []interactionScenario{
		{name: "greeting", steps: []interactionStep{{final: interactionGreeting}}, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "single safe read", steps: []interactionStep{inspect, answer}, wantTools: 1, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "list to final", steps: []interactionStep{list, answer}, wantTools: 1, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "list inspect final", steps: []interactionStep{list, inspect, {final: interactionFinal(interactionClaim(1, "The inspected Namespace is Active."))}}, wantTools: 2, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "empty result", steps: []interactionStep{list, {final: interactionGreeting}}, toolMode: "empty", wantTools: 1, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "partial result", steps: []interactionStep{list, answer}, toolMode: "partial", wantTools: 1, wantReason: domain.RunTerminalPartialResult, wantMessages: 2},
		{name: "truncated result", steps: []interactionStep{list, answer}, toolMode: "truncated", wantTools: 1, wantReason: domain.RunTerminalPartialResult, wantMessages: 2},
		{name: "unavailable source", steps: []interactionStep{list, {final: interactionGreeting}}, toolMode: "unavailable", wantTools: 1, wantReason: domain.RunTerminalSourceUnavailable, wantMessages: 2},
		{name: "two Evidence two claims", steps: []interactionStep{list, inspect, {final: interactionFinal(interactionClaim(0, "The listed Namespace is Active.") + "," + interactionClaim(1, "The inspected Namespace is Active."))}}, wantTools: 2, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "retained history and reported path", steps: []interactionStep{{final: interactionGreeting}, list, inspect, {final: interactionFinal(interactionClaim(1, "The inspected Namespace is Active."))}}, history: true, wantTools: 2, wantReason: domain.RunTerminalCompleted, wantMessages: 4},
		{name: "typed clarification", steps: []interactionStep{{final: clarification}}, wantReason: domain.RunTerminalNeedsUserInput, wantMessages: 2},
		{name: "plan only", steps: []interactionStep{{final: `{"schema_version":1,"title":"Bounded plan","steps":[{"description":"Inspect one admitted resource."}],"limitations":[],"evidence_citations":[]}`}}, plan: true, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "Tool timeout", steps: []interactionStep{list}, toolMode: "timeout", wantTools: 1, wantReason: domain.RunTerminalTimedOut, wantFailure: domain.FailureToolTimeout, wantMessages: 1},
		{name: "Tool result malformed", steps: []interactionStep{list}, toolMode: "invalid", wantTools: 1, wantReason: domain.RunTerminalFailed, wantFailure: domain.FailureToolResult, wantMessages: 1},
		{name: "malformed provider", steps: []interactionStep{{provider: "malformed"}}, wantReason: domain.RunTerminalFailed, wantFailure: domain.FailureStreamMalformed, wantMessages: 1},
		{name: "duplicate provider finish", steps: []interactionStep{{final: interactionGreeting, provider: "duplicate_finish"}}, wantReason: domain.RunTerminalFailed, wantFailure: domain.FailureStreamDuplicate, wantMessages: 1},
		{name: "out of order provider event", steps: []interactionStep{{final: interactionGreeting, provider: "after_finish"}}, wantReason: domain.RunTerminalFailed, wantFailure: domain.FailureStreamAfterFinish, wantMessages: 1},
		{name: "invalid Evidence reference", steps: []interactionStep{list, {final: interactionFinal(interactionClaim(99, "An unsupported current observation."))}}, wantTools: 1, wantReason: domain.RunTerminalFailed, wantFailure: domain.FailureEvidenceUnknown, wantMessages: 1},
		{name: "presentation order", steps: []interactionStep{{final: `{"questions":[],"limitations":[],"outcome":"answer","response_schema_version":1,"proposed_actions":[],"evidence_citations":[],"answer_markdown":"Hello from the fixture."}`}}, wantReason: domain.RunTerminalCompleted, wantMessages: 2},
		{name: "commit failure", steps: []interactionStep{{final: interactionGreeting}}, failComplete: true, wantReason: domain.RunTerminalPersistenceDegraded, wantFailure: domain.FailurePersistence, wantMessages: 1},
	}
	for _, native := range []bool{false, true} {
		for _, scenario := range cases {
			if native && (scenario.name == "duplicate provider finish" || scenario.name == "out of order provider event") {
				continue
			}
			scenario.responses = native
			t.Run(fmt.Sprintf("responses=%t/%s", native, scenario.name), func(t *testing.T) {
				harness := newInteractionHarness(t, scenario)
				if scenario.history {
					_, result := harness.run(t, "\u4f60\u597d")
					if result.TerminalReason != domain.RunTerminalCompleted {
						t.Fatalf("greeting = %#v", result)
					}
				}
				if scenario.plan {
					outcome, err := harness.coordinator.ExecuteUICommand(context.Background(), application.UICommand{Kind: application.UICommandArmPlan, RequestID: 96})
					if err != nil || outcome.Failure != "" {
						t.Fatalf("plan = %#v, %v", outcome, err)
					}
				}
				runID, result := harness.run(t, "\u73b0\u5728\u96c6\u7fa4\u6709\u54ea\u4e9b ns")
				if result.TerminalReason != scenario.wantReason || result.Diagnostic != scenario.wantFailure {
					t.Fatalf("terminal = %#v, want %s/%s", result, scenario.wantReason, scenario.wantFailure)
				}
				requests := harness.model.snapshot()
				calls, evidence := harness.tool.snapshot()
				if len(requests) != len(scenario.steps) || len(calls) != scenario.wantTools {
					t.Fatalf("model/Tool/Kubernetes calls = %d/%d/0, want %d/%d/0", len(requests), len(calls), len(scenario.steps), scenario.wantTools)
				}
				for _, call := range calls {
					if call.RunID() != runID || call.Scope().Generation != 7 || call.PolicyGeneration() != 1 {
						t.Fatalf("Tool binding = %#v", call)
					}
				}
				for index, item := range evidence {
					wantID := domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 9500+index))
					if item.ID != wantID || item.Validate() != nil || item.RunID != runID || item.Scope.Generation != 7 || item.PolicyGeneration != 1 {
						t.Fatalf("Evidence binding = %#v", item)
					}
				}
				page, err := harness.messages.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{SessionID: harness.session.ID, Limit: sessioncontract.MaxMessagePageSize})
				if err != nil || len(page.Messages) != scenario.wantMessages {
					t.Fatalf("committed Messages = %d, %v, want %d", len(page.Messages), err, scenario.wantMessages)
				}
				seen := make(map[domain.MessageID]struct{})
				for _, message := range page.Messages {
					if _, duplicate := seen[message.ID]; duplicate {
						t.Fatal("duplicate committed transcript row")
					}
					seen[message.ID] = struct{}{}
					if strings.Contains(message.Content, "evidence_citations") {
						t.Fatal("wire envelope persisted as transcript")
					}
				}
				terminals := 0
				wantText := "Synthetic Namespace observations are available."
				switch scenario.name {
				case "greeting", "empty result", "unavailable source", "presentation order", "commit failure":
					wantText = "Hello from the fixture."
				case "typed clarification":
					wantText = "More information is needed:\n\n1. Which namespace?\n   - 1: Working namespace\n   - 2: Another namespace"
				case "plan only":
					wantText = "## Bounded plan\n\n1. Inspect one admitted resource."
				}
				if scenario.wantFailure.Valid() && scenario.wantFailure != domain.FailurePersistence {
					wantText = scenario.wantFailure.SafeMessage()
					if scenario.wantFailure == domain.FailureToolTimeout {
						wantText = "The diagnostic run reached its time limit."
					}
					if scenario.wantFailure == domain.FailureStreamMalformed {
						wantText = "The model endpoint returned an invalid response stream."
					}
				}
				for _, event := range harness.events.Events() {
					if event.ConversationInput != nil && event.ConversationInput.Status.Queued != 0 {
						t.Fatal("scenario without queued input created a queued successor")
					}
					if event.RunID != runID || !event.Terminal() {
						continue
					}
					terminals++
					if event.Validate() != nil || event.TerminalOutcome == nil || event.TerminalOutcome.Reason != scenario.wantReason || event.TerminalOutcome.Diagnostic != scenario.wantFailure {
						t.Fatalf("terminal TUI projection = %#v", event)
					}
					if event.Text != wantText {
						t.Fatalf("unsafe or inaccurate terminal message = %q", event.Text)
					}
				}
				if terminals != 1 {
					t.Fatalf("terminal rows = %d, want 1", terminals)
				}
				frame := renderIntegrationUI(harness.events.Events())
				// The ordinary narrow row intentionally elides provenance metadata.
				// Inspect a wide frame as well to assert its complete terminal value.
				wideFrame := renderIntegrationUI(harness.events.Events(), 2048)
				if !strings.Contains(wideFrame, "Result: "+strings.ReplaceAll(string(scenario.wantReason), "_", " ")) {
					t.Fatal("TUI did not render the exact terminal state")
				}
				if strings.Contains(frame, "evidence_citations") || strings.Contains(frame, "synthetic-conformance-credential") || strings.Contains(frame, "http://") {
					t.Fatal("TUI exposed wire or endpoint content")
				}
				if scenario.history {
					assertHistoricalResponseProtocol(t, requests[1], "\u73b0\u5728\u96c6\u7fa4\u6709\u54ea\u4e9b ns")
				}
			})
		}
	}
}

func interactionProtocol(native bool) domain.ModelAPIProtocol {
	if native {
		return domain.ModelAPIProtocolResponses
	}
	return domain.ModelAPIProtocolChatCompletions
}

func TestInteractionFollowUpWithDeniedLogs(t *testing.T) {
	for _, responses := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%t", responses), func(t *testing.T) {
			logs := interactionStep{tool: domain.ToolNameGetPodLogs, arguments: `{"container":"app","container_mode":null,"include_ephemeral":null,"include_init":null,"namespace":null,"pod_name":"sample-pod","purpose":"Inspect bounded current logs.","search":null,"since_seconds":null,"tail_lines":null}`}
			harness := newInteractionHarness(t, interactionScenario{responses: responses, toolMode: "deny_logs", steps: []interactionStep{
				{tool: domain.ToolNameGetResource}, logs,
				{final: interactionFinal(interactionClaim(0, "The Namespace is Active."))},
				{tool: domain.ToolNameListResources}, {tool: domain.ToolNameGetResource}, logs,
				{final: interactionFinal(interactionClaim(2, "The listed Namespace is Active.") + "," + interactionClaim(3, "The inspected Namespace is Active.") + `,{"claim":"Application HTTP and caller networking are unverified.","claim_type":"uncertainty","evidence_ids":[]}`)},
			}})
			var lastRun domain.AgentRunID
			for _, question := range []string{"Inspect the current state without changes.", "Explain the current evidence and unverified HTTP and network behavior without changes."} {
				runID, result := harness.run(t, question)
				lastRun = runID
				if result.Diagnostic != "" || result.TerminalReason != domain.RunTerminalPolicyDenied {
					t.Fatalf("run %s = %#v", runID, result)
				}
			}
			page, err := harness.messages.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{SessionID: harness.session.ID, Limit: sessioncontract.MaxMessagePageSize})
			if err != nil || len(page.Messages) != 4 {
				t.Fatalf("committed messages = %d, %v", len(page.Messages), err)
			}
			calls, _ := harness.tool.snapshot()
			if len(calls) != 5 || len(harness.model.snapshot()) != 7 {
				t.Fatalf("Tool/model calls = %d/%d", len(calls), len(harness.model.snapshot()))
			}
			assertHistoricalResponseProtocol(t, harness.model.snapshot()[3], "Explain the current evidence and unverified HTTP and network behavior without changes.")
			terminals := 0
			for _, event := range harness.events.Events() {
				if event.RunID != lastRun || !event.Terminal() {
					continue
				}
				terminals++
				if event.Kind != application.UIEventRunCompleted || event.Validate() != nil || len(event.EvidenceReferences) != 2 ||
					event.AnswerProvenance == nil || event.AnswerProvenance.CheckedSourceCount != 2 || event.AnswerProvenance.UncheckedSourceCount != 1 ||
					event.AnswerProvenance.CoverageState != application.UIAnswerCoveragePartial ||
					!event.AnswerProvenance.HasUncertainty {
					t.Fatalf("follow-up projection = %#v", event)
				}
			}
			if terminals != 1 {
				t.Fatalf("follow-up terminal events = %d, want 1", terminals)
			}
		})
	}
}

func TestInteractionDeniedLogsThenLogBudgetStopsWithoutEventFailure(t *testing.T) {
	for _, responses := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%t", responses), func(t *testing.T) {
			logs := interactionStep{tool: domain.ToolNameGetPodLogs, arguments: `{"container":null,"container_mode":"all","include_ephemeral":false,"include_init":false,"namespace":null,"pod_name":"sample-a","purpose":"Inspect bounded current logs.","search":null,"since_seconds":null,"tail_lines":null}`}
			second := logs
			second.arguments = strings.Replace(logs.arguments, "sample-a", "sample-b", 1)
			harness := newInteractionHarness(t, interactionScenario{responses: responses, toolMode: "deny_logs", steps: []interactionStep{{tool: domain.ToolNameGetResource}, logs, second}})
			runID, result := harness.run(t, "Inspect the available evidence and application logs without changes.")
			if result.Status != domain.AgentRunStatusCompleted || result.TerminalReason != domain.RunTerminalBudgetExhausted || result.Diagnostic != "" {
				t.Fatalf("budget stop = %#v", result)
			}
			calls, _ := harness.tool.snapshot()
			if len(calls) != 2 || len(harness.model.snapshot()) != 3 {
				t.Fatalf("Tool/model calls = %d/%d", len(calls), len(harness.model.snapshot()))
			}
			terminals := 0
			for _, event := range harness.events.Events() {
				if event.RunID != runID || !event.Terminal() {
					continue
				}
				terminals++
				if event.Kind != application.UIEventRunCompleted || event.Validate() != nil || event.AnswerProvenance == nil ||
					event.AnswerProvenance.CheckedSourceCount != 1 || event.AnswerProvenance.UncheckedSourceCount != 1 {
					t.Fatalf("budget stop lost accepted source coverage: %#v", event)
				}
			}
			page, err := harness.messages.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{SessionID: harness.session.ID, Limit: sessioncontract.MaxMessagePageSize})
			if terminals != 1 || err != nil || len(page.Messages) != 2 {
				t.Fatalf("budget terminal/messages = %d/%d, error = %v", terminals, len(page.Messages), err)
			}
		})
	}
}

func TestInteractionCompositionPrecommitFailureMakesZeroRuntimeCalls(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%t", native), func(t *testing.T) { testInteractionPrecommit(t, native) })
	}
}
func testInteractionPrecommit(t *testing.T, native bool) {
	harness := newInteractionHarness(t, interactionScenario{responses: native, failBegin: true})
	_, err := harness.coordinator.StartRun(context.Background(), application.StartRunCommand{SessionID: harness.session.ID, Question: "A bounded question."})
	if !errors.Is(err, application.ErrPersistenceUnavailable) {
		t.Fatalf("precommit error = %v", err)
	}
	calls, evidence := harness.tool.snapshot()
	if len(harness.model.snapshot()) != 0 || len(calls) != 0 || len(evidence) != 0 {
		t.Fatal("precommit failure made runtime calls")
	}
	page, err := harness.messages.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{SessionID: harness.session.ID, Limit: sessioncontract.MaxMessagePageSize})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("precommit ghost Messages = %d, %v", len(page.Messages), err)
	}
}

func integrationTemperature(value float64) *float64 { return &value }
