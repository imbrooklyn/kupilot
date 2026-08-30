package kube

import (
	"errors"
	"strings"
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
)

const (
	maxProjectedIdentityBytes = 256
	maxProjectedStatusBytes   = 256
	maxToolProjectedTextBytes = 8 * 1024
	maxToolConditions         = 20
	maxToolContainers         = 50
	maxToolServicePorts       = 20
)

var errUnsafeKubernetesProjection = errors.New("Kubernetes projection is invalid")

func projectPod(object *corev1.Pod, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindPod)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Phase = sanitizeSummaryText(string(object.Status.Phase), maxProjectedStatusBytes)
	result.Status.Desired = domain.Count(int32(len(object.Spec.Containers)))
	ready := int32(0)
	for _, status := range object.Status.ContainerStatuses {
		if status.Ready {
			ready++
		}
	}
	result.Status.Ready = domain.Count(ready)
	result.Status.Reason = containerReason(object.Status.InitContainerStatuses)
	if result.Status.Reason == "" {
		result.Status.Reason = containerReason(object.Status.ContainerStatuses)
	}
	if result.Status.Reason == "" {
		for _, condition := range object.Status.Conditions {
			if condition.Status == corev1.ConditionFalse && condition.Reason != "" {
				result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
				break
			}
		}
	}
	result.Owners, err = projectControllerOwner(object.OwnerReferences, domain.ResourceKindPod)
	if err != nil || result.Validate() != nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectDeployment(object *appsv1.Deployment, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindDeployment)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	if object.Spec.Replicas != nil {
		result.Status.Desired = domain.Count(*object.Spec.Replicas)
	}
	result.Status.Ready = domain.Count(object.Status.ReadyReplicas)
	result.Status.Available = domain.Count(object.Status.AvailableReplicas)
	for _, condition := range object.Status.Conditions {
		if condition.Status != corev1.ConditionTrue && condition.Reason != "" {
			result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
			break
		}
	}
	if result.Validate() != nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectReplicaSet(object *appsv1.ReplicaSet, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindReplicaSet)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	if object.Spec.Replicas != nil {
		result.Status.Desired = domain.Count(*object.Spec.Replicas)
	}
	result.Status.Ready = domain.Count(object.Status.ReadyReplicas)
	result.Status.Available = domain.Count(object.Status.AvailableReplicas)
	for _, condition := range object.Status.Conditions {
		if condition.Status == corev1.ConditionTrue && condition.Reason != "" {
			result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
			break
		}
	}
	result.Owners, err = projectControllerOwner(object.OwnerReferences, domain.ResourceKindReplicaSet)
	if err != nil || result.Validate() != nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectJob(object *batchv1.Job, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindJob)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	if object.Spec.Completions != nil {
		result.Status.Desired = domain.Count(*object.Spec.Completions)
	}
	result.Status.Active = domain.Count(object.Status.Active)
	result.Status.Succeeded = domain.Count(object.Status.Succeeded)
	result.Status.Failed = domain.Count(object.Status.Failed)
	for _, condition := range object.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobFailed:
			result.Status.Phase = "Failed"
		case batchv1.JobComplete:
			result.Status.Phase = "Complete"
		}
		if condition.Reason != "" {
			result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
		}
		break
	}
	if result.Status.Phase == "" && object.Status.Active > 0 {
		result.Status.Phase = "Active"
	}
	result.Owners, err = projectControllerOwner(object.OwnerReferences, domain.ResourceKindJob)
	if err != nil || result.Validate() != nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectService(object *corev1.Service, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindService)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.ServiceType = sanitizeSummaryText(string(object.Spec.Type), maxProjectedStatusBytes)
	if result.Validate() != nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectNamespace(object *corev1.Namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, "", expectedName, domain.ResourceKindNamespace)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Phase = sanitizeSummaryText(string(object.Status.Phase), maxProjectedStatusBytes)
	for _, condition := range object.Status.Conditions {
		if condition.Status == corev1.ConditionTrue && condition.Reason != "" {
			result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
			break
		}
	}
	return validatedSummary(result)
}

