package kube

import (
	"context"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
)

const (
	maxBroadResourceFieldBytes        = 8 * 1024
	maxResourceContinuationTokenBytes = 4 * 1024
)

var (
	errResourceResponseLimit   = errors.New("Kubernetes resource response exceeded its byte limit")
	errBroadResourceProjection = errors.New("Kubernetes broad resource projection is invalid")
)

type responseBudgetContextKey struct{}

type responseBudgetSet struct {
	aggregate *responseByteBudget
	page      *responseByteBudget
}

type responseByteBudget struct {
	mu        sync.Mutex
	remaining int
	observed  int
	exhausted bool
}

func withResponseByteBudget(ctx context.Context, maximum int) (context.Context, *responseByteBudget) {
	budget := &responseByteBudget{remaining: maximum}
	return context.WithValue(ctx, responseBudgetContextKey{}, responseBudgetSet{aggregate: budget}), budget
}

func withResponsePageBudget(ctx context.Context, maximum int) (context.Context, *responseByteBudget) {
	page := &responseByteBudget{remaining: maximum}
	set, _ := ctx.Value(responseBudgetContextKey{}).(responseBudgetSet)
	set.page = page
	return context.WithValue(ctx, responseBudgetContextKey{}, set), page
}

func responseBudgetsFromContext(ctx context.Context) []*responseByteBudget {
	if ctx == nil {
		return nil
	}
	set, _ := ctx.Value(responseBudgetContextKey{}).(responseBudgetSet)
	result := make([]*responseByteBudget, 0, 2)
	if set.aggregate != nil {
		result = append(result, set.aggregate)
	}
	if set.page != nil {
		result = append(result, set.page)
	}
	return result
}

func (budget *responseByteBudget) allowance(maximum int) int {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.remaining < maximum {
		return budget.remaining
	}
	return maximum
}

func (budget *responseByteBudget) record(count int) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	budget.observed += count
	budget.remaining -= count
	if budget.remaining < 0 {
		budget.remaining = 0
	}
}

func (budget *responseByteBudget) exhaustWhenEmpty() {
	budget.mu.Lock()
	if budget.remaining == 0 {
		budget.exhausted = true
	}
	budget.mu.Unlock()
}

func (budget *responseByteBudget) snapshot() (int, bool) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.observed, budget.exhausted
}

func (budget *responseByteBudget) remainingBytes() int {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.remaining
}

type budgetedResponseBody struct {
	base    io.ReadCloser
	budgets []*responseByteBudget
	pending bool
}

func (body *budgetedResponseBody) Read(destination []byte) (int, error) {
	if body.pending {
		return 0, errResourceResponseLimit
	}
	if len(destination) == 0 {
		return 0, nil
	}
	allowed := len(destination)
	for _, budget := range body.budgets {
		allowed = budget.allowance(allowed)
	}
	if allowed == 0 {
		var probe [1]byte
		read, err := body.base.Read(probe[:])
		if read == 0 {
			return 0, err
		}
		for _, budget := range body.budgets {
			budget.exhaustWhenEmpty()
		}
		return 0, errResourceResponseLimit
	}
	read, err := body.base.Read(destination[:allowed])
	for _, budget := range body.budgets {
		budget.record(read)
	}
	if read == allowed && allowed < len(destination) && err == nil {
		var probe [1]byte
		probed, probeErr := body.base.Read(probe[:])
		switch {
		case probed > 0:
			body.pending = true
			for _, budget := range body.budgets {
				budget.exhaustWhenEmpty()
			}
		case probeErr != nil:
			err = probeErr
		default:
			// A Reader must not return (0, nil) for a non-empty buffer. Treat
			// that ambiguous boundary conservatively as an over-limit body.
			body.pending = true
			for _, budget := range body.budgets {
				budget.exhaustWhenEmpty()
			}
		}
	}
	return read, err
}

func (body *budgetedResponseBody) Close() error { return body.base.Close() }

// QueryResources performs one exact policy-bound GET or runtime-paginated
// LIST. All client-go discovery, dynamic, metadata, GVR, and continuation
// values remain confined to this adapter.
func (reader *ToolResourceReader) QueryResources(
	ctx context.Context,
	request toolcontract.ResourceQueryRequest,
) (toolcontract.ResourceQueryObservation, error) {
	const operation = "tool_query_resources"
	if request.Validate() != nil {
		return toolcontract.ResourceQueryObservation{}, newKubeSafeError(
			ClassPolicyDenied,
			"kubernetes_resource_policy_denied",
			operation,
			"The Kubernetes resource query is not admitted by the frozen local policy.",
		)
	}
	if err := reader.validateResourceQueryCurrent(ctx, request.Query, operation); err != nil {
		return toolcontract.ResourceQueryObservation{}, err
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Query.Scope, operation)
	if err != nil {
		return toolcontract.ResourceQueryObservation{}, err
	}
	queryContext, byteBudget := withResponseByteBudget(ctx, request.Query.Limits.MaxBytes)
	if request.Policy.Type.Group != "" && !request.Policy.Type.BuiltIn {
		if err := reader.validateDiscoveredResource(queryContext, bundle, request); err != nil {
			return toolcontract.ResourceQueryObservation{}, err
		}
		if err := reader.validateResourceQueryCurrent(queryContext, request.Query, operation); err != nil {
			return toolcontract.ResourceQueryObservation{}, err
		}
		if byteBudget.remainingBytes() == 0 {
			return toolcontract.ResourceQueryObservation{}, newKubeSafeError(
				ClassBudgetExhausted,
				"kubernetes_resource_byte_limit",
				operation,
				"The Kubernetes discovery response exhausted the resource byte limit before the resource read could start.",
			)
		}
	}
	if request.Query.Verb == domain.ResourceVerbGet {
		return reader.queryOneResource(queryContext, bundle, byteBudget, request)
	}
	return reader.queryResourcePages(queryContext, bundle, byteBudget, request)
}

