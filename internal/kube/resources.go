package kube

import (
	"context"
	"sort"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// Compile-time checks keep the adapter bound to the current consumer ports.
var (
	_ application.ScopeClientFactory = (*Gateway)(nil)
	_ application.NamespaceReader    = (*Gateway)(nil)
	_ application.ResourceService    = (*Gateway)(nil)
	_ application.ScopeClient        = (*scopeClient)(nil)
)