func projectNode(object *corev1.Node, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, "", expectedName, domain.ResourceKindNode)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Ready = domain.Count(0)
	result.Status.Desired = domain.Count(1)
	for _, condition := range object.Status.Conditions {
		switch {
		case condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue:
			result.Status.Phase = "Ready"
			result.Status.Ready = domain.Count(1)
		case condition.Type == corev1.NodeReady && condition.Status != corev1.ConditionTrue:
			result.Status.Phase = "NotReady"
			if condition.Reason != "" {
				result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
			}
		case condition.Type != corev1.NodeReady && condition.Status == corev1.ConditionTrue && result.Status.Reason == "":
			result.Status.Reason = sanitizeSummaryText(string(condition.Type), maxProjectedStatusBytes)
		}
	}
	if result.Status.Phase == "" {
		result.Status.Phase = "Unknown"
	}
	return validatedSummary(result)
}

func projectPersistentVolumeClaim(object *corev1.PersistentVolumeClaim, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindPersistentVolumeClaim)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Phase = sanitizeSummaryText(string(object.Status.Phase), maxProjectedStatusBytes)
	return validatedSummary(result)
}

func projectPersistentVolume(object *corev1.PersistentVolume, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, "", expectedName, domain.ResourceKindPersistentVolume)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Phase = sanitizeSummaryText(string(object.Status.Phase), maxProjectedStatusBytes)
	result.Status.Reason = sanitizeSummaryText(object.Status.Reason, maxProjectedStatusBytes)
	return validatedSummary(result)
}

func projectConfigMap(object *corev1.ConfigMap, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindConfigMap)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	return validatedSummary(result)
}

func projectStatefulSet(object *appsv1.StatefulSet, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindStatefulSet)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	if object.Spec.Replicas != nil {
		result.Status.Desired = domain.Count(*object.Spec.Replicas)
	}
	result.Status.Ready = domain.Count(object.Status.ReadyReplicas)
	result.Status.Available = domain.Count(object.Status.AvailableReplicas)
	result.Status.Succeeded = domain.Count(object.Status.CurrentReplicas)
	return validatedSummary(result)
}

func projectDaemonSet(object *appsv1.DaemonSet, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindDaemonSet)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Desired = domain.Count(object.Status.DesiredNumberScheduled)
	result.Status.Ready = domain.Count(object.Status.NumberReady)
	result.Status.Available = domain.Count(object.Status.NumberAvailable)
	return validatedSummary(result)
}

func projectCronJob(object *batchv1.CronJob, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindCronJob)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Active = domain.Count(int32(len(object.Status.Active)))
	if object.Spec.Suspend != nil && *object.Spec.Suspend {
		result.Status.Phase = "Suspended"
	} else if len(object.Status.Active) > 0 {
		result.Status.Phase = "Active"
	} else {
		result.Status.Phase = "Idle"
	}
	return validatedSummary(result)
}

func projectIngress(object *networkingv1.Ingress, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindIngress)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	return validatedSummary(result)
}

func projectHorizontalPodAutoscaler(object *autoscalingv2.HorizontalPodAutoscaler, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindHorizontalPodAutoscaler)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Desired = domain.Count(object.Status.DesiredReplicas)
	result.Status.Ready = domain.Count(object.Status.CurrentReplicas)
	for _, condition := range object.Status.Conditions {
		if condition.Status != corev1.ConditionTrue && condition.Reason != "" {
			result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
			break
		}
	}
	return validatedSummary(result)
}

func projectPodDisruptionBudget(object *policyv1.PodDisruptionBudget, namespace, expectedName string) (domain.ResourceSummary, error) {
	if object == nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result, err := projectMetadata(object.ObjectMeta, namespace, expectedName, domain.ResourceKindPodDisruptionBudget)
	if err != nil {
		return domain.ResourceSummary{}, err
	}
	result.Status.Desired = domain.Count(object.Status.DesiredHealthy)
	result.Status.Ready = domain.Count(object.Status.CurrentHealthy)
	result.Status.Available = domain.Count(object.Status.DisruptionsAllowed)
	for _, condition := range object.Status.Conditions {
		if condition.Status != metav1.ConditionTrue && condition.Reason != "" {
			result.Status.Reason = sanitizeSummaryText(condition.Reason, maxProjectedStatusBytes)
			break
		}
	}
	return validatedSummary(result)
}

