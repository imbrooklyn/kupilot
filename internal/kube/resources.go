package kube

import (
	"context"
	"io"
	"sort"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

const maxEndpointEntries = 1000

// GetResource performs one fixed typed GET in the bound Namespace and returns
// only a project-owned safe summary.
func (gateway *Gateway) GetResource(
	ctx context.Context,
	client application.ScopeClient,
	scope domain.ClusterScope,
	reference domain.ResourceRef,
) (domain.ResourceSummary, error) {
	if err := validateResourceContext(ctx, scope, "get_resource"); err != nil {
		return domain.ResourceSummary{}, err
	}
	kind, allowed := domain.ResourceKindForReference(reference)
	if !allowed {
		return domain.ResourceSummary{}, resourceKindDeniedError("get_resource")
	}
	if reference.Namespace != scope.Namespace {
		return domain.ResourceSummary{}, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_cross_namespace_denied",
			"get_resource",
			"Cross-Namespace Kubernetes reads are not allowed.",
		)
	}
	if domain.ValidateLiveResourceRef(reference) != nil {
		return domain.ResourceSummary{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_resource_reference_invalid",
			"get_resource",
			"The Kubernetes resource reference is invalid.",
		)
	}
	bundle, err := gateway.resourceBundle(client, scope, "get_resource")
	if err != nil {
		return domain.ResourceSummary{}, err
	}

	var projected domain.ResourceSummary
	switch kind {
	case domain.ResourceKindPod:
		object, rawErr := bundle.typed.CoreV1().Pods(scope.Namespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "get_resource"); err != nil {
			return domain.ResourceSummary{}, err
		}
		projected, err = projectPod(object, scope.Namespace, reference.Name)
	case domain.ResourceKindDeployment:
		object, rawErr := bundle.typed.AppsV1().Deployments(scope.Namespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "get_resource"); err != nil {
			return domain.ResourceSummary{}, err
		}
		projected, err = projectDeployment(object, scope.Namespace, reference.Name)
	case domain.ResourceKindReplicaSet:
		object, rawErr := bundle.typed.AppsV1().ReplicaSets(scope.Namespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "get_resource"); err != nil {
			return domain.ResourceSummary{}, err
		}
		projected, err = projectReplicaSet(object, scope.Namespace, reference.Name)
	case domain.ResourceKindJob:
		object, rawErr := bundle.typed.BatchV1().Jobs(scope.Namespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "get_resource"); err != nil {
			return domain.ResourceSummary{}, err
		}
		projected, err = projectJob(object, scope.Namespace, reference.Name)
	case domain.ResourceKindService:
		object, rawErr := bundle.typed.CoreV1().Services(scope.Namespace).Get(ctx, reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "get_resource"); err != nil {
			return domain.ResourceSummary{}, err
		}
		projected, err = projectService(object, scope.Namespace, reference.Name)
	default:
		return domain.ResourceSummary{}, resourceKindDeniedError("get_resource")
	}
	if err != nil || projected.Validate() != nil {
		return domain.ResourceSummary{}, invalidKubernetesProjectionError("get_resource")
	}
	return projected, nil
}

