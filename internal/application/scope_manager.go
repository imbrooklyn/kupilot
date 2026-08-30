// Package application owns Kupilot use-case orchestration and consumer ports.
package application

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// ContextCandidate is safe kubeconfig display metadata without live authority.
type ContextCandidate struct {
	Name             string
	DefaultNamespace string
	Current          bool
	ExecCredentials  bool
}

func (candidate ContextCandidate) valid() bool {
	return domain.ValidContextName(candidate.Name) && domain.ValidNamespaceName(candidate.DefaultNamespace)
}

// ScopeClient is an opaque Context-owned lifecycle handle. It exposes no
// Kubernetes operation or vendor type.
type ScopeClient interface {
	Context() ContextCandidate
	Close()
}

// ScopeClientFactory is the Application-owned client activation port.
type ScopeClientFactory interface {
	Contexts(context.Context) ([]ContextCandidate, error)
	Create(context.Context, string) (ScopeClient, error)
}

// NamespaceReader is the Application-owned fixed Namespace discovery port.
type NamespaceReader interface {
	VerifyNamespace(context.Context, ScopeClient, string) error
	ListNamespaces(context.Context, ScopeClient, int) (domain.NamespaceList, error)
}

// ResourceService is the Application-owned Picker read port. Tool handlers may
// later own separate interfaces satisfied by the same adapter.
type ResourceService interface {
	GetResource(context.Context, ScopeClient, domain.ClusterScope, domain.ResourceRef) (domain.ResourceSummary, error)
	ListResources(context.Context, ScopeClient, domain.ClusterScope, domain.ResourceKind, int) (domain.ResourceList, error)
}

// ScopeInvalidationHook clears current-generation authority outside this
// manager. It performs local synchronous state invalidation only and exposes no
// executor or write capability.
type ScopeInvalidationHook interface {
	InvalidateScope(generation int64) error
}

// ScopeState is the complete process-local scope lifecycle state.
type ScopeState string

const (
	ScopeStateUnavailable ScopeState = "unavailable"
	ScopeStateActivating  ScopeState = "activating"
	ScopeStateActive      ScopeState = "active"
	ScopeStateClosed      ScopeState = "closed"
)

// ScopeView is a safe immutable snapshot for delivery and tests.
type ScopeView struct {
	State      ScopeState
	Generation int64
	Scope      *domain.ClusterScope
}

type namespaceCacheKey struct {
	generation int64
	limit      int
}

type resourceCacheKey struct {
	generation int64
	kind       domain.ResourceKind
	limit      int
}

// ScopeManager serializes scope commits and owns current client, run
// cancellation, selected ResourceRef, and generation-isolated Picker caches.
type ScopeManager struct {
	switchMu sync.Mutex
	mu       sync.RWMutex

	factory      ScopeClientFactory
	namespaces   NamespaceReader
	resources    ResourceService
	hook         ScopeInvalidationHook
	approvalHook ScopeInvalidationHook
	now          func() time.Time
	access       domain.NamespaceAccessPolicy

	state      ScopeState
	generation int64
	scope      *domain.ClusterScope
	client     ScopeClient

	activeRunID domain.AgentRunID
	runCancel   context.CancelFunc

	selectedResource *domain.ResourceRef
	namespaceCache   map[namespaceCacheKey]domain.NamespaceList
	resourceCache    map[resourceCacheKey]domain.ResourceList
}

// BindApprovalInvalidationHook attaches the admitted approval lifecycle before
// the first scope activation. Both invalidation hooks run before client reuse
// or construction; a failure keeps the new scope unavailable.
func (manager *ScopeManager) BindApprovalInvalidationHook(hook ScopeInvalidationHook) error {
	if manager == nil || hook == nil {
		return newScopeError(
			domain.SafeErrorClassConfigurationInvalid,
			"scope_approval_hook_invalid",
			"bind_approval_invalidation",
			"Approval invalidation could not be bound safely.",
		)
	}
	manager.switchMu.Lock()
	defer manager.switchMu.Unlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.state != ScopeStateUnavailable || manager.generation != 0 || manager.client != nil || manager.approvalHook != nil {
		return newScopeError(
			domain.SafeErrorClassConflict,
			"scope_approval_hook_conflict",
			"bind_approval_invalidation",
			"Approval invalidation could not be bound safely.",
		)
	}
	manager.approvalHook = hook
	return nil
}

