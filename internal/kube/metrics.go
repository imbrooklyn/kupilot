package kube

import (
	"context"
	"math"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsapi "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// ReadMetrics performs an exact target identity GET followed by one typed
// metrics.k8s.io/v1beta1 GET. Kubernetes quantities never cross this adapter.
func (reader *ToolResourceReader) ReadMetrics(ctx context.Context, request toolcontract.MetricReadRequest) (toolcontract.MetricObservation, error) {
	const operation = "tool_get_metrics"
	if err := reader.validateContext(ctx, request.Scope, operation); err != nil {
		return toolcontract.MetricObservation{}, err
	}
	if request.Validate() != nil {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassInvalidInput, "kubernetes_tool_metric_request_invalid", operation, "The Kubernetes metrics request is invalid.")
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassStaleScope, "kubernetes_metric_policy_generation_stale", operation, "The observability policy changed before the Kubernetes read completed.")
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, operation)
	if err != nil {
		return toolcontract.MetricObservation{}, err
	}
	if bundle.metrics == nil {
		return toolcontract.MetricObservation{Resource: request.Reference, Availability: toolcontract.MetricUnsupported}, nil
	}
	boundedContext, responseBudget := withResponseByteBudget(ctx, request.LimitBytes)
	if request.Reference.Kind == string(domain.ResourceKindPod) {
		pod, rawErr := bundle.typed.CoreV1().Pods(request.Reference.Namespace).Get(boundedContext, request.Reference.Name, metav1.GetOptions{})
		if _, exhausted := responseBudget.snapshot(); exhausted {
			return toolcontract.MetricObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_metric_byte_limit", operation, "The Kubernetes metrics response exceeded its aggregate byte limit.")
		}
		if err := resourceCallError(boundedContext, rawErr, operation); err != nil {
			return toolcontract.MetricObservation{}, err
		}
		projected, projectErr := projectPod(pod, request.Reference.Namespace, request.Reference.Name)
		if projectErr != nil {
			return toolcontract.MetricObservation{}, invalidKubernetesProjectionError(operation)
		}
		if request.Reference.UID != "" && projected.Reference.UID != request.Reference.UID {
			return toolcontract.MetricObservation{}, newKubeSafeError(ClassNotFound, "kubernetes_metric_target_uid_not_found", operation, "The requested Kubernetes object was not found.")
		}
		if responseBudget.remainingBytes() == 0 {
			return toolcontract.MetricObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_metric_byte_limit", operation, "The Kubernetes metrics byte limit was exhausted by target validation before the Metrics API could be read.")
		}
		if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
			return toolcontract.MetricObservation{}, newKubeSafeError(ClassStaleScope, "kubernetes_metric_policy_generation_stale", operation, "The observability policy changed before the Kubernetes read completed.")
		}
		metric, rawErr := bundle.metrics.PodMetricses(request.Reference.Namespace).Get(boundedContext, request.Reference.Name, metav1.GetOptions{})
		if _, exhausted := responseBudget.snapshot(); exhausted {
			return toolcontract.MetricObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_metric_byte_limit", operation, "The Kubernetes metrics response exceeded its aggregate byte limit.")
		}
		if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(boundedContext, request.PolicyGeneration) {
			return toolcontract.MetricObservation{}, newKubeSafeError(ClassStaleScope, "kubernetes_metric_policy_generation_stale", operation, "The observability policy changed before the Kubernetes read completed.")
		}
		if boundedContext.Err() != nil {
			return toolcontract.MetricObservation{}, resourceCallError(boundedContext, boundedContext.Err(), operation)
		}
		if rawErr != nil {
			if apierrors.IsNotFound(rawErr) {
				observed := unavailableMetric(projected.Reference, rawErr)
				observed.SourceBytes, _ = responseBudget.snapshot()
				return observed, validateMetricProjection(observed, request, operation)
			}
			return toolcontract.MetricObservation{}, resourceCallError(boundedContext, rawErr, operation)
		}
		observed, projectErr := projectPodMetric(metric, projected.Reference, request.MaxContainers)
		if projectErr != nil {
			return toolcontract.MetricObservation{}, invalidKubernetesProjectionError(operation)
		}
		observed.SourceBytes, observed.Truncated = responseBudget.snapshot()
		if observed.Truncated {
			observed.Partial = true
			observed.Availability = toolcontract.MetricPartial
		}
		return observed, validateMetricProjection(observed, request, operation)
	}
	node, rawErr := bundle.typed.CoreV1().Nodes().Get(boundedContext, request.Reference.Name, metav1.GetOptions{})
	if _, exhausted := responseBudget.snapshot(); exhausted {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_metric_byte_limit", operation, "The Kubernetes metrics response exceeded its aggregate byte limit.")
	}
	if err := resourceCallError(boundedContext, rawErr, operation); err != nil {
		return toolcontract.MetricObservation{}, err
	}
	projected, projectErr := projectNode(node, request.Reference.Name)
	if projectErr != nil {
		return toolcontract.MetricObservation{}, invalidKubernetesProjectionError(operation)
	}
	if request.Reference.UID != "" && projected.Reference.UID != request.Reference.UID {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassNotFound, "kubernetes_metric_target_uid_not_found", operation, "The requested Kubernetes object was not found.")
	}
	if responseBudget.remainingBytes() == 0 {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_metric_byte_limit", operation, "The Kubernetes metrics byte limit was exhausted by target validation before the Metrics API could be read.")
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassStaleScope, "kubernetes_metric_policy_generation_stale", operation, "The observability policy changed before the Kubernetes read completed.")
	}
	metric, rawErr := bundle.metrics.NodeMetricses().Get(boundedContext, request.Reference.Name, metav1.GetOptions{})
	if _, exhausted := responseBudget.snapshot(); exhausted {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassBudgetExhausted, "kubernetes_metric_byte_limit", operation, "The Kubernetes metrics response exceeded its aggregate byte limit.")
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(boundedContext, request.PolicyGeneration) {
		return toolcontract.MetricObservation{}, newKubeSafeError(ClassStaleScope, "kubernetes_metric_policy_generation_stale", operation, "The observability policy changed before the Kubernetes read completed.")
	}
	if boundedContext.Err() != nil {
		return toolcontract.MetricObservation{}, resourceCallError(boundedContext, boundedContext.Err(), operation)
	}
	if rawErr != nil {
		if apierrors.IsNotFound(rawErr) {
			observed := unavailableMetric(projected.Reference, rawErr)
			observed.SourceBytes, _ = responseBudget.snapshot()
			return observed, validateMetricProjection(observed, request, operation)
		}
		return toolcontract.MetricObservation{}, resourceCallError(boundedContext, rawErr, operation)
	}
	usage, usageErr := normalizedUsage(metric.Usage)
	if usageErr != nil || metric.Timestamp.IsZero() || metric.Window.Duration <= 0 {
		return toolcontract.MetricObservation{}, invalidKubernetesProjectionError(operation)
	}
	observed := toolcontract.MetricObservation{Resource: projected.Reference, Availability: toolcontract.MetricAvailable,
		Timestamp: metric.Timestamp.Time.UTC(), Window: metric.Window.Duration, Usage: usage}
	observed.SourceBytes, observed.Truncated = responseBudget.snapshot()
	if observed.Truncated {
		observed.Partial = true
		observed.Availability = toolcontract.MetricPartial
	}
	return observed, validateMetricProjection(observed, request, operation)
}