// ListResources performs one fixed typed LIST with no selector, pagination
// input, or all-Namespace fallback. Both the API request and local projection
// enforce the requested limit of at most 50.
func (gateway *Gateway) ListResources(
	ctx context.Context,
	client application.ScopeClient,
	scope domain.ClusterScope,
	kind domain.ResourceKind,
	limit int,
) (domain.ResourceList, error) {
	if err := validateResourceContext(ctx, scope, "list_resources"); err != nil {
		return domain.ResourceList{}, err
	}
	if !kind.Valid() {
		return domain.ResourceList{}, resourceKindDeniedError("list_resources")
	}
	if limit < 1 || limit > domain.MaxResourceSummaries {
		return domain.ResourceList{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_resource_limit_invalid",
			"list_resources",
			"The Kubernetes resource result limit is invalid.",
		)
	}
	bundle, err := gateway.resourceBundle(client, scope, "list_resources")
	if err != nil {
		return domain.ResourceList{}, err
	}
	options := metav1.ListOptions{Limit: int64(limit)}
	items := make([]domain.ResourceSummary, 0, limit)
	truncated := false

	switch kind {
	case domain.ResourceKindPod:
		list, rawErr := bundle.typed.CoreV1().Pods(scope.Namespace).List(ctx, options)
		if err := resourceCallError(ctx, rawErr, "list_resources"); err != nil {
			return domain.ResourceList{}, err
		}
		if list == nil {
			return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
		}
		truncated = list.Continue != "" || len(list.Items) > limit
		for index := 0; index < min(len(list.Items), limit); index++ {
			projected, projectErr := projectPod(&list.Items[index], scope.Namespace, "")
			if projectErr != nil {
				return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
			}
			items = append(items, projected)
		}
	case domain.ResourceKindDeployment:
		list, rawErr := bundle.typed.AppsV1().Deployments(scope.Namespace).List(ctx, options)
		if err := resourceCallError(ctx, rawErr, "list_resources"); err != nil {
			return domain.ResourceList{}, err
		}
		if list == nil {
			return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
		}
		truncated = list.Continue != "" || len(list.Items) > limit
		for index := 0; index < min(len(list.Items), limit); index++ {
			projected, projectErr := projectDeployment(&list.Items[index], scope.Namespace, "")
			if projectErr != nil {
				return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
			}
			items = append(items, projected)
		}
	case domain.ResourceKindReplicaSet:
		list, rawErr := bundle.typed.AppsV1().ReplicaSets(scope.Namespace).List(ctx, options)
		if err := resourceCallError(ctx, rawErr, "list_resources"); err != nil {
			return domain.ResourceList{}, err
		}
		if list == nil {
			return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
		}
		truncated = list.Continue != "" || len(list.Items) > limit
		for index := 0; index < min(len(list.Items), limit); index++ {
			projected, projectErr := projectReplicaSet(&list.Items[index], scope.Namespace, "")
			if projectErr != nil {
				return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
			}
			items = append(items, projected)
		}
	case domain.ResourceKindJob:
		list, rawErr := bundle.typed.BatchV1().Jobs(scope.Namespace).List(ctx, options)
		if err := resourceCallError(ctx, rawErr, "list_resources"); err != nil {
			return domain.ResourceList{}, err
		}
		if list == nil {
			return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
		}
		truncated = list.Continue != "" || len(list.Items) > limit
		for index := 0; index < min(len(list.Items), limit); index++ {
			projected, projectErr := projectJob(&list.Items[index], scope.Namespace, "")
			if projectErr != nil {
				return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
			}
			items = append(items, projected)
		}
	case domain.ResourceKindService:
		list, rawErr := bundle.typed.CoreV1().Services(scope.Namespace).List(ctx, options)
		if err := resourceCallError(ctx, rawErr, "list_resources"); err != nil {
			return domain.ResourceList{}, err
		}
		if list == nil {
			return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
		}
		truncated = list.Continue != "" || len(list.Items) > limit
		for index := 0; index < min(len(list.Items), limit); index++ {
			projected, projectErr := projectService(&list.Items[index], scope.Namespace, "")
			if projectErr != nil {
				return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
			}
			items = append(items, projected)
		}
	default:
		return domain.ResourceList{}, resourceKindDeniedError("list_resources")
	}

	sort.Slice(items, func(left, right int) bool {
		return items[left].Reference.Name < items[right].Reference.Name
	})
	if len(items) > limit {
		items = items[:limit]
	}
	result := domain.ResourceList{Items: items, Truncated: truncated}
	if result.Validate() != nil {
		return domain.ResourceList{}, invalidKubernetesProjectionError("list_resources")
	}
	return result, nil
}

