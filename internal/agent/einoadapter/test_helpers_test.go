package einoadapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	testRunID        domain.AgentRunID = "00000000-0000-7000-8000-000000008001"
	testSessionID    domain.SessionID  = "00000000-0000-7000-8000-000000008002"
	testMessageID    domain.MessageID  = "00000000-0000-7000-8000-000000008003"
	testEvidenceID   domain.EvidenceID = "00000000-0000-7000-8000-000000008006"
	secondEvidenceID domain.EvidenceID = "00000000-0000-7000-8000-000000008007"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.UnixMilli(10_000).UTC()}
}

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *testClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration).Truncate(time.Millisecond)
}

type testIdentifiers struct {
	mu      sync.Mutex
	counter uint64
}

func (source *testIdentifiers) next() string {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.counter++
	return fmt.Sprintf("00000000-0000-7000-8000-%012x", 0x8100+source.counter)
}

func (source *testIdentifiers) NewModelRequestID() (domain.ModelRequestID, error) {
	return domain.ModelRequestID(source.next()), nil
}

func (source *testIdentifiers) NewToolInvocationID() (domain.ToolInvocationID, error) {
	return domain.ToolInvocationID(source.next()), nil
}

func (source *testIdentifiers) NewDiagnosisID() (domain.DiagnosisID, error) {
	return domain.DiagnosisID(source.next()), nil
}

type testScopeGuard struct {
	mu      sync.Mutex
	current bool
	checks  int
}

func newTestScopeGuard() *testScopeGuard {
	return &testScopeGuard{current: true}
}

func (guard *testScopeGuard) Current(ctx context.Context, scope domain.ClusterScope) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.checks++
	return ctx.Err() == nil && guard.current && scope.Generation == 7
}

func (guard *testScopeGuard) SetCurrent(current bool) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.current = current
}

func (guard *testScopeGuard) Checks() int {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return guard.checks
}

type modelScript func(context.Context, domain.ModelRequest, agent.ModelStreamConsumer) *domain.ModelError

type recordingModel struct {
	mu        sync.Mutex
	scripts   []modelScript
	requests  []domain.ModelRequest
	active    int
	maxActive int
}

func (model *recordingModel) Stream(ctx context.Context, request domain.ModelRequest, consume agent.ModelStreamConsumer) *domain.ModelError {
	model.mu.Lock()
	call := len(model.requests)
	model.requests = append(model.requests, request)
	model.active++
	if model.active > model.maxActive {
		model.maxActive = model.active
	}
	var script modelScript
	if call < len(model.scripts) {
		script = model.scripts[call]
	}
	model.mu.Unlock()
	defer func() {
		model.mu.Lock()
		model.active--
		model.mu.Unlock()
	}()
	if script == nil {
		return domain.NewModelError(domain.ModelErrorCodeInternal, domain.ModelOperationStream, string(request.ID))
	}
	return script(ctx, request, consume)
}

func (model *recordingModel) Requests() []domain.ModelRequest {
	model.mu.Lock()
	defer model.mu.Unlock()
	return append([]domain.ModelRequest(nil), model.requests...)
}

func (model *recordingModel) MaxActive() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.maxActive
}

type recordingTool struct {
	mu        sync.Mutex
	calls     []agent.BoundToolCall
	active    int
	maxActive int
	execute   func(context.Context, agent.BoundToolCall) domain.ToolResult
}

func (tool *recordingTool) Execute(ctx context.Context, call agent.BoundToolCall) domain.ToolResult {
	tool.mu.Lock()
	tool.calls = append(tool.calls, call)
	tool.active++
	if tool.active > tool.maxActive {
		tool.maxActive = tool.active
	}
	tool.mu.Unlock()
	defer func() {
		tool.mu.Lock()
		tool.active--
		tool.mu.Unlock()
	}()
	return tool.execute(ctx, call)
}

func (tool *recordingTool) Calls() []agent.BoundToolCall {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return append([]agent.BoundToolCall(nil), tool.calls...)
}

func (tool *recordingTool) MaxActive() int {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return tool.maxActive
}

func fixedHandlers(tool agent.Tool) agent.ToolHandlers {
	return agent.ToolHandlers{
		GetResource:         tool,
		ListResources:       tool,
		GetEvents:           tool,
		GetPodLogs:          tool,
		GetPreviousPodLogs:  tool,
		GetRelatedResources: tool,
		GetClusterOverview:  tool,
	}
}

type eventRecorder struct {
	mu     sync.Mutex
	events []agent.RunEvent
	result agent.EventSinkResult
}

func newEventRecorder() *eventRecorder {
	return &eventRecorder{result: agent.EventSinkAccepted}
}

func (recorder *eventRecorder) Publish(_ context.Context, event agent.RunEvent) agent.EventSinkResult {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return recorder.result
}

func (recorder *eventRecorder) Events() []agent.RunEvent {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]agent.RunEvent(nil), recorder.events...)
}