func (reader *ToolResourceReader) validateResourceQueryCurrent(ctx context.Context, query domain.ResourceQuery, operation string) error {
	if err := reader.validateContext(ctx, query.Scope, operation); err != nil {
		return err
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, query.PolicyGeneration) {
		return newKubeSafeError(
			ClassStaleScope,
			"kubernetes_resource_policy_generation_stale",
			operation,
			"The resource policy changed before the Kubernetes read completed.",
		)
	}
	return nil
}

func (reader *ToolResourceReader) validateDiscoveredResource(
	ctx context.Context,
	bundle *ClientBundle,
	request toolcontract.ResourceQueryRequest,
) error {
	const operation = "discover_resource_policy"
	resourceType := request.Policy.Type
	discoveryContext, pageBudget := withResponsePageBudget(ctx, request.Query.Limits.PageBytes)
	var resources metav1.APIResourceList
	rawErr := bundle.typed.Discovery().RESTClient().Get().
		AbsPath("/apis", resourceType.Group, resourceType.Version).
		SetHeader("Accept", "application/json").
		Do(discoveryContext).
		Into(&resources)
	if _, exhausted := pageBudget.snapshot(); exhausted {
		return newKubeSafeError(ClassBudgetExhausted, "kubernetes_resource_page_byte_limit", operation, "The Kubernetes discovery response exceeded the resource page byte limit.")
	}
	if budgets := responseBudgetsFromContext(ctx); len(budgets) != 0 {
		if observed, exhausted := budgets[0].snapshot(); exhausted || observed > request.Query.Limits.MaxBytes {
			return newKubeSafeError(ClassBudgetExhausted, "kubernetes_resource_byte_limit", operation, "The Kubernetes discovery response exceeded the resource byte limit.")
		}
	}
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return err
	}
	if resources.GroupVersion != resourceType.APIVersion() {
		return invalidKubernetesProjectionError(operation)
	}
	for _, discovered := range resources.APIResources {
		if discovered.Name != resourceType.Resource {
			continue
		}
		if strings.Contains(discovered.Name, "/") || discovered.Kind != resourceType.Kind ||
			discovered.Namespaced != resourceType.Namespaced() || !discoveryAllowsVerb(discovered.Verbs, request.Query.Verb) {
			return newKubeSafeError(ClassPolicyDenied, "kubernetes_discovery_policy_mismatch", operation, "The discovered Kubernetes API does not match the exact local resource policy.")
		}
		return nil
	}
	return newKubeSafeError(ClassUnsupported, "kubernetes_resource_api_unavailable", operation, "The exact policy-admitted Kubernetes API is not available.")
}

func discoveryAllowsVerb(verbs metav1.Verbs, verb domain.ResourceVerb) bool {
	for _, current := range verbs {
		if current == string(verb) {
			return true
		}
	}
	return false
}

func (reader *ToolResourceReader) queryOneResource(
	ctx context.Context,
	bundle *ClientBundle,
	byteBudget *responseByteBudget,
	request toolcontract.ResourceQueryRequest,
) (toolcontract.ResourceQueryObservation, error) {
	const operation = "tool_get_resource"
	requestContext, pageBudget := withResponsePageBudget(ctx, request.Query.Limits.PageBytes)
	var observation toolcontract.ResourceObservation
	var err error
	if metadataOnlyResource(request.Policy.Type) {
		var resource metadata.ResourceInterface
		namespaceable := bundle.metadata.Resource(resourceGVR(request.Policy.Type))
		if request.Policy.Type.Namespaced() {
			resource = namespaceable.Namespace(request.Query.Namespace)
		} else {
			resource = namespaceable
		}
		object, rawErr := resource.Get(requestContext, request.Query.Name, metav1.GetOptions{})
		if callErr := resourceCallError(requestContext, rawErr, operation); callErr != nil {
			err = callErr
		} else {
			observation, err = projectPartialResource(request.Policy, object)
		}
	} else if request.Policy.Type.BuiltIn {
		observation, err = readBroadBuiltInResource(requestContext, bundle, request)
	} else {
		var resource dynamic.ResourceInterface
		namespaceable := bundle.dynamic.Resource(resourceGVR(request.Policy.Type))
		if request.Policy.Type.Namespaced() {
			resource = namespaceable.Namespace(request.Query.Namespace)
		} else {
			resource = namespaceable
		}
		object, rawErr := resource.Get(requestContext, request.Query.Name, metav1.GetOptions{})
		if callErr := resourceCallError(requestContext, rawErr, operation); callErr != nil {
			err = callErr
		} else {
			observation, err = projectUnstructuredResource(request.Policy, object)
		}
	}
	if _, exhausted := pageBudget.snapshot(); exhausted {
		return toolcontract.ResourceQueryObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_resource_page_byte_limit", operation, "The Kubernetes resource response exceeded the resource page byte limit.")
	}
	observed, aggregateExhausted := byteBudget.snapshot()
	if aggregateExhausted {
		return toolcontract.ResourceQueryObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_resource_byte_limit", operation, "The Kubernetes resource response exceeded the resource byte limit.")
	} else if observed > request.Query.Limits.MaxBytes {
		return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
	}
	if err != nil {
		return toolcontract.ResourceQueryObservation{}, err
	}
	if err := reader.validateResourceQueryCurrent(requestContext, request.Query, operation); err != nil {
		return toolcontract.ResourceQueryObservation{}, err
	}
	partial := observation.Truncated
	reason := ""
	if partial {
		reason = "field_limit"
	}
	page := domain.ResourcePage{
		Type: request.Policy.Type, Items: []domain.ResourceSummary{observation.Summary}, PagesRead: 1,
		ScannedItems: 1, MatchedItems: 1, ObservedBytes: observed, Partial: partial, Truncated: partial, Reason: reason,
	}
	result := toolcontract.ResourceQueryObservation{Page: page, Items: []toolcontract.ResourceObservation{observation}}
	if result.Validate(request) != nil {
		return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
	}
	return result, nil
}