// endpointCounts is intentionally package-private. EndpointSlice remains an
// indirect Service relationship source and cannot become a selectable Kind.
type endpointCounts struct {
	Ready     int
	NotReady  int
	Truncated bool
}

func (gateway *Gateway) countServiceEndpoints(
	ctx context.Context,
	client application.ScopeClient,
	scope domain.ClusterScope,
	serviceName string,
) (endpointCounts, error) {
	if err := validateResourceContext(ctx, scope, "count_service_endpoints"); err != nil {
		return endpointCounts{}, err
	}
	if !domain.ValidResourceName(serviceName) {
		return endpointCounts{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_service_name_invalid",
			"count_service_endpoints",
			"A valid Kubernetes Service name is required.",
		)
	}
	bundle, err := gateway.resourceBundle(client, scope, "count_service_endpoints")
	if err != nil {
		return endpointCounts{}, err
	}
	selector := labels.Set{discoveryv1.LabelServiceName: serviceName}.AsSelector().String()
	list, rawErr := bundle.typed.DiscoveryV1().EndpointSlices(scope.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
		Limit:         domain.MaxResourceSummaries,
	})
	if err := resourceCallError(ctx, rawErr, "count_service_endpoints"); err != nil {
		return endpointCounts{}, err
	}
	if list == nil {
		return endpointCounts{}, invalidKubernetesProjectionError("count_service_endpoints")
	}
	result := endpointCounts{Truncated: list.Continue != "" || len(list.Items) > domain.MaxResourceSummaries}
	entries := 0
	for index := 0; index < min(len(list.Items), domain.MaxResourceSummaries); index++ {
		item := &list.Items[index]
		if item.Namespace != scope.Namespace || item.Labels[discoveryv1.LabelServiceName] != serviceName {
			return endpointCounts{}, invalidKubernetesProjectionError("count_service_endpoints")
		}
		for _, endpoint := range item.Endpoints {
			if entries == maxEndpointEntries {
				result.Truncated = true
				return result, nil
			}
			entries++
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				result.Ready++
			} else {
				result.NotReady++
			}
		}
	}
	return result, nil
}

func (gateway *Gateway) resourceBundle(client application.ScopeClient, scope domain.ClusterScope, operation string) (*ClientBundle, error) {
	bundle, err := gateway.clientBundle(client, operation)
	if err != nil {
		return nil, err
	}
	contextInfo := bundle.Context()
	if contextInfo.Name != scope.Context {
		return nil, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_cross_context_denied",
			operation,
			"Cross-Context Kubernetes reads are not allowed.",
		)
	}
	return bundle, nil
}

func validateResourceContext(ctx context.Context, scope domain.ClusterScope, operation string) error {
	if ctx == nil {
		return newKubeSafeError(ClassInvalidInput, "kubernetes_context_required", operation, "An operation Context is required.")
	}
	if ctx.Err() != nil {
		return classifyContextError(ctx.Err(), operation)
	}
	if scope.Namespace == "" {
		return newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_all_namespaces_denied",
			operation,
			"All-Namespace Kubernetes reads are not allowed.",
		)
	}
	if scope.Validate() != nil {
		return newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_scope_invalid",
			operation,
			"The Kubernetes ClusterScope is invalid.",
		)
	}
	return nil
}

func resourceCallError(ctx context.Context, raw error, operation string) error {
	if ctx != nil && ctx.Err() != nil {
		return classifyContextError(ctx.Err(), operation)
	}
	if raw != nil {
		return classifyKubernetesError(operation, raw)
	}
	return nil
}

func resourceKindDeniedError(operation string) *SafeError {
	return newKubeSafeError(
		ClassPolicyDenied,
		"kubernetes_resource_kind_denied",
		operation,
		"The requested Kubernetes resource Kind is not allowed.",
	)
}

// ToolResourceReader binds the Tool-owned read port to one exact live scope
// client. It exposes no client-go value, selector, GVR, pagination, or write
// capability.
type ToolResourceReader struct {
	gateway *Gateway
	client  application.ScopeClient
	scope   domain.ClusterScope
}

