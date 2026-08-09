package kube

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	maxProjectedIdentityBytes = 256
	maxProjectedStatusBytes   = 256
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
	if result.Validate() != nil {
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

func projectMetadata(metadata metav1.ObjectMeta, namespace, expectedName string, kind domain.ResourceKind) (domain.ResourceSummary, error) {
	if !kind.Valid() || metadata.Namespace != namespace || !domain.ValidNamespaceName(metadata.Namespace) ||
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
			case reference.APIVersion == "apps/v1" && reference.Kind == "StatefulSet":
				allowed = true
				referenceOnly = true
			}
		case domain.ResourceKindReplicaSet:
			allowed = reference.APIVersion == "apps/v1" && reference.Kind == "Deployment"
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