func testInput(t *testing.T, clock *testClock, limits agent.RunBudgetLimits) agent.RunInput {
	t.Helper()
	input, err := agent.NewRunInput(
		testRunID,
		testSessionID,
		testMessageID,
		"Why is the selected Pod not Ready?",
		domain.ClusterScope{
			Context:         "test-context",
			Namespace:       "test-namespace",
			NamespaceAccess: domain.NamespaceAccessCurrent,
			Generation:      7,
			ActivatedAt:     clock.Now(),
		},
		&domain.ResourceRef{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  "test-namespace",
			Name:       "sample-pod",
		},
		limits,
	)
	if err != nil {
		t.Fatalf("NewRunInput() error = %v", err)
	}
	return input
}

func testAdapter(t *testing.T, clock *testClock, model agent.Model, tool agent.Tool, guard agent.RunScopeGuard) *Adapter {
	t.Helper()
	adapter, err := New(Config{
		Model:       model,
		Tools:       fixedHandlers(tool),
		ScopeGuard:  guard,
		Identifiers: &testIdentifiers{},
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adapter
}

func scriptedEvents(events ...domain.ModelStreamEvent) modelScript {
	return func(ctx context.Context, request domain.ModelRequest, consume agent.ModelStreamConsumer) *domain.ModelError {
		for _, event := range events {
			if err := ctx.Err(); err != nil {
				return domain.NewModelError(domain.ModelErrorCodeCancelled, domain.ModelOperationStream, string(request.ID))
			}
			consume(event)
		}
		return nil
	}
}

func toolCallEvents(calls ...domain.ModelToolCall) []domain.ModelStreamEvent {
	events := make([]domain.ModelStreamEvent, 0, len(calls)+1)
	for index, call := range calls {
		events = append(events, domain.ModelStreamEvent{
			Sequence: index + 1,
			Kind:     domain.ModelStreamEventToolCallFragment,
			ToolCallFragment: &domain.ModelToolCallFragment{
				Index:             index,
				IDFragment:        call.ID,
				NameFragment:      string(call.Name),
				ArgumentsFragment: call.ArgumentsJSON,
			},
		})
	}
	events = append(events, domain.ModelStreamEvent{
		Sequence: len(calls) + 1,
		Kind:     domain.ModelStreamEventCompleted,
		Completion: &domain.ModelCompletion{
			FinishReason: domain.ModelFinishReasonToolCalls,
		},
	})
	return events
}

func diagnosisEvents(content string) []domain.ModelStreamEvent {
	return []domain.ModelStreamEvent{
		{Sequence: 1, Kind: domain.ModelStreamEventTextDelta, TextDelta: content},
		{Sequence: 2, Kind: domain.ModelStreamEventCompleted, Completion: &domain.ModelCompletion{FinishReason: domain.ModelFinishReasonStop}},
	}
}

func resourceCall(id, podName string) domain.ModelToolCall {
	return domain.ModelToolCall{
		ID:            id,
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"purpose":"Inspect the selected Pod.","resource":{"kind":"Pod","name":"` + podName + `"}}`,
	}
}

func eventsCall(id, podName string) domain.ModelToolCall {
	return domain.ModelToolCall{
		ID:            id,
		Name:          domain.ToolNameGetEvents,
		ArgumentsJSON: `{"purpose":"Inspect recent Events.","resource":{"kind":"Pod","name":"` + podName + `"}}`,
	}
}

func successfulToolResult(t *testing.T, call agent.BoundToolCall, evidenceID domain.EvidenceID, observedAt time.Time, data string) domain.ToolResult {
	t.Helper()
	evidence := domain.Evidence{
		ID:           evidenceID,
		RunID:        call.RunID(),
		InvocationID: call.InvocationID(),
		Category:     domain.EvidenceCategoryCondition,
		Scope:        call.Scope().Snapshot(),
		Resource: domain.ResourceRef{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  call.Scope().Namespace,
			Name:       "sample-pod",
		},
		Fact:        "The projected Pod condition is not Ready.",
		Fingerprint: domain.SHA256Hex(string(evidenceID)),
		ObservedAt:  observedAt,
	}
	result := domain.ToolResult{
		InvocationID: call.InvocationID(),
		Name:         call.Name(),
		Version:      call.Version(),
		Scope:        call.Scope().Snapshot(),
		ObservedAt:   observedAt,
		Status:       domain.ToolResultStatusSuccess,
		DataJSON:     data,
		Evidence:     []domain.Evidence{evidence},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ToolResult.Validate() error = %v", err)
	}
	return result
}

func emptyToolResult(t *testing.T, call agent.BoundToolCall, observedAt time.Time) domain.ToolResult {
	t.Helper()
	result := domain.ToolResult{
		InvocationID: call.InvocationID(),
		Name:         call.Name(),
		Version:      call.Version(),
		Scope:        call.Scope().Snapshot(),
		ObservedAt:   observedAt,
		Status:       domain.ToolResultStatusSuccess,
		DataJSON:     `{"items":[]}`,
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ToolResult.Validate() error = %v", err)
	}
	return result
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "model", name))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", name, err)
	}
	return string(content)
}

func assertTerminalSequence(t *testing.T, events []agent.RunEvent) {
	t.Helper()
	terminals := 0
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event[%d].Sequence = %d", index, event.Sequence)
		}
		if event.Terminal() {
			terminals++
			if index != len(events)-1 {
				t.Fatalf("terminal event at index %d of %d", index, len(events))
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal event count = %d", terminals)
	}
}
