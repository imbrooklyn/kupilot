package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	maxEndpointEntries     = 1000
	maxRelatedSelectorKeys = 64
)

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

// ReadRelatedResources performs only the fixed relationship reads admitted by
// the root Kind. Every selector is derived from an already-read object, and the
// result contains project-owned observations rather than Kubernetes objects.
func (reader *ToolResourceReader) ReadRelatedResources(
	ctx context.Context,
	request toolcontract.RelatedReadRequest,
) (toolcontract.RelatedObservationGraph, error) {
	const operation = "tool_get_related_resources"
	if err := reader.validateContext(ctx, request.Scope, operation); err != nil {
		return toolcontract.RelatedObservationGraph{}, err
	}
	if _, allowed := domain.ResourceKindForReference(request.Reference); !allowed {
		return toolcontract.RelatedObservationGraph{}, resourceKindDeniedError(operation)
	}
	if request.Reference.Namespace != request.Scope.Namespace {
		return toolcontract.RelatedObservationGraph{}, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_cross_namespace_denied",
			operation,
			"Cross-Namespace Kubernetes reads are not allowed.",
		)
	}
	if request.Validate() != nil {
		return toolcontract.RelatedObservationGraph{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_tool_related_request_invalid",
			operation,
			"The Kubernetes relationship request is invalid.",
		)
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, operation)
	if err != nil {
		return toolcontract.RelatedObservationGraph{}, err
	}
	root, err := readRelatedRoot(ctx, bundle, request, operation)
	if err != nil {
		return toolcontract.RelatedObservationGraph{}, err
	}
	if request.Reference.UID != "" && request.Reference.UID != root.observation.Summary.Reference.UID {
		return toolcontract.RelatedObservationGraph{}, newKubeSafeError(
			ClassNotFound,
			"kubernetes_related_target_uid_not_found",
			operation,
			"The requested Kubernetes object was not found.",
		)
	}
	builder := newRelatedGraphBuilder(request, root.observation)
	switch {
	case root.deployment != nil:
		err = reader.expandRelatedDeployment(ctx, bundle, builder, root.deployment, operation)
	case root.replicaSet != nil:
		err = reader.expandRelatedReplicaSet(ctx, bundle, builder, root.replicaSet, operation)
	case root.pod != nil:
		err = reader.expandRelatedPod(ctx, bundle, builder, root.pod, operation)
	case root.job != nil:
		err = reader.expandRelatedJob(ctx, bundle, builder, root.job, operation)
	case root.service != nil:
		err = reader.expandRelatedService(ctx, bundle, builder, root.service, operation)
	default:
		err = invalidKubernetesProjectionError(operation)
	}
	if err != nil {
		return toolcontract.RelatedObservationGraph{}, err
	}
	result := builder.result()
	if result.Validate(request) != nil {
		return toolcontract.RelatedObservationGraph{}, invalidKubernetesProjectionError(operation)
	}
	return result, nil
}

type relatedRootObject struct {
	observation toolcontract.ResourceObservation
	deployment  *appsv1.Deployment
	replicaSet  *appsv1.ReplicaSet
	pod         *corev1.Pod
	job         *batchv1.Job
	service     *corev1.Service
}

func readRelatedRoot(
	ctx context.Context,
	bundle *ClientBundle,
	request toolcontract.RelatedReadRequest,
	operation string,
) (relatedRootObject, error) {
	kind, _ := domain.ResourceKindForReference(request.Reference)
	switch kind {
	case domain.ResourceKindDeployment:
		object, rawErr := bundle.typed.AppsV1().Deployments(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, operation); err != nil {
			return relatedRootObject{}, err
		}
		observation, err := projectToolDeployment(object, request.Scope.Namespace, request.Reference.Name, toolcontract.ResourceDetailSummary)
		return relatedRootObject{observation: observation, deployment: object}, projectionOrError(err, observation, operation)
	case domain.ResourceKindReplicaSet:
		object, rawErr := bundle.typed.AppsV1().ReplicaSets(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, operation); err != nil {
			return relatedRootObject{}, err
		}
		observation, err := projectToolReplicaSet(object, request.Scope.Namespace, request.Reference.Name, toolcontract.ResourceDetailSummary)
		return relatedRootObject{observation: observation, replicaSet: object}, projectionOrError(err, observation, operation)
	case domain.ResourceKindPod:
		object, rawErr := bundle.typed.CoreV1().Pods(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, operation); err != nil {
			return relatedRootObject{}, err
		}
		observation, err := projectToolPod(object, request.Scope.Namespace, request.Reference.Name, toolcontract.ResourceDetailSummary)
		return relatedRootObject{observation: observation, pod: object}, projectionOrError(err, observation, operation)
	case domain.ResourceKindJob:
		object, rawErr := bundle.typed.BatchV1().Jobs(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, operation); err != nil {
			return relatedRootObject{}, err
		}
		observation, err := projectToolJob(object, request.Scope.Namespace, request.Reference.Name, toolcontract.ResourceDetailSummary)
		return relatedRootObject{observation: observation, job: object}, projectionOrError(err, observation, operation)
	case domain.ResourceKindService:
		object, rawErr := bundle.typed.CoreV1().Services(request.Scope.Namespace).Get(ctx, request.Reference.Name, metav1.GetOptions{})
		if err := resourceCallError(ctx, rawErr, operation); err != nil {
			return relatedRootObject{}, err
		}
		observation, err := projectToolService(object, request.Scope.Namespace, request.Reference.Name, toolcontract.ResourceDetailSummary)
		return relatedRootObject{observation: observation, service: object}, projectionOrError(err, observation, operation)
	default:
		return relatedRootObject{}, resourceKindDeniedError(operation)
	}
}

