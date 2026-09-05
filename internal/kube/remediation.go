package kube

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// Remediator implements only the fixed typed P0 mutation catalog. It exposes
// no generic patch, update, delete, eviction, or request-builder surface.
type Remediator struct {
	binding *ToolScopeBinding
	now     func() time.Time
}

var (
	_ application.RemediationProposalPreparer = (*Remediator)(nil)
	_ application.RemediationActionExecutor   = (*Remediator)(nil)
	_ application.LocalNamespaceResolver      = (*Remediator)(nil)
)

func NewRemediator(binding *ToolScopeBinding, now func() time.Time) (*Remediator, error) {
	if binding == nil || binding.gateway == nil || now == nil {
		return nil, remediationError(ClassConfigurationInvalid, "remediation_dependencies_invalid", "create_remediator", "The typed remediation adapter dependencies are invalid.")
	}
	return &Remediator{binding: binding, now: now}, nil
}

// ResolveLocalExecutionNamespace binds a local process action to the exact
// frozen Kubernetes scope without giving the process any Kubernetes
// credential or client handle.
func (remediator *Remediator) ResolveLocalExecutionNamespace(ctx context.Context, scope domain.ClusterScope) (domain.ResourceRef, error) {
	if remediator == nil || ctx == nil || scope.Validate() != nil {
		return domain.ResourceRef{}, remediationInputError("resolve_local_execution_namespace")
	}
	bundle, client, err := remediator.capture(scope, "resolve_local_execution_namespace")
	if err != nil {
		return domain.ResourceRef{}, err
	}
	object, rawErr := bundle.typed.CoreV1().Namespaces().Get(ctx, scope.Namespace, metav1.GetOptions{})
	if ctx.Err() != nil {
		return domain.ResourceRef{}, classifyContextError(ctx.Err(), "resolve_local_execution_namespace")
	}
	if rawErr != nil {
		return domain.ResourceRef{}, classifyKubernetesError("resolve_local_execution_namespace", rawErr)
	}
	if object == nil || object.Name != scope.Namespace || object.UID == "" || object.ResourceVersion == "" {
		return domain.ResourceRef{}, remediationProjectionError("resolve_local_execution_namespace")
	}
	result := remediationReference("v1", "Namespace", "", object.Name, object.UID, object.ResourceVersion)
	if result.Validate() != nil || !remediator.bindingCurrent(client, scope) {
		return domain.ResourceRef{}, remediationStaleError("resolve_local_execution_namespace")
	}
	return result, nil
}

func (remediator *Remediator) PrepareRemediationAction(
	ctx context.Context,
	request application.RemediationProposalRequest,
) (domain.RemediationActionPlan, error) {
	if remediator == nil || ctx == nil || request.Validate() != nil {
		return domain.RemediationActionPlan{}, remediationInputError("prepare_remediation")
	}
	if ctx.Err() != nil {
		return domain.RemediationActionPlan{}, classifyContextError(ctx.Err(), "prepare_remediation")
	}
	preparedAt := remediator.now().UTC().Truncate(time.Millisecond)
	if preparedAt.IsZero() || preparedAt.UnixMilli() < 0 || preparedAt.Before(request.Scope.ActivatedAt) {
		return domain.RemediationActionPlan{}, remediationError(ClassInternal, "remediation_clock_invalid", "prepare_remediation", "The typed remediation time could not be generated safely.")
	}
	plan, err := remediator.prepareAt(ctx, request, preparedAt)
	if err != nil {
		return domain.RemediationActionPlan{}, err
	}
	if plan.Validate() != nil {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_remediation")
	}
	return plan, nil
}

func (remediator *Remediator) prepareAt(
	ctx context.Context,
	request application.RemediationProposalRequest,
	preparedAt time.Time,
) (domain.RemediationActionPlan, error) {
	bundle, client, err := remediator.capture(request.Scope, "prepare_remediation")
	if err != nil {
		return domain.RemediationActionPlan{}, err
	}
	var plan domain.RemediationActionPlan
	switch request.Operation {
	case domain.ActionOperationScaleWorkload:
		plan, err = prepareScale(ctx, bundle, request, preparedAt)
	case domain.ActionOperationRollbackDeployment:
		plan, err = prepareRollback(ctx, bundle, request, preparedAt)
	case domain.ActionOperationDeleteOwnedPod:
		plan, err = preparePodDelete(ctx, bundle, request, preparedAt)
	case domain.ActionOperationCordonNode, domain.ActionOperationUncordonNode:
		plan, err = prepareNodeScheduling(ctx, bundle, request, preparedAt)
	case domain.ActionOperationDrainNode:
		plan, err = prepareDrain(ctx, bundle, request, preparedAt)
	default:
		err = remediationInputError("prepare_remediation")
	}
	if ctx.Err() != nil {
		return domain.RemediationActionPlan{}, classifyContextError(ctx.Err(), "prepare_remediation")
	}
	if err != nil {
		return domain.RemediationActionPlan{}, err
	}
	if !remediator.bindingCurrent(client, request.Scope) {
		return domain.RemediationActionPlan{}, remediationStaleError("prepare_remediation")
	}
	return plan, nil
}

func prepareScale(ctx context.Context, bundle *ClientBundle, request application.RemediationProposalRequest, preparedAt time.Time) (domain.RemediationActionPlan, error) {
	var reference domain.ResourceRef
	var generation, current int64
	var fingerprint string
	switch request.Target.Kind {
	case "Deployment":
		object, err := bundle.typed.AppsV1().Deployments(request.Target.Namespace).Get(ctx, request.Target.Name, metav1.GetOptions{})
		if err != nil {
			return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_scale", err)
		}
		if object == nil || object.Name != request.Target.Name || object.Namespace != request.Target.Namespace ||
			object.UID == "" || object.ResourceVersion == "" || object.Generation < 1 || object.DeletionTimestamp != nil {
			return domain.RemediationActionPlan{}, remediationProjectionError("prepare_scale")
		}
		fingerprint, err = deploymentTemplateFingerprint(object.Spec.Template)
		if err != nil {
			return domain.RemediationActionPlan{}, remediationProjectionError("prepare_scale")
		}
		current = 1
		if object.Spec.Replicas != nil {
			current = int64(*object.Spec.Replicas)
		}
		reference = remediationReference("apps/v1", "Deployment", object.Namespace, object.Name, object.UID, object.ResourceVersion)
		generation = object.Generation
	case "StatefulSet":
		object, err := bundle.typed.AppsV1().StatefulSets(request.Target.Namespace).Get(ctx, request.Target.Name, metav1.GetOptions{})
		if err != nil {
			return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_scale", err)
		}
		if object == nil || object.Name != request.Target.Name || object.Namespace != request.Target.Namespace ||
			object.UID == "" || object.ResourceVersion == "" || object.Generation < 1 || object.DeletionTimestamp != nil {
			return domain.RemediationActionPlan{}, remediationProjectionError("prepare_scale")
		}
		fingerprint, err = deploymentTemplateFingerprint(object.Spec.Template)
		if err != nil {
			return domain.RemediationActionPlan{}, remediationProjectionError("prepare_scale")
		}
		current = 1
		if object.Spec.Replicas != nil {
			current = int64(*object.Spec.Replicas)
		}
		reference = remediationReference("apps/v1", "StatefulSet", object.Namespace, object.Name, object.UID, object.ResourceVersion)
		generation = object.Generation
	default:
		return domain.RemediationActionPlan{}, remediationInputError("prepare_scale")
	}
	if current < 0 || request.ReplicaTarget == current {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_scale_no_semantic_change", "prepare_scale", "The requested scale operation has no admitted semantic change.")
	}
	return remediationPlan(request, domain.ActionTarget{Resource: reference, Subresource: "scale", Fingerprint: fingerprint, Generation: generation}, domain.ActionParameters{
		Kind: domain.ActionParametersReplicaTarget, ReplicaCurrent: current, ReplicaTarget: request.ReplicaTarget,
	}, domain.RemediationTargetSet{}, 1, preparedAt), nil
}

