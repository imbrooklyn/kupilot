package kube

import (
	"context"
	"sort"
	"sync"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Gateway adapts one S09 client factory to Application-owned scope and Picker
// ports without exposing client-go values.
type Gateway struct {
	factory *ClientFactory
}

// NewGateway validates the fixed Kubernetes adapter dependency.
func NewGateway(factory *ClientFactory) (*Gateway, error) {
	if factory == nil || factory.loader == nil {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_client_factory_required",
			"create_kubernetes_gateway",
			"A Kubernetes client factory is required.",
		)
	}
	return &Gateway{factory: factory}, nil
}

// Contexts returns deterministic safe kubeconfig candidates and performs no
// Kubernetes API request.
func (gateway *Gateway) Contexts(ctx context.Context) ([]application.ContextCandidate, error) {
	if gateway == nil || gateway.factory == nil || gateway.factory.loader == nil {
		return nil, gatewayUnavailableError("list_contexts")
	}
	contexts, err := gateway.factory.loader.Contexts(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]application.ContextCandidate, len(contexts))
	for index, current := range contexts {
		result[index] = contextCandidate(current)
	}
	return result, nil
}

// Create builds a fresh opaque client. Command cancellation owns construction,
// while the returned handle owns the completed bundle until Close.
func (gateway *Gateway) Create(ctx context.Context, contextName string) (application.ScopeClient, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, cancelledClientError()
	}
	if gateway == nil || gateway.factory == nil {
		return nil, gatewayUnavailableError("create_scope_client")
	}
	ownerContext, cancelOwner := context.WithCancel(context.Background())
	stopForwarding := context.AfterFunc(ctx, cancelOwner)
	bundle, err := gateway.factory.Create(ownerContext, contextName)
	if err != nil {
		stopForwarding()
		cancelOwner()
		return nil, err
	}
	if bundle == nil {
		stopForwarding()
		cancelOwner()
		return nil, newKubeSafeError(
			ClassInternal,
			"kubernetes_client_missing",
			"create_scope_client",
			"The Kubernetes client factory returned no client.",
		)
	}
	if !stopForwarding() || ctx.Err() != nil {
		cancelOwner()
		bundle.Close()
		return nil, cancelledClientError()
	}
	return &scopeClient{bundle: bundle, cancel: cancelOwner}, nil
}

// VerifyNamespace performs one exact Namespace GET. Invalid names are rejected
// before the typed client is called.
func (gateway *Gateway) VerifyNamespace(ctx context.Context, client application.ScopeClient, namespace string) error {
	if ctx == nil {
		return newKubeSafeError(ClassInvalidInput, "kubernetes_context_required", "verify_namespace", "An operation Context is required.")
	}
	if ctx.Err() != nil {
		return classifyContextError(ctx.Err(), "verify_namespace")
	}
	if !domain.ValidNamespaceName(namespace) {
		return newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_namespace_invalid",
			"verify_namespace",
			"A valid Kubernetes Namespace is required.",
		)
	}
	bundle, err := gateway.clientBundle(client, "verify_namespace")
	if err != nil {
		return err
	}
	result, rawErr := bundle.typed.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if ctx.Err() != nil {
		return classifyContextError(ctx.Err(), "verify_namespace")
	}
	if rawErr != nil {
		return classifyKubernetesError("verify_namespace", rawErr)
	}
	if result == nil || result.Name != namespace || !domain.ValidNamespaceName(result.Name) {
		return invalidKubernetesProjectionError("verify_namespace")
	}
	return nil
}