func projectionOrError(err error, observation toolcontract.ResourceObservation, operation string) error {
	if err != nil || observation.Validate() != nil {
		return invalidKubernetesProjectionError(operation)
	}
	return nil
}

type relatedGraphBuilder struct {
	request toolcontract.RelatedReadRequest
	graph   toolcontract.RelatedObservationGraph
	nodes   map[string]int
	edges   map[string]struct{}
	gaps    map[string]struct{}
}

func newRelatedGraphBuilder(request toolcontract.RelatedReadRequest, root toolcontract.ResourceObservation) *relatedGraphBuilder {
	reference := relatedReferenceFromResource(root.Summary.Reference, false)
	return &relatedGraphBuilder{
		request: request,
		graph: toolcontract.RelatedObservationGraph{
			Root:  reference,
			Nodes: []toolcontract.RelatedNodeObservation{{Reference: reference, Resource: root, Fetched: true, Hop: 0}},
			Edges: []toolcontract.RelatedEdgeObservation{},
			Gaps:  []toolcontract.RelatedGapObservation{},
		},
		nodes: map[string]int{relatedReferenceKey(reference): 0},
		edges: make(map[string]struct{}),
		gaps:  make(map[string]struct{}),
	}
}

func (builder *relatedGraphBuilder) result() toolcontract.RelatedObservationGraph {
	sort.Slice(builder.graph.Nodes, func(left, right int) bool {
		return relatedNodeAdapterSortKey(builder.graph.Nodes[left]) < relatedNodeAdapterSortKey(builder.graph.Nodes[right])
	})
	sort.Slice(builder.graph.Edges, func(left, right int) bool {
		return relatedEdgeAdapterSortKey(builder.graph.Edges[left]) < relatedEdgeAdapterSortKey(builder.graph.Edges[right])
	})
	sort.Slice(builder.graph.Gaps, func(left, right int) bool {
		return relatedGapAdapterSortKey(builder.graph.Gaps[left]) < relatedGapAdapterSortKey(builder.graph.Gaps[right])
	})
	return builder.graph
}

func (builder *relatedGraphBuilder) addFetched(
	from toolcontract.RelatedReference,
	observation toolcontract.ResourceObservation,
	relation toolcontract.RelatedRelation,
	hop int,
	selector labels.Selector,
	selectorKeys int,
) bool {
	reference := relatedReferenceFromResource(observation.Summary.Reference, false)
	return builder.addConnectedNode(from, toolcontract.RelatedNodeObservation{
		Reference: reference, Resource: observation, Fetched: true, Hop: hop,
	}, relation, hop, selector, selectorKeys)
}

func (builder *relatedGraphBuilder) addReferenceOnly(
	from, reference toolcontract.RelatedReference,
	relation toolcontract.RelatedRelation,
	hop int,
) bool {
	reference.ReferenceOnly = true
	reference.ResourceVersion = ""
	return builder.addConnectedNode(from, toolcontract.RelatedNodeObservation{
		Reference: reference, Fetched: false, Hop: hop,
	}, relation, hop, nil, 0)
}

