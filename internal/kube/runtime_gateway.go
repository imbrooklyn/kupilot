package kube

import (
	"context"
	"sync"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
)

// ToolScopeBinding connects one verified opaque scope client to the fixed Tool
// read ports without exposing client-go values. ScopeManager remains the sole
// owner of client closure and generation invalidation.
type ToolScopeBinding struct {
	gateway *Gateway

	mu          sync.Mutex
	client      application.ScopeClient
	reader      *ToolResourceReader
	generation  int64
	policyGuard toolcontract.PolicyGenerationGuard
}

var (
	_ application.ScopeClientFactory                = (*ToolScopeBinding)(nil)
	_ application.ScopeInvalidationHook             = (*ToolScopeBinding)(nil)
	_ toolcontract.ResourceReader                   = (*ToolScopeBinding)(nil)
	_ toolcontract.ResourceQueryReader              = (*ToolScopeBinding)(nil)
	_ toolcontract.EventReader                      = (*ToolScopeBinding)(nil)
	_ toolcontract.PodLogReader                     = (*ToolScopeBinding)(nil)
	_ toolcontract.PodLogsReader                    = (*ToolScopeBinding)(nil)
	_ toolcontract.MetricReader                     = (*ToolScopeBinding)(nil)
	_ toolcontract.RelatedResourceReader            = (*ToolScopeBinding)(nil)
	_ toolcontract.RemoteTargetResolver             = (*ToolScopeBinding)(nil)
	_ toolcontract.RemoteCommandExecutor            = (*ToolScopeBinding)(nil)
	_ toolcontract.DiagnosticPodRunner              = (*ToolScopeBinding)(nil)
	_ application.RemoteDiagnosticActionRevalidator = (*ToolScopeBinding)(nil)
)

// NewToolScopeBinding wraps the existing narrow Gateway without creating a
// client or performing kubeconfig or Kubernetes I/O.
func NewToolScopeBinding(gateway *Gateway) (*ToolScopeBinding, error) {
	if gateway == nil || gateway.factory == nil {
		return nil, gatewayUnavailableError("create_tool_scope_binding")
	}
	return &ToolScopeBinding{gateway: gateway}, nil
}

// ConfigureResourcePolicyGuard binds the Application-owned policy-generation
// authority before any broad resource query can run. It performs no I/O.
func (gateway *ToolScopeBinding) ConfigureResourcePolicyGuard(guard toolcontract.PolicyGenerationGuard) error {
	if gateway == nil || gateway.gateway == nil || guard == nil {
		return newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_resource_policy_guard_invalid",
			"configure_resource_policy_guard",
			"The Kubernetes resource reader requires a current policy-generation guard.",
		)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.reader != nil {
		return newKubeSafeError(ClassPolicyDenied, "kubernetes_resource_policy_guard_late", "configure_resource_policy_guard", "The resource policy guard must be configured before resource reads start.")
	}
	gateway.policyGuard = guard
	return nil
}

// Contexts delegates safe local kubeconfig projection without performing a
// Kubernetes API request.
func (gateway *ToolScopeBinding) Contexts(ctx context.Context) ([]application.ContextCandidate, error) {
	if gateway == nil || gateway.gateway == nil {
		return nil, gatewayUnavailableError("list_contexts")
	}
	return gateway.gateway.Contexts(ctx)
}

// Create delegates client construction and remembers only the opaque handle
// needed to bind Tool reads after Application publishes an active scope.
func (gateway *ToolScopeBinding) Create(ctx context.Context, contextName string) (application.ScopeClient, error) {
	if gateway == nil || gateway.gateway == nil {
		return nil, gatewayUnavailableError("create_scope_client")
	}
	client, err := gateway.gateway.Create(ctx, contextName)
	if err != nil {
		return nil, err
	}
	gateway.mu.Lock()
	gateway.client = client
	gateway.reader = nil
	gateway.mu.Unlock()
	return client, nil
}

// InvalidateScope synchronously removes the old bound reader before the
// ScopeManager closes or reuses its opaque client.
func (gateway *ToolScopeBinding) InvalidateScope(generation int64) error {
	if gateway == nil || generation < 1 {
		return newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_scope_generation_invalid",
			"invalidate_tool_scope",
			"The Kubernetes scope state is invalid.",
		)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if generation <= gateway.generation {
		return newKubeSafeError(
			ClassStaleScope,
			"kubernetes_scope_generation_stale",
			"invalidate_tool_scope",
			"The Kubernetes scope changed before the request could start.",
		)
	}
	gateway.generation = generation
	gateway.reader = nil
	return nil
}

// ReadResource delegates one fixed direct read through the current immutable
// client/scope binding.
func (gateway *ToolScopeBinding) ReadResource(
	ctx context.Context,
	request toolcontract.ResourceReadRequest,
) (toolcontract.ResourceObservation, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.ResourceObservation{}, err
	}
	return reader.ReadResource(ctx, request)
}