// NewScopeManager validates all required consumer ports and creates an
// unavailable generation-zero manager.
func NewScopeManager(
	factory ScopeClientFactory,
	namespaces NamespaceReader,
	resources ResourceService,
	hook ScopeInvalidationHook,
	now func() time.Time,
	configuredAccess ...domain.NamespaceAccessPolicy,
) (*ScopeManager, error) {
	if factory == nil || namespaces == nil || resources == nil || hook == nil || now == nil {
		return nil, newScopeError(
			domain.SafeErrorClassConfigurationInvalid,
			"scope_manager_dependency_required",
			"create_scope_manager",
			"Scope management dependencies are unavailable.",
		)
	}
	access := domain.NamespaceAccessCurrent
	if len(configuredAccess) > 1 {
		return nil, newScopeError(
			domain.SafeErrorClassConfigurationInvalid,
			"scope_namespace_access_invalid",
			"create_scope_manager",
			"The Kubernetes namespace-access policy is invalid.",
		)
	}
	if len(configuredAccess) == 1 {
		access = configuredAccess[0]
	}
	if !access.Valid() {
		return nil, newScopeError(
			domain.SafeErrorClassConfigurationInvalid,
			"scope_namespace_access_invalid",
			"create_scope_manager",
			"The Kubernetes namespace-access policy is invalid.",
		)
	}
	return &ScopeManager{
		factory:        factory,
		namespaces:     namespaces,
		resources:      resources,
		hook:           hook,
		now:            now,
		access:         access,
		state:          ScopeStateUnavailable,
		namespaceCache: make(map[namespaceCacheKey]domain.NamespaceList),
		resourceCache:  make(map[resourceCacheKey]domain.ResourceList),
	}, nil
}