func (builder *relatedGraphBuilder) addConnectedNode(
	from toolcontract.RelatedReference,
	node toolcontract.RelatedNodeObservation,
	relation toolcontract.RelatedRelation,
	hop int,
	selector labels.Selector,
	selectorKeys int,
) bool {
	toKey := relatedReferenceKey(node.Reference)
	if toKey == relatedReferenceKey(builder.graph.Root) {
		builder.addGap(from, relation, domain.SafeErrorClassPolicyDenied, hop)
		return false
	}
	edgeKey := relatedEdgeAdapterKey(relation, from, node.Reference)
	if _, duplicate := builder.edges[edgeKey]; duplicate {
		return true
	}
	if len(builder.graph.Edges) >= builder.request.MaxEdges {
		builder.graph.Truncated = true
		return false
	}
	if _, exists := builder.nodes[toKey]; !exists {
		if len(builder.graph.Nodes) >= builder.request.MaxNodes {
			builder.graph.Truncated = true
			return false
		}
		builder.nodes[toKey] = len(builder.graph.Nodes)
		builder.graph.Nodes = append(builder.graph.Nodes, node)
	}
	edge := toolcontract.RelatedEdgeObservation{
		From: from, To: node.Reference, ToPresent: true, Relation: relation, Hop: hop,
	}
	if relation == toolcontract.RelatedRelationSelectorMatch {
		edge.SelectorKeyCount = selectorKeys
		edge.SelectorFingerprint = domain.SHA256Hex(selector.String())
	}
	builder.edges[edgeKey] = struct{}{}
	builder.graph.Edges = append(builder.graph.Edges, edge)
	return true
}

func (builder *relatedGraphBuilder) addEndpointCounts(
	service toolcontract.RelatedReference,
	hop int,
	counts endpointCounts,
) bool {
	edgeKey := relatedEdgeAdapterKey(toolcontract.RelatedRelationServiceEndpoints, service, toolcontract.RelatedReference{})
	if _, duplicate := builder.edges[edgeKey]; duplicate {
		return true
	}
	if len(builder.graph.Edges) >= builder.request.MaxEdges {
		builder.graph.Truncated = true
		return false
	}
	builder.edges[edgeKey] = struct{}{}
	builder.graph.Edges = append(builder.graph.Edges, toolcontract.RelatedEdgeObservation{
		From: service, Relation: toolcontract.RelatedRelationServiceEndpoints, Hop: hop,
		ReadyEndpoints: counts.Ready, NotReadyEndpoints: counts.NotReady, Truncated: counts.Truncated,
	})
	builder.graph.Truncated = builder.graph.Truncated || counts.Truncated
	return true
}

func (builder *relatedGraphBuilder) addGap(
	from toolcontract.RelatedReference,
	relation toolcontract.RelatedRelation,
	class domain.SafeErrorClass,
	hop int,
) {
	key := relatedEdgeAdapterKey(relation, from, toolcontract.RelatedReference{}) + "\x00" + string(class)
	if _, duplicate := builder.gaps[key]; duplicate {
		return
	}
	if len(builder.graph.Gaps) >= builder.request.MaxEdges {
		builder.graph.Truncated = true
		return
	}
	builder.gaps[key] = struct{}{}
	builder.graph.Gaps = append(builder.graph.Gaps, toolcontract.RelatedGapObservation{
		From: from, Relation: relation, Class: class, Hop: hop,
	})
}

func (builder *relatedGraphBuilder) hasCapacityForEdge() bool {
	if len(builder.graph.Edges) >= builder.request.MaxEdges {
		builder.graph.Truncated = true
		return false
	}
	return true
}