// NewToolResourceReader validates one immutable scope/client binding without
// performing a Kubernetes API action.
func NewToolResourceReader(
	gateway *Gateway,
	client application.ScopeClient,
	scope domain.ClusterScope,
) (*ToolResourceReader, error) {
	if gateway == nil || scope.Validate() != nil {
		return nil, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_tool_reader_binding_invalid",
			"bind_tool_resource_reader",
			"The Kubernetes Tool reader binding is invalid.",
		)
	}
	if _, err := gateway.resourceBundle(client, scope, "bind_tool_resource_reader"); err != nil {
		return nil, err
	}
	return &ToolResourceReader{gateway: gateway, client: client, scope: scope}, nil
}

// ReadResource performs one exact typed GET and returns the Tool-owned reader
// DTO rather than a Kubernetes object.
func (reader *ToolResourceReader) ReadResource(
	ctx context.Context,
	request toolcontract.ResourceReadRequest,
) (toolcontract.ResourceObservation, error) {
	if err := reader.validateContext(ctx, request.Scope, "tool_get_resource"); err != nil {
		return toolcontract.ResourceObservation{}, err
	}
	kind, allowed := domain.ResourceKindForReference(request.Reference)
	if !allowed {
		return toolcontract.ResourceObservation{}, resourceKindDeniedError("tool_get_resource")
	}
	if request.Reference.Namespace != request.Scope.Namespace {
		return toolcontract.ResourceObservation{}, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_cross_namespace_denied",
			"tool_get_resource",
			"Cross-Namespace Kubernetes reads are not allowed.",
		)
	}
	if request.Validate() != nil {
		return toolcontract.ResourceObservation{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_tool_resource_request_invalid",
			"tool_get_resource",
			"The Kubernetes Tool resource request is invalid.",
		)
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, "tool_get_resource")
	if err != nil {
		return toolcontract.ResourceObservation{}, err
	}
	var observation toolcontract.ResourceObservation
	switch kind {
	case domain.ResourceKindPod:
		object, rawErr := bundle.typed.CoreV1().Pods(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "tool_get_resource"); err != nil {
			return toolcontract.ResourceObservation{}, err
		}
		observation, err = projectToolPod(object, request.Scope.Namespace, request.Reference.Name, request.Detail)
	case domain.ResourceKindDeployment:
		object, rawErr := bundle.typed.AppsV1().Deployments(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "tool_get_resource"); err != nil {
			return toolcontract.ResourceObservation{}, err
		}
		observation, err = projectToolDeployment(object, request.Scope.Namespace, request.Reference.Name, request.Detail)
	case domain.ResourceKindReplicaSet:
		object, rawErr := bundle.typed.AppsV1().ReplicaSets(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "tool_get_resource"); err != nil {
			return toolcontract.ResourceObservation{}, err
		}
		observation, err = projectToolReplicaSet(object, request.Scope.Namespace, request.Reference.Name, request.Detail)
	case domain.ResourceKindJob:
		object, rawErr := bundle.typed.BatchV1().Jobs(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "tool_get_resource"); err != nil {
			return toolcontract.ResourceObservation{}, err
		}
		observation, err = projectToolJob(object, request.Scope.Namespace, request.Reference.Name, request.Detail)
	case domain.ResourceKindService:
		object, rawErr := bundle.typed.CoreV1().Services(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, "tool_get_resource"); err != nil {
			return toolcontract.ResourceObservation{}, err
		}
		observation, err = projectToolService(object, request.Scope.Namespace, request.Reference.Name, request.Detail)
	default:
		return toolcontract.ResourceObservation{}, resourceKindDeniedError("tool_get_resource")
	}
	if err != nil || observation.Validate() != nil {
		return toolcontract.ResourceObservation{}, invalidKubernetesProjectionError("tool_get_resource")
	}
	return observation, nil
}