// View returns one defensively copied lifecycle snapshot.
func (manager *ScopeManager) View() ScopeView {
	if manager == nil {
		return ScopeView{State: ScopeStateUnavailable}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	view := ScopeView{State: manager.state, Generation: manager.generation}
	if manager.scope != nil {
		scope := *manager.scope
		view.Scope = &scope
	}
	return view
}

// CurrentScope returns the complete active scope when one is available.
func (manager *ScopeManager) CurrentScope() (domain.ClusterScope, bool) {
	if manager == nil {
		return domain.ClusterScope{}, false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.state != ScopeStateActive || manager.scope == nil || manager.client == nil {
		return domain.ClusterScope{}, false
	}
	return *manager.scope, true
}

// ListContexts returns sorted safe local candidates and performs no Kubernetes
// API request.
func (manager *ScopeManager) ListContexts(ctx context.Context) ([]ContextCandidate, error) {
	if manager == nil || manager.factory == nil {
		return nil, scopeUnavailableError("list_contexts")
	}
	if err := contextInputError(ctx, "list_contexts"); err != nil {
		return nil, err
	}
	candidates, rawErr := manager.factory.Contexts(ctx)
	if err := contextResultError(ctx, rawErr, "list_contexts"); err != nil {
		return nil, err
	}
	result := append([]ContextCandidate(nil), candidates...)
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	for index, candidate := range result {
		if !candidate.valid() || index > 0 && result[index-1].Name == candidate.Name {
			return nil, newScopeError(
				domain.SafeErrorClassInvalidExternalResponse,
				"scope_context_candidates_invalid",
				"list_contexts",
				"Kubernetes Context candidates were invalid.",
			)
		}
	}
	return result, nil
}

// SwitchContext commits invalidation before disposing the old client and
// constructing and verifying the selected target.
func (manager *ScopeManager) SwitchContext(ctx context.Context, target string, expectedGeneration int64) (domain.ClusterScope, error) {
	return manager.switchContext(ctx, target, "", expectedGeneration)
}

// ActivateScope commits one exact Context and Namespace pair without first
// inheriting or verifying the kubeconfig Context's default Namespace.
func (manager *ScopeManager) ActivateScope(
	ctx context.Context,
	target domain.ScopeCandidate,
	expectedGeneration int64,
) (domain.ClusterScope, error) {
	if target.Validate() != nil {
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_candidate_invalid",
			"activate_scope",
			"A valid Kubernetes Context and Namespace must be selected.",
		)
	}
	return manager.switchContext(ctx, target.Context, target.Namespace, expectedGeneration)
}

func (manager *ScopeManager) switchContext(
	ctx context.Context,
	targetContext string,
	targetNamespace string,
	expectedGeneration int64,
) (domain.ClusterScope, error) {
	if manager == nil {
		return domain.ClusterScope{}, scopeUnavailableError("switch_context")
	}
	manager.switchMu.Lock()
	defer manager.switchMu.Unlock()
	if err := contextInputError(ctx, "switch_context"); err != nil {
		return domain.ClusterScope{}, err
	}
	if err := manager.checkExpectedGeneration(expectedGeneration); err != nil {
		return domain.ClusterScope{}, err
	}
	if !domain.ValidContextName(targetContext) {
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_context_invalid",
			"switch_context",
			"A valid Kubernetes Context must be selected.",
		)
	}
	candidates, err := manager.ListContexts(ctx)
	if err != nil {
		return domain.ClusterScope{}, err
	}
	found := false
	for _, candidate := range candidates {
		if candidate.Name == targetContext {
			found = true
			break
		}
	}
	if !found {
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassNotFound,
			"scope_context_not_found",
			"switch_context",
			"The selected Kubernetes Context was not found.",
		)
	}

	generation, oldClient, cancel, err := manager.beginInvalidation()
	if err != nil {
		return domain.ClusterScope{}, err
	}
	manager.finishLocalInvalidation(generation, cancel)
	hookErr := manager.invalidateHook(generation)
	if oldClient != nil {
		oldClient.Close()
	}
	if hookErr != nil {
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, hookErr
	}

	client, rawErr := manager.factory.Create(ctx, targetContext)
	if err := contextResultError(ctx, rawErr, "switch_context"); err != nil {
		if client != nil {
			client.Close()
		}
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, err
	}
	if client == nil {
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassInternal,
			"scope_client_missing",
			"switch_context",
			"The selected Kubernetes Context could not be activated safely.",
		)
	}
	clientContext := client.Context()
	if !clientContext.valid() || clientContext.Name != targetContext {
		client.Close()
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassInvalidExternalResponse,
			"scope_client_context_invalid",
			"switch_context",
			"The selected Kubernetes Context returned invalid scope metadata.",
		)
	}
	namespace := clientContext.DefaultNamespace
	if targetNamespace != "" {
		namespace = targetNamespace
	}
	rawErr = manager.namespaces.VerifyNamespace(ctx, client, namespace)
	if err := contextResultError(ctx, rawErr, "verify_namespace"); err != nil {
		client.Close()
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, err
	}
	return manager.publishActive(client, clientContext.Name, namespace, generation)
}

// SwitchNamespace verifies a candidate before the generation commit. A failed
// candidate leaves the current scope and client unchanged.
func (manager *ScopeManager) SwitchNamespace(ctx context.Context, target string, expectedGeneration int64) (domain.ClusterScope, error) {
	if manager == nil {
		return domain.ClusterScope{}, scopeUnavailableError("switch_namespace")
	}
	manager.switchMu.Lock()
	defer manager.switchMu.Unlock()
	if err := contextInputError(ctx, "switch_namespace"); err != nil {
		return domain.ClusterScope{}, err
	}
	if !domain.ValidNamespaceName(target) {
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_namespace_invalid",
			"switch_namespace",
			"A valid Kubernetes Namespace must be selected.",
		)
	}
	current, client, err := manager.captureActiveGeneration(expectedGeneration)
	if err != nil {
		return domain.ClusterScope{}, err
	}
	if current.Namespace == target {
		return current, nil
	}
	rawErr := manager.namespaces.VerifyNamespace(ctx, client, target)
	if err := contextResultError(ctx, rawErr, "verify_namespace"); err != nil {
		return domain.ClusterScope{}, err
	}

	generation, retainedClient, cancel, err := manager.beginInvalidation()
	if err != nil {
		return domain.ClusterScope{}, err
	}
	if retainedClient != client {
		if retainedClient != nil {
			retainedClient.Close()
		}
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassInternal,
			"scope_client_changed",
			"switch_namespace",
			"The active Kubernetes client changed unexpectedly.",
		)
	}
	manager.finishLocalInvalidation(generation, cancel)
	if err := manager.invalidateHook(generation); err != nil {
		client.Close()
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, err
	}
	return manager.publishActive(client, current.Context, target, generation)
}

