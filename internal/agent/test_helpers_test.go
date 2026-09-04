package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	testRunID        domain.AgentRunID       = "00000000-0000-7000-8000-000000004001"
	testSessionID    domain.SessionID        = "00000000-0000-7000-8000-000000004002"
	testMessageID    domain.MessageID        = "00000000-0000-7000-8000-000000004003"
	testInvocationID domain.ToolInvocationID = "00000000-0000-7000-8000-000000004005"
	testEvidenceID   domain.EvidenceID       = "00000000-0000-7000-8000-000000004006"
	testDiagnosisID  domain.DiagnosisID      = "00000000-0000-7000-8000-000000004007"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.UnixMilli(1_000).UTC()}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func testRunInput(t *testing.T, question string) RunInput {
	t.Helper()
	clock := newFakeClock()
	input, err := NewRunInput(
		testRunID,
		testSessionID,
		testMessageID,
		question,
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
		DefaultRunBudgetLimits(),
	)
	if err != nil {
		t.Fatalf("NewRunInput() error = %v", err)
	}
	return input
}

func testBoundCall(t *testing.T, input RunInput, invocationID domain.ToolInvocationID, podName string) BoundToolCall {
	t.Helper()
	call, err := BindToolCall(input, invocationID, ToolSelection{
		ID:            "call-1",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"detail":"describe","name":"` + podName + `","namespace":null,"purpose":"Inspect the selected Pod.","resource_type":"pods"}`,
	})
	if err != nil {
		t.Fatalf("BindToolCall() error = %v", err)
	}
	return call
}

func testToolResult(t *testing.T, call BoundToolCall, evidenceID domain.EvidenceID, observedAt time.Time) domain.ToolResult {
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
		Fingerprint: domain.SHA256Hex("sample-pod-not-ready"),
		ObservedAt:  observedAt,
	}
	result := domain.ToolResult{
		InvocationID: call.InvocationID(),
		Name:         call.Name(),
		Version:      call.Version(),
		Scope:        call.Scope().Snapshot(),
		ObservedAt:   observedAt,
		Status:       domain.ToolResultStatusSuccess,
		DataJSON:     `{"resource":{"kind":"Pod","name":"sample-pod","ready":false}}`,
		Evidence:     []domain.Evidence{evidence},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ToolResult.Validate() error = %v", err)
	}
	return result
}

type fakeTool struct {
	mu     sync.Mutex
	calls  int
	result func(BoundToolCall) domain.ToolResult
}

func (tool *fakeTool) Execute(_ context.Context, call BoundToolCall) domain.ToolResult {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.calls++
	return tool.result(call)
}

func (tool *fakeTool) Calls() int {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return tool.calls
}

func testToolHandlers(tool Tool) ToolHandlers {
	return ToolHandlers{
		GetResource:         tool,
		ListResources:       tool,
		GetEvents:           tool,
		GetPodLogs:          tool,
		GetPreviousPodLogs:  tool,
		GetPodMetrics:       tool,
		GetNodeMetrics:      tool,
		QueryPrometheus:     tool,
		QueryLoki:           tool,
		GetRelatedResources: tool,
		GetClusterOverview:  tool,
	}
}