// ListResources performs one fixed typed LIST by reusing the S10 bounded
// summary reader and translating it to the Tool-owned reader DTO.
func (reader *ToolResourceReader) ListResources(
	ctx context.Context,
	request toolcontract.ResourceListRequest,
) (toolcontract.ResourceObservationList, error) {
	if err := reader.validateContext(ctx, request.Scope, "tool_list_resources"); err != nil {
		return toolcontract.ResourceObservationList{}, err
	}
	if !request.Kind.Valid() {
		return toolcontract.ResourceObservationList{}, resourceKindDeniedError("tool_list_resources")
	}
	if request.Validate() != nil {
		return toolcontract.ResourceObservationList{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_tool_resource_list_invalid",
			"tool_list_resources",
			"The Kubernetes Tool resource list request is invalid.",
		)
	}
	list, err := reader.gateway.ListResources(ctx, reader.client, request.Scope, request.Kind, request.Limit)
	if err != nil {
		return toolcontract.ResourceObservationList{}, err
	}
	result := toolcontract.ResourceObservationList{
		Items:     make([]toolcontract.ResourceObservation, len(list.Items)),
		Truncated: list.Truncated,
	}
	for index, item := range list.Items {
		result.Items[index] = toolcontract.ResourceObservation{Summary: item}
	}
	if result.Validate(request) != nil {
		return toolcontract.ResourceObservationList{}, invalidKubernetesProjectionError("tool_list_resources")
	}
	return result, nil
}

// ReadEvents performs one target precheck followed by one fixed namespaced
// Event LIST. The field selector is constructed internally from the verified
// target and cannot be supplied by a model call.
func (reader *ToolResourceReader) ReadEvents(
	ctx context.Context,
	request toolcontract.EventReadRequest,
) (toolcontract.EventObservationList, error) {
	const operation = "tool_get_events"
	if err := reader.validateContext(ctx, request.Scope, operation); err != nil {
		return toolcontract.EventObservationList{}, err
	}
	if _, allowed := domain.ResourceKindForReference(request.Reference); !allowed {
		return toolcontract.EventObservationList{}, resourceKindDeniedError(operation)
	}
	if request.Reference.Namespace != request.Scope.Namespace {
		return toolcontract.EventObservationList{}, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_cross_namespace_denied",
			operation,
			"Cross-Namespace Kubernetes reads are not allowed.",
		)
	}
	if request.Validate() != nil {
		return toolcontract.EventObservationList{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_tool_event_request_invalid",
			operation,
			"The Kubernetes Event request is invalid.",
		)
	}
	target, err := reader.ReadResource(ctx, toolcontract.ResourceReadRequest{
		Scope: request.Scope, Reference: request.Reference, Detail: toolcontract.ResourceDetailSummary,
	})
	if err != nil {
		return toolcontract.EventObservationList{}, err
	}
	verified := target.Summary.Reference
	if request.Reference.UID != "" && request.Reference.UID != verified.UID {
		return toolcontract.EventObservationList{}, newKubeSafeError(
			ClassNotFound,
			"kubernetes_event_target_uid_not_found",
			operation,
			"The requested Kubernetes object was not found.",
		)
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, operation)
	if err != nil {
		return toolcontract.EventObservationList{}, err
	}
	selectorTerms := []fields.Selector{
		fields.OneTermEqualSelector("involvedObject.kind", verified.Kind),
		fields.OneTermEqualSelector("involvedObject.name", verified.Name),
		fields.OneTermEqualSelector("involvedObject.namespace", verified.Namespace),
	}
	uidFilterDegraded := verified.UID == ""
	if !uidFilterDegraded {
		selectorTerms = append(selectorTerms, fields.OneTermEqualSelector("involvedObject.uid", verified.UID))
	}
	selector := fields.AndSelectors(selectorTerms...).String()
	list, rawErr := bundle.typed.CoreV1().Events(request.Scope.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: selector,
		Limit:         int64(request.Limit),
	})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return toolcontract.EventObservationList{}, err
	}
	if list == nil {
		return toolcontract.EventObservationList{}, invalidKubernetesProjectionError(operation)
	}
	result := toolcontract.EventObservationList{
		Target:            verified,
		Items:             make([]toolcontract.EventObservation, 0, min(len(list.Items), request.Limit)),
		UIDFilterDegraded: uidFilterDegraded,
		Truncated:         list.Continue != "" || len(list.Items) > request.Limit,
	}
	for index := 0; index < min(len(list.Items), request.Limit); index++ {
		item := &list.Items[index]
		if !eventMatchesTarget(item, verified) {
			result.Truncated = true
			continue
		}
		projected, projectErr := projectToolEvent(item, verified)
		if projectErr != nil {
			return toolcontract.EventObservationList{}, invalidKubernetesProjectionError(operation)
		}
		if projected.LastObservedAt.Before(request.NotBefore) {
			continue
		}
		result.Items = append(result.Items, projected)
	}
	if result.Validate(request) != nil {
		return toolcontract.EventObservationList{}, invalidKubernetesProjectionError(operation)
	}
	return result, nil
}