func prepareRollback(ctx context.Context, bundle *ClientBundle, request application.RemediationProposalRequest, preparedAt time.Time) (domain.RemediationActionPlan, error) {
	deployment, err := bundle.typed.AppsV1().Deployments(request.Target.Namespace).Get(ctx, request.Target.Name, metav1.GetOptions{})
	if err != nil {
		return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_rollback", err)
	}
	if deployment == nil || deployment.Name != request.Target.Name || deployment.Namespace != request.Target.Namespace ||
		deployment.UID == "" || deployment.ResourceVersion == "" || deployment.Generation < 1 || deployment.DeletionTimestamp != nil {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_rollback")
	}
	if deployment.Spec.Paused {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_rollback_paused", "prepare_rollback", "A paused Deployment cannot be rolled back safely.")
	}
	currentRevision, ok := deploymentRevision(deployment.Annotations)
	if !ok || request.Revision >= currentRevision {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_rollback_revision_invalid", "prepare_rollback", "The requested Deployment rollback revision is not an admitted prior revision.")
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil || selector.Empty() {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_rollback")
	}
	replicaSets, err := bundle.typed.AppsV1().ReplicaSets(request.Target.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String(), Limit: domain.MaxRemediationPlanMembers + 1})
	if err != nil {
		return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_rollback", err)
	}
	if replicaSets == nil || replicaSets.Continue != "" || len(replicaSets.Items) > domain.MaxRemediationPlanMembers {
		return domain.RemediationActionPlan{}, remediationError(ClassBudgetExhausted, "remediation_rollback_set_incomplete", "prepare_rollback", "The bounded ReplicaSet revision set is incomplete.")
	}
	var source *appsv1.ReplicaSet
	for index := range replicaSets.Items {
		candidate := &replicaSets.Items[index]
		if !controlledBy(candidate.OwnerReferences, "Deployment", deployment.Name, string(deployment.UID)) {
			continue
		}
		revision, revisionOK := deploymentRevision(candidate.Annotations)
		if revisionOK && revision == request.Revision {
			if source != nil {
				return domain.RemediationActionPlan{}, remediationProjectionError("prepare_rollback")
			}
			source = candidate
		}
	}
	if source == nil || source.Name == "" || source.Namespace != request.Target.Namespace || source.UID == "" ||
		source.ResourceVersion == "" || source.DeletionTimestamp != nil {
		return domain.RemediationActionPlan{}, remediationError(ClassNotFound, "remediation_rollback_revision_not_found", "prepare_rollback", "The exact prior ReplicaSet revision is unavailable.")
	}
	currentFingerprint, err := rollbackTemplateFingerprint(deployment.Spec.Template)
	if err != nil {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_rollback")
	}
	sourceFingerprint, err := rollbackTemplateFingerprint(source.Spec.Template)
	if err != nil || sourceFingerprint == currentFingerprint {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_rollback_template_unchanged", "prepare_rollback", "The requested prior revision does not produce an admitted template change.")
	}
	memberSet, err := domain.NewRemediationTargetSet([]domain.RemediationPlanMember{{
		Role:        domain.RemediationMemberRollbackSource,
		Resource:    remediationReference("apps/v1", "ReplicaSet", source.Namespace, source.Name, source.UID, source.ResourceVersion),
		Fingerprint: domain.ActionDigest(sourceFingerprint), Revision: request.Revision,
	}})
	if err != nil {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_rollback")
	}
	target := domain.ActionTarget{
		Resource:    remediationReference("apps/v1", "Deployment", deployment.Namespace, deployment.Name, deployment.UID, deployment.ResourceVersion),
		Fingerprint: currentFingerprint, Generation: deployment.Generation, Revision: currentRevision,
		TargetSetDigest: memberSet.Digest(), TargetCount: 1,
	}
	return remediationPlan(request, target, domain.ActionParameters{Kind: domain.ActionParametersRevision, Revision: request.Revision}, memberSet, 1, preparedAt), nil
}

func preparePodDelete(ctx context.Context, bundle *ClientBundle, request application.RemediationProposalRequest, preparedAt time.Time) (domain.RemediationActionPlan, error) {
	pod, err := bundle.typed.CoreV1().Pods(request.Target.Namespace).Get(ctx, request.Target.Name, metav1.GetOptions{})
	if err != nil {
		return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_owned_pod_delete", err)
	}
	if pod == nil || pod.Name != request.Target.Name || pod.Namespace != request.Target.Namespace || pod.UID == "" ||
		pod.ResourceVersion == "" || pod.DeletionTimestamp != nil || isStaticOrMirrorPod(pod) {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_pod_not_ordinary", "prepare_owned_pod_delete", "The Pod is not an admitted ordinary controller-owned target.")
	}
	owner, ok := soleController(pod.OwnerReferences)
	if !ok {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_pod_owner_ambiguous", "prepare_owned_pod_delete", "The Pod does not have one exact admitted controller owner.")
	}
	controller, err := resolveController(ctx, bundle, pod.Namespace, owner)
	if err != nil {
		return domain.RemediationActionPlan{}, err
	}
	fingerprint, err := podIdentityFingerprint(pod)
	if err != nil {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_owned_pod_delete")
	}
	set, err := domain.NewRemediationTargetSet([]domain.RemediationPlanMember{{Role: domain.RemediationMemberPodController, Resource: controller}})
	if err != nil {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_owned_pod_delete")
	}
	target := domain.ActionTarget{
		Resource:    remediationReference("v1", "Pod", pod.Namespace, pod.Name, pod.UID, pod.ResourceVersion),
		Fingerprint: fingerprint, TargetSetDigest: set.Digest(), TargetCount: 1,
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersPodDelete, GracePeriodSeconds: domain.RemediationGracePeriodSeconds}
	return remediationPlan(request, target, parameters, set, 1, preparedAt), nil
}

