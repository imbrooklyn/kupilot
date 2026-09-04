package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const maxMetricAge = 5 * time.Minute

var (
	ErrInvalidMetricToolDependencies = errors.New("metrics Tool dependencies are invalid")
	ErrInvalidMetricRead             = errors.New("metrics read data is invalid")
)

type MetricAvailability string

const (
	MetricAvailable   MetricAvailability = "available"
	MetricUnavailable MetricAvailability = "unavailable"
	MetricUnsupported MetricAvailability = "unsupported"
	MetricStale       MetricAvailability = "stale"
	MetricPartial     MetricAvailability = "partial"
)

func (state MetricAvailability) valid() bool {
	return state == MetricAvailable || state == MetricUnavailable || state == MetricUnsupported || state == MetricStale || state == MetricPartial
}

type MetricReadRequest struct {
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Reference        domain.ResourceRef
	MaxContainers    int
	LimitBytes       int
}

func (request MetricReadRequest) Validate() error {
	kind := domain.ResourceKind(request.Reference.Kind)
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || (kind != domain.ResourceKindPod && kind != domain.ResourceKindNode) ||
		domain.ValidateLiveResourceRef(request.Reference) != nil || !request.Scope.AllowsReference(request.Reference) ||
		request.MaxContainers < 1 || request.MaxContainers > domain.MaxMetricContainers ||
		request.LimitBytes < 1 || request.LimitBytes > domain.MaxObservabilityBytes {
		return ErrInvalidMetricRead
	}
	return nil
}

type ContainerMetricObservation struct {
	Name  string
	Usage domain.NormalizedResourceUsage
}

type MetricObservation struct {
	Resource     domain.ResourceRef
	Availability MetricAvailability
	Timestamp    time.Time
	Window       time.Duration
	Usage        domain.NormalizedResourceUsage
	Containers   []ContainerMetricObservation
	Partial      bool
	Truncated    bool
	SourceBytes  int
}

func (observation MetricObservation) Validate(request MetricReadRequest) error {
	if request.Validate() != nil || !observation.Availability.valid() ||
		domain.ValidateLiveResourceRef(observation.Resource) != nil ||
		observation.Resource.APIVersion != request.Reference.APIVersion || observation.Resource.Kind != request.Reference.Kind ||
		observation.Resource.Namespace != request.Reference.Namespace || observation.Resource.Name != request.Reference.Name ||
		request.Reference.UID != "" && observation.Resource.UID != request.Reference.UID ||
		observation.SourceBytes < 0 || observation.SourceBytes > request.LimitBytes || len(observation.Containers) > request.MaxContainers {
		return ErrInvalidMetricRead
	}
	if observation.Availability == MetricUnavailable || observation.Availability == MetricUnsupported {
		if !observation.Timestamp.IsZero() || observation.Window != 0 || len(observation.Containers) != 0 || observation.Usage != (domain.NormalizedResourceUsage{}) {
			return ErrInvalidMetricRead
		}
		return nil
	}
	if !validRequiredUTCTime(observation.Timestamp) || observation.Window <= 0 || observation.Window > maxMetricAge || observation.Usage.Validate() != nil {
		return ErrInvalidMetricRead
	}
	seen := make(map[string]struct{}, len(observation.Containers))
	for _, container := range observation.Containers {
		if !domain.ValidResourceName(container.Name) || container.Usage.Validate() != nil {
			return ErrInvalidMetricRead
		}
		if _, duplicate := seen[container.Name]; duplicate {
			return ErrInvalidMetricRead
		}
		seen[container.Name] = struct{}{}
	}
	return nil
}

type MetricReader interface {
	ReadMetrics(context.Context, MetricReadRequest) (MetricObservation, error)
}

type MetricToolDependencies struct {
	Reader      MetricReader
	ScopeGuard  ScopeGuard
	PolicyGuard PolicyGenerationGuard
	EvidenceIDs EvidenceIDSource
	Now         func() time.Time
}

func (dependencies MetricToolDependencies) validate() error {
	if dependencies.Reader == nil || dependencies.ScopeGuard == nil || dependencies.PolicyGuard == nil ||
		dependencies.EvidenceIDs == nil || dependencies.Now == nil || !validRequiredUTCTime(dependencies.Now()) {
		return ErrInvalidMetricToolDependencies
	}
	return nil
}

