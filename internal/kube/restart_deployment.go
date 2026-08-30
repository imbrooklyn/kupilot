package kube

import (
	"context"
	"encoding/json"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DeploymentRestarter is the sole Kubernetes mutation adapter. It shares
// the active opaque scope binding but exposes no client-go value or generic
// write capability.
type DeploymentRestarter struct {
	binding *ToolScopeBinding
	now     func() time.Time
}

var (
	_ application.RestartDeploymentProposalPreparer = (*DeploymentRestarter)(nil)
	_ approval.RestartDeploymentRevalidator         = (*DeploymentRestarter)(nil)
	_ approval.RestartDeploymentExecutor            = (*DeploymentRestarter)(nil)
)

// NewDeploymentRestarter validates fixed local dependencies without performing
// Kubernetes I/O. The clock supplies only the internally generated UTC value.
func NewDeploymentRestarter(binding *ToolScopeBinding, now func() time.Time) (*DeploymentRestarter, error) {
	if binding == nil || binding.gateway == nil || now == nil {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_restart_dependencies_invalid",
			"create_restart_deployment_adapter",
			"The Deployment restart adapter dependencies are invalid.",
		)
	}
	return &DeploymentRestarter{binding: binding, now: now}, nil
}

// PrepareRestartDeploymentProposal performs one exact read and derives the
// identity, Pod-template fingerprint, and generation that are bound into an
// approval digest. Model output supplies only the admitted target name and a
// human-readable reason; it cannot supply write authority or concurrency data.
func (restarter *DeploymentRestarter) PrepareRestartDeploymentProposal(
	ctx context.Context,
	scope domain.ScopeSnapshot,
	target domain.ResourceRef,
	reason string,
) (domain.OperationIntent, error) {
	if ctx == nil || scope.Validate() != nil || target.Validate() != nil ||
		target.APIVersion != domain.RestartDeploymentTargetAPIVersion ||
		target.Kind != domain.RestartDeploymentTargetKind || target.Namespace != scope.Namespace ||
		target.UID != "" || target.ResourceVersion != "" || !domain.ValidApprovalReasonSummary(reason) {
		return domain.OperationIntent{}, restartInputError("prepare_restart_deployment")
	}
	if ctx.Err() != nil {
		return domain.OperationIntent{}, classifyContextError(ctx.Err(), "prepare_restart_deployment")
	}
	bundle, client, err := restarter.capture(scope, "prepare_restart_deployment")
	if err != nil {
		return domain.OperationIntent{}, err
	}
	object, rawErr := bundle.typed.AppsV1().Deployments(scope.Namespace).Get(
		ctx,
		target.Name,
		metav1.GetOptions{},
	)
	if ctx.Err() != nil {
		return domain.OperationIntent{}, classifyContextError(ctx.Err(), "prepare_restart_deployment")
	}
	if rawErr != nil {
		return domain.OperationIntent{}, classifyKubernetesError("prepare_restart_deployment", rawErr)
	}
	if object == nil || object.APIVersion != "" && object.APIVersion != domain.RestartDeploymentTargetAPIVersion ||
		object.Kind != "" && object.Kind != domain.RestartDeploymentTargetKind ||
		object.Namespace != scope.Namespace || object.Name != target.Name || object.UID == "" || object.Generation < 1 {
		return domain.OperationIntent{}, invalidKubernetesProjectionError("prepare_restart_deployment")
	}
	fingerprint, err := deploymentTemplateFingerprint(object.Spec.Template)
	if err != nil {
		return domain.OperationIntent{}, invalidKubernetesProjectionError("prepare_restart_deployment")
	}
	intent := domain.OperationIntent{
		Operation:            domain.ApprovalOperationRestartDeployment,
		Scope:                scope,
		DeploymentName:       object.Name,
		DeploymentUID:        string(object.UID),
		TemplateFingerprint:  fingerprint,
		DeploymentGeneration: object.Generation,
		PolicyVersion:        domain.RestartDeploymentApprovalPolicyVersion,
		ReasonSummary:        reason,
	}
	if intent.Validate() != nil {
		return domain.OperationIntent{}, restartInputError("prepare_restart_deployment")
	}
	if !restarter.bindingCurrent(client, scope) {
		return domain.OperationIntent{}, restartStaleScopeError("prepare_restart_deployment")
	}
	return intent, nil
}