func (reader *ToolResourceReader) expandRelatedDeployment(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	deployment *appsv1.Deployment,
	operation string,
) error {
	if !relatedIncludes(builder.request, toolcontract.RelatedIncludeReplicaSets) &&
		!relatedIncludes(builder.request, toolcontract.RelatedIncludePods) {
		return nil
	}
	root := builder.graph.Root
	if deployment == nil || deployment.UID == "" {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	selector, _, ok := safeObjectSelector(deployment.Spec.Selector)
	if !ok {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	if !builder.hasCapacityForEdge() || len(builder.graph.Nodes) >= builder.request.MaxNodes {
		builder.graph.Truncated = true
		return nil
	}
	limit := relatedListLimit(builder.request)
	list, rawErr := bundle.typed.AppsV1().ReplicaSets(builder.request.Scope.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(), Limit: limit,
	})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return relatedBranchError(ctx, builder, root, toolcontract.RelatedRelationOwnerReference, 1, err)
	}
	if list == nil {
		return invalidKubernetesProjectionError(operation)
	}
	if list.Continue != "" || len(list.Items) > int(limit) {
		builder.graph.Truncated = true
	}
	sort.Slice(list.Items, func(left, right int) bool { return list.Items[left].Name < list.Items[right].Name })
	replicaSets := make(map[string]*appsv1.ReplicaSet)
	references := make(map[string]toolcontract.RelatedReference)
	previousName := ""
	for index := range list.Items {
		item := &list.Items[index]
		if previousName == item.Name {
			return invalidKubernetesProjectionError(operation)
		}
		previousName = item.Name
		if !controllerOwnerMatches(item.OwnerReferences, "apps/v1", "Deployment", deployment.Name, string(deployment.UID)) {
			continue
		}
		observation, err := projectToolReplicaSet(item, builder.request.Scope.Namespace, "", toolcontract.ResourceDetailSummary)
		if err != nil {
			return invalidKubernetesProjectionError(operation)
		}
		if !builder.addFetched(root, observation, toolcontract.RelatedRelationOwnerReference, 1, nil, 0) {
			break
		}
		uid := observation.Summary.Reference.UID
		if uid == "" {
			builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
			continue
		}
		replicaSets[uid] = item
		references[uid] = relatedReferenceFromResource(observation.Summary.Reference, false)
	}
	if builder.request.Depth < 2 || !relatedIncludes(builder.request, toolcontract.RelatedIncludePods) || len(replicaSets) == 0 {
		return nil
	}
	if !builder.hasCapacityForEdge() || len(builder.graph.Nodes) >= builder.request.MaxNodes {
		builder.graph.Truncated = true
		return nil
	}
	pods, rawErr := bundle.typed.CoreV1().Pods(builder.request.Scope.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(), Limit: limit,
	})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return relatedBranchError(ctx, builder, root, toolcontract.RelatedRelationOwnerReference, 2, err)
	}
	if pods == nil {
		return invalidKubernetesProjectionError(operation)
	}
	if pods.Continue != "" || len(pods.Items) > int(limit) {
		builder.graph.Truncated = true
	}
	sort.Slice(pods.Items, func(left, right int) bool { return pods.Items[left].Name < pods.Items[right].Name })
	previousName = ""
	for index := range pods.Items {
		pod := &pods.Items[index]
		if previousName == pod.Name {
			return invalidKubernetesProjectionError(operation)
		}
		previousName = pod.Name
		ownerUID := controllerOwnerUID(pod.OwnerReferences, "apps/v1", "ReplicaSet")
		from, owned := references[ownerUID]
		if !owned {
			continue
		}
		observation, err := projectToolPod(pod, builder.request.Scope.Namespace, "", toolcontract.ResourceDetailSummary)
		if err != nil {
			return invalidKubernetesProjectionError(operation)
		}
		if !builder.addFetched(from, observation, toolcontract.RelatedRelationOwnerReference, 2, nil, 0) {
			break
		}
	}
	return nil
}