// ReadPodLog performs one exact Pod precheck and, only after deterministic
// container selection, one bounded pods/log GET.
func (reader *ToolResourceReader) ReadPodLog(
	ctx context.Context,
	request toolcontract.PodLogReadRequest,
) (toolcontract.PodLogObservation, error) {
	const operation = "tool_get_pod_logs"
	if request.Previous {
		const previousOperation = "tool_get_previous_pod_logs"
		return reader.readPodLog(ctx, request, previousOperation)
	}
	return reader.readPodLog(ctx, request, operation)
}

func (reader *ToolResourceReader) readPodLog(
	ctx context.Context,
	request toolcontract.PodLogReadRequest,
	operation string,
) (toolcontract.PodLogObservation, error) {
	if err := reader.validateContext(ctx, request.Scope, operation); err != nil {
		return toolcontract.PodLogObservation{}, err
	}
	if request.Validate() != nil {
		return toolcontract.PodLogObservation{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_tool_log_request_invalid",
			operation,
			"The Kubernetes Pod log request is invalid.",
		)
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, operation)
	if err != nil {
		return toolcontract.PodLogObservation{}, err
	}
	pod, rawErr := bundle.typed.CoreV1().Pods(request.Scope.Namespace).Get(ctx, request.PodName, metav1.GetOptions{})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return toolcontract.PodLogObservation{}, err
	}
	projectedPod, projectErr := projectPod(pod, request.Scope.Namespace, request.PodName)
	if projectErr != nil {
		return toolcontract.PodLogObservation{}, invalidKubernetesProjectionError(operation)
	}
	selection, err := selectPodLogContainer(pod, request.Container, operation)
	if err != nil {
		return toolcontract.PodLogObservation{}, err
	}
	observation := toolcontract.PodLogObservation{
		Pod:                   projectedPod.Reference,
		Container:             selection.name,
		InitContainer:         selection.init,
		Previous:              request.Previous,
		Availability:          toolcontract.PodLogAvailable,
		RestartCount:          selection.restartCount,
		LastTerminationReason: projectToolText(selection.lastTerminationReason, maxProjectedIdentityBytes),
	}
	if request.Previous && !selection.previousAvailable {
		observation.Availability = toolcontract.PodLogNoPreviousInstance
		if observation.Validate(request) != nil {
			return toolcontract.PodLogObservation{}, invalidKubernetesProjectionError(operation)
		}
		return observation, nil
	}
	if !request.Previous && !selection.currentAvailable {
		observation.Availability = toolcontract.PodLogContainerNotRunning
		if observation.Validate(request) != nil {
			return toolcontract.PodLogObservation{}, invalidKubernetesProjectionError(operation)
		}
		return observation, nil
	}
	tailLines := int64(request.TailLines)
	sinceSeconds := int64(request.SinceSeconds)
	limitBytes := int64(request.LimitBytes)
	stream, rawErr := bundle.typed.CoreV1().Pods(request.Scope.Namespace).GetLogs(request.PodName, &corev1.PodLogOptions{
		Container:    selection.name,
		Previous:     request.Previous,
		TailLines:    &tailLines,
		SinceSeconds: &sinceSeconds,
		LimitBytes:   &limitBytes,
	}).Stream(ctx)
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return toolcontract.PodLogObservation{}, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, int64(request.LimitBytes)+1))
	closeErr := stream.Close()
	if ctx.Err() != nil {
		return toolcontract.PodLogObservation{}, resourceCallError(ctx, ctx.Err(), operation)
	}
	if readErr != nil {
		return toolcontract.PodLogObservation{}, resourceCallError(ctx, readErr, operation)
	}
	if closeErr != nil {
		return toolcontract.PodLogObservation{}, resourceCallError(ctx, closeErr, operation)
	}
	if len(payload) > request.LimitBytes {
		payload = payload[:request.LimitBytes]
		observation.Truncated = true
	}
	content, contentErr := toolcontract.NewPodLogContent(payload, request.LimitBytes)
	if contentErr != nil {
		return toolcontract.PodLogObservation{}, invalidKubernetesProjectionError(operation)
	}
	observation.Content = content
	if observation.Validate(request) != nil {
		return toolcontract.PodLogObservation{}, invalidKubernetesProjectionError(operation)
	}
	return observation, nil
}