// RevalidateApprovedRestart performs the mandatory exact fresh Deployment GET
// and returns only identity, fingerprint, generation, and fresh resourceVersion.
func (restarter *DeploymentRestarter) RevalidateApprovedRestart(
	ctx context.Context,
	intent domain.OperationIntent,
) (approval.RestartDeploymentObservation, error) {
	if ctx == nil || intent.Validate() != nil {
		return approval.RestartDeploymentObservation{}, restartInputError("revalidate_restart_deployment")
	}
	if ctx.Err() != nil {
		return approval.RestartDeploymentObservation{}, classifyContextError(ctx.Err(), "revalidate_restart_deployment")
	}
	bundle, client, err := restarter.capture(intent.Scope, "revalidate_restart_deployment")
	if err != nil {
		return approval.RestartDeploymentObservation{}, err
	}
	object, rawErr := bundle.typed.AppsV1().Deployments(intent.Scope.Namespace).Get(
		ctx,
		intent.DeploymentName,
		metav1.GetOptions{},
	)
	if ctx.Err() != nil {
		return approval.RestartDeploymentObservation{}, classifyContextError(ctx.Err(), "revalidate_restart_deployment")
	}
	if rawErr != nil {
		return approval.RestartDeploymentObservation{}, restartReadError(rawErr)
	}
	if object == nil || object.APIVersion != "" && object.APIVersion != domain.RestartDeploymentTargetAPIVersion ||
		object.Kind != "" && object.Kind != domain.RestartDeploymentTargetKind ||
		object.Namespace != intent.Scope.Namespace || object.Name != intent.DeploymentName ||
		object.UID == "" || object.ResourceVersion == "" || object.Generation < 1 {
		return approval.RestartDeploymentObservation{}, invalidKubernetesProjectionError("revalidate_restart_deployment")
	}
	fingerprint, err := deploymentTemplateFingerprint(object.Spec.Template)
	if err != nil {
		return approval.RestartDeploymentObservation{}, invalidKubernetesProjectionError("revalidate_restart_deployment")
	}
	if string(object.UID) != intent.DeploymentUID || fingerprint != intent.TemplateFingerprint ||
		object.Generation != intent.DeploymentGeneration {
		return approval.RestartDeploymentObservation{}, restartTargetChangedError()
	}
	observation := approval.RestartDeploymentObservation{
		Scope: intent.Scope, DeploymentName: object.Name, DeploymentUID: string(object.UID),
		TemplateFingerprint: fingerprint, DeploymentGeneration: object.Generation,
		ResourceVersion: object.ResourceVersion,
	}
	if observation.Validate() != nil || !restarter.bindingCurrent(client, intent.Scope) {
		return approval.RestartDeploymentObservation{}, restartStaleScopeError("revalidate_restart_deployment")
	}
	return observation, nil
}

// ExecuteApprovedRestart sends exactly one fixed merge patch. The execution
// contains no annotation or timestamp input; both are generated here.
func (restarter *DeploymentRestarter) ExecuteApprovedRestart(
	ctx context.Context,
	execution approval.RestartDeploymentExecution,
) (approval.RestartDeploymentResult, error) {
	if ctx == nil || execution.Validate() != nil {
		return approval.RestartDeploymentResult{}, restartInputError("execute_restart_deployment")
	}
	if ctx.Err() != nil {
		return approval.RestartDeploymentResult{}, classifyContextError(ctx.Err(), "execute_restart_deployment")
	}
	observation := execution.Observation()
	bundle, client, err := restarter.capture(observation.Scope, "execute_restart_deployment")
	if err != nil {
		return approval.RestartDeploymentResult{}, err
	}
	restartedAt := restarter.now().UTC().Truncate(time.Millisecond)
	if restartedAt.IsZero() || restartedAt.UnixMilli() < 0 {
		return approval.RestartDeploymentResult{}, newKubeSafeError(
			ClassInternal,
			"kubernetes_restart_clock_invalid",
			"execute_restart_deployment",
			"The Deployment restart time could not be generated safely.",
		)
	}
	patch, err := json.Marshal(restartDeploymentPatch{
		Metadata: restartPatchMetadata{ResourceVersion: observation.ResourceVersion},
		Spec: restartPatchSpec{Template: restartPatchTemplate{Metadata: restartPatchTemplateMetadata{
			Annotations: restartPatchAnnotations{RestartedAt: restartedAt.Format(time.RFC3339Nano)},
		}}},
	})
	if err != nil {
		return approval.RestartDeploymentResult{}, newKubeSafeError(
			ClassInternal,
			"kubernetes_restart_patch_invalid",
			"execute_restart_deployment",
			"The fixed Deployment restart request could not be generated safely.",
		)
	}
	if !restarter.bindingCurrent(client, observation.Scope) {
		return approval.RestartDeploymentResult{}, restartStaleScopeError("execute_restart_deployment")
	}
	object, rawErr := bundle.typed.AppsV1().Deployments(observation.Scope.Namespace).Patch(
		ctx,
		observation.DeploymentName,
		types.MergePatchType,
		patch,
		metav1.PatchOptions{},
	)
	if ctx.Err() != nil {
		return approval.RestartDeploymentResult{}, classifyContextError(ctx.Err(), "execute_restart_deployment")
	}
	if rawErr != nil {
		return approval.RestartDeploymentResult{}, restartWriteError(rawErr)
	}
	if object == nil || object.APIVersion != "" && object.APIVersion != domain.RestartDeploymentTargetAPIVersion ||
		object.Kind != "" && object.Kind != domain.RestartDeploymentTargetKind ||
		object.Namespace != observation.Scope.Namespace || object.Name != observation.DeploymentName ||
		string(object.UID) != observation.DeploymentUID || object.ResourceVersion == "" || object.Generation < 1 ||
		object.ResourceVersion == observation.ResourceVersion ||
		object.Generation-observation.DeploymentGeneration != 1 {
		return approval.RestartDeploymentResult{}, invalidKubernetesProjectionError("execute_restart_deployment")
	}
	targetReplicas := int64(1)
	if object.Spec.Replicas != nil {
		targetReplicas = int64(*object.Spec.Replicas)
	}
	if targetReplicas < 0 {
		return approval.RestartDeploymentResult{}, invalidKubernetesProjectionError("execute_restart_deployment")
	}
	if !restarter.bindingCurrent(client, observation.Scope) {
		return approval.RestartDeploymentResult{}, restartStaleScopeError("execute_restart_deployment")
	}
	result := approval.RestartDeploymentResult{
		Scope: observation.Scope, DeploymentName: object.Name, DeploymentUID: string(object.UID),
		PreviousResourceVersion: observation.ResourceVersion, ResourceVersion: object.ResourceVersion,
		TargetGeneration: object.Generation, TargetReplicas: targetReplicas,
		RestartedAt: restartedAt,
	}
	if result.Validate() != nil {
		return approval.RestartDeploymentResult{}, invalidKubernetesProjectionError("execute_restart_deployment")
	}
	return result, nil
}