func (reader *ToolResourceReader) expandRelatedReplicaSet(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	replicaSet *appsv1.ReplicaSet,
	operation string,
) error {
	root := builder.graph.Root
	if replicaSet == nil {
		return invalidKubernetesProjectionError(operation)
	}
	if relatedIncludes(builder.request, toolcontract.RelatedIncludeOwners) {
		if err := reader.followReplicaSetOwner(ctx, bundle, builder, replicaSet, operation); err != nil {
			return err
		}
	}
	if !relatedIncludes(builder.request, toolcontract.RelatedIncludePods) {
		return nil
	}
	if replicaSet.UID == "" {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	selector, _, ok := safeObjectSelector(replicaSet.Spec.Selector)
	if !ok {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	return reader.listOwnedPods(ctx, bundle, builder, root, selector, string(replicaSet.UID), "apps/v1", "ReplicaSet", 1, operation)
}

func (reader *ToolResourceReader) followReplicaSetOwner(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	replicaSet *appsv1.ReplicaSet,
	operation string,
) error {
	root := builder.graph.Root
	owner, found := fixedControllerOwner(replicaSet.OwnerReferences)
	if !found {
		if controllerOwnerCount(replicaSet.OwnerReferences) > 1 {
			builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassPolicyDenied, 1)
		}
		return nil
	}
	if owner.APIVersion != "apps/v1" || owner.Kind != "Deployment" || owner.UID == "" {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	reference, ok := relatedOwnerReference(builder.request.Scope.Namespace, owner)
	if !ok {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	if !builder.hasCapacityForEdge() || len(builder.graph.Nodes) >= builder.request.MaxNodes {
		builder.graph.Truncated = true
		return nil
	}
	object, rawErr := bundle.typed.AppsV1().Deployments(builder.request.Scope.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		if branchErr := relatedBranchError(ctx, builder, root, toolcontract.RelatedRelationOwnerReference, 1, err); branchErr != nil {
			return branchErr
		}
		builder.addReferenceOnly(root, reference, toolcontract.RelatedRelationOwnerReference, 1)
		return nil
	}
	if object == nil || object.UID != owner.UID {
		builder.addReferenceOnly(root, reference, toolcontract.RelatedRelationOwnerReference, 1)
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassNotFound, 1)
		return nil
	}
	observation, err := projectToolDeployment(object, builder.request.Scope.Namespace, owner.Name, toolcontract.ResourceDetailSummary)
	if err != nil {
		return invalidKubernetesProjectionError(operation)
	}
	builder.addFetched(root, observation, toolcontract.RelatedRelationOwnerReference, 1, nil, 0)
	return nil
}

func (reader *ToolResourceReader) expandRelatedJob(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	job *batchv1.Job,
	operation string,
) error {
	if !relatedIncludes(builder.request, toolcontract.RelatedIncludePods) {
		return nil
	}
	root := builder.graph.Root
	if job == nil || job.UID == "" {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	selector, _, ok := safeObjectSelector(job.Spec.Selector)
	if !ok {
		selector = labels.Set{"batch.kubernetes.io/controller-uid": string(job.UID)}.AsSelector()
	}
	return reader.listOwnedPods(ctx, bundle, builder, root, selector, string(job.UID), "batch/v1", "Job", 1, operation)
}

func (reader *ToolResourceReader) listOwnedPods(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	from toolcontract.RelatedReference,
	selector labels.Selector,
	ownerUID, ownerAPIVersion, ownerKind string,
	hop int,
	operation string,
) error {
	if !builder.hasCapacityForEdge() || len(builder.graph.Nodes) >= builder.request.MaxNodes {
		builder.graph.Truncated = true
		return nil
	}
	limit := relatedListLimit(builder.request)
	list, rawErr := bundle.typed.CoreV1().Pods(builder.request.Scope.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(), Limit: limit,
	})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return relatedBranchError(ctx, builder, from, toolcontract.RelatedRelationOwnerReference, hop, err)
	}
	if list == nil {
		return invalidKubernetesProjectionError(operation)
	}
	if list.Continue != "" || len(list.Items) > int(limit) {
		builder.graph.Truncated = true
	}
	sort.Slice(list.Items, func(left, right int) bool { return list.Items[left].Name < list.Items[right].Name })
	previousName := ""
	for index := range list.Items {
		pod := &list.Items[index]
		if previousName == pod.Name {
			return invalidKubernetesProjectionError(operation)
		}
		previousName = pod.Name
		if !controllerOwnerMatches(pod.OwnerReferences, ownerAPIVersion, ownerKind, "", ownerUID) {
			continue
		}
		observation, err := projectToolPod(pod, builder.request.Scope.Namespace, "", toolcontract.ResourceDetailSummary)
		if err != nil {
			return invalidKubernetesProjectionError(operation)
		}
		if !builder.addFetched(from, observation, toolcontract.RelatedRelationOwnerReference, hop, nil, 0) {
			break
		}
	}
	return nil
}

func (reader *ToolResourceReader) expandRelatedService(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	service *corev1.Service,
	operation string,
) error {
	if service == nil {
		return invalidKubernetesProjectionError(operation)
	}
	root := builder.graph.Root
	if relatedIncludes(builder.request, toolcontract.RelatedIncludePods) {
		if len(service.Spec.Selector) == 0 || len(service.Spec.Selector) > maxRelatedSelectorKeys {
			builder.addGap(root, toolcontract.RelatedRelationSelectorMatch, domain.SafeErrorClassUnsupported, 1)
		} else {
			selector := labels.Set(service.Spec.Selector).AsSelector()
			if err := reader.listSelectorMatchedPods(ctx, bundle, builder, root, selector, len(service.Spec.Selector), 1, operation); err != nil {
				return err
			}
		}
	}
	if relatedIncludes(builder.request, toolcontract.RelatedIncludeServiceEndpoints) && builder.hasCapacityForEdge() {
		counts, err := reader.gateway.countServiceEndpoints(ctx, reader.client, builder.request.Scope, service.Name)
		if err != nil {
			return relatedBranchError(ctx, builder, root, toolcontract.RelatedRelationServiceEndpoints, 1, err)
		}
		builder.addEndpointCounts(root, 1, counts)
	}
	return nil
}

func (reader *ToolResourceReader) listSelectorMatchedPods(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	from toolcontract.RelatedReference,
	selector labels.Selector,
	selectorKeys, hop int,
	operation string,
) error {
	if selector == nil || selector.Empty() || selectorKeys < 1 {
		builder.addGap(from, toolcontract.RelatedRelationSelectorMatch, domain.SafeErrorClassUnsupported, hop)
		return nil
	}
	if !builder.hasCapacityForEdge() || len(builder.graph.Nodes) >= builder.request.MaxNodes {
		builder.graph.Truncated = true
		return nil
	}
	limit := relatedListLimit(builder.request)
	list, rawErr := bundle.typed.CoreV1().Pods(builder.request.Scope.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(), Limit: limit,
	})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return relatedBranchError(ctx, builder, from, toolcontract.RelatedRelationSelectorMatch, hop, err)
	}
	if list == nil {
		return invalidKubernetesProjectionError(operation)
	}
	if list.Continue != "" || len(list.Items) > int(limit) {
		builder.graph.Truncated = true
	}
	sort.Slice(list.Items, func(left, right int) bool { return list.Items[left].Name < list.Items[right].Name })
	previousName := ""
	for index := range list.Items {
		pod := &list.Items[index]
		if previousName == pod.Name {
			return invalidKubernetesProjectionError(operation)
		}
		previousName = pod.Name
		if !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		observation, err := projectToolPod(pod, builder.request.Scope.Namespace, "", toolcontract.ResourceDetailSummary)
		if err != nil {
			return invalidKubernetesProjectionError(operation)
		}
		if !builder.addFetched(from, observation, toolcontract.RelatedRelationSelectorMatch, hop, selector, selectorKeys) {
			break
		}
	}
	return nil
}