// BindRun registers the one active AgentRun cancellation function against an
// exact immutable scope. Run starts serialize behind scope switch validation.
func (manager *ScopeManager) BindRun(scope domain.ClusterScope, runID domain.AgentRunID, cancel context.CancelFunc) error {
	if manager == nil {
		return scopeUnavailableError("bind_run")
	}
	manager.switchMu.Lock()
	defer manager.switchMu.Unlock()
	if scope.Validate() != nil || !runID.Valid() || cancel == nil {
		return newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_run_binding_invalid",
			"bind_run",
			"The diagnostic run could not be bound to the active Kubernetes scope.",
		)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.currentScopeLocked(scope) {
		return staleScopeError("bind_run")
	}
	if manager.runCancel != nil {
		return newScopeError(
			domain.SafeErrorClassConflict,
			"scope_run_already_active",
			"bind_run",
			"Another diagnostic run is already active.",
		)
	}
	manager.activeRunID = runID
	manager.runCancel = cancel
	return nil
}

// UnbindRun clears cancellation authority only for the matching active run.
func (manager *ScopeManager) UnbindRun(runID domain.AgentRunID) {
	if manager == nil || !runID.Valid() {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.activeRunID == runID {
		manager.activeRunID = ""
		manager.runCancel = nil
	}
}

// SelectResource stores one unverified direct target for the exact current
// scope. It does not perform Kubernetes I/O or create Evidence.
func (manager *ScopeManager) SelectResource(scope domain.ClusterScope, reference domain.ResourceRef) error {
	if manager == nil {
		return scopeUnavailableError("select_resource")
	}
	if scope.Validate() != nil {
		return newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_resource_invalid",
			"select_resource",
			"The selected ResourceRef was invalid.",
		)
	}
	if _, allowed := domain.ResourceKindForReference(reference); !allowed {
		return newScopeError(
			domain.SafeErrorClassPolicyDenied,
			"scope_resource_kind_denied",
			"select_resource",
			"The selected resource Kind is not allowed.",
		)
	}
	if domain.ValidateLiveResourceRef(reference) != nil {
		return newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_resource_invalid",
			"select_resource",
			"The selected ResourceRef was invalid.",
		)
	}
	if !domain.ReferenceMatchesWorkingNamespace(reference, scope.Namespace) {
		return newScopeError(
			domain.SafeErrorClassPolicyDenied,
			"scope_resource_namespace_denied",
			"select_resource",
			"The selected ResourceRef is outside the active Namespace.",
		)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.currentScopeLocked(scope) {
		return staleScopeError("select_resource")
	}
	copy := reference
	manager.selectedResource = &copy
	return nil
}

// SelectedResource returns the unverified target for the exact current scope.
func (manager *ScopeManager) SelectedResource(scope domain.ClusterScope) (*domain.ResourceRef, error) {
	if manager == nil {
		return nil, scopeUnavailableError("selected_resource")
	}
	if scope.Validate() != nil {
		return nil, newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_invalid",
			"selected_resource",
			"The active Kubernetes context and namespace were invalid.",
		)
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.currentScopeLocked(scope) {
		return nil, staleScopeError("selected_resource")
	}
	if manager.selectedResource == nil {
		return nil, nil
	}
	copy := *manager.selectedResource
	return &copy, nil
}