type GetPodMetricsTool struct{ dependencies MetricToolDependencies }
type GetNodeMetricsTool struct{ dependencies MetricToolDependencies }

var _ agent.Tool = (*GetPodMetricsTool)(nil)
var _ agent.Tool = (*GetNodeMetricsTool)(nil)

func NewGetPodMetricsTool(dependencies MetricToolDependencies) (*GetPodMetricsTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidMetricToolDependencies
	}
	return &GetPodMetricsTool{dependencies: dependencies}, nil
}

func NewGetNodeMetricsTool(dependencies MetricToolDependencies) (*GetNodeMetricsTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidMetricToolDependencies
	}
	return &GetNodeMetricsTool{dependencies: dependencies}, nil
}

func (tool *GetPodMetricsTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil {
		return ToolResult{}
	}
	return executeMetricTool(ctx, call, domain.ResourceKindPod, tool.dependencies)
}

func (tool *GetNodeMetricsTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil {
		return ToolResult{}
	}
	return executeMetricTool(ctx, call, domain.ResourceKindNode, tool.dependencies)
}

type metricToolArguments struct {
	Namespace string `json:"namespace,omitempty"`
	NodeName  string `json:"node_name,omitempty"`
	PodName   string `json:"pod_name,omitempty"`
	Purpose   string `json:"purpose"`
}

func decodeMetricCall(call BoundToolCall, kind domain.ResourceKind) (MetricReadRequest, error) {
	want := domain.ToolNameGetPodMetrics
	if kind == domain.ResourceKindNode {
		want = domain.ToolNameGetNodeMetrics
	}
	if call.Validate() != nil || call.Name() != want || call.Version() != agent.ToolCatalogVersion {
		return MetricReadRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments metricToolArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() {
		return MetricReadRequest{}, ErrInvalidCanonicalArguments
	}
	reference := domain.ResourceRef{APIVersion: "v1", Kind: string(kind), Namespace: arguments.Namespace, Name: arguments.PodName}
	if kind == domain.ResourceKindNode {
		reference.Namespace, reference.Name = "", arguments.NodeName
	}
	request := MetricReadRequest{Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Reference: reference, MaxContainers: call.Ceilings().MaxMetricContainers, LimitBytes: call.Ceilings().MaxMetricBytes}
	if request.Validate() != nil {
		return MetricReadRequest{}, ErrInvalidCanonicalArguments
	}
	return request, nil
}

type safeMetricContainer struct {
	CPUMilli    int64  `json:"cpu_milli"`
	MemoryBytes int64  `json:"memory_bytes"`
	Name        string `json:"name"`
}

type safeMetricData struct {
	Availability  MetricAvailability    `json:"availability"`
	Containers    []safeMetricContainer `json:"containers"`
	CPUMilli      int64                 `json:"cpu_milli"`
	MemoryBytes   int64                 `json:"memory_bytes"`
	ObservedAt    string                `json:"observed_at,omitempty"`
	Resource      safeResourceReference `json:"resource"`
	SourceTrust   string                `json:"source_trust"`
	Truncated     bool                  `json:"truncated"`
	WindowSeconds int64                 `json:"window_seconds,omitempty"`
}

func executeMetricTool(ctx context.Context, call BoundToolCall, kind domain.ResourceKind, dependencies MetricToolDependencies) ToolResult {
	if dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	started := dependencies.Now()
	request, err := decodeMetricCall(call, kind)
	if err != nil {
		return failedResult(call, started, domain.SafeErrorClassPolicyDenied)
	}
	if ctx == nil {
		return failedResult(call, started, domain.SafeErrorClassInvalidInput)
	}
	if ctx.Err() != nil {
		return failedResult(call, started, classifyFailure(ctx, ctx.Err()))
	}
	if !dependencies.ScopeGuard.Current(ctx, call.Scope()) || !dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return failedResult(call, started, domain.SafeErrorClassStaleScope)
	}
	observation, readErr := dependencies.Reader.ReadMetrics(ctx, request)
	completed := dependencies.Now()
	if !validRequiredUTCTime(completed) || completed.Before(started) {
		return failedResult(call, started, domain.SafeErrorClassInternal)
	}
	if !dependencies.ScopeGuard.Current(ctx, call.Scope()) || !dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration()) {
		return failedResult(call, completed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, completed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, completed, classifyFailure(ctx, readErr))
	}
	if observation.Availability == MetricAvailable && !observation.Timestamp.IsZero() && completed.Sub(observation.Timestamp) > maxMetricAge {
		observation.Availability = MetricStale
		observation.Partial = true
	}
	if observation.Validate(request) != nil || !observation.Timestamp.IsZero() && observation.Timestamp.After(completed) {
		return failedResult(call, completed, domain.SafeErrorClassInvalidExternalResponse)
	}
	return buildMetricResult(call, completed, observation, dependencies.EvidenceIDs)
}