func readBroadBuiltInResource(
	ctx context.Context,
	bundle *ClientBundle,
	request toolcontract.ResourceQueryRequest,
) (toolcontract.ResourceObservation, error) {
	if bundle == nil || bundle.typed == nil || !request.Policy.Type.BuiltIn ||
		request.Policy.Type != domain.BuiltInResourceType(domain.ResourceKind(request.Policy.Type.Kind)) {
		return toolcontract.ResourceObservation{}, errors.New("invalid built-in resource policy")
	}
	namespace := request.Query.Namespace
	name := request.Query.Name
	detail := request.Detail
	switch domain.ResourceKind(request.Policy.Type.Kind) {
	case domain.ResourceKindNamespace:
		object, rawErr := bundle.typed.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectNamespace(object, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindNode:
		object, rawErr := bundle.typed.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectNode(object, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindPod:
		object, rawErr := bundle.typed.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		observation, projectionErr := projectToolPod(object, namespace, name, detail)
		return finishBroadBuiltInGet(ctx, request.Policy, object, observation, rawErr, projectionErr)
	case domain.ResourceKindService:
		object, rawErr := bundle.typed.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
		observation, projectionErr := projectToolService(object, namespace, name, detail)
		return finishBroadBuiltInGet(ctx, request.Policy, object, observation, rawErr, projectionErr)
	case domain.ResourceKindPersistentVolumeClaim:
		object, rawErr := bundle.typed.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectPersistentVolumeClaim(object, namespace, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindPersistentVolume:
		object, rawErr := bundle.typed.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectPersistentVolume(object, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindDeployment:
		object, rawErr := bundle.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		observation, projectionErr := projectToolDeployment(object, namespace, name, detail)
		return finishBroadBuiltInGet(ctx, request.Policy, object, observation, rawErr, projectionErr)
	case domain.ResourceKindReplicaSet:
		object, rawErr := bundle.typed.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
		observation, projectionErr := projectToolReplicaSet(object, namespace, name, detail)
		return finishBroadBuiltInGet(ctx, request.Policy, object, observation, rawErr, projectionErr)
	case domain.ResourceKindStatefulSet:
		object, rawErr := bundle.typed.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectStatefulSet(object, namespace, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindDaemonSet:
		object, rawErr := bundle.typed.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectDaemonSet(object, namespace, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindJob:
		object, rawErr := bundle.typed.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		observation, projectionErr := projectToolJob(object, namespace, name, detail)
		return finishBroadBuiltInGet(ctx, request.Policy, object, observation, rawErr, projectionErr)
	case domain.ResourceKindCronJob:
		object, rawErr := bundle.typed.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectCronJob(object, namespace, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindIngress:
		object, rawErr := bundle.typed.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectIngress(object, namespace, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindHorizontalPodAutoscaler:
		object, rawErr := bundle.typed.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectHorizontalPodAutoscaler(object, namespace, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	case domain.ResourceKindPodDisruptionBudget:
		object, rawErr := bundle.typed.PolicyV1().PodDisruptionBudgets(namespace).Get(ctx, name, metav1.GetOptions{})
		summary, projectionErr := projectPodDisruptionBudget(object, namespace, name)
		return finishBroadBuiltInGet(ctx, request.Policy, object, toolcontract.ResourceObservation{Summary: summary}, rawErr, projectionErr)
	default:
		return toolcontract.ResourceObservation{}, errors.New("unsupported built-in resource policy")
	}
}

func finishBroadBuiltInGet(
	ctx context.Context,
	policy domain.ResourcePolicy,
	object runtime.Object,
	observation toolcontract.ResourceObservation,
	rawErr error,
	projectionErr error,
) (toolcontract.ResourceObservation, error) {
	if err := resourceCallError(ctx, rawErr, "tool_get_resource"); err != nil {
		return toolcontract.ResourceObservation{}, err
	}
	return projectBroadTypedObject(policy, object, observation, projectionErr)
}

func (reader *ToolResourceReader) queryResourcePages(
	ctx context.Context,
	bundle *ClientBundle,
	byteBudget *responseByteBudget,
	request toolcontract.ResourceQueryRequest,
) (toolcontract.ResourceQueryObservation, error) {
	const operation = "tool_list_resources"
	labelSelector, fieldSelector, err := resourceSelectors(request.Policy, request.Query.Filters)
	if err != nil {
		return toolcontract.ResourceQueryObservation{}, newKubeSafeError(ClassPolicyDenied, "kubernetes_resource_filter_denied", operation, "The resource filter is not admitted by the exact local policy.")
	}
	items := make([]toolcontract.ResourceObservation, 0, request.Query.Limits.MaxReturned)
	seen := make(map[string]struct{}, request.Query.Limits.MaxReturned)
	continuation := ""
	pagesRead := 0
	scanned := 0
	matched := 0
	partialReason := ""
	moreAvailable := false
	seenContinuations := make(map[string]struct{}, request.Query.Limits.MaxPages)
	for pagesRead < request.Query.Limits.MaxPages && scanned < request.Query.Limits.MaxItems {
		if byteBudget.remainingBytes() == 0 {
			if len(items) == 0 {
				return toolcontract.ResourceQueryObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_resource_byte_limit", operation, "The Kubernetes resource byte limit was exhausted before a page could be read.")
			}
			partialReason = "byte_limit"
			moreAvailable = true
			break
		}
		if err := reader.validateResourceQueryCurrent(ctx, request.Query, operation); err != nil {
			return toolcontract.ResourceQueryObservation{}, err
		}
		pageLimit := min(request.Query.Limits.PageItems, request.Query.Limits.MaxItems-scanned)
		if bundle.requestTimeout <= 0 {
			return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
		}
		timeoutSeconds := int64((bundle.requestTimeout + time.Second - 1) / time.Second)
		options := metav1.ListOptions{
			LabelSelector: labelSelector, FieldSelector: fieldSelector,
			Limit: int64(pageLimit), Continue: continuation, TimeoutSeconds: &timeoutSeconds,
		}
		pageContext, pageBudget := withResponsePageBudget(ctx, request.Query.Limits.PageBytes)
		pageItems, next, readErr := readBroadResourcePage(pageContext, bundle, request.Policy, request.Query.Namespace, options)
		_, pageExhausted := pageBudget.snapshot()
		_, aggregateExhausted := byteBudget.snapshot()
		if pageExhausted || aggregateExhausted {
			if len(items) == 0 {
				code := "kubernetes_resource_byte_limit"
				message := "The Kubernetes resource response exceeded the resource byte limit."
				if pageExhausted && !aggregateExhausted {
					code = "kubernetes_resource_page_byte_limit"
					message = "The Kubernetes resource response exceeded the resource page byte limit."
				}
				return toolcontract.ResourceQueryObservation{}, newKubeSafeError(ClassBudgetExhausted, code, operation, message)
			}
			partialReason = "byte_limit"
			moreAvailable = true
			break
		}
		if readErr != nil && ctx.Err() == nil && errors.Is(readErr, errBroadResourceProjection) {
			return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
		}
		if callErr := resourceCallError(ctx, readErr, operation); callErr != nil {
			return toolcontract.ResourceQueryObservation{}, callErr
		}
		if !validResourceContinuation(next) {
			return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
		}
		if next != "" {
			if _, duplicate := seenContinuations[next]; duplicate {
				return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
			}
			seenContinuations[next] = struct{}{}
		}
		if err := reader.validateResourceQueryCurrent(ctx, request.Query, operation); err != nil {
			return toolcontract.ResourceQueryObservation{}, err
		}
		if len(pageItems) > pageLimit {
			return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
		}
		pagesRead++
		for _, object := range pageItems {
			scanned++
			observation, projectErr := projectBroadResource(request.Policy, object)
			if projectErr != nil {
				return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
			}
			if observation.Truncated && partialReason == "" {
				partialReason = "field_limit"
			}
			if !resourceMatchesFilters(observation, request.Query.Filters) {
				continue
			}
			matched++
			key := observation.Summary.Reference.Namespace + "\x00" + observation.Summary.Reference.Name
			if _, duplicate := seen[key]; duplicate {
				return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
			}
			if len(items) == request.Query.Limits.MaxReturned {
				partialReason = "return_limit"
				moreAvailable = true
				continue
			}
			seen[key] = struct{}{}
			items = append(items, observation)
		}
		continuation = next
		moreAvailable = continuation != "" || matched > len(items)
		if continuation == "" {
			break
		}
		moreAvailable = true
		if len(items) >= request.Query.Limits.MaxReturned {
			if partialReason == "" {
				partialReason = "return_limit"
			}
			break
		}
	}
	if continuation != "" && partialReason == "" {
		switch {
		case scanned >= request.Query.Limits.MaxItems:
			partialReason = "item_limit"
		case pagesRead >= request.Query.Limits.MaxPages:
			partialReason = "page_limit"
		default:
			partialReason = "return_limit"
		}
	}
	sort.Slice(items, func(left, right int) bool {
		leftRef := items[left].Summary.Reference
		rightRef := items[right].Summary.Reference
		return leftRef.Namespace+"\x00"+leftRef.Name < rightRef.Namespace+"\x00"+rightRef.Name
	})
	summaries := make([]domain.ResourceSummary, len(items))
	for index := range items {
		summaries[index] = items[index].Summary
	}
	observed, exhausted := byteBudget.snapshot()
	if exhausted && partialReason == "" {
		partialReason = "byte_limit"
		moreAvailable = true
	}
	partial := partialReason != ""
	page := domain.ResourcePage{
		Type: request.Policy.Type, Items: summaries, PagesRead: pagesRead, ScannedItems: scanned, MatchedItems: matched,
		ObservedBytes: observed, Partial: partial, Truncated: partial, MoreAvailable: moreAvailable && partial,
		Reason: partialReason,
	}
	result := toolcontract.ResourceQueryObservation{Page: page, Items: items}
	if result.Validate(request) != nil {
		return toolcontract.ResourceQueryObservation{}, invalidKubernetesProjectionError(operation)
	}
	return result, nil
}

func validResourceContinuation(value string) bool {
	if len(value) > maxResourceContinuationTokenBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) {
			return false
		}
	}
	return true
}

type broadPageItem struct {
	unstructured *unstructured.Unstructured
	metadata     *metav1.PartialObjectMetadata
	observation  *toolcontract.ResourceObservation
}

func readBroadResourcePage(
	ctx context.Context,
	bundle *ClientBundle,
	policy domain.ResourcePolicy,
	namespace string,
	options metav1.ListOptions,
) ([]broadPageItem, string, error) {
	if metadataOnlyResource(policy.Type) {
		var resource metadata.ResourceInterface
		namespaceable := bundle.metadata.Resource(resourceGVR(policy.Type))
		if policy.Type.Namespaced() {
			resource = namespaceable.Namespace(namespace)
		} else {
			resource = namespaceable
		}
		list, err := resource.List(ctx, options)
		if err != nil || list == nil {
			return nil, "", err
		}
		items := make([]broadPageItem, len(list.Items))
		for index := range list.Items {
			items[index].metadata = &list.Items[index]
		}
		return items, list.Continue, nil
	}
	if policy.Type.BuiltIn {
		return readBroadBuiltInPage(ctx, bundle, policy, namespace, options)
	}
	var resource dynamic.ResourceInterface
	namespaceable := bundle.dynamic.Resource(resourceGVR(policy.Type))
	if policy.Type.Namespaced() {
		resource = namespaceable.Namespace(namespace)
	} else {
		resource = namespaceable
	}
	list, err := resource.List(ctx, options)
	if err != nil || list == nil {
		return nil, "", err
	}
	items := make([]broadPageItem, len(list.Items))
	for index := range list.Items {
		items[index].unstructured = &list.Items[index]
	}
	return items, list.GetContinue(), nil
}

func projectBroadResource(policy domain.ResourcePolicy, item broadPageItem) (toolcontract.ResourceObservation, error) {
	if item.metadata != nil && item.unstructured == nil && item.observation == nil {
		return projectPartialResource(policy, item.metadata)
	}
	if item.unstructured != nil && item.metadata == nil && item.observation == nil {
		return projectUnstructuredResource(policy, item.unstructured)
	}
	if item.observation != nil && item.metadata == nil && item.unstructured == nil {
		observation := *item.observation
		if observation.Validate() != nil || observation.Summary.EffectiveType() != policy.Type {
			return toolcontract.ResourceObservation{}, errors.New("invalid typed broad resource projection")
		}
		return observation, nil
	}
	return toolcontract.ResourceObservation{}, errors.New("invalid broad resource page item")
}

func readBroadBuiltInPage(
	ctx context.Context,
	bundle *ClientBundle,
	policy domain.ResourcePolicy,
	namespace string,
	options metav1.ListOptions,
) ([]broadPageItem, string, error) {
	if bundle == nil || bundle.typed == nil || !policy.Type.BuiltIn || policy.Type != domain.BuiltInResourceType(domain.ResourceKind(policy.Type.Kind)) {
		return nil, "", errors.New("invalid built-in resource policy")
	}
	switch domain.ResourceKind(policy.Type.Kind) {
	case domain.ResourceKindNamespace:
		list, err := bundle.typed.CoreV1().Namespaces().List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *corev1.Namespace) (toolcontract.ResourceObservation, error) {
			summary, err := projectNamespace(object, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindNode:
		list, err := bundle.typed.CoreV1().Nodes().List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *corev1.Node) (toolcontract.ResourceObservation, error) {
			summary, err := projectNode(object, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindPod:
		list, err := bundle.typed.CoreV1().Pods(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *corev1.Pod) (toolcontract.ResourceObservation, error) {
			observation, err := projectToolPod(object, object.Namespace, "", toolcontract.ResourceDetailSummary)
			return projectBroadTypedObject(policy, object, observation, err)
		})
	case domain.ResourceKindService:
		list, err := bundle.typed.CoreV1().Services(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *corev1.Service) (toolcontract.ResourceObservation, error) {
			observation, err := projectToolService(object, object.Namespace, "", toolcontract.ResourceDetailSummary)
			return projectBroadTypedObject(policy, object, observation, err)
		})
	case domain.ResourceKindPersistentVolumeClaim:
		list, err := bundle.typed.CoreV1().PersistentVolumeClaims(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *corev1.PersistentVolumeClaim) (toolcontract.ResourceObservation, error) {
			summary, err := projectPersistentVolumeClaim(object, object.Namespace, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindPersistentVolume:
		list, err := bundle.typed.CoreV1().PersistentVolumes().List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *corev1.PersistentVolume) (toolcontract.ResourceObservation, error) {
			summary, err := projectPersistentVolume(object, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindDeployment:
		list, err := bundle.typed.AppsV1().Deployments(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *appsv1.Deployment) (toolcontract.ResourceObservation, error) {
			observation, err := projectToolDeployment(object, object.Namespace, "", toolcontract.ResourceDetailSummary)
			return projectBroadTypedObject(policy, object, observation, err)
		})
	case domain.ResourceKindReplicaSet:
		list, err := bundle.typed.AppsV1().ReplicaSets(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *appsv1.ReplicaSet) (toolcontract.ResourceObservation, error) {
			observation, err := projectToolReplicaSet(object, object.Namespace, "", toolcontract.ResourceDetailSummary)
			return projectBroadTypedObject(policy, object, observation, err)
		})
	case domain.ResourceKindStatefulSet:
		list, err := bundle.typed.AppsV1().StatefulSets(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *appsv1.StatefulSet) (toolcontract.ResourceObservation, error) {
			summary, err := projectStatefulSet(object, object.Namespace, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindDaemonSet:
		list, err := bundle.typed.AppsV1().DaemonSets(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *appsv1.DaemonSet) (toolcontract.ResourceObservation, error) {
			summary, err := projectDaemonSet(object, object.Namespace, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindJob:
		list, err := bundle.typed.BatchV1().Jobs(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *batchv1.Job) (toolcontract.ResourceObservation, error) {
			observation, err := projectToolJob(object, object.Namespace, "", toolcontract.ResourceDetailSummary)
			return projectBroadTypedObject(policy, object, observation, err)
		})
	case domain.ResourceKindCronJob:
		list, err := bundle.typed.BatchV1().CronJobs(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *batchv1.CronJob) (toolcontract.ResourceObservation, error) {
			summary, err := projectCronJob(object, object.Namespace, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindIngress:
		list, err := bundle.typed.NetworkingV1().Ingresses(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *networkingv1.Ingress) (toolcontract.ResourceObservation, error) {
			summary, err := projectIngress(object, object.Namespace, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindHorizontalPodAutoscaler:
		list, err := bundle.typed.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *autoscalingv2.HorizontalPodAutoscaler) (toolcontract.ResourceObservation, error) {
			summary, err := projectHorizontalPodAutoscaler(object, object.Namespace, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	case domain.ResourceKindPodDisruptionBudget:
		list, err := bundle.typed.PolicyV1().PodDisruptionBudgets(namespace).List(ctx, options)
		if err != nil || list == nil {
			return nil, "", broadListResultError(err, list == nil)
		}
		return projectBroadTypedPage(policy, list.Items, list.Continue, func(object *policyv1.PodDisruptionBudget) (toolcontract.ResourceObservation, error) {
			summary, err := projectPodDisruptionBudget(object, object.Namespace, "")
			return projectBroadTypedObject(policy, object, toolcontract.ResourceObservation{Summary: summary}, err)
		})
	default:
		return nil, "", errors.New("unsupported built-in resource policy")
	}
}

func broadListResultError(err error, missing bool) error {
	if err != nil {
		return err
	}
	if missing {
		return errBroadResourceProjection
	}
	return nil
}

func projectBroadTypedPage[T any](
	policy domain.ResourcePolicy,
	objects []T,
	continuation string,
	project func(*T) (toolcontract.ResourceObservation, error),
) ([]broadPageItem, string, error) {
	items := make([]broadPageItem, len(objects))
	for index := range objects {
		observation, err := project(&objects[index])
		if err != nil {
			return nil, "", errors.Join(errBroadResourceProjection, err)
		}
		copy := observation
		items[index].observation = &copy
	}
	return items, continuation, nil
}

func projectBroadTypedObject(
	policy domain.ResourcePolicy,
	object runtime.Object,
	observation toolcontract.ResourceObservation,
	projectionErr error,
) (toolcontract.ResourceObservation, error) {
	metadataObject, ok := object.(metav1.Object)
	if projectionErr != nil || !ok || metadataObject == nil || observation.Summary.EffectiveType() != policy.Type {
		return toolcontract.ResourceObservation{}, errors.New("invalid typed Kubernetes resource projection")
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		return toolcontract.ResourceObservation{}, err
	}
	unstructuredObject := &unstructured.Unstructured{Object: raw}
	projected := make([]toolcontract.ResourceFieldObservation, 0, len(policy.Fields))
	truncated := observation.Truncated
	for _, field := range policy.Fields {
		if field.DataClass == domain.ResourceDataSensitive {
			continue
		}
		value, present, handled := metadataFieldValue(metadataObject, field)
		if !handled {
			value, present, err = unstructuredFieldValue(unstructuredObject, field)
			if err != nil {
				return toolcontract.ResourceObservation{}, err
			}
		}
		bounded, limited := boundedResourceField(value)
		truncated = truncated || limited
		projected = append(projected, toolcontract.ResourceFieldObservation{
			Field: field.ID, Path: field.Path, Scalar: field.Scalar, DataClass: field.DataClass,
			Value: toolcontract.ExternalText{Value: bounded, Truncated: limited}, Present: present,
		})
	}
	observation.Summary.Type = policy.Type
	observation.Fields = projected
	observation.Truncated = truncated
	if observation.Validate() != nil {
		return toolcontract.ResourceObservation{}, errors.New("invalid typed Kubernetes resource projection")
	}
	return observation, nil
}

func projectPartialResource(policy domain.ResourcePolicy, object *metav1.PartialObjectMetadata) (toolcontract.ResourceObservation, error) {
	if object == nil {
		return toolcontract.ResourceObservation{}, errors.New("missing metadata object")
	}
	fields := make([]toolcontract.ResourceFieldObservation, 0, len(policy.Fields))
	truncated := false
	for _, field := range policy.Fields {
		if field.DataClass == domain.ResourceDataSensitive {
			continue
		}
		value, present, _ := metadataFieldValue(object, field)
		bounded, limited := boundedResourceField(value)
		truncated = truncated || limited
		fields = append(fields, toolcontract.ResourceFieldObservation{
			Field: field.ID, Path: field.Path, Scalar: field.Scalar, DataClass: field.DataClass,
			Value: toolcontract.ExternalText{Value: bounded, Truncated: limited}, Present: present,
		})
	}
	return resourceObservationFromMetadata(policy.Type, object, fields, truncated)
}

func projectUnstructuredResource(policy domain.ResourcePolicy, object *unstructured.Unstructured) (toolcontract.ResourceObservation, error) {
	if object == nil || object.GetAPIVersion() != policy.Type.APIVersion() || object.GetKind() != policy.Type.Kind {
		return toolcontract.ResourceObservation{}, errors.New("resource API identity mismatch")
	}
	fields := make([]toolcontract.ResourceFieldObservation, 0, len(policy.Fields))
	truncated := false
	for _, field := range policy.Fields {
		if field.DataClass == domain.ResourceDataSensitive {
			continue
		}
		value, present, err := unstructuredFieldValue(object, field)
		if err != nil {
			return toolcontract.ResourceObservation{}, err
		}
		bounded, limited := boundedResourceField(value)
		truncated = truncated || limited
		fields = append(fields, toolcontract.ResourceFieldObservation{
			Field: field.ID, Path: field.Path, Scalar: field.Scalar, DataClass: field.DataClass,
			Value: toolcontract.ExternalText{Value: bounded, Truncated: limited}, Present: present,
		})
	}
	return resourceObservationFromMetadata(policy.Type, object, fields, truncated)
}

func resourceObservationFromMetadata(
	resourceType domain.ResourceType,
	metadata metav1.Object,
	projected []toolcontract.ResourceFieldObservation,
	truncated bool,
) (toolcontract.ResourceObservation, error) {
	createdAt := time.Time{}
	creationTimestamp := metadata.GetCreationTimestamp()
	if !creationTimestamp.IsZero() {
		createdAt = creationTimestamp.Time.UTC()
	}
	summary := domain.ResourceSummary{
		Type: resourceType,
		Reference: domain.ResourceRef{
			APIVersion: resourceType.APIVersion(), Kind: resourceType.Kind, Namespace: metadata.GetNamespace(),
			Name: metadata.GetName(), UID: string(metadata.GetUID()), ResourceVersion: metadata.GetResourceVersion(),
		},
		CreatedAt: createdAt,
		Status:    resourceStatusFromFields(projected),
	}
	observation := toolcontract.ResourceObservation{Summary: summary, Fields: projected, Truncated: truncated}
	if observation.Validate() != nil {
		return toolcontract.ResourceObservation{}, errors.New("invalid resource projection")
	}
	return observation, nil
}

func metadataFieldValue(object metav1.Object, field domain.ResourceFieldPolicy) (string, bool, bool) {
	switch field.Path {
	case "metadata.name":
		return object.GetName(), object.GetName() != "", true
	case "metadata.namespace":
		return object.GetNamespace(), object.GetNamespace() != "", true
	case "metadata.creationTimestamp":
		creationTimestamp := object.GetCreationTimestamp()
		if creationTimestamp.IsZero() {
			return "", false, true
		}
		return creationTimestamp.Time.UTC().Format(time.RFC3339Nano), true, true
	}
	if field.SelectorSource == domain.ResourceSelectorLabel {
		value, found := object.GetLabels()[field.SelectorKey]
		return value, found, true
	}
	return "", false, false
}

func unstructuredFieldValue(object *unstructured.Unstructured, field domain.ResourceFieldPolicy) (string, bool, error) {
	if value, present, handled := metadataFieldValue(object, field); handled {
		return value, present, nil
	}
	value, present, err := unstructured.NestedFieldNoCopy(object.Object, strings.Split(field.Path, ".")...)
	if err != nil || !present || value == nil {
		return "", present, err
	}
	switch field.Scalar {
	case domain.ResourceScalarString:
		result, ok := value.(string)
		if !ok {
			return "", false, errors.New("projected field is not a string")
		}
		return result, true, nil
	case domain.ResourceScalarInteger:
		switch number := value.(type) {
		case int64:
			return strconv.FormatInt(number, 10), true, nil
		case int32:
			return strconv.FormatInt(int64(number), 10), true, nil
		case int:
			return strconv.Itoa(number), true, nil
		case float64:
			if number != float64(int64(number)) {
				return "", false, errors.New("projected integer is fractional")
			}
			return strconv.FormatInt(int64(number), 10), true, nil
		default:
			return "", false, errors.New("projected field is not an integer")
		}
	case domain.ResourceScalarDecimal:
		switch number := value.(type) {
		case float64:
			return strconv.FormatFloat(number, 'g', -1, 64), true, nil
		case int64:
			return strconv.FormatInt(number, 10), true, nil
		default:
			return "", false, errors.New("projected field is not numeric")
		}
	case domain.ResourceScalarBoolean:
		result, ok := value.(bool)
		if !ok {
			return "", false, errors.New("projected field is not Boolean")
		}
		return strconv.FormatBool(result), true, nil
	case domain.ResourceScalarTimestamp:
		result, ok := value.(string)
		if !ok {
			return "", false, errors.New("projected timestamp is not a string")
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, result)
		if parseErr != nil {
			return "", false, errors.New("projected timestamp is invalid")
		}
		return parsed.UTC().Format(time.RFC3339Nano), true, nil
	default:
		return "", false, errors.New("projected scalar type is invalid")
	}
}

func resourceStatusFromFields(projected []toolcontract.ResourceFieldObservation) domain.ResourceStatus {
	status := domain.ResourceStatus{}
	for _, field := range projected {
		if !field.Present {
			continue
		}
		switch field.Field {
		case "phase":
			status.Phase = safeStatusText(field.Value.Value)
		case "reason":
			status.Reason = safeStatusText(field.Value.Value)
		case "service_type":
			status.ServiceType = safeStatusText(field.Value.Value)
		case "ready":
			status.Ready = parseOptionalCount(field.Value.Value)
		case "desired":
			status.Desired = parseOptionalCount(field.Value.Value)
		case "available":
			status.Available = parseOptionalCount(field.Value.Value)
		case "active":
			status.Active = parseOptionalCount(field.Value.Value)
		case "succeeded":
			status.Succeeded = parseOptionalCount(field.Value.Value)
		case "failed":
			status.Failed = parseOptionalCount(field.Value.Value)
		}
	}
	return status
}

func safeStatusText(value string) string {
	if len(value) > 256 || !utf8.ValidString(value) {
		return ""
	}
	return value
}

func parseOptionalCount(value string) domain.OptionalCount {
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 0 {
		return domain.OptionalCount{}
	}
	return domain.Count(int32(parsed))
}

func boundedResourceField(value string) (string, bool) {
	if len(value) <= maxBroadResourceFieldBytes {
		return value, false
	}
	end := maxBroadResourceFieldBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func resourceGVR(resourceType domain.ResourceType) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: resourceType.Group, Version: resourceType.Version, Resource: resourceType.Resource}
}

func metadataOnlyResource(resourceType domain.ResourceType) bool {
	return resourceType.Group == "" && (resourceType.Resource == "secrets" || resourceType.Resource == "configmaps")
}

func resourceSelectors(policy domain.ResourcePolicy, filters []domain.ResourceFilter) (string, string, error) {
	labelRequirements := make([]labels.Requirement, 0, len(filters))
	fieldSelectors := make([]fields.Selector, 0, len(filters))
	for _, filter := range filters {
		field, found := policy.Field(filter.Field)
		if !found || field.DataClass == domain.ResourceDataSensitive {
			return "", "", domain.ErrResourcePolicyDenied
		}
		switch field.SelectorSource {
		case domain.ResourceSelectorNone:
			continue
		case domain.ResourceSelectorField:
			switch filter.Operator {
			case domain.ResourceFilterEquals:
				fieldSelectors = append(fieldSelectors, fields.OneTermEqualSelector(field.SelectorKey, filter.Value))
			case domain.ResourceFilterNotEquals:
				fieldSelectors = append(fieldSelectors, fields.OneTermNotEqualSelector(field.SelectorKey, filter.Value))
			default:
				return "", "", domain.ErrResourcePolicyDenied
			}
		case domain.ResourceSelectorLabel:
			operator := selection.Equals
			values := []string{filter.Value}
			switch filter.Operator {
			case domain.ResourceFilterEquals:
			case domain.ResourceFilterNotEquals:
				operator = selection.NotEquals
			case domain.ResourceFilterExists:
				operator = selection.Exists
				values = nil
			default:
				return "", "", domain.ErrResourcePolicyDenied
			}
			requirement, err := labels.NewRequirement(field.SelectorKey, operator, values)
			if err != nil {
				return "", "", domain.ErrResourcePolicyDenied
			}
			labelRequirements = append(labelRequirements, *requirement)
		default:
			return "", "", domain.ErrResourcePolicyDenied
		}
	}
	labelSelector := ""
	if len(labelRequirements) != 0 {
		labelSelector = labels.NewSelector().Add(labelRequirements...).String()
	}
	fieldSelector := ""
	if len(fieldSelectors) != 0 {
		fieldSelector = fields.AndSelectors(fieldSelectors...).String()
	}
	return labelSelector, fieldSelector, nil
}

func resourceMatchesFilters(observation toolcontract.ResourceObservation, filters []domain.ResourceFilter) bool {
	for _, filter := range filters {
		var projected *toolcontract.ResourceFieldObservation
		for index := range observation.Fields {
			if observation.Fields[index].Field == filter.Field {
				projected = &observation.Fields[index]
				break
			}
		}
		if projected == nil || !resourceFieldMatches(*projected, filter) {
			return false
		}
	}
	return true
}

func resourceFieldMatches(field toolcontract.ResourceFieldObservation, filter domain.ResourceFilter) bool {
	if filter.Operator == domain.ResourceFilterExists {
		return field.Present
	}
	if !field.Present {
		return false
	}
	left, right := field.Value.Value, filter.Value
	switch filter.Operator {
	case domain.ResourceFilterEquals:
		comparison, valid := compareResourceScalar(field.Scalar, left, right)
		return valid && comparison == 0
	case domain.ResourceFilterNotEquals:
		comparison, valid := compareResourceScalar(field.Scalar, left, right)
		return valid && comparison != 0
	case domain.ResourceFilterContains:
		return strings.Contains(left, right)
	case domain.ResourceFilterStartsWith:
		return strings.HasPrefix(left, right)
	case domain.ResourceFilterGreaterThan, domain.ResourceFilterLessThan:
		comparison, valid := compareResourceScalar(field.Scalar, left, right)
		if !valid {
			return false
		}
		if filter.Operator == domain.ResourceFilterGreaterThan {
			return comparison > 0
		}
		return comparison < 0
	default:
		return false
	}
}

func compareResourceScalar(kind domain.ResourceScalarType, left, right string) (int, bool) {
	switch kind {
	case domain.ResourceScalarInteger:
		leftValue, leftErr := strconv.ParseInt(left, 10, 64)
		rightValue, rightErr := strconv.ParseInt(right, 10, 64)
		if leftErr != nil || rightErr != nil {
			return 0, false
		}
		if leftValue < rightValue {
			return -1, true
		}
		if leftValue > rightValue {
			return 1, true
		}
		return 0, true
	case domain.ResourceScalarDecimal:
		leftValue, leftErr := strconv.ParseFloat(left, 64)
		rightValue, rightErr := strconv.ParseFloat(right, 64)
		if leftErr != nil || rightErr != nil {
			return 0, false
		}
		if leftValue < rightValue {
			return -1, true
		}
		if leftValue > rightValue {
			return 1, true
		}
		return 0, true
	case domain.ResourceScalarTimestamp:
		leftValue, leftErr := time.Parse(time.RFC3339Nano, left)
		rightValue, rightErr := time.Parse(time.RFC3339Nano, right)
		if leftErr != nil || rightErr != nil {
			return 0, false
		}
		if leftValue.Before(rightValue) {
			return -1, true
		}
		if leftValue.After(rightValue) {
			return 1, true
		}
		return 0, true
	case domain.ResourceScalarBoolean:
		leftValue, leftErr := strconv.ParseBool(left)
		rightValue, rightErr := strconv.ParseBool(right)
		if leftErr != nil || rightErr != nil {
			return 0, false
		}
		if leftValue == rightValue {
			return 0, true
		}
		if !leftValue {
			return -1, true
		}
		return 1, true
	default:
		return strings.Compare(left, right), true
	}
}

var _ toolcontract.ResourceQueryReader = (*ToolResourceReader)(nil)