// ListNamespaces performs one fixed cluster-scope Namespace list for scope
// selection and returns only bounded names.
func (gateway *Gateway) ListNamespaces(ctx context.Context, client application.ScopeClient, limit int) (domain.NamespaceList, error) {
	if ctx == nil {
		return domain.NamespaceList{}, newKubeSafeError(ClassInvalidInput, "kubernetes_context_required", "list_namespaces", "An operation Context is required.")
	}
	if ctx.Err() != nil {
		return domain.NamespaceList{}, classifyContextError(ctx.Err(), "list_namespaces")
	}
	if limit < 1 || limit > domain.MaxResourceSummaries {
		return domain.NamespaceList{}, newKubeSafeError(
			ClassInvalidInput,
			"kubernetes_namespace_limit_invalid",
			"list_namespaces",
			"The Kubernetes Namespace result limit is invalid.",
		)
	}
	bundle, err := gateway.clientBundle(client, "list_namespaces")
	if err != nil {
		return domain.NamespaceList{}, err
	}
	result, rawErr := bundle.typed.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: int64(limit)})
	if ctx.Err() != nil {
		return domain.NamespaceList{}, classifyContextError(ctx.Err(), "list_namespaces")
	}
	if rawErr != nil {
		return domain.NamespaceList{}, classifyKubernetesError("list_namespaces", rawErr)
	}
	if result == nil {
		return domain.NamespaceList{}, invalidKubernetesProjectionError("list_namespaces")
	}
	projectedCount := min(len(result.Items), limit)
	items := make([]domain.NamespaceSummary, 0, projectedCount)
	seen := make(map[string]struct{}, projectedCount)
	for index := 0; index < projectedCount; index++ {
		item := result.Items[index]
		if !domain.ValidNamespaceName(item.Name) {
			return domain.NamespaceList{}, invalidKubernetesProjectionError("list_namespaces")
		}
		if _, exists := seen[item.Name]; exists {
			return domain.NamespaceList{}, invalidKubernetesProjectionError("list_namespaces")
		}
		seen[item.Name] = struct{}{}
		items = append(items, domain.NamespaceSummary{Name: item.Name})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Name < items[right].Name })
	truncated := result.Continue != "" || len(result.Items) > limit
	projected := domain.NamespaceList{Items: items, Truncated: truncated}
	if projected.Validate() != nil {
		return domain.NamespaceList{}, invalidKubernetesProjectionError("list_namespaces")
	}
	return projected, nil
}

func (gateway *Gateway) clientBundle(client application.ScopeClient, operation string) (*ClientBundle, error) {
	if gateway == nil || gateway.factory == nil {
		return nil, gatewayUnavailableError(operation)
	}
	owned, ok := client.(*scopeClient)
	if !ok || owned == nil {
		return nil, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_scope_client_denied",
			operation,
			"The Kubernetes scope client is not owned by this adapter.",
		)
	}
	owned.mu.Lock()
	defer owned.mu.Unlock()
	if owned.closed || owned.bundle == nil || owned.bundle.typed == nil || owned.bundle.dynamic == nil || owned.bundle.metadata == nil {
		return nil, newKubeSafeError(
			ClassCancelled,
			"kubernetes_scope_client_closed",
			operation,
			"The Kubernetes scope client is closed.",
		)
	}
	return owned.bundle, nil
}

type scopeClient struct {
	mu     sync.Mutex
	bundle *ClientBundle
	cancel context.CancelFunc
	closed bool
}

func (client *scopeClient) Context() application.ContextCandidate {
	if client == nil {
		return application.ContextCandidate{}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed || client.bundle == nil {
		return application.ContextCandidate{}
	}
	return contextCandidate(client.bundle.Context())
}

func (client *scopeClient) Close() {
	if client == nil {
		return
	}
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return
	}
	client.closed = true
	bundle := client.bundle
	cancel := client.cancel
	client.bundle = nil
	client.cancel = nil
	client.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if bundle != nil {
		bundle.Close()
	}
}

func contextCandidate(info ContextInfo) application.ContextCandidate {
	return application.ContextCandidate{
		Name:             info.Name,
		DefaultNamespace: info.Namespace,
		Current:          info.Current,
		ExecCredentials:  info.ExecCredentials,
	}
}

func gatewayUnavailableError(operation string) *SafeError {
	return newKubeSafeError(
		ClassInternal,
		"kubernetes_gateway_unavailable",
		operation,
		"The Kubernetes gateway is unavailable.",
	)
}

func invalidKubernetesProjectionError(operation string) *SafeError {
	return newKubeSafeError(
		ClassInvalidExternalResponse,
		"kubernetes_projection_invalid",
		operation,
		"Kubernetes returned data that could not be projected safely.",
	)
}
