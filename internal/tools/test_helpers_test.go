package tools

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	testRunID        domain.AgentRunID       = "00000000-0000-7000-8000-000000010001"
	testSessionID    domain.SessionID        = "00000000-0000-7000-8000-000000010002"
	testMessageID    domain.MessageID        = "00000000-0000-7000-8000-000000010003"
	testInvocationID domain.ToolInvocationID = "00000000-0000-7000-8000-000000010004"
)

var (
	testActivatedAt = time.UnixMilli(1_000).UTC()
	testObservedAt  = time.UnixMilli(2_000).UTC()
)

type fakeResourceReader struct {
	mu           sync.Mutex
	getCalls     int
	listCalls    int
	getRequests  []ResourceReadRequest
	listRequests []ResourceListRequest
	getFn        func(context.Context, ResourceReadRequest) (ResourceObservation, error)
	listFn       func(context.Context, ResourceListRequest) (ResourceObservationList, error)
}

type fakeEventReader struct {
	mu       sync.Mutex
	calls    int
	requests []EventReadRequest
	readFn   func(context.Context, EventReadRequest) (EventObservationList, error)
}

func (reader *fakeEventReader) ReadEvents(ctx context.Context, request EventReadRequest) (EventObservationList, error) {
	reader.mu.Lock()
	reader.calls++
	reader.requests = append(reader.requests, request)
	function := reader.readFn
	reader.mu.Unlock()
	if function == nil {
		return EventObservationList{}, nil
	}
	return function(ctx, request)
}

func (reader *fakeEventReader) count() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type fakePodLogReader struct {
	mu       sync.Mutex
	calls    int
	requests []PodLogReadRequest
	readFn   func(context.Context, PodLogReadRequest) (PodLogObservation, error)
}

type fakeRelatedResourceReader struct {
	mu       sync.Mutex
	calls    int
	requests []RelatedReadRequest
	readFn   func(context.Context, RelatedReadRequest) (RelatedObservationGraph, error)
}

func (reader *fakeRelatedResourceReader) ReadRelatedResources(
	ctx context.Context,
	request RelatedReadRequest,
) (RelatedObservationGraph, error) {
	reader.mu.Lock()
	reader.calls++
	reader.requests = append(reader.requests, request)
	function := reader.readFn
	reader.mu.Unlock()
	if function == nil {
		return RelatedObservationGraph{}, nil
	}
	return function(ctx, request)
}

func (reader *fakeRelatedResourceReader) count() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

func (reader *fakeRelatedResourceReader) lastRequest() RelatedReadRequest {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.requests[len(reader.requests)-1]
}

func (reader *fakePodLogReader) ReadPodLog(ctx context.Context, request PodLogReadRequest) (PodLogObservation, error) {
	reader.mu.Lock()
	reader.calls++
	reader.requests = append(reader.requests, request)
	function := reader.readFn
	reader.mu.Unlock()
	if function == nil {
		return PodLogObservation{}, nil
	}
	return function(ctx, request)
}

func (reader *fakePodLogReader) count() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

func (reader *fakePodLogReader) lastRequest() PodLogReadRequest {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.requests[len(reader.requests)-1]
}

type staticLogPolicy struct {
	mu       sync.Mutex
	decision LogPolicyDecision
	calls    int
}

func (policy *staticLogPolicy) AuthorizeLogRead(context.Context, LogPolicyRequest) LogPolicyDecision {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	policy.calls++
	return policy.decision
}

func (policy *staticLogPolicy) count() int {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return policy.calls
}

func (reader *fakeResourceReader) ReadResource(ctx context.Context, request ResourceReadRequest) (ResourceObservation, error) {
	reader.mu.Lock()
	reader.getCalls++
	reader.getRequests = append(reader.getRequests, request)
	function := reader.getFn
	reader.mu.Unlock()
	if function == nil {
		return ResourceObservation{}, nil
	}
	return function(ctx, request)
}

func (reader *fakeResourceReader) ListResources(ctx context.Context, request ResourceListRequest) (ResourceObservationList, error) {
	reader.mu.Lock()
	reader.listCalls++
	reader.listRequests = append(reader.listRequests, request)
	function := reader.listFn
	reader.mu.Unlock()
	if function == nil {
		return ResourceObservationList{}, nil
	}
	return function(ctx, request)
}

func (reader *fakeResourceReader) counts() (int, int) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.getCalls, reader.listCalls
}

func (reader *fakeResourceReader) lastListRequest() ResourceListRequest {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.listRequests[len(reader.listRequests)-1]
}

type sequenceScopeGuard struct {
	mu      sync.Mutex
	results []bool
	calls   int
}

func (guard *sequenceScopeGuard) Current(_ context.Context, _ domain.ClusterScope) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.calls++
	if len(guard.results) == 0 {
		return true
	}
	result := guard.results[0]
	if len(guard.results) > 1 {
		guard.results = guard.results[1:]
	}
	return result
}