func unavailableMetric(reference domain.ResourceRef, raw error) toolcontract.MetricObservation {
	availability := toolcontract.MetricUnavailable
	if status, ok := raw.(apierrors.APIStatus); ok {
		details := status.Status().Details
		if details == nil || details.Name == "" {
			availability = toolcontract.MetricUnsupported
		}
	}
	return toolcontract.MetricObservation{Resource: reference, Availability: availability}
}

func projectPodMetric(metric *metricsapi.PodMetrics, reference domain.ResourceRef, maximum int) (toolcontract.MetricObservation, error) {
	if metric == nil || metric.Name != reference.Name || metric.Namespace != reference.Namespace || metric.Timestamp.IsZero() || metric.Window.Duration <= 0 {
		return toolcontract.MetricObservation{}, errBroadResourceProjection
	}
	result := toolcontract.MetricObservation{Resource: reference, Availability: toolcontract.MetricAvailable,
		Timestamp: metric.Timestamp.Time.UTC(), Window: metric.Window.Duration, Containers: make([]toolcontract.ContainerMetricObservation, 0, min(len(metric.Containers), maximum))}
	for index := 0; index < len(metric.Containers) && index < maximum; index++ {
		container := metric.Containers[index]
		if !domain.ValidResourceName(container.Name) {
			return toolcontract.MetricObservation{}, errBroadResourceProjection
		}
		usage, err := normalizedUsage(container.Usage)
		if err != nil {
			return toolcontract.MetricObservation{}, err
		}
		if result.Usage.CPUMilli > math.MaxInt64-usage.CPUMilli || result.Usage.MemoryBytes > math.MaxInt64-usage.MemoryBytes {
			return toolcontract.MetricObservation{}, errBroadResourceProjection
		}
		result.Usage.CPUMilli += usage.CPUMilli
		result.Usage.MemoryBytes += usage.MemoryBytes
		result.Containers = append(result.Containers, toolcontract.ContainerMetricObservation{Name: container.Name, Usage: usage})
	}
	if len(metric.Containers) > maximum {
		result.Partial, result.Truncated, result.Availability = true, true, toolcontract.MetricPartial
	}
	return result, nil
}

func normalizedUsage(usage corev1.ResourceList) (domain.NormalizedResourceUsage, error) {
	cpu, cpuFound := usage[corev1.ResourceCPU]
	memory, memoryFound := usage[corev1.ResourceMemory]
	if !cpuFound || !memoryFound {
		return domain.NormalizedResourceUsage{}, errBroadResourceProjection
	}
	result := domain.NormalizedResourceUsage{CPUMilli: cpu.MilliValue(), MemoryBytes: memory.Value()}
	if result.Validate() != nil {
		return domain.NormalizedResourceUsage{}, errBroadResourceProjection
	}
	return result, nil
}

func validateMetricProjection(observation toolcontract.MetricObservation, request toolcontract.MetricReadRequest, operation string) error {
	if observation.Validate(request) != nil {
		return invalidKubernetesProjectionError(operation)
	}
	return nil
}