func (reader *ToolResourceReader) expandRelatedPod(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	pod *corev1.Pod,
	operation string,
) error {
	if pod == nil {
		return invalidKubernetesProjectionError(operation)
	}
	if relatedIncludes(builder.request, toolcontract.RelatedIncludeOwners) {
		if err := reader.followPodOwner(ctx, bundle, builder, pod, operation); err != nil {
			return err
		}
	}
	if !relatedIncludes(builder.request, toolcontract.RelatedIncludeServices) &&
		!relatedIncludes(builder.request, toolcontract.RelatedIncludeServiceEndpoints) {
		return nil
	}
	root := builder.graph.Root
	if !builder.hasCapacityForEdge() || len(builder.graph.Nodes) >= builder.request.MaxNodes {
		builder.graph.Truncated = true
		return nil
	}
	limit := int64(domain.MaxResourceSummaries)
	services, rawErr := bundle.typed.CoreV1().Services(builder.request.Scope.Namespace).List(ctx, metav1.ListOptions{Limit: limit})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return relatedBranchError(ctx, builder, root, toolcontract.RelatedRelationSelectorMatch, 1, err)
	}
	if services == nil {
		return invalidKubernetesProjectionError(operation)
	}
	if services.Continue != "" || len(services.Items) > int(limit) {
		builder.graph.Truncated = true
	}
	sort.Slice(services.Items, func(left, right int) bool { return services.Items[left].Name < services.Items[right].Name })
	type matchedService struct {
		object    *corev1.Service
		reference toolcontract.RelatedReference
	}
	matched := make([]matchedService, 0)
	previousName := ""
	for index := range services.Items {
		service := &services.Items[index]
		if previousName == service.Name {
			return invalidKubernetesProjectionError(operation)
		}
		previousName = service.Name
		if len(service.Spec.Selector) == 0 {
			continue
		}
		if len(service.Spec.Selector) > maxRelatedSelectorKeys {
			builder.addGap(root, toolcontract.RelatedRelationSelectorMatch, domain.SafeErrorClassUnsupported, 1)
			continue
		}
		selector := labels.Set(service.Spec.Selector).AsSelector()
		if !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		observation, err := projectToolService(service, builder.request.Scope.Namespace, "", toolcontract.ResourceDetailSummary)
		if err != nil {
			return invalidKubernetesProjectionError(operation)
		}
		if !builder.addFetched(root, observation, toolcontract.RelatedRelationSelectorMatch, 1, selector, len(service.Spec.Selector)) {
			break
		}
		matched = append(matched, matchedService{
			object: service, reference: relatedReferenceFromResource(observation.Summary.Reference, false),
		})
	}
	if builder.request.Depth < 2 || !relatedIncludes(builder.request, toolcontract.RelatedIncludeServiceEndpoints) {
		return nil
	}
	for _, service := range matched {
		if !builder.hasCapacityForEdge() {
			break
		}
		counts, err := reader.gateway.countServiceEndpoints(ctx, reader.client, builder.request.Scope, service.object.Name)
		if err != nil {
			if branchErr := relatedBranchError(ctx, builder, service.reference, toolcontract.RelatedRelationServiceEndpoints, 2, err); branchErr != nil {
				return branchErr
			}
			continue
		}
		builder.addEndpointCounts(service.reference, 2, counts)
	}
	return nil
}

