package kube

import (
	"context"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResolveObservationTarget performs one exact Pod metadata GET. Log content
// and optional data-source traffic remain structurally unavailable here.
func (reader *ToolResourceReader) ResolveObservationTarget(
	ctx context.Context,
	request toolcontract.ObservationTargetRequest,
) (toolcontract.ResolvedObservationTarget, error) {
	const operation = "resolve_observation_target"
	if err := reader.validateContext(ctx, request.Scope, operation); err != nil {
		return toolcontract.ResolvedObservationTarget{}, err
	}
	if request.Validate() != nil {
		return toolcontract.ResolvedObservationTarget{}, newKubeSafeError(
			ClassInvalidInput, "kubernetes_observation_target_invalid", operation,
			"The observation Pod target is invalid.",
		)
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return toolcontract.ResolvedObservationTarget{}, newKubeSafeError(
			ClassStaleScope, "kubernetes_observation_policy_generation_stale", operation,
			"The observation policy changed before the target read started.",
		)
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, operation)
	if err != nil {
		return toolcontract.ResolvedObservationTarget{}, err
	}
	pod, rawErr := bundle.typed.CoreV1().Pods(request.Namespace).Get(ctx, request.PodName, metav1.GetOptions{})
	if err := resourceCallError(ctx, rawErr, operation); err != nil {
		return toolcontract.ResolvedObservationTarget{}, err
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return toolcontract.ResolvedObservationTarget{}, newKubeSafeError(
			ClassStaleScope, "kubernetes_observation_policy_generation_stale", operation,
			"The observation policy changed before the target read completed.",
		)
	}
	projected, err := projectPod(pod, request.Namespace, request.PodName)
	if err != nil {
		return toolcontract.ResolvedObservationTarget{}, invalidKubernetesProjectionError(operation)
	}
	result := toolcontract.ResolvedObservationTarget{Reference: projected.Reference}
	if !request.AllContainers && request.Operation != domain.ActionOperationPrometheusQuery && request.Operation != domain.ActionOperationLokiQuery {
		selection, selectionErr := selectPodLogContainer(pod, request.RequestedContainer, operation)
		if selectionErr != nil {
			return toolcontract.ResolvedObservationTarget{}, selectionErr
		}
		result.Container = selection.name
	}
	if result.Validate(request) != nil {
		return toolcontract.ResolvedObservationTarget{}, invalidKubernetesProjectionError(operation)
	}
	return result, nil
}

// RevalidateObservationAction repeats the exact target preparation after a
// decision and before durable consume. A changed UID, resourceVersion, or
// selected container invalidates the envelope.
func (reader *ToolResourceReader) RevalidateObservationAction(ctx context.Context, plan domain.ObservationActionPlan) error {
	if plan.Validate() != nil {
		return newKubeSafeError(
			ClassInvalidInput, "kubernetes_observation_action_invalid", "revalidate_observation_action",
			"The observation action is invalid.",
		)
	}
	parameters := plan.Parameters.Observation
	request := toolcontract.ObservationTargetRequest{
		Scope: plan.Scope, PolicyGeneration: plan.PolicyGeneration, Operation: plan.Operation,
		Namespace: plan.Target.Resource.Namespace, PodName: plan.Target.Resource.Name,
		RequestedContainer: parameters.Container, AllContainers: parameters.AllContainers,
		IncludeInit: parameters.IncludeInit, IncludeEphemeral: parameters.IncludeEphemeral,
	}
	target, err := reader.ResolveObservationTarget(ctx, request)
	if err != nil {
		return err
	}
	if target.Reference != plan.Target.Resource || target.Container != parameters.Container {
		return newKubeSafeError(
			ClassConflict, "kubernetes_observation_target_changed", "revalidate_observation_action",
			"The observation Pod target changed before approval consumption.",
		)
	}
	return nil
}

var _ toolcontract.ObservationTargetResolver = (*ToolResourceReader)(nil)