type selectedPodLogContainer struct {
	name                  string
	init                  bool
	restartCount          int32
	lastTerminationReason string
	previousAvailable     bool
	currentAvailable      bool
}

func selectPodLogContainer(pod *corev1.Pod, requested, operation string) (selectedPodLogContainer, error) {
	if pod == nil {
		return selectedPodLogContainer{}, invalidKubernetesProjectionError(operation)
	}
	name := requested
	initContainer := false
	if name == "" {
		if len(pod.Spec.Containers) != 1 {
			return selectedPodLogContainer{}, newKubeSafeError(
				ClassInvalidInput,
				"kubernetes_log_container_required",
				operation,
				"A container name is required when the Pod has multiple application containers.",
			)
		}
		name = pod.Spec.Containers[0].Name
	} else {
		matches := 0
		for _, container := range pod.Spec.Containers {
			if container.Name == name {
				matches++
			}
		}
		for _, container := range pod.Spec.InitContainers {
			if container.Name == name {
				matches++
				initContainer = true
			}
		}
		if matches == 0 {
			return selectedPodLogContainer{}, newKubeSafeError(
				ClassNotFound,
				"kubernetes_log_container_not_found",
				operation,
				"The requested Pod container was not found.",
			)
		}
		if matches != 1 {
			return selectedPodLogContainer{}, invalidKubernetesProjectionError(operation)
		}
	}
	if !domain.ValidResourceName(name) {
		return selectedPodLogContainer{}, invalidKubernetesProjectionError(operation)
	}
	statuses := pod.Status.ContainerStatuses
	if initContainer {
		statuses = pod.Status.InitContainerStatuses
	}
	var status *corev1.ContainerStatus
	for index := range statuses {
		if statuses[index].Name != name {
			continue
		}
		if status != nil {
			return selectedPodLogContainer{}, invalidKubernetesProjectionError(operation)
		}
		status = &statuses[index]
	}
	selection := selectedPodLogContainer{name: name, init: initContainer}
	if status == nil {
		return selection, nil
	}
	selection.restartCount = status.RestartCount
	selection.currentAvailable = status.State.Running != nil || status.State.Terminated != nil
	if status.LastTerminationState.Terminated != nil {
		selection.lastTerminationReason = status.LastTerminationState.Terminated.Reason
		selection.previousAvailable = status.RestartCount > 0
	}
	return selection, nil
}