func buildMetricResult(call BoundToolCall, observed time.Time, observation MetricObservation, evidenceIDs EvidenceIDSource) ToolResult {
	resource, _, err := safeEventReference(security.NewRedactor(), observation.Resource)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	containers := make([]safeMetricContainer, len(observation.Containers))
	for index, item := range observation.Containers {
		containers[index] = safeMetricContainer{Name: item.Name, CPUMilli: item.Usage.CPUMilli, MemoryBytes: item.Usage.MemoryBytes}
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	data := safeMetricData{Availability: observation.Availability, Containers: containers, CPUMilli: observation.Usage.CPUMilli,
		MemoryBytes: observation.Usage.MemoryBytes, Resource: resource, SourceTrust: security.UntrustedDataClass,
		Truncated: observation.Truncated || observation.Partial}
	if !observation.Timestamp.IsZero() {
		data.ObservedAt = observation.Timestamp.Format(time.RFC3339Nano)
		data.WindowSeconds = int64(observation.Window / time.Second)
	}
	partial := observation.Availability == MetricPartial || observation.Availability == MetricStale || observation.Partial || observation.Truncated
	reason := ""
	if partial {
		reason = string(observation.Availability)
		if observation.Truncated {
			reason = "item_limit"
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	guard, _ := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	encoded, err := guard.CanonicalizeJSONObject(raw)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	result := ToolResult{InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(), ObservedAt: observed,
		Status: domain.ToolResultStatusSuccess, DataJSON: encoded, Warnings: []domain.ToolResultWarning{}, Truncation: domain.ToolResultTruncation{ReturnedCount: len(containers)}}
	if partial {
		result.Status = domain.ToolResultStatusPartial
		result.Truncation.Truncated = true
		result.Truncation.Reason = reason
	}
	if observation.Availability == MetricUnavailable || observation.Availability == MetricUnsupported {
		result.Warnings = append(result.Warnings, domain.ToolResultWarning{Code: "metrics_" + string(observation.Availability), SafeMessage: "Kubernetes metrics are " + string(observation.Availability) + " for this exact target."})
	} else {
		from := observation.Timestamp.Add(-observation.Window).UTC()
		through := observation.Timestamp
		fact, truncated := boundedEvidenceFact(fmt.Sprintf("Metrics API snapshot for %s %s reports %d CPU millicores and %d memory bytes across %d container(s).", observation.Resource.Kind, observation.Resource.Name, observation.Usage.CPUMilli, observation.Usage.MemoryBytes, len(containers)))
		template := evidenceTemplate{category: domain.EvidenceCategoryMetricSnapshot, resource: observation.Resource, policyVersion: domain.ObservabilityPolicyVersion,
			fact: fact, sourcePath: "apis/metrics.k8s.io/v1beta1/" + metricResourcePath(observation.Resource), severity: stableSeverity(domain.EvidenceSeverityInfo),
			observedFrom: &from, observedThrough: &through, truncated: truncated || partial, partial: partial}
		result.Evidence = previewEvidence(call, observed, []evidenceTemplate{template})
		if _, measureErr := measureResult(call, result); measureErr != nil {
			return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
		}
		evidence, materializeErr := materializeEvidence(ResourceToolDependencies{EvidenceIDs: evidenceIDs}, call, observed, []evidenceTemplate{template})
		if materializeErr != nil {
			return failedResult(call, observed, domain.SafeErrorClassInternal)
		}
		result.Evidence = evidence
	}
	return finalizePlannedResult(call, observed, result)
}

func metricResourcePath(reference domain.ResourceRef) string {
	if reference.Kind == "Node" {
		return "nodes/" + reference.Name
	}
	return "namespaces/" + reference.Namespace + "/pods/" + reference.Name
}