func validatedSummary(result domain.ResourceSummary) (domain.ResourceSummary, error) {
	if result.Validate() != nil {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectMetadata(metadata metav1.ObjectMeta, namespace, expectedName string, kind domain.ResourceKind) (domain.ResourceSummary, error) {
	if !kind.Valid() || kind.Namespaced() && (metadata.Namespace != namespace || !domain.ValidNamespaceName(metadata.Namespace)) ||
		kind.ClusterScoped() && (namespace != "" || metadata.Namespace != "") ||
		!domain.ValidResourceName(metadata.Name) || expectedName != "" && metadata.Name != expectedName ||
		!safeIdentityText(string(metadata.UID)) || !safeIdentityText(metadata.ResourceVersion) {
		return domain.ResourceSummary{}, errUnsafeKubernetesProjection
	}
	result := domain.ResourceSummary{
		Reference: domain.ResourceRef{
			APIVersion:      kind.APIVersion(),
			Kind:            string(kind),
			Namespace:       namespace,
			Name:            metadata.Name,
			UID:             string(metadata.UID),
			ResourceVersion: metadata.ResourceVersion,
		},
	}
	if !metadata.CreationTimestamp.IsZero() {
		result.CreatedAt = metadata.CreationTimestamp.Time.UTC()
	}
	return result, nil
}

func projectControllerOwner(references []metav1.OwnerReference, child domain.ResourceKind) ([]domain.ResourceOwner, error) {
	var result []domain.ResourceOwner
	for _, reference := range references {
		if reference.Controller == nil || !*reference.Controller {
			continue
		}
		referenceOnly := false
		allowed := false
		switch child {
		case domain.ResourceKindPod:
			switch {
			case reference.APIVersion == "apps/v1" && reference.Kind == "ReplicaSet":
				allowed = true
			case reference.APIVersion == "batch/v1" && reference.Kind == "Job":
				allowed = true
			case reference.APIVersion == "apps/v1" && (reference.Kind == "StatefulSet" || reference.Kind == "DaemonSet"):
				allowed = true
			}
		case domain.ResourceKindReplicaSet:
			allowed = reference.APIVersion == "apps/v1" && reference.Kind == "Deployment"
		case domain.ResourceKindJob:
			allowed = reference.APIVersion == "batch/v1" && reference.Kind == "CronJob"
		}
		if !allowed {
			continue
		}
		if len(result) != 0 || !domain.ValidResourceName(reference.Name) || !safeIdentityText(string(reference.UID)) {
			return nil, errUnsafeKubernetesProjection
		}
		result = append(result, domain.ResourceOwner{
			APIVersion:    reference.APIVersion,
			Kind:          reference.Kind,
			Name:          reference.Name,
			UID:           string(reference.UID),
			ReferenceOnly: referenceOnly,
		})
	}
	return result, nil
}

func containerReason(statuses []corev1.ContainerStatus) string {
	for _, status := range statuses {
		switch {
		case status.State.Waiting != nil && status.State.Waiting.Reason != "":
			return sanitizeSummaryText(status.State.Waiting.Reason, maxProjectedStatusBytes)
		case status.State.Terminated != nil && status.State.Terminated.Reason != "":
			return sanitizeSummaryText(status.State.Terminated.Reason, maxProjectedStatusBytes)
		}
	}
	return ""
}

func safeIdentityText(value string) bool {
	if len(value) > maxProjectedIdentityBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if unsafeProjectedRune(current) {
			return false
		}
	}
	return true
}

func sanitizeSummaryText(value string, limit int) string {
	value = strings.ToValidUTF8(value, string(utf8.RuneError))
	var builder strings.Builder
	builder.Grow(limit)
	for _, current := range value {
		if unsafeProjectedRune(current) {
			continue
		}
		if builder.Len()+utf8.RuneLen(current) > limit {
			break
		}
		builder.WriteRune(current)
	}
	result := strings.TrimSpace(builder.String())
	return result
}

func unsafeProjectedRune(value rune) bool {
	return unicode.IsControl(value) || unicode.In(value, unicode.Cf) || kubeBidirectionalControl(value)
}

func kubeBidirectionalControl(value rune) bool {
	return value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}

func projectToolPod(
	object *corev1.Pod,
	namespace string,
	expectedName string,
	detail toolcontract.ResourceDetail,
) (toolcontract.ResourceObservation, error) {
	summary, err := projectPod(object, namespace, expectedName)
	if err != nil || object == nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	result := projectToolMetadata(object.ObjectMeta, summary)
	result.Status.NodeName = projectToolText(object.Spec.NodeName, maxProjectedIdentityBytes)
	constraints := len(object.Spec.NodeSelector) + len(object.Spec.Tolerations) + len(object.Spec.TopologySpreadConstraints)
	if object.Spec.Affinity != nil {
		constraints++
	}
	result.Status.SchedulingConstraints = domain.Count(boundedInt32(constraints))
	if detail == toolcontract.ResourceDetailDiagnostic {
		result.Conditions, result.Truncated = projectPodConditions(object.Status.Conditions, result.Truncated)
		result.Containers, result.Truncated = projectPodContainers(object, result.Truncated)
	}
	if result.Validate() != nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectToolDeployment(
	object *appsv1.Deployment,
	namespace string,
	expectedName string,
	detail toolcontract.ResourceDetail,
) (toolcontract.ResourceObservation, error) {
	summary, err := projectDeployment(object, namespace, expectedName)
	if err != nil || object == nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	result := projectToolMetadata(object.ObjectMeta, summary)
	result.Status.Current = domain.Count(object.Status.Replicas)
	result.Status.Updated = domain.Count(object.Status.UpdatedReplicas)
	result.Status.Unavailable = domain.Count(object.Status.UnavailableReplicas)
	if object.Status.ObservedGeneration >= 0 {
		result.Status.ObservedGeneration = toolcontract.OptionalInt64{Value: object.Status.ObservedGeneration, Present: true}
	}
	result.Status.SelectorKeyCount = domain.Count(selectorKeyCount(object.Spec.Selector))
	result.Status.Revision = fixedRevision(object.Annotations)
	if detail == toolcontract.ResourceDetailDiagnostic {
		result.Conditions, result.Truncated = projectDeploymentConditions(object.Status.Conditions, result.Truncated)
	}
	if result.Validate() != nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectToolReplicaSet(
	object *appsv1.ReplicaSet,
	namespace string,
	expectedName string,
	detail toolcontract.ResourceDetail,
) (toolcontract.ResourceObservation, error) {
	summary, err := projectReplicaSet(object, namespace, expectedName)
	if err != nil || object == nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	result := projectToolMetadata(object.ObjectMeta, summary)
	result.Status.Current = domain.Count(object.Status.Replicas)
	unavailable := object.Status.Replicas - object.Status.AvailableReplicas
	if unavailable < 0 {
		unavailable = 0
	}
	result.Status.Unavailable = domain.Count(unavailable)
	if object.Status.ObservedGeneration >= 0 {
		result.Status.ObservedGeneration = toolcontract.OptionalInt64{Value: object.Status.ObservedGeneration, Present: true}
	}
	result.Status.SelectorKeyCount = domain.Count(selectorKeyCount(object.Spec.Selector))
	result.Status.Revision = fixedRevision(object.Annotations)
	if detail == toolcontract.ResourceDetailDiagnostic {
		result.Conditions, result.Truncated = projectReplicaSetConditions(object.Status.Conditions, result.Truncated)
	}
	if result.Validate() != nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectToolJob(
	object *batchv1.Job,
	namespace string,
	expectedName string,
	detail toolcontract.ResourceDetail,
) (toolcontract.ResourceObservation, error) {
	summary, err := projectJob(object, namespace, expectedName)
	if err != nil || object == nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	result := projectToolMetadata(object.ObjectMeta, summary)
	if object.Spec.Parallelism != nil && *object.Spec.Parallelism >= 0 {
		result.Status.Parallelism = domain.Count(*object.Spec.Parallelism)
	}
	if object.Spec.BackoffLimit != nil && *object.Spec.BackoffLimit >= 0 {
		result.Status.BackoffLimit = domain.Count(*object.Spec.BackoffLimit)
	}
	if object.Spec.ActiveDeadlineSeconds != nil && *object.Spec.ActiveDeadlineSeconds >= 0 {
		result.Status.ActiveDeadlineSeconds = toolcontract.OptionalInt64{Value: *object.Spec.ActiveDeadlineSeconds, Present: true}
	}
	if detail == toolcontract.ResourceDetailDiagnostic {
		result.Conditions, result.Truncated = projectJobConditions(object.Status.Conditions, result.Truncated)
	}
	if result.Validate() != nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectToolService(
	object *corev1.Service,
	namespace string,
	expectedName string,
	detail toolcontract.ResourceDetail,
) (toolcontract.ResourceObservation, error) {
	summary, err := projectService(object, namespace, expectedName)
	if err != nil || object == nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	result := projectToolMetadata(object.ObjectMeta, summary)
	result.Status.SelectorKeyCount = domain.Count(boundedInt32(len(object.Spec.Selector)))
	if detail == toolcontract.ResourceDetailDiagnostic {
		limit := min(len(object.Spec.Ports), maxToolServicePorts)
		result.ServicePorts = make([]toolcontract.ServicePortObservation, 0, limit)
		for index := 0; index < limit; index++ {
			port := object.Spec.Ports[index]
			result.ServicePorts = append(result.ServicePorts, toolcontract.ServicePortObservation{
				Name:       projectToolText(port.Name, maxProjectedIdentityBytes),
				Protocol:   projectToolText(string(port.Protocol), maxProjectedIdentityBytes),
				Port:       port.Port,
				TargetPort: projectToolText(port.TargetPort.String(), maxProjectedIdentityBytes),
			})
		}
		result.Truncated = result.Truncated || len(object.Spec.Ports) > limit
	}
	if result.Validate() != nil {
		return toolcontract.ResourceObservation{}, errUnsafeKubernetesProjection
	}
	return result, nil
}

func projectToolMetadata(metadata metav1.ObjectMeta, summary domain.ResourceSummary) toolcontract.ResourceObservation {
	result := toolcontract.ResourceObservation{
		Summary:      summary,
		Generation:   toolcontract.OptionalInt64{Value: metadata.Generation, Present: true},
		LabelCount:   len(metadata.Labels),
		Labels:       []toolcontract.LabelObservation{},
		Conditions:   []toolcontract.ConditionObservation{},
		Containers:   []toolcontract.ContainerObservation{},
		ServicePorts: []toolcontract.ServicePortObservation{},
	}
	for _, key := range []string{
		"app.kubernetes.io/name",
		"app.kubernetes.io/instance",
		"app.kubernetes.io/component",
		"pod-template-hash",
		"controller-uid",
		"job-name",
		"batch.kubernetes.io/controller-uid",
		"batch.kubernetes.io/job-name",
	} {
		value, exists := metadata.Labels[key]
		if !exists {
			continue
		}
		projected := projectToolText(value, maxProjectedIdentityBytes)
		result.Truncated = result.Truncated || projected.Truncated
		result.Labels = append(result.Labels, toolcontract.LabelObservation{Key: key, Value: projected})
	}
	return result
}

func projectPodConditions(values []corev1.PodCondition, truncated bool) ([]toolcontract.ConditionObservation, bool) {
	limit := min(len(values), maxToolConditions)
	result := make([]toolcontract.ConditionObservation, 0, limit)
	for index := 0; index < limit; index++ {
		value := values[index]
		condition := toolcontract.ConditionObservation{
			Type:    projectToolText(string(value.Type), maxProjectedIdentityBytes),
			Status:  projectToolText(string(value.Status), maxProjectedIdentityBytes),
			Reason:  projectToolText(value.Reason, maxProjectedIdentityBytes),
			Message: projectToolText(value.Message, maxToolProjectedTextBytes),
		}
		if !value.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = value.LastTransitionTime.Time.UTC()
		}
		truncated = truncated || condition.Type.Truncated || condition.Status.Truncated || condition.Reason.Truncated || condition.Message.Truncated
		result = append(result, condition)
	}
	return result, truncated || len(values) > limit
}

func projectDeploymentConditions(values []appsv1.DeploymentCondition, truncated bool) ([]toolcontract.ConditionObservation, bool) {
	limit := min(len(values), maxToolConditions)
	result := make([]toolcontract.ConditionObservation, 0, limit)
	for index := 0; index < limit; index++ {
		value := values[index]
		condition := toolcontract.ConditionObservation{
			Type:    projectToolText(string(value.Type), maxProjectedIdentityBytes),
			Status:  projectToolText(string(value.Status), maxProjectedIdentityBytes),
			Reason:  projectToolText(value.Reason, maxProjectedIdentityBytes),
			Message: projectToolText(value.Message, maxToolProjectedTextBytes),
		}
		if !value.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = value.LastTransitionTime.Time.UTC()
		}
		truncated = truncated || condition.Type.Truncated || condition.Status.Truncated || condition.Reason.Truncated || condition.Message.Truncated
		result = append(result, condition)
	}
	return result, truncated || len(values) > limit
}

func projectReplicaSetConditions(values []appsv1.ReplicaSetCondition, truncated bool) ([]toolcontract.ConditionObservation, bool) {
	limit := min(len(values), maxToolConditions)
	result := make([]toolcontract.ConditionObservation, 0, limit)
	for index := 0; index < limit; index++ {
		value := values[index]
		condition := toolcontract.ConditionObservation{
			Type:    projectToolText(string(value.Type), maxProjectedIdentityBytes),
			Status:  projectToolText(string(value.Status), maxProjectedIdentityBytes),
			Reason:  projectToolText(value.Reason, maxProjectedIdentityBytes),
			Message: projectToolText(value.Message, maxToolProjectedTextBytes),
		}
		if !value.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = value.LastTransitionTime.Time.UTC()
		}
		truncated = truncated || condition.Type.Truncated || condition.Status.Truncated || condition.Reason.Truncated || condition.Message.Truncated
		result = append(result, condition)
	}
	return result, truncated || len(values) > limit
}

func projectJobConditions(values []batchv1.JobCondition, truncated bool) ([]toolcontract.ConditionObservation, bool) {
	limit := min(len(values), maxToolConditions)
	result := make([]toolcontract.ConditionObservation, 0, limit)
	for index := 0; index < limit; index++ {
		value := values[index]
		condition := toolcontract.ConditionObservation{
			Type:    projectToolText(string(value.Type), maxProjectedIdentityBytes),
			Status:  projectToolText(string(value.Status), maxProjectedIdentityBytes),
			Reason:  projectToolText(value.Reason, maxProjectedIdentityBytes),
			Message: projectToolText(value.Message, maxToolProjectedTextBytes),
		}
		if !value.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = value.LastTransitionTime.Time.UTC()
		}
		truncated = truncated || condition.Type.Truncated || condition.Status.Truncated || condition.Reason.Truncated || condition.Message.Truncated
		result = append(result, condition)
	}
	return result, truncated || len(values) > limit
}

func projectPodContainers(object *corev1.Pod, truncated bool) ([]toolcontract.ContainerObservation, bool) {
	images := make(map[string]string, maxToolContainers)
	projectedImages := 0
	appendImages := func(values []corev1.Container) {
		for _, container := range values {
			if projectedImages == maxToolContainers {
				truncated = true
				return
			}
			images[container.Name] = container.Image
			projectedImages++
		}
	}
	appendImages(object.Spec.InitContainers)
	appendImages(object.Spec.Containers)
	result := make([]toolcontract.ContainerObservation, 0, min(maxToolContainers, len(object.Status.InitContainerStatuses)+len(object.Status.ContainerStatuses)))
	appendStatuses := func(values []corev1.ContainerStatus, initContainer bool) {
		for _, value := range values {
			if len(result) == maxToolContainers {
				truncated = true
				return
			}
			image := value.Image
			if image == "" {
				image = images[value.Name]
			}
			container := toolcontract.ContainerObservation{
				Name:         projectToolText(value.Name, maxProjectedIdentityBytes),
				Image:        projectToolText(image, maxToolProjectedTextBytes),
				Ready:        value.Ready,
				RestartCount: value.RestartCount,
				Init:         initContainer,
				Current:      projectContainerState(value.State),
				Last:         projectContainerState(value.LastTerminationState),
			}
			truncated = truncated || container.Name.Truncated || container.Image.Truncated ||
				container.Current.State.Truncated || container.Current.Reason.Truncated || container.Current.Message.Truncated ||
				container.Last.State.Truncated || container.Last.Reason.Truncated || container.Last.Message.Truncated
			result = append(result, container)
		}
	}
	appendStatuses(object.Status.InitContainerStatuses, true)
	appendStatuses(object.Status.ContainerStatuses, false)
	return result, truncated
}

func projectContainerState(value corev1.ContainerState) toolcontract.ContainerStateObservation {
	result := toolcontract.ContainerStateObservation{}
	switch {
	case value.Running != nil:
		result.State = projectToolText("running", maxProjectedIdentityBytes)
		if !value.Running.StartedAt.IsZero() {
			result.StartedAt = value.Running.StartedAt.Time.UTC()
		}
	case value.Waiting != nil:
		result.State = projectToolText("waiting", maxProjectedIdentityBytes)
		result.Reason = projectToolText(value.Waiting.Reason, maxProjectedIdentityBytes)
		result.Message = projectToolText(value.Waiting.Message, maxToolProjectedTextBytes)
	case value.Terminated != nil:
		result.State = projectToolText("terminated", maxProjectedIdentityBytes)
		result.Reason = projectToolText(value.Terminated.Reason, maxProjectedIdentityBytes)
		result.Message = projectToolText(value.Terminated.Message, maxToolProjectedTextBytes)
		if value.Terminated.ExitCode >= 0 {
			result.ExitCode = domain.Count(value.Terminated.ExitCode)
		}
		if !value.Terminated.StartedAt.IsZero() {
			result.StartedAt = value.Terminated.StartedAt.Time.UTC()
		}
		if !value.Terminated.FinishedAt.IsZero() {
			result.FinishedAt = value.Terminated.FinishedAt.Time.UTC()
		}
	}
	return result
}

func projectToolText(value string, maximumBytes int) toolcontract.ExternalText {
	value = strings.ToValidUTF8(value, string(utf8.RuneError))
	if len(value) <= maximumBytes {
		return toolcontract.ExternalText{Value: value}
	}
	var builder strings.Builder
	builder.Grow(maximumBytes)
	for _, current := range value {
		width := utf8.RuneLen(current)
		if width < 0 || builder.Len()+width > maximumBytes {
			break
		}
		builder.WriteRune(current)
	}
	return toolcontract.ExternalText{Value: builder.String(), Truncated: true}
}

func fixedRevision(annotations map[string]string) toolcontract.ExternalText {
	return projectToolText(annotations["deployment.kubernetes.io/revision"], maxProjectedIdentityBytes)
}

func selectorKeyCount(selector *metav1.LabelSelector) int32 {
	if selector == nil {
		return 0
	}
	return boundedInt32(len(selector.MatchLabels) + len(selector.MatchExpressions))
}

func boundedInt32(value int) int32 {
	if value < 0 {
		return 0
	}
	if int64(value) > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	return int32(value)
}