func eventMatchesTarget(event *corev1.Event, target domain.ResourceRef) bool {
	if event == nil {
		return false
	}
	reference := event.InvolvedObject
	if reference.APIVersion != target.APIVersion || reference.Kind != target.Kind || reference.Namespace != target.Namespace || reference.Name != target.Name {
		return false
	}
	return target.UID == "" || string(reference.UID) == target.UID
}

func projectToolEvent(event *corev1.Event, target domain.ResourceRef) (toolcontract.EventObservation, error) {
	if event == nil {
		return toolcontract.EventObservation{}, errUnsafeKubernetesProjection
	}
	first, last := eventObservationTimes(event)
	if !validProjectedEventTime(first) || !validProjectedEventTime(last) || last.Before(first) {
		return toolcontract.EventObservation{}, errUnsafeKubernetesProjection
	}
	count := event.Count
	if event.Series != nil && event.Series.Count > count {
		count = event.Series.Count
	}
	if count < 1 {
		count = 1
	}
	result := toolcontract.EventObservation{
		Type:                projectToolText(event.Type, maxProjectedIdentityBytes),
		Reason:              projectToolText(event.Reason, maxProjectedIdentityBytes),
		Message:             projectToolText(event.Message, maxToolProjectedTextBytes),
		Involved:            target,
		ReportingSource:     projectToolText(event.Source.Component, maxProjectedIdentityBytes),
		ReportingController: projectToolText(event.ReportingController, maxProjectedIdentityBytes),
		FirstObservedAt:     first,
		LastObservedAt:      last,
		Count:               count,
	}
	result.Truncated = result.Type.Truncated || result.Reason.Truncated || result.Message.Truncated ||
		result.ReportingSource.Truncated || result.ReportingController.Truncated
	return result, nil
}

func eventObservationTimes(event *corev1.Event) (time.Time, time.Time) {
	first := event.FirstTimestamp.Time
	if event.FirstTimestamp.IsZero() {
		first = event.EventTime.Time
	}
	if first.IsZero() {
		first = event.CreationTimestamp.Time
	}
	last := time.Time{}
	if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
		last = event.Series.LastObservedTime.Time
	}
	if last.IsZero() && !event.LastTimestamp.IsZero() {
		last = event.LastTimestamp.Time
	}
	if last.IsZero() && !event.EventTime.IsZero() {
		last = event.EventTime.Time
	}
	if last.IsZero() {
		last = first
	}
	return first.UTC(), last.UTC()
}

func validProjectedEventTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0
}

func (reader *ToolResourceReader) validateContext(ctx context.Context, scope domain.ClusterScope, operation string) error {
	if ctx == nil {
		return newKubeSafeError(ClassInvalidInput, "kubernetes_context_required", operation, "An operation Context is required.")
	}
	if ctx.Err() != nil {
		return classifyContextError(ctx.Err(), operation)
	}
	if reader == nil || reader.gateway == nil || reader.client == nil || reader.scope.Validate() != nil {
		return gatewayUnavailableError(operation)
	}
	if scope != reader.scope {
		return newKubeSafeError(
			ClassStaleScope,
			"kubernetes_tool_reader_scope_stale",
			operation,
			"The bound Kubernetes Tool reader scope is stale.",
		)
	}
	return nil
}

// Compile-time checks keep the adapter bound to the current consumer ports.
var (
	_ application.ScopeClientFactory = (*Gateway)(nil)
	_ application.NamespaceReader    = (*Gateway)(nil)
	_ application.ResourceService    = (*Gateway)(nil)
	_ application.ScopeClient        = (*scopeClient)(nil)
	_ toolcontract.ResourceReader    = (*ToolResourceReader)(nil)
	_ toolcontract.EventReader       = (*ToolResourceReader)(nil)
	_ toolcontract.PodLogReader      = (*ToolResourceReader)(nil)
)
