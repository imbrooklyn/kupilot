package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

// ListResourcesTool returns bounded candidate summaries from one fixed Kind in
// the active Namespace. It is not a resource browser.
type ListResourcesTool struct {
	dependencies ResourceToolDependencies
}

var _ agent.Tool = (*ListResourcesTool)(nil)

// NewListResourcesTool validates the immutable dependencies for list_resources.
func NewListResourcesTool(dependencies ResourceToolDependencies) (*ListResourcesTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidResourceToolDependencies
	}
	return &ListResourcesTool{dependencies: dependencies}, nil
}

// Execute performs one selector-free bounded list, then filters and orders the
// projected summaries locally before Evidence creation.
func (tool *ListResourcesTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil || tool.dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	observed := observedAt(tool.dependencies, call)
	arguments, request, err := decodeListResourcesCall(call)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	if ctx == nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidInput)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	observations, readErr := tool.dependencies.Reader.ListResources(ctx, request)
	if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
		return failedResult(call, observed, domain.SafeErrorClassStaleScope)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	if readErr != nil {
		return failedResult(call, observed, classifyFailure(ctx, readErr))
	}
	if observations.validate(request) != nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidExternalResponse)
	}
	sort.Slice(observations.Items, func(left, right int) bool {
		leftReference := observations.Items[left].Summary.Reference
		rightReference := observations.Items[right].Summary.Reference
		return leftReference.Namespace+"\x00"+leftReference.Name < rightReference.Namespace+"\x00"+rightReference.Name
	})
	filtered := make([]ResourceObservation, 0, min(len(observations.Items), request.Limit))
	matchedCount := 0
	for _, observation := range observations.Items {
		if arguments.NameQuery != "" && !strings.Contains(strings.ToLower(observation.Summary.Reference.Name), strings.ToLower(arguments.NameQuery)) {
			continue
		}
		if arguments.HealthFilter == "abnormal" && !resourceAbnormal(observation.Summary) {
			continue
		}
		matchedCount++
		if len(filtered) < request.Limit {
			filtered = append(filtered, observation)
		}
	}
	data, summaries, templates, warnings, processingLimited, err := tool.project(filtered, arguments, matchedCount)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	reason := ""
	if observations.Truncated || len(observations.Items) > request.Limit || arguments.Limit > request.Limit || matchedCount > len(filtered) {
		reason = itemLimitReason
	} else if processingLimited {
		reason = fieldLimitReason
	}
	templates, evidenceLimited := limitEvidenceTemplates(call, templates)
	if evidenceLimited && reason == "" {
		reason = evidenceLimitReason
	}
	if len(templates) < len(data.Items) {
		data.Items = data.Items[:len(templates)]
		summaries = summaries[:len(templates)]
		data.ReturnedCount = len(data.Items)
	}
	planned := fitListResourcesResult(call, observed, data, summaries, previewEvidence(call, observed, templates), warnings, reason)
	if planned.Status == domain.ToolResultStatusDenied || planned.Status == domain.ToolResultStatusError {
		return planned
	}
	if len(planned.Evidence) > len(templates) {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	evidence, err := materializeEvidence(tool.dependencies, call, observed, templates[:len(planned.Evidence)])
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	if planned.Truncation.Truncated {
		for index := range evidence {
			evidence[index].Truncated = true
		}
	}
	planned.Evidence = evidence
	return finalizePlannedResult(call, observed, planned)
}

type listResourceItem struct {
	CreatedAt string                `json:"created_at,omitempty"`
	Reference safeResourceReference `json:"reference"`
	Status    safeResourceStatus    `json:"status"`
}

type listResourcesData struct {
	HealthFilter    string             `json:"health_filter"`
	InstructionLike bool               `json:"instruction_like"`
	Items           []listResourceItem `json:"items"`
	Kind            string             `json:"kind"`
	MatchedCount    int                `json:"matched_count"`
	NameQuery       string             `json:"name_query,omitempty"`
	RedactionCount  int                `json:"redaction_count"`
	ReturnedCount   int                `json:"returned_count"`
	SourceTrust     string             `json:"source_trust"`
	Truncated       bool               `json:"truncated"`
}

