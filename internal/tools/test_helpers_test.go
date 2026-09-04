package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	queryFn      func(context.Context, ResourceQueryRequest) (ResourceQueryObservation, error)
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
	mu           sync.Mutex
	calls        int
	requests     []PodLogReadRequest
	readFn       func(context.Context, PodLogReadRequest) (PodLogObservation, error)
	manyCalls    int
	manyRequests []PodLogsReadRequest
	readManyFn   func(context.Context, PodLogsReadRequest) (PodLogsObservation, error)
}

type fakeMetricReader struct{}

func (*fakeMetricReader) ReadMetrics(context.Context, MetricReadRequest) (MetricObservation, error) {
	return MetricObservation{}, nil
}

type fakePrometheusReader struct{}

func (*fakePrometheusReader) QueryPrometheus(context.Context, PrometheusReadRequest) (PrometheusObservation, error) {
	return PrometheusObservation{}, nil
}

type fakeLokiReader struct{}

func (*fakeLokiReader) QueryLoki(context.Context, LokiReadRequest) (LokiObservation, error) {
	return LokiObservation{}, nil
}

type staticObservationPolicy struct {
	decision ObservationPolicyDecision
}

func (policy staticObservationPolicy) AuthorizeObservation(context.Context, ObservationPolicyRequest) ObservationPolicyDecision {
	return policy.decision
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

func (reader *fakePodLogReader) ReadPodLogs(ctx context.Context, request PodLogsReadRequest) (PodLogsObservation, error) {
	reader.mu.Lock()
	reader.manyCalls++
	reader.manyRequests = append(reader.manyRequests, request)
	function := reader.readManyFn
	reader.mu.Unlock()
	if function == nil {
		return PodLogsObservation{}, nil
	}
	return function(ctx, request)
}

func (reader *fakePodLogReader) manyCount() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.manyCalls
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

func (reader *fakeResourceReader) QueryResources(ctx context.Context, request ResourceQueryRequest) (ResourceQueryObservation, error) {
	reader.mu.Lock()
	queryFn := reader.queryFn
	reader.mu.Unlock()
	if queryFn != nil {
		return queryFn(ctx, request)
	}
	if request.Validate() != nil {
		return ResourceQueryObservation{}, ErrInvalidResourceRead
	}
	if request.Query.Verb == domain.ResourceVerbGet {
		observation, err := reader.ReadResource(ctx, ResourceReadRequest{
			Scope: request.Query.Scope,
			Reference: domain.ResourceRef{
				APIVersion: request.Policy.Type.APIVersion(), Kind: request.Policy.Type.Kind,
				Namespace: request.Query.Namespace, Name: request.Query.Name,
			},
			Detail: request.Detail,
		})
		if err != nil {
			return ResourceQueryObservation{}, err
		}
		observation.Summary.Type = request.Policy.Type
		page := domain.ResourcePage{
			Type: request.Policy.Type, Items: []domain.ResourceSummary{observation.Summary},
			PagesRead: 1, ScannedItems: 1, MatchedItems: 1,
		}
		return ResourceQueryObservation{Page: page, Items: []ResourceObservation{observation}}, nil
	}
	kind := domain.ResourceKind(request.Policy.Type.Kind)
	observations, err := reader.ListResources(ctx, ResourceListRequest{
		Scope: request.Query.Scope, Kind: kind, Namespace: request.Query.Namespace,
		AllNamespaces: request.Query.AllNamespaces, Limit: request.Query.Limits.MaxReturned,
	})
	if err != nil {
		return ResourceQueryObservation{}, err
	}
	scanned := len(observations.Items)
	items := make([]ResourceObservation, 0, len(observations.Items))
	for _, item := range observations.Items {
		item.Summary.Type = request.Policy.Type
		item.Fields = testProjectedFields(request.Policy, item)
		if testResourceMatches(item, request.Query.Filters) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(left, right int) bool {
		leftRef, rightRef := items[left].Summary.Reference, items[right].Summary.Reference
		return leftRef.Namespace+"\x00"+leftRef.Name < rightRef.Namespace+"\x00"+rightRef.Name
	})
	matched := len(items)
	partial := observations.Truncated
	reason := ""
	moreAvailable := false
	if len(items) > request.Query.Limits.MaxReturned {
		items = items[:request.Query.Limits.MaxReturned]
		partial, moreAvailable, reason = true, true, "return_limit"
	} else if partial {
		moreAvailable, reason = true, "item_limit"
	}
	summaries := make([]domain.ResourceSummary, len(items))
	for index := range items {
		summaries[index] = items[index].Summary
	}
	page := domain.ResourcePage{
		Type: request.Policy.Type, Items: summaries, PagesRead: 1, ScannedItems: scanned, MatchedItems: matched,
		Partial: partial, Truncated: partial, MoreAvailable: moreAvailable, Reason: reason,
	}
	return ResourceQueryObservation{Page: page, Items: items}, nil
}

func testProjectedFields(policy domain.ResourcePolicy, observation ResourceObservation) []ResourceFieldObservation {
	values := map[string]string{
		"name": observation.Summary.Reference.Name, "namespace": observation.Summary.Reference.Namespace,
		"phase": observation.Summary.Status.Phase, "reason": observation.Summary.Status.Reason,
		"service_type": observation.Summary.Status.ServiceType,
	}
	if observation.Summary.Status.Ready.Present {
		values["ready"] = fmt.Sprintf("%d", observation.Summary.Status.Ready.Value)
	}
	if observation.Summary.Status.Desired.Present {
		values["desired"] = fmt.Sprintf("%d", observation.Summary.Status.Desired.Value)
	}
	result := make([]ResourceFieldObservation, 0, len(values))
	for id, value := range values {
		field, found := policy.Field(id)
		if !found || value == "" {
			continue
		}
		result = append(result, ResourceFieldObservation{
			Field: id, Path: field.Path, Scalar: field.Scalar, DataClass: field.DataClass,
			Value: ExternalText{Value: value}, Present: true,
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Field < result[right].Field })
	return result
}

func testResourceMatches(observation ResourceObservation, filters []domain.ResourceFilter) bool {
	for _, filter := range filters {
		matched := false
		for _, field := range observation.Fields {
			if field.Field == filter.Field {
				switch filter.Operator {
				case domain.ResourceFilterEquals:
					matched = field.Present && field.Value.Value == filter.Value
				case domain.ResourceFilterNotEquals:
					matched = field.Present && field.Value.Value != filter.Value
				case domain.ResourceFilterExists:
					matched = field.Present
				}
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
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

type alwaysCurrentPolicyGuard struct{}

func (alwaysCurrentPolicyGuard) CurrentPolicyGeneration(context.Context, domain.PolicyGeneration) bool {
	return true
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
	queryReader, _ := reader.(ResourceQueryReader)
	return ResourceToolDependencies{
		EvidenceIDs: &sequenceEvidenceIDs{},
		Now:         func() time.Time { return testObservedAt },
		Reader:      reader,
		QueryReader: queryReader,
		ScopeGuard:  guard,
		PolicyGuard: alwaysCurrentPolicyGuard{},
		Text:        security.NewRedactor(),
	}
}

func metricDependencies(reader MetricReader, guard ScopeGuard) MetricToolDependencies {
	return MetricToolDependencies{
		Reader: reader, ScopeGuard: guard, PolicyGuard: alwaysCurrentPolicyGuard{},
		EvidenceIDs: &sequenceEvidenceIDs{}, Now: func() time.Time { return testObservedAt },
	}
}

func sourceDependencies(prometheus PrometheusReader, loki LokiReader, guard ScopeGuard) DataSourceToolDependencies {
	return DataSourceToolDependencies{
		Prometheus: prometheus, Loki: loki, ScopeGuard: guard, PolicyGuard: alwaysCurrentPolicyGuard{},
		Policy: staticObservationPolicy{decision: ObservationPolicyAllowed}, EvidenceIDs: &sequenceEvidenceIDs{},
		Text: security.NewRedactor(), Now: func() time.Time { return testObservedAt },
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
	arguments = broadGetArguments(t, arguments)
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{
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
	arguments = broadListArguments(t, arguments)
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{
		ID:            "call-list-1",
		Name:          domain.ToolNameListResources,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(list_resources) error = %v", err)
	}
	return call
}

func broadGetArguments(t *testing.T, arguments string) string {
	t.Helper()
	var current struct {
		ResourceType string `json:"resource_type"`
	}
	if json.Unmarshal([]byte(arguments), &current) == nil && current.ResourceType != "" {
		return arguments
	}
	var legacy struct {
		Detail    string `json:"detail"`
		Namespace string `json:"namespace"`
		Purpose   string `json:"purpose"`
		Resource  struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"resource"`
	}
	if json.Unmarshal([]byte(arguments), &legacy) != nil {
		return arguments
	}
	detail := legacy.Detail
	if detail == "" || detail == "diagnostic" {
		detail = string(domain.ResourceViewDescribe)
	}
	namespace := legacy.Resource.Namespace
	if namespace == "" {
		namespace = legacy.Namespace
	}
	encoded, err := json.Marshal(struct {
		Detail       string `json:"detail"`
		Name         string `json:"name"`
		Namespace    string `json:"namespace"`
		Purpose      string `json:"purpose"`
		ResourceType string `json:"resource_type"`
	}{detail, legacy.Resource.Name, namespace, legacy.Purpose, resourceTypeID(legacy.Resource.Kind)})
	if err != nil {
		t.Fatalf("encode broad get arguments: %v", err)
	}
	return string(encoded)
}

func broadListArguments(t *testing.T, arguments string) string {
	t.Helper()
	var current struct {
		ResourceType string `json:"resource_type"`
	}
	if json.Unmarshal([]byte(arguments), &current) == nil && current.ResourceType != "" {
		return arguments
	}
	var legacy struct {
		Health    string `json:"health_filter"`
		Kind      string `json:"kind"`
		Limit     int    `json:"limit"`
		Namespace string `json:"namespace"`
		Purpose   string `json:"purpose"`
	}
	if json.Unmarshal([]byte(arguments), &legacy) != nil {
		return arguments
	}
	var limit *int
	if legacy.Limit != 0 {
		limit = &legacy.Limit
	}
	filters := []testResourceFilterArgument{}
	encoded, err := json.Marshal(struct {
		Filters      []testResourceFilterArgument `json:"filters"`
		Format       domain.ResourceView          `json:"format"`
		Limit        *int                         `json:"limit"`
		Namespace    string                       `json:"namespace"`
		Purpose      string                       `json:"purpose"`
		ResourceType string                       `json:"resource_type"`
	}{filters, domain.ResourceViewList, limit, legacy.Namespace, legacy.Purpose, resourceTypeID(legacy.Kind)})
	if err != nil {
		t.Fatalf("encode broad list arguments: %v", err)
	}
	return string(encoded)
}

type testResourceFilterArgument struct {
	Field    string                        `json:"field"`
	Operator domain.ResourceFilterOperator `json:"operator"`
	Value    string                        `json:"value,omitempty"`
}

func resourceTypeID(kind string) string {
	for _, policy := range domain.BuiltInResourcePolicies() {
		if policy.Type.Kind == kind {
			return policy.Type.ID
		}
	}
	return "unknown-resource-type"
}

func boundEventCall(t *testing.T, input agent.RunInput, arguments string) agent.BoundToolCall {
	t.Helper()
	arguments = completeEventArguments(t, arguments)
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{
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
	arguments = completeLogArguments(t, arguments)
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{
		ID:            "call-logs-1",
		Name:          name,
		ArgumentsJSON: arguments,
	})
	if err != nil {
		t.Fatalf("agent.BindToolCall(%s) error = %v", name, err)
	}
	return call
}

func completeEventArguments(t *testing.T, arguments string) string {
	t.Helper()
	var value map[string]any
	if json.Unmarshal([]byte(arguments), &value) != nil {
		return arguments
	}
	for _, name := range []string{"limit", "reason", "since_seconds", "type"} {
		if _, found := value[name]; !found {
			value[name] = nil
		}
	}
	resource, ok := value["resource"].(map[string]any)
	if ok {
		for _, name := range []string{"api_version", "namespace", "uid"} {
			if _, found := resource[name]; !found {
				resource[name] = nil
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode complete Event arguments: %v", err)
	}
	return string(encoded)
}

func completeLogArguments(t *testing.T, arguments string) string {
	t.Helper()
	var value map[string]any
	if json.Unmarshal([]byte(arguments), &value) != nil {
		return arguments
	}
	for _, name := range []string{"container", "container_mode", "include_ephemeral", "include_init", "namespace", "search", "since_seconds", "tail_lines"} {
		if _, found := value[name]; !found {
			value[name] = nil
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode complete log arguments: %v", err)
	}
	return string(encoded)
}

func boundRelatedCall(t *testing.T, input agent.RunInput, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{
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
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{
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