func (reader *ToolResourceReader) followPodOwner(
	ctx context.Context,
	bundle *ClientBundle,
	builder *relatedGraphBuilder,
	pod *corev1.Pod,
	operation string,
) error {
	root := builder.graph.Root
	owner, found := fixedControllerOwner(pod.OwnerReferences)
	if !found {
		if controllerOwnerCount(pod.OwnerReferences) > 1 {
			builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassPolicyDenied, 1)
		}
		return nil
	}
	reference, ok := relatedOwnerReference(builder.request.Scope.Namespace, owner)
	if !ok {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		return nil
	}
	if relatedReferenceKey(reference) == relatedReferenceKey(root) {
		builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassPolicyDenied, 1)
		return nil
	}
	followable := owner.UID != "" &&
		(owner.APIVersion == "apps/v1" && owner.Kind == "ReplicaSet" || owner.APIVersion == "batch/v1" && owner.Kind == "Job")
	if !followable {
		builder.addReferenceOnly(root, reference, toolcontract.RelatedRelationOwnerReference, 1)
		if owner.APIVersion != "apps/v1" || owner.Kind != "StatefulSet" {
			builder.addGap(root, toolcontract.RelatedRelationOwnerReference, domain.SafeErrorClassUnsupported, 1)
		}
		return nil
	}
	if !builder.hasCapacityForEdge() || len(builder.graph.Nodes) >= builder.request.MaxNodes {
		builder.graph.Truncated = true
		return nil
	}
	var observation toolcontract.ResourceObservation
	var rawErr error
	switch owner.Kind {
	case "ReplicaSet":
		object, err := bundle.typed.AppsV1().ReplicaSets(builder.request.Scope.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		rawErr = err
		if err == nil && object != nil && object.UID == owner.UID {
			observation, err = projectToolReplicaSet(object, builder.request.Scope.Namespace, owner.Name, toolcontract.ResourceDetailSummary)
			rawErr = err
		} else if err == nil {
			rawErr = newKubeSafeError(ClassNotFound, "kubernetes_related_owner_changed", operation, "The related Kubernetes owner was not found.")
		}
	case "Job":
		object, err := bundle.typed.BatchV1().Jobs(builder.request.Scope.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		rawErr = err
		if err == nil && object != nil && object.UID == owner.UID {
			observation, err = projectToolJob(object, builder.request.Scope.Namespace, owner.Name, toolcontract.ResourceDetailSummary)
			rawErr = err
		} else if err == nil {
			rawErr = newKubeSafeError(ClassNotFound, "kubernetes_related_owner_changed", operation, "The related Kubernetes owner was not found.")
		}
	}
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		if branchErr := relatedBranchError(ctx, builder, root, toolcontract.RelatedRelationOwnerReference, 1, err); branchErr != nil {
			return branchErr
		}
		builder.addReferenceOnly(root, reference, toolcontract.RelatedRelationOwnerReference, 1)
		return nil
	}
	if observation.Validate() != nil {
		return invalidKubernetesProjectionError(operation)
	}
	builder.addFetched(root, observation, toolcontract.RelatedRelationOwnerReference, 1, nil, 0)
	return nil
}

func relatedIncludes(request toolcontract.RelatedReadRequest, wanted toolcontract.RelatedInclude) bool {
	for _, include := range request.Includes {
		if include == wanted {
			return true
		}
	}
	return false
}

func relatedListLimit(request toolcontract.RelatedReadRequest) int64 {
	return int64(min(request.MaxNodes, domain.MaxResourceSummaries))
}

func safeObjectSelector(value *metav1.LabelSelector) (labels.Selector, int, bool) {
	if value == nil {
		return nil, 0, false
	}
	keyCount := len(value.MatchLabels) + len(value.MatchExpressions)
	if keyCount < 1 || keyCount > 64 {
		return nil, 0, false
	}
	selector, err := metav1.LabelSelectorAsSelector(value)
	if err != nil || selector == nil || selector.Empty() {
		return nil, 0, false
	}
	return selector, keyCount, true
}

func controllerOwnerMatches(
	references []metav1.OwnerReference,
	apiVersion, kind, name, uid string,
) bool {
	matched := false
	for _, reference := range references {
		if reference.Controller == nil || !*reference.Controller || reference.APIVersion != apiVersion || reference.Kind != kind ||
			name != "" && reference.Name != name || string(reference.UID) != uid {
			continue
		}
		if matched {
			return false
		}
		matched = true
	}
	return matched
}

func controllerOwnerUID(references []metav1.OwnerReference, apiVersion, kind string) string {
	result := ""
	for _, reference := range references {
		if reference.Controller == nil || !*reference.Controller || reference.APIVersion != apiVersion || reference.Kind != kind || reference.UID == "" {
			continue
		}
		if result != "" {
			return ""
		}
		result = string(reference.UID)
	}
	return result
}

func fixedControllerOwner(references []metav1.OwnerReference) (metav1.OwnerReference, bool) {
	var result metav1.OwnerReference
	found := false
	for _, reference := range references {
		if reference.Controller == nil || !*reference.Controller {
			continue
		}
		if found {
			return metav1.OwnerReference{}, false
		}
		result = reference
		found = true
	}
	return result, found
}

func controllerOwnerCount(references []metav1.OwnerReference) int {
	count := 0
	for _, reference := range references {
		if reference.Controller != nil && *reference.Controller {
			count++
		}
	}
	return count
}