func (tool *ListResourcesTool) project(
	observations []ResourceObservation,
	arguments listResourcesArguments,
	matchedCount int,
) (listResourcesData, []domain.ResourceSummary, []evidenceTemplate, []domain.ToolResultWarning, bool, error) {
	data := listResourcesData{
		HealthFilter:  arguments.HealthFilter,
		Items:         make([]listResourceItem, 0, len(observations)),
		Kind:          arguments.Kind,
		MatchedCount:  matchedCount,
		NameQuery:     arguments.NameQuery,
		SourceTrust:   security.UntrustedDataClass,
		ReturnedCount: len(observations),
	}
	metadata := textMetadata{}
	warnings := []domain.ToolResultWarning{}
	getProjector := GetResourceTool{dependencies: tool.dependencies}
	for _, observation := range observations {
		reference, current, err := getProjector.safeResourceReference(observation)
		if err != nil {
			return listResourcesData{}, nil, nil, nil, false, err
		}
		metadata.merge(current)
		if current.blocked {
			warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
		}
		status, current, err := getProjector.safeStatus(observation)
		if err != nil {
			return listResourcesData{}, nil, nil, nil, false, err
		}
		metadata.merge(current)
		if current.blocked {
			warnings = appendWarning(warnings, sensitiveFieldWarningCode, "One Kubernetes field was hidden because it may contain sensitive data.")
		}
		item := listResourceItem{Reference: reference, Status: status}
		if !observation.Summary.CreatedAt.IsZero() {
			item.CreatedAt = observation.Summary.CreatedAt.Format(time.RFC3339Nano)
		}
		data.Items = append(data.Items, item)
	}
	data.InstructionLike = metadata.instructionLike
	data.RedactionCount = metadata.redactions
	data.Truncated = metadata.truncated || metadata.blocked
	summaries := make([]domain.ResourceSummary, 0, len(data.Items))
	templates := make([]evidenceTemplate, 0, len(data.Items))
	for index, item := range data.Items {
		reference := domainReference(item.Reference)
		summary := domain.ResourceSummary{
			Reference: reference,
			CreatedAt: observations[index].Summary.CreatedAt,
			Status:    domainResourceStatus(item.Status),
		}
		if summary.Validate() != nil {
			return listResourcesData{}, nil, nil, nil, false, ErrInvalidResourceRead
		}
		summaries = append(summaries, summary)
		fact, factTruncated := boundedEvidenceFact(resourceStatusFact(item.Reference, item.Status))
		templates = append(templates, evidenceTemplate{
			category:       domain.EvidenceCategoryResourceStatus,
			resource:       reference,
			fact:           fact,
			sourcePath:     "projected.status",
			severity:       resourceStatusSeverity(item.Reference.Kind, item.Status),
			redactionCount: item.Status.redactionCount,
			truncated:      item.Status.truncated || factTruncated,
		})
	}
	return data, summaries, templates, warnings, data.Truncated || evidenceTemplatesTruncated(templates), nil
}