// ListNamespaces performs one bounded read or returns a same-generation cache
// copy. Every adapter return is checked against the complete bound scope.
func (manager *ScopeManager) ListNamespaces(ctx context.Context, scope domain.ClusterScope, limit int) (domain.NamespaceList, error) {
	if manager == nil {
		return domain.NamespaceList{}, scopeUnavailableError("list_namespaces")
	}
	if err := validateBoundRead(ctx, scope, limit, "list_namespaces"); err != nil {
		return domain.NamespaceList{}, err
	}
	key := namespaceCacheKey{generation: scope.Generation, limit: limit}
	manager.mu.RLock()
	if !manager.currentScopeLocked(scope) {
		manager.mu.RUnlock()
		return domain.NamespaceList{}, staleScopeError("list_namespaces")
	}
	client := manager.client
	if cached, exists := manager.namespaceCache[key]; exists {
		manager.mu.RUnlock()
		return cloneNamespaceList(cached), nil
	}
	manager.mu.RUnlock()

	result, rawErr := manager.namespaces.ListNamespaces(ctx, client, limit)
	if !manager.scopeCurrent(scope) {
		return domain.NamespaceList{}, staleScopeError("list_namespaces")
	}
	if err := contextResultError(ctx, rawErr, "list_namespaces"); err != nil {
		return domain.NamespaceList{}, err
	}
	if result.Validate() != nil {
		return domain.NamespaceList{}, invalidProjectionError("list_namespaces")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.currentScopeLocked(scope) {
		return domain.NamespaceList{}, staleScopeError("list_namespaces")
	}
	manager.namespaceCache[key] = cloneNamespaceList(result)
	return cloneNamespaceList(result), nil
}

// GetResource performs one exact direct-target read with pre-call and
// post-result complete-scope checks.
func (manager *ScopeManager) GetResource(ctx context.Context, scope domain.ClusterScope, reference domain.ResourceRef) (domain.ResourceSummary, error) {
	if manager == nil {
		return domain.ResourceSummary{}, scopeUnavailableError("get_resource")
	}
	if err := contextInputError(ctx, "get_resource"); err != nil {
		return domain.ResourceSummary{}, err
	}
	if scope.Validate() != nil {
		return domain.ResourceSummary{}, newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_resource_request_invalid",
			"get_resource",
			"The resource request was invalid.",
		)
	}
	if _, allowed := domain.ResourceKindForReference(reference); !allowed {
		return domain.ResourceSummary{}, newScopeError(
			domain.SafeErrorClassPolicyDenied,
			"scope_resource_kind_denied",
			"get_resource",
			"The requested resource Kind is not allowed.",
		)
	}
	if domain.ValidateLiveResourceRef(reference) != nil {
		return domain.ResourceSummary{}, newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_resource_request_invalid",
			"get_resource",
			"The resource request was invalid.",
		)
	}
	if !domain.ReferenceMatchesWorkingNamespace(reference, scope.Namespace) {
		return domain.ResourceSummary{}, newScopeError(
			domain.SafeErrorClassPolicyDenied,
			"scope_resource_namespace_denied",
			"get_resource",
			"The resource request is outside the bound Namespace.",
		)
	}
	client, err := manager.captureClientForScope(scope, "get_resource")
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result, rawErr := manager.resources.GetResource(ctx, client, scope, reference)
	if !manager.scopeCurrent(scope) {
		return domain.ResourceSummary{}, staleScopeError("get_resource")
	}
	if err := contextResultError(ctx, rawErr, "get_resource"); err != nil {
		return domain.ResourceSummary{}, err
	}
	if result.Validate() != nil || !sameResourceIdentity(result.Reference, reference) {
		return domain.ResourceSummary{}, invalidProjectionError("get_resource")
	}
	return cloneResourceSummary(result), nil
}