func prepareNodeScheduling(ctx context.Context, bundle *ClientBundle, request application.RemediationProposalRequest, preparedAt time.Time) (domain.RemediationActionPlan, error) {
	node, err := bundle.typed.CoreV1().Nodes().Get(ctx, request.Target.Name, metav1.GetOptions{})
	if err != nil {
		return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_node_scheduling", err)
	}
	want := request.Operation == domain.ActionOperationCordonNode
	if node == nil || node.Name != request.Target.Name || node.UID == "" || node.ResourceVersion == "" ||
		node.DeletionTimestamp != nil || node.Spec.Unschedulable == want {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_node_no_semantic_change", "prepare_node_scheduling", "The requested Node scheduling operation has no admitted semantic change.")
	}
	target := domain.ActionTarget{Resource: remediationReference("v1", "Node", "", node.Name, node.UID, node.ResourceVersion)}
	return remediationPlan(request, target, domain.ActionParameters{Kind: domain.ActionParametersNodeScheduling, Unschedulable: want}, domain.RemediationTargetSet{}, 1, preparedAt), nil
}

func prepareDrain(ctx context.Context, bundle *ClientBundle, request application.RemediationProposalRequest, preparedAt time.Time) (domain.RemediationActionPlan, error) {
	if request.Scope.NamespaceAccess != domain.NamespaceAccessAll {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_drain_requires_all_namespaces", "prepare_drain", "Drain requires the explicit all-Namespace scope policy.")
	}
	node, err := bundle.typed.CoreV1().Nodes().Get(ctx, request.Target.Name, metav1.GetOptions{})
	if err != nil {
		return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_drain", err)
	}
	if node == nil || node.Name != request.Target.Name || node.UID == "" || node.ResourceVersion == "" ||
		node.DeletionTimestamp != nil || node.Spec.Unschedulable {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_drain_node_ineligible", "prepare_drain", "The Node is not an admitted schedulable drain target.")
	}
	pods, err := bundle.typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name, Limit: domain.MaxRemediationPlanMembers + 1})
	if err != nil {
		return domain.RemediationActionPlan{}, classifyKubernetesError("prepare_drain", err)
	}
	if pods == nil || pods.Continue != "" || len(pods.Items) > domain.MaxRemediationPlanMembers {
		return domain.RemediationActionPlan{}, remediationError(ClassBudgetExhausted, "remediation_drain_pod_set_incomplete", "prepare_drain", "The bounded drain Pod set is incomplete.")
	}
	sort.Slice(pods.Items, func(left, right int) bool {
		if pods.Items[left].Namespace != pods.Items[right].Namespace {
			return pods.Items[left].Namespace < pods.Items[right].Namespace
		}
		if pods.Items[left].Name != pods.Items[right].Name {
			return pods.Items[left].Name < pods.Items[right].Name
		}
		return string(pods.Items[left].UID) < string(pods.Items[right].UID)
	})
	members := make([]domain.RemediationPlanMember, 0, len(pods.Items)+8)
	eligible := make([]corev1.Pod, 0, len(pods.Items))
	for index := range pods.Items {
		pod := &pods.Items[index]
		if pod.Spec.NodeName != node.Name || pod.UID == "" || pod.ResourceVersion == "" || pod.DeletionTimestamp != nil ||
			isStaticOrMirrorPod(pod) || podHasLocalData(pod) {
			return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_drain_pod_ineligible", "prepare_drain", "The complete drain set contains an ineligible static, deleting, or local-data Pod.")
		}
		owner, controlled := soleController(pod.OwnerReferences)
		if !controlled || owner.Kind == "DaemonSet" {
			return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_drain_controller_ineligible", "prepare_drain", "The complete drain set contains an unmanaged, ambiguous, or DaemonSet Pod.")
		}
		fingerprint, fingerprintErr := podIdentityFingerprint(pod)
		if fingerprintErr != nil {
			return domain.RemediationActionPlan{}, remediationProjectionError("prepare_drain")
		}
		eligible = append(eligible, *pod.DeepCopy())
		members = append(members, domain.RemediationPlanMember{
			Role:        domain.RemediationMemberDrainPod,
			Resource:    remediationReference("v1", "Pod", pod.Namespace, pod.Name, pod.UID, pod.ResourceVersion),
			Fingerprint: domain.ActionDigest(fingerprint), Order: len(eligible),
		})
	}
	if len(eligible) == 0 {
		return domain.RemediationActionPlan{}, remediationError(ClassPolicyDenied, "remediation_drain_empty", "prepare_drain", "The Node has no admitted Pod set to drain; use the separate cordon action if needed.")
	}
	pdbMembers, err := matchingPDBMembers(ctx, bundle, eligible)
	if err != nil {
		return domain.RemediationActionPlan{}, err
	}
	members = append(members, pdbMembers...)
	set, err := domain.NewRemediationTargetSet(members)
	if err != nil {
		return domain.RemediationActionPlan{}, remediationProjectionError("prepare_drain")
	}
	target := domain.ActionTarget{
		Resource:        remediationReference("v1", "Node", "", node.Name, node.UID, node.ResourceVersion),
		TargetSetDigest: set.Digest(), TargetCount: len(members),
	}
	parameters := domain.ActionParameters{
		Kind: domain.ActionParametersDrainPlan, PlanDigest: set.Digest(), PlanTargetCount: len(members),
		GracePeriodSeconds: domain.RemediationGracePeriodSeconds,
	}
	return remediationPlan(request, target, parameters, set, len(eligible)+1, preparedAt), nil
}