func relatedOwnerReference(namespace string, owner metav1.OwnerReference) (toolcontract.RelatedReference, bool) {
	if !domain.ValidNamespaceName(namespace) || !domain.ValidResourceName(owner.Name) ||
		!validRelatedOwnerAPIVersion(owner.APIVersion) || !validRelatedOwnerKind(owner.Kind) ||
		!safeIdentityText(string(owner.UID)) ||
		owner.APIVersion == "v1" && (owner.Kind == "Secret" || owner.Kind == "ConfigMap") {
		return toolcontract.RelatedReference{}, false
	}
	return toolcontract.RelatedReference{
		APIVersion: owner.APIVersion, Kind: owner.Kind, Namespace: namespace,
		Name: owner.Name, UID: string(owner.UID), ReferenceOnly: true,
	}, true
}

func validRelatedOwnerAPIVersion(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 2 {
		return false
	}
	for index, part := range parts {
		if !validRelatedOwnerAPISegment(part, index == 0 && len(parts) == 2) {
			return false
		}
	}
	return true
}

func validRelatedOwnerAPISegment(value string, allowDot bool) bool {
	if value == "" || !relatedOwnerLowerOrDigit(value[0]) || !relatedOwnerLowerOrDigit(value[len(value)-1]) {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		current := value[index]
		if relatedOwnerLowerOrDigit(current) || current == '-' || allowDot && current == '.' {
			continue
		}
		return false
	}
	return true
}

func validRelatedOwnerKind(value string) bool {
	if len(value) < 1 || len(value) > 63 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		current := value[index]
		if current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z' || current >= '0' && current <= '9' {
			continue
		}
		return false
	}
	return true
}

func relatedOwnerLowerOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func relatedReferenceFromResource(reference domain.ResourceRef, referenceOnly bool) toolcontract.RelatedReference {
	return toolcontract.RelatedReference{
		APIVersion: reference.APIVersion, Kind: reference.Kind, Namespace: reference.Namespace,
		Name: reference.Name, UID: reference.UID, ResourceVersion: reference.ResourceVersion, ReferenceOnly: referenceOnly,
	}
}

func relatedReferenceKey(reference toolcontract.RelatedReference) string {
	return strings.Join([]string{
		reference.APIVersion, reference.Kind, reference.Namespace, reference.Name,
	}, "\x00")
}

func relatedEdgeAdapterKey(
	relation toolcontract.RelatedRelation,
	from, to toolcontract.RelatedReference,
) string {
	return strings.Join([]string{string(relation), relatedReferenceKey(from), relatedReferenceKey(to)}, "\x00")
}

func relatedNodeAdapterSortKey(node toolcontract.RelatedNodeObservation) string {
	return fmt.Sprintf("%02d\x00%s", node.Hop, relatedReferenceKey(node.Reference))
}

func relatedEdgeAdapterSortKey(edge toolcontract.RelatedEdgeObservation) string {
	return fmt.Sprintf("%02d\x00%s", edge.Hop, relatedEdgeAdapterKey(edge.Relation, edge.From, edge.To))
}

func relatedGapAdapterSortKey(gap toolcontract.RelatedGapObservation) string {
	return fmt.Sprintf("%02d\x00%s\x00%s", gap.Hop, relatedEdgeAdapterKey(gap.Relation, gap.From, toolcontract.RelatedReference{}), gap.Class)
}

func relatedBranchError(
	ctx context.Context,
	builder *relatedGraphBuilder,
	from toolcontract.RelatedReference,
	relation toolcontract.RelatedRelation,
	hop int,
	err error,
) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	type classified interface {
		Class() domain.SafeErrorClass
	}
	var failure classified
	if !errors.As(err, &failure) {
		return err
	}
	switch failure.Class() {
	case domain.SafeErrorClassPermissionDenied,
		domain.SafeErrorClassNotFound,
		domain.SafeErrorClassConflict,
		domain.SafeErrorClassUnsupported,
		domain.SafeErrorClassPolicyDenied,
		domain.SafeErrorClassRateLimited,
		domain.SafeErrorClassUnavailable:
		builder.addGap(from, relation, failure.Class(), hop)
		return nil
	default:
		return err
	}
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
	_ application.ScopeClientFactory     = (*Gateway)(nil)
	_ application.NamespaceReader        = (*Gateway)(nil)
	_ application.ResourceService        = (*Gateway)(nil)
	_ application.ScopeClient            = (*scopeClient)(nil)
	_ toolcontract.ResourceReader        = (*ToolResourceReader)(nil)
	_ toolcontract.EventReader           = (*ToolResourceReader)(nil)
	_ toolcontract.PodLogReader          = (*ToolResourceReader)(nil)
	_ toolcontract.RelatedResourceReader = (*ToolResourceReader)(nil)
)