// ListResources performs one fixed-Kind bounded current-Namespace list or
// returns a same-generation cache copy.
func (manager *ScopeManager) ListResources(ctx context.Context, scope domain.ClusterScope, kind domain.ResourceKind, limit int) (domain.ResourceList, error) {
	if manager == nil {
		return domain.ResourceList{}, scopeUnavailableError("list_resources")
	}
	if err := validateBoundRead(ctx, scope, limit, "list_resources"); err != nil {
		return domain.ResourceList{}, err
	}
	if !kind.Valid() {
		return domain.ResourceList{}, newScopeError(
			domain.SafeErrorClassPolicyDenied,
			"scope_resource_kind_denied",
			"list_resources",
			"The requested resource Kind is not allowed.",
		)
	}
	key := resourceCacheKey{generation: scope.Generation, kind: kind, limit: limit}
	manager.mu.RLock()
	if !manager.currentScopeLocked(scope) {
		manager.mu.RUnlock()
		return domain.ResourceList{}, staleScopeError("list_resources")
	}
	client := manager.client
	if cached, exists := manager.resourceCache[key]; exists {
		manager.mu.RUnlock()
		return cloneResourceList(cached), nil
	}
	manager.mu.RUnlock()

	result, rawErr := manager.resources.ListResources(ctx, client, scope, kind, limit)
	if !manager.scopeCurrent(scope) {
		return domain.ResourceList{}, staleScopeError("list_resources")
	}
	if err := contextResultError(ctx, rawErr, "list_resources"); err != nil {
		return domain.ResourceList{}, err
	}
	if result.Validate() != nil || !resourceListMatches(result, scope.Namespace, kind) {
		return domain.ResourceList{}, invalidProjectionError("list_resources")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.currentScopeLocked(scope) {
		return domain.ResourceList{}, staleScopeError("list_resources")
	}
	manager.resourceCache[key] = cloneResourceList(result)
	return cloneResourceList(result), nil
}

// Close permanently invalidates current scope authority and closes its client.
func (manager *ScopeManager) Close() error {
	if manager == nil {
		return nil
	}
	manager.switchMu.Lock()
	defer manager.switchMu.Unlock()
	manager.mu.RLock()
	alreadyClosed := manager.state == ScopeStateClosed
	manager.mu.RUnlock()
	if alreadyClosed {
		return nil
	}
	generation, client, cancel, err := manager.beginInvalidation()
	if err != nil {
		return err
	}
	manager.finishLocalInvalidation(generation, cancel)
	hookErr := manager.invalidateHook(generation)
	if client != nil {
		client.Close()
	}
	manager.mu.Lock()
	manager.state = ScopeStateClosed
	manager.scope = nil
	manager.client = nil
	manager.mu.Unlock()
	return hookErr
}

func (manager *ScopeManager) beginInvalidation() (int64, ScopeClient, context.CancelFunc, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.state == ScopeStateClosed {
		return 0, nil, nil, newScopeError(
			domain.SafeErrorClassCancelled,
			"scope_manager_closed",
			"invalidate_scope",
			"Scope management is closed.",
		)
	}
	if manager.generation == math.MaxInt64 {
		return 0, nil, nil, newScopeError(
			domain.SafeErrorClassInternal,
			"scope_generation_exhausted",
			"invalidate_scope",
			"Scope generation could not be advanced safely.",
		)
	}
	manager.generation++
	manager.state = ScopeStateActivating
	client := manager.client
	cancel := manager.runCancel
	manager.scope = nil
	manager.client = nil
	manager.activeRunID = ""
	manager.runCancel = nil
	return manager.generation, client, cancel, nil
}

func (manager *ScopeManager) finishLocalInvalidation(generation int64, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.generation != generation {
		return
	}
	manager.selectedResource = nil
	clear(manager.namespaceCache)
	clear(manager.resourceCache)
}

func (manager *ScopeManager) invalidateHook(generation int64) error {
	var approvalErr error
	if manager.approvalHook != nil {
		approvalErr = manager.approvalHook.InvalidateScope(generation)
	}
	runtimeErr := manager.hook.InvalidateScope(generation)
	if approvalErr != nil || runtimeErr != nil {
		return newScopeError(
			domain.SafeErrorClassInternal,
			"scope_invalidation_failed",
			"invalidate_scope",
			"Scope-dependent state could not be invalidated safely.",
		)
	}
	return nil
}

func (manager *ScopeManager) publishActive(client ScopeClient, contextName, namespace string, generation int64) (domain.ClusterScope, error) {
	activatedAt := manager.now().UTC()
	scope := domain.ClusterScope{
		Context:         contextName,
		Namespace:       namespace,
		NamespaceAccess: manager.access,
		Generation:      generation,
		ActivatedAt:     activatedAt,
	}
	if scope.Validate() != nil {
		client.Close()
		manager.markUnavailable(generation)
		return domain.ClusterScope{}, newScopeError(
			domain.SafeErrorClassInternal,
			"scope_activation_invalid",
			"activate_scope",
			"The Kubernetes scope could not be activated safely.",
		)
	}
	manager.mu.Lock()
	if manager.state != ScopeStateActivating || manager.generation != generation {
		manager.mu.Unlock()
		client.Close()
		return domain.ClusterScope{}, staleScopeError("activate_scope")
	}
	manager.scope = &scope
	manager.client = client
	manager.state = ScopeStateActive
	manager.mu.Unlock()
	return scope, nil
}

func (manager *ScopeManager) markUnavailable(generation int64) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.state == ScopeStateClosed || manager.generation != generation {
		return
	}
	manager.state = ScopeStateUnavailable
	manager.scope = nil
	manager.client = nil
}