func (restarter *DeploymentRestarter) capture(
	scope domain.ScopeSnapshot,
	operation string,
) (*ClientBundle, application.ScopeClient, error) {
	if restarter == nil || restarter.binding == nil || restarter.binding.gateway == nil ||
		scope.Validate() != nil || !domain.ValidContextName(scope.Context) ||
		!domain.ValidNamespaceName(scope.Namespace) || scope.Generation < 1 {
		return nil, nil, restartInputError(operation)
	}
	restarter.binding.mu.Lock()
	defer restarter.binding.mu.Unlock()
	client := restarter.binding.client
	if client == nil || restarter.binding.generation != scope.Generation || client.Context().Name != scope.Context {
		return nil, nil, restartStaleScopeError(operation)
	}
	bundle, err := restarter.binding.gateway.clientBundle(client, operation)
	if err != nil {
		return nil, nil, err
	}
	return bundle, client, nil
}

func (restarter *DeploymentRestarter) bindingCurrent(client application.ScopeClient, scope domain.ScopeSnapshot) bool {
	if restarter == nil || restarter.binding == nil || client == nil {
		return false
	}
	restarter.binding.mu.Lock()
	defer restarter.binding.mu.Unlock()
	return restarter.binding.client == client && restarter.binding.generation == scope.Generation &&
		client.Context().Name == scope.Context
}

func deploymentTemplateFingerprint(template corev1.PodTemplateSpec) (string, error) {
	canonical, err := json.Marshal(template)
	if err != nil {
		return "", err
	}
	return domain.SHA256Hex(string(canonical)), nil
}

type restartDeploymentPatch struct {
	Metadata restartPatchMetadata `json:"metadata"`
	Spec     restartPatchSpec     `json:"spec"`
}

type restartPatchMetadata struct {
	ResourceVersion string `json:"resourceVersion"`
}

type restartPatchSpec struct {
	Template restartPatchTemplate `json:"template"`
}

type restartPatchTemplate struct {
	Metadata restartPatchTemplateMetadata `json:"metadata"`
}

type restartPatchTemplateMetadata struct {
	Annotations restartPatchAnnotations `json:"annotations"`
}

type restartPatchAnnotations struct {
	RestartedAt string `json:"kupilot.io/restartedAt"`
}

func restartInputError(operation string) *SafeError {
	return newKubeSafeError(
		ClassInvalidInput,
		"kubernetes_restart_request_invalid",
		operation,
		"The Deployment restart request is invalid.",
	)
}

func restartStaleScopeError(operation string) *SafeError {
	return newKubeSafeError(
		ClassStaleScope,
		"kubernetes_restart_scope_stale",
		operation,
		"The Kubernetes scope changed before the Deployment restart could continue.",
	)
}

func restartTargetChangedError() *SafeError {
	return newKubeSafeError(
		domain.SafeErrorClassConflict,
		"kubernetes_restart_target_changed",
		"revalidate_restart_deployment",
		"The Deployment changed after the restart was proposed.",
	)
}

func restartReadError(raw error) *SafeError {
	return classifyKubernetesError("revalidate_restart_deployment", raw)
}

func restartWriteError(raw error) *SafeError {
	if apierrors.IsConflict(raw) {
		return newKubeSafeError(
			domain.SafeErrorClassConflict,
			"kubernetes_restart_conflict",
			"execute_restart_deployment",
			"The Deployment changed before the restart request was accepted.",
		)
	}
	if apierrors.IsForbidden(raw) {
		return newKubeSafeError(
			ClassPermissionDenied,
			"kubernetes_restart_permission_denied",
			"execute_restart_deployment",
			"Kubernetes denied the Deployment restart request.",
		)
	}
	return classifyKubernetesError("execute_restart_deployment", raw)
}