func matchingPDBMembers(ctx context.Context, bundle *ClientBundle, pods []corev1.Pod) ([]domain.RemediationPlanMember, error) {
	namespaces := make(map[string]struct{})
	for _, pod := range pods {
		namespaces[pod.Namespace] = struct{}{}
	}
	orderedNamespaces := make([]string, 0, len(namespaces))
	for namespace := range namespaces {
		orderedNamespaces = append(orderedNamespaces, namespace)
	}
	sort.Strings(orderedNamespaces)
	pdbs := make([]policyv1.PodDisruptionBudget, 0, 8)
	for _, namespace := range orderedNamespaces {
		list, err := bundle.typed.PolicyV1().PodDisruptionBudgets(namespace).List(ctx, metav1.ListOptions{Limit: domain.MaxRemediationPlanMembers + 1})
		if err != nil {
			return nil, classifyKubernetesError("prepare_drain_pdb", err)
		}
		if list == nil || list.Continue != "" || len(list.Items) > domain.MaxRemediationPlanMembers {
			return nil, remediationError(ClassBudgetExhausted, "remediation_drain_pdb_set_incomplete", "prepare_drain_pdb", "The bounded PodDisruptionBudget set is incomplete.")
		}
		pdbs = append(pdbs, list.Items...)
	}
	sort.Slice(pdbs, func(left, right int) bool {
		if pdbs[left].Namespace != pdbs[right].Namespace {
			return pdbs[left].Namespace < pdbs[right].Namespace
		}
		return pdbs[left].Name < pdbs[right].Name
	})
	matchedCounts := make([]int32, len(pdbs))
	for _, pod := range pods {
		matches := -1
		for index := range pdbs {
			pdb := &pdbs[index]
			if pdb.Namespace != pod.Namespace || pdb.Spec.Selector == nil {
				continue
			}
			selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil || selector.Empty() {
				return nil, remediationProjectionError("prepare_drain_pdb")
			}
			if selector.Matches(labels.Set(pod.Labels)) {
				if matches >= 0 {
					return nil, remediationError(ClassPolicyDenied, "remediation_drain_multiple_pdbs", "prepare_drain_pdb", "A drain Pod matches more than one PodDisruptionBudget.")
				}
				matches = index
			}
		}
		if matches >= 0 {
			matchedCounts[matches]++
		}
	}
	result := make([]domain.RemediationPlanMember, 0, len(pdbs))
	for index := range pdbs {
		if matchedCounts[index] == 0 {
			continue
		}
		pdb := &pdbs[index]
		if pdb.UID == "" || pdb.ResourceVersion == "" || pdb.DeletionTimestamp != nil ||
			pdb.Status.DisruptionsAllowed < matchedCounts[index] {
			return nil, remediationError(ClassPolicyDenied, "remediation_drain_pdb_blocked", "prepare_drain_pdb", "A matching PodDisruptionBudget does not admit the complete drain plan.")
		}
		result = append(result, domain.RemediationPlanMember{
			Role:               domain.RemediationMemberDrainPDB,
			Resource:           remediationReference("policy/v1", "PodDisruptionBudget", pdb.Namespace, pdb.Name, pdb.UID, pdb.ResourceVersion),
			DisruptionsAllowed: pdb.Status.DisruptionsAllowed,
		})
	}
	return result, nil
}

func remediationPlan(request application.RemediationProposalRequest, target domain.ActionTarget, parameters domain.ActionParameters, targetSet domain.RemediationTargetSet, maximumItems int, preparedAt time.Time) domain.RemediationActionPlan {
	return domain.RemediationActionPlan{
		RunID: request.RunID, SessionID: request.SessionID, Scope: request.Scope, PolicyGeneration: request.PolicyGeneration,
		Operation: request.Operation, Target: target, Parameters: parameters, TargetSet: targetSet,
		ReasonSummary: request.ReasonSummary,
		Limits:        domain.ActionLimits{Timeout: domain.TypedRemediationActionTimeout, MaximumItems: maximumItems}, PreparedAt: preparedAt,
	}
}

func (remediator *Remediator) RevalidateRemediationAction(ctx context.Context, plan domain.RemediationActionPlan) error {
	if remediator == nil || ctx == nil || plan.Validate() != nil {
		return remediationInputError("revalidate_remediation")
	}
	if ctx.Err() != nil {
		return classifyContextError(ctx.Err(), "revalidate_remediation")
	}
	bundle, client, err := remediator.capture(plan.Scope, "revalidate_remediation")
	if err != nil {
		return err
	}
	if err := authorizeRemediation(ctx, bundle, plan); err != nil {
		return err
	}
	if !remediator.bindingCurrent(client, plan.Scope) {
		return remediationStaleError("revalidate_remediation")
	}
	fresh, err := remediator.prepareAt(ctx, proposalFromPlan(plan), plan.PreparedAt)
	if err != nil {
		return err
	}
	if fresh != plan {
		return remediationTargetChangedError("revalidate_remediation")
	}
	return nil
}

func proposalFromPlan(plan domain.RemediationActionPlan) application.RemediationProposalRequest {
	target := plan.Target.Resource
	target.UID, target.ResourceVersion = "", ""
	return application.RemediationProposalRequest{
		RunID: plan.RunID, SessionID: plan.SessionID, Scope: plan.Scope, PolicyGeneration: plan.PolicyGeneration,
		Operation: plan.Operation, Target: target, ReplicaTarget: plan.Parameters.ReplicaTarget,
		Revision: plan.Parameters.Revision, ReasonSummary: plan.ReasonSummary,
	}
}

func authorizeRemediation(ctx context.Context, bundle *ClientBundle, plan domain.RemediationActionPlan) error {
	attributes := make([]authorizationv1.ResourceAttributes, 0, 2)
	namespace := plan.Target.Resource.Namespace
	switch plan.Operation {
	case domain.ActionOperationScaleWorkload:
		resource := "deployments"
		if plan.Target.Resource.Kind == "StatefulSet" {
			resource = "statefulsets"
		}
		attributes = append(attributes, authorizationv1.ResourceAttributes{Group: "apps", Version: "v1", Resource: resource, Subresource: "scale", Verb: "update", Namespace: namespace, Name: plan.Target.Resource.Name})
	case domain.ActionOperationRollbackDeployment:
		attributes = append(attributes, authorizationv1.ResourceAttributes{Group: "apps", Version: "v1", Resource: "deployments", Verb: "update", Namespace: namespace, Name: plan.Target.Resource.Name})
	case domain.ActionOperationDeleteOwnedPod:
		attributes = append(attributes, authorizationv1.ResourceAttributes{Version: "v1", Resource: "pods", Verb: "delete", Namespace: namespace, Name: plan.Target.Resource.Name})
	case domain.ActionOperationCordonNode, domain.ActionOperationUncordonNode:
		attributes = append(attributes, authorizationv1.ResourceAttributes{Version: "v1", Resource: "nodes", Verb: "patch", Name: plan.Target.Resource.Name})
	case domain.ActionOperationDrainNode:
		attributes = append(attributes, authorizationv1.ResourceAttributes{Version: "v1", Resource: "nodes", Verb: "patch", Name: plan.Target.Resource.Name})
		namespaces := make(map[string]struct{})
		for _, member := range plan.TargetSet.Members() {
			if member.Role == domain.RemediationMemberDrainPod {
				namespaces[member.Resource.Namespace] = struct{}{}
			}
		}
		ordered := make([]string, 0, len(namespaces))
		for value := range namespaces {
			ordered = append(ordered, value)
		}
		sort.Strings(ordered)
		for _, value := range ordered {
			attributes = append(attributes, authorizationv1.ResourceAttributes{Version: "v1", Resource: "pods", Subresource: "eviction", Verb: "create", Namespace: value})
		}
	default:
		return remediationInputError("authorize_remediation")
	}
	for _, attribute := range attributes {
		review, err := bundle.typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attribute}}, metav1.CreateOptions{})
		if err != nil {
			return classifyKubernetesError("authorize_remediation", err)
		}
		if review == nil || !review.Status.Allowed || review.Status.Denied {
			return remediationError(ClassPermissionDenied, "remediation_rbac_denied", "authorize_remediation", "Kubernetes RBAC does not admit the exact typed remediation request.")
		}
	}
	return nil
}