func (manager *ScopeManager) checkExpectedGeneration(expected int64) error {
	if expected < 0 {
		return newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_generation_invalid",
			"switch_scope",
			"The expected Kubernetes scope state was invalid.",
		)
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.state == ScopeStateClosed {
		return newScopeError(
			domain.SafeErrorClassCancelled,
			"scope_manager_closed",
			"switch_scope",
			"Scope management is closed.",
		)
	}
	if manager.generation != expected {
		return staleScopeError("switch_scope")
	}
	return nil
}

func (manager *ScopeManager) captureActiveGeneration(expected int64) (domain.ClusterScope, ScopeClient, error) {
	if err := manager.checkExpectedGeneration(expected); err != nil {
		return domain.ClusterScope{}, nil, err
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.state != ScopeStateActive || manager.scope == nil || manager.client == nil {
		return domain.ClusterScope{}, nil, scopeUnavailableError("switch_namespace")
	}
	return *manager.scope, manager.client, nil
}

func (manager *ScopeManager) captureClientForScope(scope domain.ClusterScope, operation string) (ScopeClient, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.currentScopeLocked(scope) {
		return nil, staleScopeError(operation)
	}
	return manager.client, nil
}

func (manager *ScopeManager) scopeCurrent(scope domain.ClusterScope) bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.currentScopeLocked(scope)
}

func (manager *ScopeManager) currentScopeLocked(scope domain.ClusterScope) bool {
	return manager.state == ScopeStateActive && manager.scope != nil && manager.client != nil && *manager.scope == scope
}

func validateBoundRead(ctx context.Context, scope domain.ClusterScope, limit int, operation string) error {
	if err := contextInputError(ctx, operation); err != nil {
		return err
	}
	if scope.Validate() != nil {
		return newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_invalid",
			operation,
			"The active Kubernetes context and namespace were invalid.",
		)
	}
	if limit < 1 || limit > domain.MaxResourceSummaries {
		return newScopeError(
			domain.SafeErrorClassInvalidInput,
			"scope_result_limit_invalid",
			operation,
			"The result limit was invalid.",
		)
	}
	return nil
}

func resourceListMatches(list domain.ResourceList, namespace string, kind domain.ResourceKind) bool {
	for _, item := range list.Items {
		if !domain.ReferenceMatchesWorkingNamespace(item.Reference, namespace) || item.Reference.Kind != string(kind) || item.Reference.APIVersion != kind.APIVersion() {
			return false
		}
	}
	return true
}

func sameResourceIdentity(actual, requested domain.ResourceRef) bool {
	return actual.APIVersion == requested.APIVersion && actual.Kind == requested.Kind &&
		actual.Namespace == requested.Namespace && actual.Name == requested.Name
}

func cloneNamespaceList(source domain.NamespaceList) domain.NamespaceList {
	return domain.NamespaceList{
		Items:     append([]domain.NamespaceSummary(nil), source.Items...),
		Truncated: source.Truncated,
	}
}

func cloneResourceSummary(source domain.ResourceSummary) domain.ResourceSummary {
	result := source
	result.Owners = append([]domain.ResourceOwner(nil), source.Owners...)
	return result
}

func cloneResourceList(source domain.ResourceList) domain.ResourceList {
	result := domain.ResourceList{
		Items:     make([]domain.ResourceSummary, len(source.Items)),
		Truncated: source.Truncated,
	}
	for index, item := range source.Items {
		result.Items[index] = cloneResourceSummary(item)
	}
	return result
}