type sequenceEvidenceIDs struct {
	mu   sync.Mutex
	next int
}

func (source *sequenceEvidenceIDs) NewEvidenceID() (domain.EvidenceID, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.next++
	return domain.EvidenceID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 10_100+source.next)), nil
}

func (source *sequenceEvidenceIDs) count() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.next
}

type fakeClassifiedError struct {
	class     domain.SafeErrorClass
	message   string
	retryable bool
}

func (failure *fakeClassifiedError) Error() string                { return failure.message }
func (failure *fakeClassifiedError) Class() domain.SafeErrorClass { return failure.class }
func (failure *fakeClassifiedError) SafeMessage() string          { return failure.message }
func (failure *fakeClassifiedError) Retryable() bool              { return failure.retryable }
func (failure *fakeClassifiedError) ErrorCode() string            { return "synthetic_failure" }
func (failure *fakeClassifiedError) Operation() string            { return "synthetic_read" }

func testDependencies(reader ResourceReader, guard ScopeGuard) ResourceToolDependencies {
	return ResourceToolDependencies{
		EvidenceIDs: &sequenceEvidenceIDs{},
		Now:         func() time.Time { return testObservedAt },
		Reader:      reader,
		ScopeGuard:  guard,
		Text:        security.NewRedactor(),
	}
}

func testRunInput(t *testing.T, resultBytes int) agent.RunInput {
	t.Helper()
	limits := agent.DefaultRunBudgetLimits()
	if resultBytes > 0 {
		limits.ToolResultBytes = resultBytes
	}
	input, err := agent.NewRunInput(
		testRunID,
		testSessionID,
		testMessageID,
		"Inspect the selected Kubernetes resource.",
		domain.ClusterScope{
			Context:         "test-context",
			Namespace:       "team-a",
			NamespaceAccess: domain.NamespaceAccessCurrent,
			Generation:      7,
			ActivatedAt:     testActivatedAt,
		},
		nil,
		limits,
	)
	if err != nil {
		t.Fatalf("agent.NewRunInput() error = %v", err)
	}
	return input
}

func boundGetCall(t *testing.T, input agent.RunInput, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID:            "call-get-1",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(get_resource) error = %v", err)
	}
	return call
}

func boundListCall(t *testing.T, input agent.RunInput, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID:            "call-list-1",
		Name:          domain.ToolNameListResources,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(list_resources) error = %v", err)
	}
	return call
}

func boundEventCall(t *testing.T, input agent.RunInput, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID:            "call-events-1",
		Name:          domain.ToolNameGetEvents,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(get_events) error = %v", err)
	}
	return call
}

func boundLogCall(t *testing.T, input agent.RunInput, name domain.ToolName, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID:            "call-logs-1",
		Name:          name,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(%s) error = %v", name, err)
	}
	return call
}

func boundRelatedCall(t *testing.T, input agent.RunInput, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID:            "call-related-1",
		Name:          domain.ToolNameGetRelatedResources,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(get_related_resources) error = %v", err)
	}
	return call
}

func boundClusterOverviewCall(t *testing.T, input agent.RunInput, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID:            "call-cluster-overview-1",
		Name:          domain.ToolNameGetClusterOverview,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(get_cluster_overview) error = %v", err)
	}
	return call
}

func boundedPodLogContent(t *testing.T, value string) PodLogContent {
	t.Helper()
	content, err := NewPodLogContent([]byte(value), domain.MaxToolResultBytes)
	if err != nil {
		t.Fatalf("NewPodLogContent() error = %v", err)
	}
	return content
}

func resourceObservation(kind domain.ResourceKind, name string) ResourceObservation {
	status := domain.ResourceStatus{}
	switch kind {
	case domain.ResourceKindPod:
		status.Phase = "Running"
		status.Desired = domain.Count(1)
		status.Ready = domain.Count(1)
	case domain.ResourceKindDeployment, domain.ResourceKindReplicaSet:
		status.Desired = domain.Count(1)
		status.Ready = domain.Count(1)
		status.Available = domain.Count(1)
	case domain.ResourceKindJob:
		status.Phase = "Complete"
		status.Desired = domain.Count(1)
		status.Succeeded = domain.Count(1)
		status.Active = domain.Count(0)
		status.Failed = domain.Count(0)
	case domain.ResourceKindService:
		status.ServiceType = "ClusterIP"
	}
	return ResourceObservation{
		Summary: domain.ResourceSummary{
			Reference: domain.ResourceRef{
				APIVersion:      kind.APIVersion(),
				Kind:            string(kind),
				Namespace:       "team-a",
				Name:            name,
				UID:             "generated-uid-" + name,
				ResourceVersion: "17",
			},
			CreatedAt: time.UnixMilli(500).UTC(),
			Status:    status,
		},
	}
}