func (remediator *Remediator) ExecuteRemediationAction(ctx context.Context, plan domain.RemediationActionPlan) (domain.RemediationAttempt, error) {
	if remediator == nil || ctx == nil || plan.Validate() != nil {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, remediationInputError("execute_remediation")
	}
	if ctx.Err() != nil {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, classifyContextError(ctx.Err(), "execute_remediation")
	}
	// This final adapter-owned read closes the approval-to-attempt target gap.
	// Exact resource-version/UID preconditions still protect the subsequent
	// request from a race after this read.
	if err := remediator.revalidateTargetWithoutRBAC(ctx, plan); err != nil {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, err
	}
	bundle, client, err := remediator.capture(plan.Scope, "execute_remediation")
	if err != nil {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, err
	}
	if !remediator.bindingCurrent(client, plan.Scope) {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, remediationStaleError("execute_remediation")
	}
	var attempt domain.RemediationAttempt
	switch plan.Operation {
	case domain.ActionOperationScaleWorkload:
		attempt, err = executeScale(ctx, bundle, plan)
	case domain.ActionOperationRollbackDeployment:
		attempt, err = executeRollback(ctx, bundle, plan)
	case domain.ActionOperationDeleteOwnedPod:
		attempt, err = executePodDelete(ctx, bundle, plan)
	case domain.ActionOperationCordonNode, domain.ActionOperationUncordonNode:
		attempt, err = executeNodeScheduling(ctx, bundle, plan)
	case domain.ActionOperationDrainNode:
		attempt, err = executeDrain(ctx, bundle, plan)
	default:
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, remediationInputError("execute_remediation")
	}
	if attempt.Validate(plan.Limits.MaximumItems) != nil {
		return domain.RemediationAttempt{State: domain.RemediationUnknown, AttemptedCount: 1, ErrorClass: ClassInvalidExternalResponse}, remediationProjectionError("execute_remediation")
	}
	return attempt, err
}

func (remediator *Remediator) revalidateTargetWithoutRBAC(ctx context.Context, plan domain.RemediationActionPlan) error {
	fresh, err := remediator.prepareAt(ctx, proposalFromPlan(plan), plan.PreparedAt)
	if err != nil {
		return err
	}
	if fresh != plan {
		return remediationTargetChangedError("execute_remediation")
	}
	return nil
}

func executeScale(ctx context.Context, bundle *ClientBundle, plan domain.RemediationActionPlan) (domain.RemediationAttempt, error) {
	scale := &autoscalingv1.Scale{ObjectMeta: metav1.ObjectMeta{
		Name: plan.Target.Resource.Name, Namespace: plan.Target.Resource.Namespace,
		UID: types.UID(plan.Target.Resource.UID), ResourceVersion: plan.Target.Resource.ResourceVersion,
	}, Spec: autoscalingv1.ScaleSpec{Replicas: int32(plan.Parameters.ReplicaTarget)}}
	var result *autoscalingv1.Scale
	var err error
	if plan.Target.Resource.Kind == "Deployment" {
		result, err = bundle.typed.AppsV1().Deployments(plan.Target.Resource.Namespace).UpdateScale(ctx, plan.Target.Resource.Name, scale, metav1.UpdateOptions{})
	} else {
		result, err = bundle.typed.AppsV1().StatefulSets(plan.Target.Resource.Namespace).UpdateScale(ctx, plan.Target.Resource.Name, scale, metav1.UpdateOptions{})
	}
	if err != nil {
		return failedRemediationAttempt(ctx, err, 1)
	}
	if result == nil || result.Name != plan.Target.Resource.Name || result.Namespace != plan.Target.Resource.Namespace ||
		string(result.UID) != plan.Target.Resource.UID || result.ResourceVersion == "" ||
		result.ResourceVersion == plan.Target.Resource.ResourceVersion || int64(result.Spec.Replicas) != plan.Parameters.ReplicaTarget {
		err := remediationProjectionError("execute_scale")
		return domain.RemediationAttempt{State: domain.RemediationUnknown, AttemptedCount: 1, ErrorClass: ClassInvalidExternalResponse}, err
	}
	return domain.RemediationAttempt{State: domain.RemediationAccepted, AcceptedCount: 1, AttemptedCount: 1}, nil
}

func executeRollback(ctx context.Context, bundle *ClientBundle, plan domain.RemediationActionPlan) (domain.RemediationAttempt, error) {
	member := plan.TargetSet.Members()[0]
	source, err := bundle.typed.AppsV1().ReplicaSets(member.Resource.Namespace).Get(ctx, member.Resource.Name, metav1.GetOptions{})
	if err != nil || source == nil {
		if err == nil {
			err = remediationProjectionError("execute_rollback")
		}
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, classifyKubernetesError("execute_rollback", err)
	}
	normalizedTemplate := rollbackDeploymentTemplate(source.Spec.Template)
	fingerprint, fingerprintErr := deploymentTemplateFingerprint(normalizedTemplate)
	if fingerprintErr != nil || string(source.UID) != member.Resource.UID || source.ResourceVersion != member.Resource.ResourceVersion || fingerprint != string(member.Fingerprint) {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, remediationTargetChangedError("execute_rollback")
	}
	deployment, err := bundle.typed.AppsV1().Deployments(plan.Target.Resource.Namespace).Get(ctx, plan.Target.Resource.Name, metav1.GetOptions{})
	if err != nil {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, classifyKubernetesError("execute_rollback", err)
	}
	if deployment == nil || deployment.Name != plan.Target.Resource.Name || deployment.Namespace != plan.Target.Resource.Namespace ||
		deployment.DeletionTimestamp != nil || string(deployment.UID) != plan.Target.Resource.UID ||
		deployment.ResourceVersion != plan.Target.Resource.ResourceVersion {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, remediationTargetChangedError("execute_rollback")
	}
	updated := deployment.DeepCopy()
	updated.Spec.Template = normalizedTemplate
	result, err := bundle.typed.AppsV1().Deployments(updated.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return failedRemediationAttempt(ctx, err, 1)
	}
	if result == nil || result.Name != plan.Target.Resource.Name || result.Namespace != plan.Target.Resource.Namespace ||
		string(result.UID) != plan.Target.Resource.UID || result.ResourceVersion == "" ||
		result.ResourceVersion == plan.Target.Resource.ResourceVersion {
		err := remediationProjectionError("execute_rollback")
		return domain.RemediationAttempt{State: domain.RemediationUnknown, AttemptedCount: 1, ErrorClass: ClassInvalidExternalResponse}, err
	}
	return domain.RemediationAttempt{State: domain.RemediationAccepted, AcceptedCount: 1, AttemptedCount: 1}, nil
}

