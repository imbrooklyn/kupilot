package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

var observabilityNow = time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)

func TestMetricToolsProjectNormalizedSnapshotAndExactProvenance(t *testing.T) {
	t.Parallel()

	reader := &recordingMetricReader{readFn: func(_ context.Context, request MetricReadRequest) (MetricObservation, error) {
		if request.Reference != (domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"}) ||
			request.PolicyGeneration != 1 || request.MaxContainers != 35 || request.LimitBytes != 256*1024 {
			t.Fatalf("ReadMetrics() request = %#v", request)
		}
		return MetricObservation{
			Resource:     domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod", UID: "generated-pod-uid", ResourceVersion: "19"},
			Availability: MetricAvailable, Timestamp: observabilityNow.Add(-30 * time.Second), Window: 15 * time.Second,
			Usage: domain.NormalizedResourceUsage{CPUMilli: 125, MemoryBytes: 64 * 1024 * 1024},
			Containers: []ContainerMetricObservation{
				{Name: "sidecar", Usage: domain.NormalizedResourceUsage{CPUMilli: 25, MemoryBytes: 16 * 1024 * 1024}},
				{Name: "app", Usage: domain.NormalizedResourceUsage{CPUMilli: 100, MemoryBytes: 48 * 1024 * 1024}},
			}, SourceBytes: 512,
		}, nil
	}}
	tool, err := NewGetPodMetricsTool(metricDependenciesAt(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewGetPodMetricsTool() error = %v", err)
	}
	call := boundMetricCall(t, domain.ToolNameGetPodMetrics, `{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect current Pod CPU and memory."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || reader.count() != 1 || len(result.Evidence) != 1 {
		t.Fatalf("Execute() result/reads = %#v/%d", result, reader.count())
	}
	if !strings.Contains(result.DataJSON, `"cpu_milli":125`) || !strings.Contains(result.DataJSON, `"memory_bytes":67108864`) ||
		strings.Index(result.DataJSON, `"name":"app"`) > strings.Index(result.DataJSON, `"name":"sidecar"`) {
		t.Fatalf("metric data JSON = %s", result.DataJSON)
	}
	evidence := result.Evidence[0]
	if evidence.Category != domain.EvidenceCategoryMetricSnapshot || evidence.PolicyVersion != domain.ObservabilityPolicyVersion ||
		evidence.PolicyGeneration != 1 || evidence.SourcePath == nil || *evidence.SourcePath != "apis/metrics.k8s.io/v1beta1/namespaces/team-a/pods/sample-pod" ||
		evidence.ObservedFrom == nil || evidence.ObservedThrough == nil || evidence.Partial || evidence.Truncated {
		t.Fatalf("metric Evidence = %#v", evidence)
	}
}

func TestMetricToolsDistinguishUnavailableUnsupportedStaleAndPartial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		observation  MetricObservation
		wantStatus   domain.ToolResultStatus
		wantReason   string
		wantEvidence int
		wantWarning  string
	}{
		{name: "unavailable", observation: metricUnavailable(MetricUnavailable), wantStatus: domain.ToolResultStatusSuccess, wantEvidence: 0, wantWarning: "metrics_unavailable"},
		{name: "unsupported", observation: metricUnavailable(MetricUnsupported), wantStatus: domain.ToolResultStatusSuccess, wantEvidence: 0, wantWarning: "metrics_unsupported"},
		{name: "stale", observation: metricAvailable(observabilityNow.Add(-6*time.Minute), false), wantStatus: domain.ToolResultStatusPartial, wantReason: "stale", wantEvidence: 1},
		{name: "partial", observation: metricAvailable(observabilityNow.Add(-time.Minute), true), wantStatus: domain.ToolResultStatusPartial, wantReason: "partial", wantEvidence: 1},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			reader := &recordingMetricReader{readFn: func(context.Context, MetricReadRequest) (MetricObservation, error) { return current.observation, nil }}
			tool, _ := NewGetNodeMetricsTool(metricDependenciesAt(reader, &sequenceScopeGuard{}))
			result := tool.Execute(context.Background(), boundMetricCall(t, domain.ToolNameGetNodeMetrics, `{"node_name":"worker-a","purpose":"Inspect current Node CPU and memory."}`))
			if result.Validate() != nil || result.Status != current.wantStatus || result.Truncation.Reason != current.wantReason ||
				len(result.Evidence) != current.wantEvidence || reader.count() != 1 {
				t.Fatalf("Execute() result = %#v", result)
			}
			if current.wantWarning != "" && (len(result.Warnings) != 1 || result.Warnings[0].Code != current.wantWarning) {
				t.Fatalf("warnings = %#v", result.Warnings)
			}
		})
	}
}

func TestMetricToolCancellationStalenessAndInvalidResponseAreSafe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ctx       context.Context
		guard     ScopeGuard
		result    MetricObservation
		readErr   error
		wantClass domain.SafeErrorClass
		wantCalls int
	}{
		{name: "cancelled", ctx: cancelledContext(), guard: &sequenceScopeGuard{}, wantClass: domain.SafeErrorClassCancelled},
		{name: "stale", ctx: context.Background(), guard: &sequenceScopeGuard{results: []bool{false}}, wantClass: domain.SafeErrorClassStaleScope},
		{name: "late stale", ctx: context.Background(), guard: &sequenceScopeGuard{results: []bool{true, false}}, result: metricAvailable(observabilityNow.Add(-time.Minute), false), wantClass: domain.SafeErrorClassStaleScope, wantCalls: 1},
		{name: "invalid external", ctx: context.Background(), guard: &sequenceScopeGuard{}, result: MetricObservation{}, wantClass: domain.SafeErrorClassInvalidExternalResponse, wantCalls: 1},
		{name: "timeout", ctx: context.Background(), guard: &sequenceScopeGuard{}, readErr: context.DeadlineExceeded, wantClass: domain.SafeErrorClassTimeout, wantCalls: 1},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			reader := &recordingMetricReader{readFn: func(context.Context, MetricReadRequest) (MetricObservation, error) {
				return current.result, current.readErr
			}}
			tool, _ := NewGetNodeMetricsTool(metricDependenciesAt(reader, current.guard))
			result := tool.Execute(current.ctx, boundMetricCall(t, domain.ToolNameGetNodeMetrics, `{"node_name":"worker-a","purpose":"Inspect current Node CPU and memory."}`))
			if result.Validate() != nil || result.Error == nil || result.Error.Class != current.wantClass || reader.count() != current.wantCalls || len(result.Evidence) != 0 {
				t.Fatalf("Execute() result/reads = %#v/%d", result, reader.count())
			}
		})
	}
}

func TestPrometheusAndLokiToolsProjectEvidenceWithoutRawQueriesOrSecrets(t *testing.T) {
	t.Parallel()

	input := observabilityRunInput(t)
	policy := &recordingObservationPolicy{decision: ObservationPolicyAllowed}
	prometheus := &recordingPrometheusReader{readFn: func(_ context.Context, request PrometheusReadRequest) (PrometheusObservation, error) {
		if request.QueryID != domain.QueryPrometheusPodCPUUsage || request.MaxSeries != 2 || request.MaxSamples != 400 || request.Step != time.Minute ||
			request.End != observabilityNow || request.Start != observabilityNow.Add(-time.Hour) {
			t.Fatalf("QueryPrometheus() request = %#v", request)
		}
		return PrometheusObservation{Reference: request.Reference, QueryID: request.QueryID, Start: request.Start, End: request.End, SourceBytes: 1024,
			Series: []PrometheusSeries{{Series: "container=app", Samples: []PrometheusSample{{Timestamp: request.Start, Value: 1.25}, {Timestamp: request.End, Value: 2.5}}}}}, nil
	}}
	loki := &recordingLokiReader{readFn: func(_ context.Context, request LokiReadRequest) (LokiObservation, error) {
		if request.QueryID != domain.QueryLokiPodLogs || request.Contains != "error" || request.MaxPages != 4 || request.PageLines != 2 || request.MaxLines != 2 {
			t.Fatalf("QueryLoki() request = %#v", request)
		}
		canary := strings.Repeat("loki-secret-canary", 3)
		content, err := NewLokiLineContent([]byte("error token="+canary), request.LimitBytes)
		if err != nil {
			t.Fatalf("NewLokiLineContent() error = %v", err)
		}
		return LokiObservation{Reference: request.Reference, QueryID: request.QueryID, Start: request.Start, End: request.End,
			Pages: 1, SourceBytes: 512, Lines: []LokiLineObservation{{Timestamp: request.End.Add(-time.Minute), Series: "container=app", Content: content}}}, nil
	}}
	dependencies := sourceDependenciesAt(prometheus, loki, &sequenceScopeGuard{}, policy)
	prometheusTool, _ := NewQueryPrometheusTool(dependencies)
	lokiTool, _ := NewQueryLokiTool(dependencies)

	promCall := boundSourceCall(t, input, domain.ToolNameQueryPrometheus,
		`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect bounded Pod CPU history.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`)
	promResult := prometheusTool.Execute(context.Background(), promCall)
	if promResult.Validate() != nil || promResult.Status != domain.ToolResultStatusSuccess || len(promResult.Evidence) != 1 || prometheus.count() != 1 {
		t.Fatalf("Prometheus Execute() result/reads = %#v/%d", promResult, prometheus.count())
	}
	promEvidence := promResult.Evidence[0]
	if promEvidence.Category != domain.EvidenceCategoryPrometheus || promEvidence.SourceOriginHash != promCall.SourcePolicy().OriginHash ||
		promEvidence.Series != "container=app" || promEvidence.ObservedFrom == nil || promEvidence.ObservedThrough == nil ||
		promEvidence.SourcePath == nil || strings.Contains(promResult.DataJSON, "sum by") {
		t.Fatalf("Prometheus Evidence/result = %#v/%s", promEvidence, promResult.DataJSON)
	}

	lokiCall := boundSourceCall(t, input, domain.ToolNameQueryLoki,
		`{"contains":"error","line_limit":2,"namespace":null,"pod_name":"sample-pod","purpose":"Inspect bounded Pod error logs.","query_id":"pod_logs","window_seconds":3600}`)
	lokiResult := lokiTool.Execute(context.Background(), lokiCall)
	canary := strings.Repeat("loki-secret-canary", 3)
	if lokiResult.Validate() != nil || lokiResult.Status != domain.ToolResultStatusSuccess || len(lokiResult.Evidence) != 1 || loki.count() != 1 ||
		strings.Contains(fmt.Sprintf("%#v", lokiResult), canary) || !strings.Contains(lokiResult.DataJSON, "[REDACTED]") {
		t.Fatalf("Loki Execute() result/reads = %#v/%d", lokiResult, loki.count())
	}
	lokiEvidence := lokiResult.Evidence[0]
	if lokiEvidence.Category != domain.EvidenceCategoryLoki || lokiEvidence.SourceOriginHash != lokiCall.SourcePolicy().OriginHash ||
		lokiEvidence.Series != "container=app" || lokiEvidence.RedactionCount != 1 || lokiEvidence.SourcePath == nil ||
		strings.Contains(lokiEvidence.Fact, canary) || policy.count() != 4 {
		t.Fatalf("Loki Evidence/policy calls = %#v/%d", lokiEvidence, policy.count())
	}
}

func TestPrometheusAndLokiToolsSanitizeUntrustedSeriesIdentities(t *testing.T) {
	t.Parallel()

	input := observabilityRunInput(t)
	canary := strings.Repeat("series-secret-canary", 3)
	unsafeSeries := "container=app,token=" + canary
	policy := &recordingObservationPolicy{decision: ObservationPolicyAllowed}
	prometheus := &recordingPrometheusReader{readFn: func(_ context.Context, request PrometheusReadRequest) (PrometheusObservation, error) {
		return PrometheusObservation{Reference: request.Reference, QueryID: request.QueryID, Start: request.Start, End: request.End, SourceBytes: 128,
			Series: []PrometheusSeries{{Series: unsafeSeries, Samples: []PrometheusSample{{Timestamp: request.End, Value: 1}}}}}, nil
	}}
	loki := &recordingLokiReader{readFn: func(_ context.Context, request LokiReadRequest) (LokiObservation, error) {
		content, err := NewLokiLineContent([]byte("safe line"), request.LimitBytes)
		if err != nil {
			t.Fatalf("NewLokiLineContent() error = %v", err)
		}
		return LokiObservation{Reference: request.Reference, QueryID: request.QueryID, Start: request.Start, End: request.End, Pages: 1, SourceBytes: 128,
			Lines: []LokiLineObservation{{Timestamp: request.End, Series: unsafeSeries, Content: content}}}, nil
	}}
	dependencies := sourceDependenciesAt(prometheus, loki, &sequenceScopeGuard{}, policy)
	prometheusTool, _ := NewQueryPrometheusTool(dependencies)
	lokiTool, _ := NewQueryLokiTool(dependencies)
	prometheusResult := prometheusTool.Execute(context.Background(), boundSourceCall(t, input, domain.ToolNameQueryPrometheus,
		`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect bounded Pod CPU history.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`))
	lokiResult := lokiTool.Execute(context.Background(), boundSourceCall(t, input, domain.ToolNameQueryLoki,
		`{"contains":null,"line_limit":2,"namespace":null,"pod_name":"sample-pod","purpose":"Inspect bounded Pod logs.","query_id":"pod_logs","window_seconds":3600}`))
	for name, result := range map[string]ToolResult{"prometheus": prometheusResult, "loki": lokiResult} {
		if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 1 ||
			strings.Contains(fmt.Sprintf("%#v", result), canary) || !strings.Contains(result.DataJSON, "[REDACTED]") || result.Evidence[0].RedactionCount < 1 {
			t.Fatalf("%s sanitized result = %#v", name, result)
		}
	}
}

func TestDataSourceToolDiscardsReturnedDataWhenPostflightAuthorizationChanges(t *testing.T) {
	t.Parallel()

	input := observabilityRunInput(t)
	for _, current := range []struct {
		name      string
		decision  ObservationPolicyDecision
		wantClass domain.SafeErrorClass
	}{
		{name: "consent revoked", decision: ObservationPolicyConsentRequired, wantClass: domain.SafeErrorClassConsentRequired},
		{name: "permission changed", decision: ObservationPolicyPermissionRequired, wantClass: domain.SafeErrorClassPolicyDenied},
		{name: "source denied", decision: ObservationPolicyDenied, wantClass: domain.SafeErrorClassPolicyDenied},
		{name: "invalid decision", decision: ObservationPolicyDecision("generated"), wantClass: domain.SafeErrorClassInternal},
	} {
		current := current
		t.Run(current.name, func(t *testing.T) {
			canary := strings.Repeat("postflight-source-canary", 3)
			reader := &recordingPrometheusReader{readFn: func(_ context.Context, request PrometheusReadRequest) (PrometheusObservation, error) {
				return PrometheusObservation{
					Reference: request.Reference, QueryID: request.QueryID, Start: request.Start, End: request.End,
					Series: []PrometheusSeries{{Series: canary, Samples: []PrometheusSample{{Timestamp: request.End, Value: 1}}}}, SourceBytes: 128,
				}, nil
			}}
			ids := &sequenceEvidenceIDs{}
			policy := &recordingObservationPolicy{decisions: []ObservationPolicyDecision{ObservationPolicyAllowed, current.decision}}
			dependencies := sourceDependenciesAt(reader, &recordingLokiReader{}, &sequenceScopeGuard{}, policy)
			dependencies.EvidenceIDs = ids
			tool, _ := NewQueryPrometheusTool(dependencies)
			call := boundSourceCall(t, input, domain.ToolNameQueryPrometheus,
				`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect bounded Pod CPU history.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`)
			result := tool.Execute(context.Background(), call)
			if result.Validate() != nil || result.Error == nil || result.Error.Class != current.wantClass || reader.count() != 1 || policy.count() != 2 || ids.count() != 0 ||
				len(result.Evidence) != 0 || strings.Contains(fmt.Sprintf("%#v", result), canary) {
				t.Fatalf("Execute() result/source/policy/ids = %#v/%d/%d/%d", result, reader.count(), policy.count(), ids.count())
			}
		})
	}
}

func TestDataSourceToolPolicyAndStateDenialsPerformZeroSourceCalls(t *testing.T) {
	input := observabilityRunInput(t)
	tests := []struct {
		name         string
		ctx          context.Context
		guard        ScopeGuard
		decision     ObservationPolicyDecision
		wantClass    domain.SafeErrorClass
		wantPolicies int
	}{
		{name: "consent", ctx: context.Background(), guard: &sequenceScopeGuard{}, decision: ObservationPolicyConsentRequired, wantClass: domain.SafeErrorClassConsentRequired, wantPolicies: 1},
		{name: "permission", ctx: context.Background(), guard: &sequenceScopeGuard{}, decision: ObservationPolicyPermissionRequired, wantClass: domain.SafeErrorClassPolicyDenied, wantPolicies: 1},
		{name: "denied", ctx: context.Background(), guard: &sequenceScopeGuard{}, decision: ObservationPolicyDenied, wantClass: domain.SafeErrorClassPolicyDenied, wantPolicies: 1},
		{name: "cancelled", ctx: cancelledContext(), guard: &sequenceScopeGuard{}, decision: ObservationPolicyAllowed, wantClass: domain.SafeErrorClassCancelled},
		{name: "stale", ctx: context.Background(), guard: &sequenceScopeGuard{results: []bool{false}}, decision: ObservationPolicyAllowed, wantClass: domain.SafeErrorClassStaleScope},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			prometheus := &recordingPrometheusReader{}
			loki := &recordingLokiReader{}
			policy := &recordingObservationPolicy{decision: current.decision}
			tool, _ := NewQueryPrometheusTool(sourceDependenciesAt(prometheus, loki, current.guard, policy))
			call := boundSourceCall(t, input, domain.ToolNameQueryPrometheus,
				`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect bounded Pod CPU history.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`)
			result := tool.Execute(current.ctx, call)
			if result.Validate() != nil || result.Error == nil || result.Error.Class != current.wantClass || prometheus.count() != 0 || loki.count() != 0 || policy.count() != current.wantPolicies {
				t.Fatalf("Execute() result/source/policy calls = %#v/%d/%d/%d", result, prometheus.count(), loki.count(), policy.count())
			}
		})
	}
}

func TestDataSourceBindingRejectsRawOrExpandedAuthorityBeforePolicyAndSource(t *testing.T) {
	input := observabilityRunInput(t)
	defaulted, err := agent.BindToolCall(input, "00000000-0000-7000-8000-000000031090", agent.ToolSelection{
		ID: "default-source", Name: domain.ToolNameQueryPrometheus,
		ArgumentsJSON: `{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"pod_cpu_usage","series_limit":null,"step_seconds":null,"window_seconds":null}`,
	})
	if err != nil || !strings.Contains(defaulted.ArgumentsJSON(), `"series_limit":6`) ||
		!strings.Contains(defaulted.ArgumentsJSON(), `"step_seconds":60`) ||
		!strings.Contains(defaulted.ArgumentsJSON(), `"window_seconds":3600`) {
		t.Fatalf("default source binding = %#v/%v", defaulted, err)
	}
	for index, arguments := range []string{
		`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query":"up","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`,
		`{"endpoint":"https://other.example","namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`,
		`{"headers":{"Authorization":"generated"},"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`,
		`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"unknown","series_limit":2,"step_seconds":60,"window_seconds":3600}`,
		`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":0,"window_seconds":3600}`,
		`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"pod_cpu_usage","series_limit":100,"step_seconds":15,"window_seconds":86400}`,
		`{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"pod_cpu_usage","series_limit":26,"step_seconds":300,"window_seconds":60}`,
		`{"contains":"error","line_limit":401,"namespace":null,"pod_name":"sample-pod","purpose":"Inspect logs.","query_id":"pod_logs","window_seconds":3600}`,
	} {
		name := domain.ToolNameQueryPrometheus
		if strings.Contains(arguments, `"query_id":"pod_logs"`) {
			name = domain.ToolNameQueryLoki
		}
		_, err := agent.BindToolCall(input, domain.ToolInvocationID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 31_000+index)), agent.ToolSelection{
			ID: "denied-source", Name: name, ArgumentsJSON: arguments,
		})
		if !errors.Is(err, agent.ErrToolPolicyDenied) {
			t.Fatalf("BindToolCall(%s) error = %v", arguments, err)
		}
	}
	disabled := testRunInput(t, 0)
	_, err = agent.BindToolCall(disabled, "00000000-0000-7000-8000-000000031099", agent.ToolSelection{
		ID: "disabled-source", Name: domain.ToolNameQueryPrometheus,
		ArgumentsJSON: `{"namespace":null,"pod_name":"sample-pod","purpose":"Inspect CPU.","query_id":"pod_cpu_usage","series_limit":2,"step_seconds":60,"window_seconds":3600}`,
	})
	if !errors.Is(err, agent.ErrToolPolicyDenied) {
		t.Fatalf("disabled source binding error = %v", err)
	}
}

func metricAvailable(timestamp time.Time, partial bool) MetricObservation {
	availability := MetricAvailable
	if partial {
		availability = MetricPartial
	}
	return MetricObservation{
		Resource:     domain.ResourceRef{APIVersion: "v1", Kind: "Node", Name: "worker-a", UID: "generated-node-uid", ResourceVersion: "9"},
		Availability: availability, Timestamp: timestamp, Window: time.Minute,
		Usage: domain.NormalizedResourceUsage{CPUMilli: 500, MemoryBytes: 1024}, Partial: partial, SourceBytes: 128,
	}
}

func metricUnavailable(availability MetricAvailability) MetricObservation {
	return MetricObservation{Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Node", Name: "worker-a", UID: "generated-node-uid", ResourceVersion: "9"}, Availability: availability, SourceBytes: 64}
}

func metricDependenciesAt(reader MetricReader, guard ScopeGuard) MetricToolDependencies {
	return MetricToolDependencies{Reader: reader, ScopeGuard: guard, PolicyGuard: alwaysCurrentPolicyGuard{}, EvidenceIDs: &sequenceEvidenceIDs{}, Now: func() time.Time { return observabilityNow }}
}

func boundMetricCall(t *testing.T, name domain.ToolName, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(testRunInput(t, 0), testInvocationID, agent.ToolSelection{ID: "metric-call", Name: name, ArgumentsJSON: arguments})
	if err != nil {
		t.Fatalf("BindToolCall(%s) error = %v", name, err)
	}
	return call
}

func observabilityRunInput(t *testing.T) agent.RunInput {
	t.Helper()
	prometheusHash := domain.SHA256Hex("https://prometheus.example")
	lokiHash := domain.SHA256Hex("https://loki.example")
	catalog, err := domain.NewObservabilityPolicyCatalog(
		domain.DataSourcePolicy{Kind: domain.DataSourcePrometheus, OriginHash: prometheusHash, Queries: []domain.ObservabilityQueryID{domain.QueryPrometheusPodCPUUsage}, RequestTimeout: 5 * time.Second},
		domain.DataSourcePolicy{Kind: domain.DataSourceLoki, OriginHash: lokiHash, Queries: []domain.ObservabilityQueryID{domain.QueryLokiPodLogs}, RequestTimeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("NewObservabilityPolicyCatalog() error = %v", err)
	}
	input, err := agent.NewRunInputWithOperationalPolicyContext(testRunID, testSessionID, testMessageID, "Inspect bounded observability data.",
		domain.ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: testActivatedAt},
		nil, agent.DefaultRunBudgetLimits(), agent.ConversationContext{}, domain.DefaultResourcePolicyCatalog(), catalog, 1)
	if err != nil {
		t.Fatalf("NewRunInputWithOperationalPolicyContext() error = %v", err)
	}
	return input
}

func boundSourceCall(t *testing.T, input agent.RunInput, name domain.ToolName, arguments string) agent.BoundToolCall {
	t.Helper()
	call, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{ID: "source-call", Name: name, ArgumentsJSON: arguments})
	if err != nil {
		t.Fatalf("BindToolCall(%s) error = %v", name, err)
	}
	return call
}

func sourceDependenciesAt(prometheus PrometheusReader, loki LokiReader, guard ScopeGuard, policy ObservationDataPolicy) DataSourceToolDependencies {
	return DataSourceToolDependencies{
		Prometheus: prometheus, Loki: loki, ScopeGuard: guard, PolicyGuard: alwaysCurrentPolicyGuard{}, Policy: policy,
		EvidenceIDs: &sequenceEvidenceIDs{}, Text: security.NewRedactor(), Now: func() time.Time { return observabilityNow },
	}
}

type recordingMetricReader struct {
	mu     sync.Mutex
	calls  int
	readFn func(context.Context, MetricReadRequest) (MetricObservation, error)
}

func (reader *recordingMetricReader) ReadMetrics(ctx context.Context, request MetricReadRequest) (MetricObservation, error) {
	reader.mu.Lock()
	reader.calls++
	read := reader.readFn
	reader.mu.Unlock()
	if read == nil {
		return MetricObservation{}, nil
	}
	return read(ctx, request)
}
func (reader *recordingMetricReader) count() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type recordingPrometheusReader struct {
	mu     sync.Mutex
	calls  int
	readFn func(context.Context, PrometheusReadRequest) (PrometheusObservation, error)
}

func (reader *recordingPrometheusReader) QueryPrometheus(ctx context.Context, request PrometheusReadRequest) (PrometheusObservation, error) {
	reader.mu.Lock()
	reader.calls++
	read := reader.readFn
	reader.mu.Unlock()
	if read == nil {
		return PrometheusObservation{}, nil
	}
	return read(ctx, request)
}
func (reader *recordingPrometheusReader) count() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type recordingLokiReader struct {
	mu     sync.Mutex
	calls  int
	readFn func(context.Context, LokiReadRequest) (LokiObservation, error)
}

func (reader *recordingLokiReader) QueryLoki(ctx context.Context, request LokiReadRequest) (LokiObservation, error) {
	reader.mu.Lock()
	reader.calls++
	read := reader.readFn
	reader.mu.Unlock()
	if read == nil {
		return LokiObservation{}, nil
	}
	return read(ctx, request)
}
func (reader *recordingLokiReader) count() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type recordingObservationPolicy struct {
	mu        sync.Mutex
	decision  ObservationPolicyDecision
	decisions []ObservationPolicyDecision
	calls     int
}

func (policy *recordingObservationPolicy) AuthorizeObservation(_ context.Context, request ObservationPolicyRequest) ObservationPolicyDecision {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if !request.valid() {
		return ObservationPolicyDenied
	}
	policy.calls++
	if len(policy.decisions) > 0 {
		decision := policy.decisions[0]
		policy.decisions = policy.decisions[1:]
		return decision
	}
	return policy.decision
}
func (policy *recordingObservationPolicy) count() int {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return policy.calls
}

func decodeSafeJSON(t *testing.T, value string) map[string]any {
	t.Helper()
	var result map[string]any
	if json.Unmarshal([]byte(value), &result) != nil {
		t.Fatalf("invalid safe JSON: %s", value)
	}
	return result
}