// ListResources delegates one fixed selector-free bounded list.
func (gateway *ToolScopeBinding) ListResources(
	ctx context.Context,
	request toolcontract.ResourceListRequest,
) (toolcontract.ResourceObservationList, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.ResourceObservationList{}, err
	}
	return reader.ListResources(ctx, request)
}

// QueryResources delegates one exact broad read while keeping dynamic and
// continuation state inside internal/kube.
func (gateway *ToolScopeBinding) QueryResources(
	ctx context.Context,
	request toolcontract.ResourceQueryRequest,
) (toolcontract.ResourceQueryObservation, error) {
	reader, err := gateway.readerFor(request.Query.Scope)
	if err != nil {
		return toolcontract.ResourceQueryObservation{}, err
	}
	return reader.QueryResources(ctx, request)
}

// ReadEvents delegates the one fixed Events relationship read.
func (gateway *ToolScopeBinding) ReadEvents(
	ctx context.Context,
	request toolcontract.EventReadRequest,
) (toolcontract.EventObservationList, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.EventObservationList{}, err
	}
	return reader.ReadEvents(ctx, request)
}

// ReadPodLog delegates one bounded non-following Pod log read.
func (gateway *ToolScopeBinding) ReadPodLog(
	ctx context.Context,
	request toolcontract.PodLogReadRequest,
) (toolcontract.PodLogObservation, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.PodLogObservation{}, err
	}
	return reader.ReadPodLog(ctx, request)
}

// ReadPodLogs delegates one bounded all-container non-following log read.
func (gateway *ToolScopeBinding) ReadPodLogs(ctx context.Context, request toolcontract.PodLogsReadRequest) (toolcontract.PodLogsObservation, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.PodLogsObservation{}, err
	}
	return reader.ReadPodLogs(ctx, request)
}

// ReadMetrics delegates one exact typed Metrics API snapshot.
func (gateway *ToolScopeBinding) ReadMetrics(
	ctx context.Context,
	request toolcontract.MetricReadRequest,
) (toolcontract.MetricObservation, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.MetricObservation{}, err
	}
	return reader.ReadMetrics(ctx, request)
}

// ReadRelatedResources delegates one code-defined bounded relationship graph.
func (gateway *ToolScopeBinding) ReadRelatedResources(
	ctx context.Context,
	request toolcontract.RelatedReadRequest,
) (toolcontract.RelatedObservationGraph, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.RelatedObservationGraph{}, err
	}
	return reader.ReadRelatedResources(ctx, request)
}

func (gateway *ToolScopeBinding) ResolvePod(ctx context.Context, request toolcontract.PodResolveRequest) (toolcontract.ResolvedPod, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.ResolvedPod{}, err
	}
	return reader.ResolvePod(ctx, request)
}

func (gateway *ToolScopeBinding) ResolveService(ctx context.Context, request toolcontract.ServiceResolveRequest) (toolcontract.ResolvedService, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.ResolvedService{}, err
	}
	return reader.ResolveService(ctx, request)
}

func (gateway *ToolScopeBinding) ExecuteRemoteCommand(ctx context.Context, request toolcontract.RemoteCommandRequest) (toolcontract.RemoteCommandObservation, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.RemoteCommandObservation{}, err
	}
	return reader.ExecuteRemoteCommand(ctx, request)
}

func (gateway *ToolScopeBinding) RunDiagnosticPod(ctx context.Context, request toolcontract.DiagnosticPodRequest) (toolcontract.DiagnosticPodObservation, error) {
	reader, err := gateway.readerFor(request.Scope)
	if err != nil {
		return toolcontract.DiagnosticPodObservation{}, err
	}
	return reader.RunDiagnosticPod(ctx, request)
}

func (gateway *ToolScopeBinding) RevalidateRemoteDiagnosticAction(ctx context.Context, plan domain.RemoteDiagnosticActionPlan) error {
	reader, err := gateway.readerFor(plan.Scope)
	if err != nil {
		return err
	}
	return reader.RevalidateRemoteDiagnosticAction(ctx, plan)
}

func (gateway *ToolScopeBinding) readerFor(scope domain.ClusterScope) (*ToolResourceReader, error) {
	if gateway == nil || gateway.gateway == nil || scope.Validate() != nil {
		return nil, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_tool_reader_binding_invalid",
			"bind_tool_scope_reader",
			"The Kubernetes reader is not bound to the active context and namespace.",
		)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.client == nil || gateway.generation != scope.Generation {
		return nil, newKubeSafeError(
			ClassStaleScope,
			"kubernetes_scope_generation_stale",
			"bind_tool_scope_reader",
			"The Kubernetes context or namespace changed before the read completed.",
		)
	}
	if gateway.reader != nil {
		return gateway.reader, nil
	}
	var reader *ToolResourceReader
	var err error
	if gateway.policyGuard == nil {
		reader, err = NewToolResourceReader(gateway.gateway, gateway.client, scope)
	} else {
		reader, err = NewToolResourceReader(gateway.gateway, gateway.client, scope, gateway.policyGuard)
	}
	if err != nil {
		return nil, err
	}
	gateway.reader = reader
	return reader, nil
}