func executePodDelete(ctx context.Context, bundle *ClientBundle, plan domain.RemediationActionPlan) (domain.RemediationAttempt, error) {
	uid := types.UID(plan.Target.Resource.UID)
	resourceVersion := plan.Target.Resource.ResourceVersion
	grace := plan.Parameters.GracePeriodSeconds
	err := bundle.typed.CoreV1().Pods(plan.Target.Resource.Namespace).Delete(ctx, plan.Target.Resource.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &resourceVersion},
	})
	if err != nil {
		return failedRemediationAttempt(ctx, err, 1)
	}
	return domain.RemediationAttempt{State: domain.RemediationAccepted, AcceptedCount: 1, AttemptedCount: 1}, nil
}

func executeNodeScheduling(ctx context.Context, bundle *ClientBundle, plan domain.RemediationActionPlan) (domain.RemediationAttempt, error) {
	var body struct {
		Metadata struct {
			UID             string `json:"uid"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
	}
	body.Metadata.UID = plan.Target.Resource.UID
	body.Metadata.ResourceVersion = plan.Target.Resource.ResourceVersion
	body.Spec.Unschedulable = plan.Parameters.Unschedulable
	patch, err := json.Marshal(body)
	if err != nil {
		return domain.RemediationAttempt{State: domain.RemediationNotAttempted}, remediationProjectionError("execute_node_scheduling")
	}
	result, err := bundle.typed.CoreV1().Nodes().Patch(ctx, plan.Target.Resource.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return failedRemediationAttempt(ctx, err, 1)
	}
	if result == nil || string(result.UID) != plan.Target.Resource.UID || result.ResourceVersion == plan.Target.Resource.ResourceVersion || result.Spec.Unschedulable != plan.Parameters.Unschedulable {
		err := remediationProjectionError("execute_node_scheduling")
		return domain.RemediationAttempt{State: domain.RemediationUnknown, AttemptedCount: 1, ErrorClass: ClassInvalidExternalResponse}, err
	}
	return domain.RemediationAttempt{State: domain.RemediationAccepted, AcceptedCount: 1, AttemptedCount: 1}, nil
}

func executeDrain(ctx context.Context, bundle *ClientBundle, plan domain.RemediationActionPlan) (domain.RemediationAttempt, error) {
	nodePlan := plan
	nodePlan.Operation = domain.ActionOperationCordonNode
	nodePlan.Parameters = domain.ActionParameters{Kind: domain.ActionParametersNodeScheduling, Unschedulable: true}
	attempt, err := executeNodeScheduling(ctx, bundle, nodePlan)
	if err != nil {
		return attempt, err
	}
	attempted, accepted := 1, 1
	if err := validateApprovedDrainPodSet(ctx, bundle, plan); err != nil {
		return domain.RemediationAttempt{
			State: domain.RemediationFailed, AcceptedCount: accepted, AttemptedCount: attempted,
			ErrorClass: remediationErrorClass(err),
		}, err
	}
	for _, member := range plan.TargetSet.Members() {
		if member.Role != domain.RemediationMemberDrainPod {
			continue
		}
		attempted++
		uid := types.UID(member.Resource.UID)
		resourceVersion := member.Resource.ResourceVersion
		grace := plan.Parameters.GracePeriodSeconds
		eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: member.Resource.Name, Namespace: member.Resource.Namespace}, DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &resourceVersion},
		}}
		if err := bundle.typed.CoreV1().Pods(member.Resource.Namespace).EvictV1(ctx, eviction); err != nil {
			failed, classified := failedRemediationAttempt(ctx, err, attempted)
			failed.AcceptedCount = accepted
			return failed, classified
		}
		accepted++
	}
	return domain.RemediationAttempt{State: domain.RemediationAccepted, AcceptedCount: accepted, AttemptedCount: attempted}, nil
}

// validateApprovedDrainPodSet runs after cordon and before the first eviction.
// It prevents a single approval from being extended to a new or changed Pod
// and ensures a stale member stops the whole eviction phase, rather than
// allowing a partial prefix of an outdated plan to execute.
func validateApprovedDrainPodSet(ctx context.Context, bundle *ClientBundle, plan domain.RemediationActionPlan) error {
	pods, err := bundle.typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + plan.Target.Resource.Name,
		Limit:         domain.MaxRemediationPlanMembers + 1,
	})
	if err != nil {
		return classifyKubernetesError("execute_drain_recheck", err)
	}
	if pods == nil || pods.Continue != "" || len(pods.Items) > domain.MaxRemediationPlanMembers {
		return remediationError(ClassBudgetExhausted, "remediation_drain_pod_set_incomplete", "execute_drain_recheck", "The bounded drain Pod set changed or is incomplete after the Node was cordoned.")
	}
	planned := make([]domain.RemediationPlanMember, 0, len(pods.Items))
	for _, member := range plan.TargetSet.Members() {
		if member.Role == domain.RemediationMemberDrainPod {
			planned = append(planned, member)
		}
	}
	sort.Slice(pods.Items, func(left, right int) bool {
		if pods.Items[left].Namespace != pods.Items[right].Namespace {
			return pods.Items[left].Namespace < pods.Items[right].Namespace
		}
		if pods.Items[left].Name != pods.Items[right].Name {
			return pods.Items[left].Name < pods.Items[right].Name
		}
		return string(pods.Items[left].UID) < string(pods.Items[right].UID)
	})
	if len(pods.Items) != len(planned) {
		return remediationTargetChangedError("execute_drain_recheck")
	}
	for index := range pods.Items {
		pod, member := &pods.Items[index], planned[index]
		fingerprint, fingerprintErr := podIdentityFingerprint(pod)
		if fingerprintErr != nil || pod.Spec.NodeName != plan.Target.Resource.Name || pod.DeletionTimestamp != nil ||
			pod.Namespace != member.Resource.Namespace || pod.Name != member.Resource.Name ||
			string(pod.UID) != member.Resource.UID || pod.ResourceVersion != member.Resource.ResourceVersion ||
			fingerprint != string(member.Fingerprint) {
			return remediationTargetChangedError("execute_drain_recheck")
		}
	}
	return nil
}

func remediationErrorClass(err error) domain.SafeErrorClass {
	var classified interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &classified) && classified.Class().Valid() {
		return classified.Class()
	}
	return ClassInternal
}

func failedRemediationAttempt(ctx context.Context, err error, attempted int) (domain.RemediationAttempt, error) {
	classified := classifyKubernetesError("execute_remediation", err)
	class := classified.Class()
	state := domain.RemediationFailed
	if ctx != nil && ctx.Err() != nil || class == ClassCancelled || class == ClassTimeout || class == ClassUnavailable || class == ClassInternal || class == ClassInvalidExternalResponse {
		state = domain.RemediationUnknown
	}
	return domain.RemediationAttempt{State: state, AttemptedCount: attempted, ErrorClass: class}, classified
}

func (remediator *Remediator) VerifyRemediationAction(ctx context.Context, plan domain.RemediationActionPlan) (domain.RemediationVerificationState, error) {
	if remediator == nil || ctx == nil || plan.Validate() != nil {
		return domain.RemediationVerificationUnavailable, remediationInputError("verify_remediation")
	}
	bundle, client, err := remediator.capture(plan.Scope, "verify_remediation")
	if err != nil {
		return domain.RemediationVerificationUnavailable, err
	}
	verified := false
	switch plan.Operation {
	case domain.ActionOperationScaleWorkload:
		var scale *autoscalingv1.Scale
		if plan.Target.Resource.Kind == "Deployment" {
			scale, err = bundle.typed.AppsV1().Deployments(plan.Target.Resource.Namespace).GetScale(ctx, plan.Target.Resource.Name, metav1.GetOptions{})
		} else {
			scale, err = bundle.typed.AppsV1().StatefulSets(plan.Target.Resource.Namespace).GetScale(ctx, plan.Target.Resource.Name, metav1.GetOptions{})
		}
		verified = err == nil && scale != nil && scale.Name == plan.Target.Resource.Name &&
			scale.Namespace == plan.Target.Resource.Namespace && string(scale.UID) == plan.Target.Resource.UID &&
			int64(scale.Spec.Replicas) == plan.Parameters.ReplicaTarget && int64(scale.Status.Replicas) == plan.Parameters.ReplicaTarget
	case domain.ActionOperationRollbackDeployment:
		object, readErr := bundle.typed.AppsV1().Deployments(plan.Target.Resource.Namespace).Get(ctx, plan.Target.Resource.Name, metav1.GetOptions{})
		err = readErr
		if err == nil && object != nil && object.Name == plan.Target.Resource.Name &&
			object.Namespace == plan.Target.Resource.Namespace && string(object.UID) == plan.Target.Resource.UID {
			fingerprint, fingerprintErr := rollbackTemplateFingerprint(object.Spec.Template)
			source := plan.TargetSet.Members()[0]
			desired := int32(1)
			if object.Spec.Replicas != nil {
				desired = *object.Spec.Replicas
			}
			verified = fingerprintErr == nil && desired >= 0 && fingerprint == string(source.Fingerprint) &&
				object.Status.ObservedGeneration >= object.Generation && object.Status.Replicas == desired &&
				object.Status.UpdatedReplicas == desired && object.Status.AvailableReplicas == desired &&
				object.Status.UnavailableReplicas == 0
		}
	case domain.ActionOperationDeleteOwnedPod:
		object, readErr := bundle.typed.CoreV1().Pods(plan.Target.Resource.Namespace).Get(ctx, plan.Target.Resource.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(readErr) {
			verified, err = true, nil
		} else {
			err = readErr
			verified = err == nil && object != nil && string(object.UID) != plan.Target.Resource.UID
		}
	case domain.ActionOperationCordonNode, domain.ActionOperationUncordonNode:
		object, readErr := bundle.typed.CoreV1().Nodes().Get(ctx, plan.Target.Resource.Name, metav1.GetOptions{})
		err = readErr
		verified = err == nil && object != nil && string(object.UID) == plan.Target.Resource.UID && object.Spec.Unschedulable == plan.Parameters.Unschedulable
	case domain.ActionOperationDrainNode:
		object, readErr := bundle.typed.CoreV1().Nodes().Get(ctx, plan.Target.Resource.Name, metav1.GetOptions{})
		err = readErr
		verified = err == nil && object != nil && string(object.UID) == plan.Target.Resource.UID && object.Spec.Unschedulable
		if verified {
			pods, listErr := bundle.typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{
				FieldSelector: "spec.nodeName=" + plan.Target.Resource.Name,
				Limit:         domain.MaxRemediationPlanMembers + 1,
			})
			err = listErr
			if err == nil && (pods == nil || pods.Continue != "" || len(pods.Items) > domain.MaxRemediationPlanMembers) {
				err = remediationError(ClassBudgetExhausted, "remediation_drain_verification_incomplete", "verify_remediation", "The bounded post-drain Pod set is incomplete.")
			}
			verified = err == nil && pods != nil && len(pods.Items) == 0
		}
	default:
		return domain.RemediationVerificationUnavailable, remediationInputError("verify_remediation")
	}
	if ctx.Err() != nil {
		class := domain.RemediationVerificationUnavailable
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = domain.RemediationVerificationTimedOut
		}
		return class, classifyContextError(ctx.Err(), "verify_remediation")
	}
	if err != nil {
		return domain.RemediationVerificationUnavailable, classifyKubernetesError("verify_remediation", err)
	}
	if !remediator.bindingCurrent(client, plan.Scope) {
		return domain.RemediationVerificationUnavailable, remediationStaleError("verify_remediation")
	}
	if !verified {
		return domain.RemediationVerificationFailed, remediationError(ClassConflict, "remediation_verification_failed", "verify_remediation", "The typed remediation request was accepted but its expected state was not verified.")
	}
	return domain.RemediationVerified, nil
}

func (remediator *Remediator) capture(scope domain.ClusterScope, operation string) (*ClientBundle, application.ScopeClient, error) {
	if remediator == nil || remediator.binding == nil || remediator.binding.gateway == nil || scope.Validate() != nil {
		return nil, nil, remediationInputError(operation)
	}
	remediator.binding.mu.Lock()
	defer remediator.binding.mu.Unlock()
	client := remediator.binding.client
	if client == nil || remediator.binding.generation != scope.Generation || client.Context().Name != scope.Context {
		return nil, nil, remediationStaleError(operation)
	}
	bundle, err := remediator.binding.gateway.clientBundle(client, operation)
	if err != nil {
		return nil, nil, err
	}
	return bundle, client, nil
}

func (remediator *Remediator) bindingCurrent(client application.ScopeClient, scope domain.ClusterScope) bool {
	if remediator == nil || remediator.binding == nil || client == nil {
		return false
	}
	remediator.binding.mu.Lock()
	defer remediator.binding.mu.Unlock()
	return remediator.binding.client == client && remediator.binding.generation == scope.Generation && client.Context().Name == scope.Context
}

func remediationReference(apiVersion, kind, namespace, name string, uid types.UID, resourceVersion string) domain.ResourceRef {
	return domain.ResourceRef{APIVersion: apiVersion, Kind: kind, Namespace: namespace, Name: name, UID: string(uid), ResourceVersion: resourceVersion}
}

func deploymentRevision(annotations map[string]string) (int64, bool) {
	value, ok := annotations["deployment.kubernetes.io/revision"]
	if !ok {
		return 0, false
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	return revision, err == nil && revision > 0
}

func controlledBy(owners []metav1.OwnerReference, kind, name, uid string) bool {
	owner, ok := soleController(owners)
	return ok && owner.Kind == kind && owner.Name == name && string(owner.UID) == uid
}

func soleController(owners []metav1.OwnerReference) (metav1.OwnerReference, bool) {
	var result metav1.OwnerReference
	count := 0
	for _, owner := range owners {
		if owner.Controller != nil && *owner.Controller {
			result, count = owner, count+1
		}
	}
	return result, count == 1 && result.UID != "" && result.Name != ""
}

func resolveController(ctx context.Context, bundle *ClientBundle, namespace string, owner metav1.OwnerReference) (domain.ResourceRef, error) {
	switch owner.APIVersion + "/" + owner.Kind {
	case "apps/v1/ReplicaSet":
		value, err := bundle.typed.AppsV1().ReplicaSets(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return domain.ResourceRef{}, classifyKubernetesError("resolve_pod_controller", err)
		}
		if value == nil || value.UID != owner.UID || value.ResourceVersion == "" {
			return domain.ResourceRef{}, remediationTargetChangedError("resolve_pod_controller")
		}
		return remediationReference("apps/v1", "ReplicaSet", namespace, value.Name, value.UID, value.ResourceVersion), nil
	case "apps/v1/StatefulSet":
		value, err := bundle.typed.AppsV1().StatefulSets(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return domain.ResourceRef{}, classifyKubernetesError("resolve_pod_controller", err)
		}
		if value == nil || value.UID != owner.UID || value.ResourceVersion == "" {
			return domain.ResourceRef{}, remediationTargetChangedError("resolve_pod_controller")
		}
		return remediationReference("apps/v1", "StatefulSet", namespace, value.Name, value.UID, value.ResourceVersion), nil
	case "apps/v1/DaemonSet":
		value, err := bundle.typed.AppsV1().DaemonSets(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return domain.ResourceRef{}, classifyKubernetesError("resolve_pod_controller", err)
		}
		if value == nil || value.UID != owner.UID || value.ResourceVersion == "" {
			return domain.ResourceRef{}, remediationTargetChangedError("resolve_pod_controller")
		}
		return remediationReference("apps/v1", "DaemonSet", namespace, value.Name, value.UID, value.ResourceVersion), nil
	case "batch/v1/Job":
		value, err := bundle.typed.BatchV1().Jobs(namespace).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return domain.ResourceRef{}, classifyKubernetesError("resolve_pod_controller", err)
		}
		if value == nil || value.UID != owner.UID || value.ResourceVersion == "" {
			return domain.ResourceRef{}, remediationTargetChangedError("resolve_pod_controller")
		}
		return remediationReference("batch/v1", "Job", namespace, value.Name, value.UID, value.ResourceVersion), nil
	default:
		return domain.ResourceRef{}, remediationError(ClassPolicyDenied, "remediation_pod_controller_unsupported", "resolve_pod_controller", "The Pod controller Kind is not admitted for exact deletion.")
	}
}

func isStaticOrMirrorPod(pod *corev1.Pod) bool {
	if pod == nil {
		return true
	}
	if pod.Annotations[corev1.MirrorPodAnnotationKey] != "" {
		return true
	}
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "Node" {
			return true
		}
	}
	return false
}

func podHasLocalData(pod *corev1.Pod) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir != nil || volume.HostPath != nil || volume.Ephemeral != nil ||
			volume.CSI != nil || volume.GitRepo != nil {
			return true
		}
	}
	return false
}

func podIdentityFingerprint(pod *corev1.Pod) (string, error) {
	if pod == nil {
		return "", domain.ErrInvalidRemediation
	}
	canonical, err := json.Marshal(struct {
		Spec   corev1.PodSpec          `json:"spec"`
		Owners []metav1.OwnerReference `json:"owners"`
	}{Spec: pod.Spec, Owners: pod.OwnerReferences})
	if err != nil {
		return "", err
	}
	return domain.SHA256Hex(string(canonical)), nil
}

// rollbackDeploymentTemplate removes the controller-owned label that exists
// on ReplicaSet templates but must not be copied back to a Deployment.
func rollbackDeploymentTemplate(template corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	normalized := *template.DeepCopy()
	delete(normalized.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	return normalized
}

func rollbackTemplateFingerprint(template corev1.PodTemplateSpec) (string, error) {
	return deploymentTemplateFingerprint(rollbackDeploymentTemplate(template))
}

func remediationError(class domain.SafeErrorClass, code, operation, message string) *SafeError {
	return newKubeSafeError(class, code, operation, message)
}

func remediationInputError(operation string) *SafeError {
	return remediationError(ClassInvalidInput, "remediation_request_invalid", operation, "The typed remediation request is invalid.")
}

func remediationProjectionError(operation string) *SafeError {
	return remediationError(ClassInvalidExternalResponse, "remediation_projection_invalid", operation, "Kubernetes returned an invalid typed remediation projection.")
}

func remediationTargetChangedError(operation string) *SafeError {
	return remediationError(ClassConflict, "remediation_target_changed", operation, "The exact remediation target or plan changed after it was proposed.")
}

func remediationStaleError(operation string) *SafeError {
	return remediationError(ClassStaleScope, "remediation_scope_stale", operation, "The Kubernetes scope changed before the typed remediation operation could continue.")
}