func invalidProjectionError(operation string) *ScopeError {
	return newScopeError(
		domain.SafeErrorClassInvalidExternalResponse,
		"scope_projection_invalid",
		operation,
		"Kubernetes returned an invalid safe projection.",
	)
}

func scopeUnavailableError(operation string) *ScopeError {
	return newScopeError(
		domain.SafeErrorClassUnavailable,
		"scope_unavailable",
		operation,
		"No active Kubernetes scope is available.",
	)
}

func staleScopeError(operation string) *ScopeError {
	return newScopeError(
		domain.SafeErrorClassStaleScope,
		"stale_scope",
		operation,
		"The bound Kubernetes scope is no longer current.",
	)
}

func contextInputError(ctx context.Context, operation string) error {
	if ctx == nil {
		return newScopeError(
			domain.SafeErrorClassInvalidInput,
			"context_required",
			operation,
			"An operation Context is required.",
		)
	}
	return contextResultError(ctx, nil, operation)
}

func contextResultError(ctx context.Context, raw error, operation string) error {
	if ctx != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return newScopeError(domain.SafeErrorClassTimeout, "scope_operation_timeout", operation, "The Kubernetes scope operation reached its time limit.")
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return newScopeError(domain.SafeErrorClassCancelled, "scope_operation_cancelled", operation, "The Kubernetes scope operation was cancelled.")
		}
	}
	if raw == nil {
		return nil
	}
	if errors.Is(raw, context.DeadlineExceeded) {
		return newScopeError(domain.SafeErrorClassTimeout, "scope_operation_timeout", operation, "The Kubernetes scope operation reached its time limit.")
	}
	if errors.Is(raw, context.Canceled) {
		return newScopeError(domain.SafeErrorClassCancelled, "scope_operation_cancelled", operation, "The Kubernetes scope operation was cancelled.")
	}
	return safeScopeError(raw, operation)
}

type classifiedSafeError interface {
	error
	Class() domain.SafeErrorClass
	SafeMessage() string
	ErrorCode() string
	Operation() string
	Retryable() bool
}

func safeScopeError(raw error, operation string) error {
	var classified classifiedSafeError
	if errors.As(raw, &classified) && classified.Class().Valid() {
		return classified
	}
	return newScopeError(
		domain.SafeErrorClassInternal,
		"scope_operation_failed",
		operation,
		"The Kubernetes scope operation failed safely.",
	)
}

// ScopeError is a bounded project-owned Application failure.
type ScopeError struct {
	class     domain.SafeErrorClass
	code      string
	operation string
	message   string
}

func newScopeError(class domain.SafeErrorClass, code, operation, message string) *ScopeError {
	return &ScopeError{class: class, code: code, operation: operation, message: message}
}

func (err *ScopeError) Error() string {
	if err == nil {
		return "Kubernetes scope management failed."
	}
	return err.message + " (" + err.code + ")"
}

// Is preserves standard cancellation and deadline matching without exposing a
// raw adapter cause.
func (err *ScopeError) Is(target error) bool {
	if err == nil {
		return false
	}
	return err.class == domain.SafeErrorClassCancelled && target == context.Canceled ||
		err.class == domain.SafeErrorClassTimeout && target == context.DeadlineExceeded
}

// Class returns the stable project-owned failure class.
func (err *ScopeError) Class() domain.SafeErrorClass {
	if err == nil {
		return domain.SafeErrorClassInternal
	}
	return err.class
}

// ErrorCode returns the code-defined stable identifier.
func (err *ScopeError) ErrorCode() string {
	if err == nil {
		return "scope_internal"
	}
	return err.code
}

// Operation returns the code-defined safe operation.
func (err *ScopeError) Operation() string {
	if err == nil {
		return "scope"
	}
	return err.operation
}

// Retryable reports the conservative fixed retry classification.
func (*ScopeError) Retryable() bool {
	return false
}

// SafeMessage returns bounded code-authored delivery text.
func (err *ScopeError) SafeMessage() string {
	return err.Error()
}