func fitListResourcesResult(
	call BoundToolCall,
	observed time.Time,
	data listResourcesData,
	summaries []domain.ResourceSummary,
	evidence []domain.Evidence,
	warnings []domain.ToolResultWarning,
	reason string,
) ToolResult {
	dataGuard, _ := security.NewOutputGuard(domain.MaxToolResultBytes)
	completeGuard, completeGuardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if completeGuardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	outputTrimmed := false
	for attempts := 0; attempts <= domain.MaxResourceSummaries; attempts++ {
		partial := reason != "" || outputTrimmed
		data.Truncated = data.Truncated || partial
		data.ReturnedCount = len(data.Items)
		currentWarnings := append([]domain.ToolResultWarning(nil), warnings...)
		if outputTrimmed {
			currentWarnings = appendWarning(currentWarnings, "output_limited", "The cluster read returned a deterministic subset because the fixed output limit was reached.")
		}
		raw, encodeErr := json.Marshal(data)
		encoded := ""
		if encodeErr == nil {
			encoded, encodeErr = dataGuard.CanonicalizeJSONObject(raw)
		}
		if encodeErr == nil {
			result := ToolResult{
				InvocationID:      call.InvocationID(),
				Name:              call.Name(),
				Version:           call.Version(),
				Scope:             call.Scope().Snapshot(),
				ObservedAt:        observed,
				Status:            domain.ToolResultStatusSuccess,
				DataJSON:          encoded,
				Evidence:          append([]domain.Evidence(nil), evidence...),
				ResourceSummaries: append([]domain.ResourceSummary(nil), summaries...),
				Warnings:          currentWarnings,
				Truncation: domain.ToolResultTruncation{
					Truncated:     partial,
					ReturnedCount: len(data.Items),
				},
			}
			if partial {
				result.Status = domain.ToolResultStatusPartial
				result.Truncation.Reason = reason
				if outputTrimmed {
					result.Truncation.Reason = outputLimitReason
				}
				for index := range result.Evidence {
					result.Evidence[index].Truncated = true
				}
			}
			measured, measureErr := measureResult(call, result)
			if measureErr == nil && completeGuard.Allows(measured.Truncation.ReturnedBytes) {
				return measured
			}
		}
		outputTrimmed = true
		if len(data.Items) == 0 {
			return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
		}
		data.Items = data.Items[:len(data.Items)-1]
		if len(evidence) > len(data.Items) {
			evidence = evidence[:len(data.Items)]
		}
		if len(summaries) > len(data.Items) {
			summaries = summaries[:len(data.Items)]
		}
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
}

func domainResourceStatus(status safeResourceStatus) domain.ResourceStatus {
	return domain.ResourceStatus{
		Phase:       status.Phase,
		Reason:      status.Reason,
		ServiceType: status.ServiceType,
		Ready:       domainOptionalCount(status.Ready),
		Desired:     domainOptionalCount(status.Desired),
		Available:   domainOptionalCount(status.Available),
		Active:      domainOptionalCount(status.Active),
		Succeeded:   domainOptionalCount(status.Succeeded),
		Failed:      domainOptionalCount(status.Failed),
	}
}

func domainOptionalCount(value *int32) domain.OptionalCount {
	if value == nil {
		return domain.OptionalCount{}
	}
	return domain.Count(*value)
}

func resourceAbnormal(summary domain.ResourceSummary) bool {
	status := summary.Status
	switch domain.ResourceKind(summary.Reference.Kind) {
	case domain.ResourceKindNamespace:
		return status.Phase != "Active" || status.Reason != ""
	case domain.ResourceKindNode:
		return status.Phase != "Ready" || status.Reason != "" || lessThan(status.Ready, status.Desired)
	case domain.ResourceKindPod:
		if status.Phase != "Running" && status.Phase != "Succeeded" || status.Reason != "" {
			return true
		}
		return lessThan(status.Ready, status.Desired)
	case domain.ResourceKindDeployment, domain.ResourceKindReplicaSet, domain.ResourceKindStatefulSet, domain.ResourceKindDaemonSet:
		return status.Reason != "" || lessThan(status.Ready, status.Desired) || lessThan(status.Available, status.Desired)
	case domain.ResourceKindJob:
		return status.Phase == "Failed" || status.Reason != "" || status.Failed.Present && status.Failed.Value > 0
	case domain.ResourceKindPersistentVolumeClaim:
		return status.Phase != "Bound" || status.Reason != ""
	case domain.ResourceKindPersistentVolume:
		return status.Phase != "Bound" && status.Phase != "Available" || status.Reason != ""
	case domain.ResourceKindHorizontalPodAutoscaler, domain.ResourceKindPodDisruptionBudget:
		return status.Reason != "" || lessThan(status.Ready, status.Desired)
	case domain.ResourceKindCronJob:
		return status.Reason != ""
	case domain.ResourceKindService, domain.ResourceKindIngress:
		// Direct Service summaries cannot prove endpoint health. Keep the bounded
		// candidate so a later fixed relationship read can gather that Evidence.
		return true
	case domain.ResourceKindConfigMap:
		return false
	default:
		return false
	}
}

func lessThan(left, right domain.OptionalCount) bool {
	return left.Present && right.Present && left.Value < right.Value
}
