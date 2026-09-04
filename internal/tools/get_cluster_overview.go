package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

// GetClusterOverviewTool returns bounded Namespace and Node health without
// discovery, raw objects, addresses, provider identifiers, or capacity maps.
type GetClusterOverviewTool struct {
	dependencies ResourceToolDependencies
}

var _ agent.Tool = (*GetClusterOverviewTool)(nil)

// NewGetClusterOverviewTool validates the immutable dependencies.
func NewGetClusterOverviewTool(dependencies ResourceToolDependencies) (*GetClusterOverviewTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidResourceToolDependencies
	}
	return &GetClusterOverviewTool{dependencies: dependencies}, nil
}

type clusterOverviewArguments struct {
	Limit   int    `json:"limit"`
	Purpose string `json:"purpose"`
}

type clusterOverviewData struct {
	InstructionLike bool               `json:"instruction_like"`
	Namespaces      []listResourceItem `json:"namespaces"`
	Nodes           []listResourceItem `json:"nodes"`
	RedactionCount  int                `json:"redaction_count"`
	SourceTrust     string             `json:"source_trust"`
	Truncated       bool               `json:"truncated"`
}

func decodeClusterOverviewCall(call BoundToolCall) (clusterOverviewArguments, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameGetClusterOverview || call.Version() != agent.ToolCatalogVersion {
		return clusterOverviewArguments{}, ErrInvalidCanonicalArguments
	}
	var arguments clusterOverviewArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() ||
		arguments.Limit < 2 || arguments.Limit > domain.MaxResourceSummaries {
		return clusterOverviewArguments{}, ErrInvalidCanonicalArguments
	}
	return arguments, nil
}

// Execute performs exactly two fixed typed LIST operations with a scope gate
// around each return, then creates Evidence only from accepted projections.
func (tool *GetClusterOverviewTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil || tool.dependencies.validate() != nil || call.Validate() != nil {
		return ToolResult{}
	}
	observed := observedAt(tool.dependencies, call)
	arguments, err := decodeClusterOverviewCall(call)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	if ctx == nil {
		return failedResult(call, observed, domain.SafeErrorClassInvalidInput)
	}
	if ctx.Err() != nil {
		return failedResult(call, observed, classifyFailure(ctx, ctx.Err()))
	}
	totalLimit := min(arguments.Limit, call.Ceilings().MaxResourceItems, domain.MaxResourceSummaries)
	read := func(kind domain.ResourceKind, limit int) (ResourceObservationList, error) {
		if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
			return ResourceObservationList{}, staleOverviewError{}
		}
		request := ResourceListRequest{Scope: call.Scope(), Kind: kind, Limit: limit}
		result, readErr := tool.dependencies.Reader.ListResources(ctx, request)
		if !tool.dependencies.ScopeGuard.Current(ctx, call.Scope()) {
			return ResourceObservationList{}, staleOverviewError{}
		}
		if ctx.Err() != nil {
			return ResourceObservationList{}, ctx.Err()
		}
		if readErr != nil {
			return ResourceObservationList{}, readErr
		}
		if result.Validate(request) != nil {
			return ResourceObservationList{}, ErrInvalidResourceRead
		}
		return result, nil
	}
	namespaceLimit := max(1, totalLimit/2)
	namespaces, err := read(domain.ResourceKindNamespace, namespaceLimit)
	if err != nil {
		return failedResult(call, observed, classifyOverviewFailure(ctx, err))
	}
	nodeLimit := max(1, totalLimit-len(namespaces.Items))
	nodes, err := read(domain.ResourceKindNode, nodeLimit)
	if err != nil {
		return failedResult(call, observed, classifyOverviewFailure(ctx, err))
	}

	projector := ListResourcesTool{dependencies: tool.dependencies}
	resourcePolicies := domain.DefaultResourcePolicyCatalog()
	namespaceType := domain.BuiltInResourceType(domain.ResourceKindNamespace)
	namespacePolicy, namespacePolicyFound := resourcePolicies.Resolve(namespaceType.ID)
	if !namespacePolicyFound {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	namespaceData, namespaceSummaries, namespaceTemplates, namespaceWarnings, namespaceLimited, err := projector.project(
		namespaces.Items,
		namespacePolicy,
		listResourcesArguments{Filters: []resourceFilterArgument{}, Format: domain.ResourceViewList, ResourceType: namespaceType.ID, Limit: namespaceLimit, Purpose: arguments.Purpose},
		overviewResourcePage(namespaceType, namespaces.Items, namespaces.Truncated),
	)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	nodeType := domain.BuiltInResourceType(domain.ResourceKindNode)
	nodePolicy, nodePolicyFound := resourcePolicies.Resolve(nodeType.ID)
	if !nodePolicyFound {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	nodeData, nodeSummaries, nodeTemplates, nodeWarnings, nodeLimited, err := projector.project(
		nodes.Items,
		nodePolicy,
		listResourcesArguments{Filters: []resourceFilterArgument{}, Format: domain.ResourceViewList, ResourceType: nodeType.ID, Limit: nodeLimit, Purpose: arguments.Purpose},
		overviewResourcePage(nodeType, nodes.Items, nodes.Truncated),
	)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassInternal)
	}
	data := clusterOverviewData{
		InstructionLike: namespaceData.InstructionLike || nodeData.InstructionLike,
		Namespaces:      namespaceData.Items,
		Nodes:           nodeData.Items,
		RedactionCount:  namespaceData.RedactionCount + nodeData.RedactionCount,
		SourceTrust:     security.UntrustedDataClass,
		Truncated:       namespaces.Truncated || nodes.Truncated || namespaceLimited || nodeLimited,
	}
	summaries := append(namespaceSummaries, nodeSummaries...)
	templates := append(namespaceTemplates, nodeTemplates...)
	warnings := append(namespaceWarnings, nodeWarnings...)
	templates, evidenceLimited := limitEvidenceTemplates(call, templates)
	if len(templates) < len(summaries) {
		summaries = summaries[:len(templates)]
		trimOverviewData(&data, len(namespaceSummaries), len(templates))
	}
	data.Truncated = data.Truncated || evidenceLimited
	planned := fitClusterOverviewResult(call, observed, data, summaries, previewEvidence(call, observed, templates), warnings)
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
			evidence[index].Partial = true
		}
	}
	planned.Evidence = evidence
	return finalizePlannedResult(call, observed, planned)
}

func overviewResourcePage(resourceType domain.ResourceType, observations []ResourceObservation, truncated bool) domain.ResourcePage {
	summaries := make([]domain.ResourceSummary, len(observations))
	for index := range observations {
		summaries[index] = observations[index].Summary
		summaries[index].Type = resourceType
	}
	reason := ""
	if truncated {
		reason = itemLimitReason
	}
	return domain.ResourcePage{
		Type: resourceType, Items: summaries, PagesRead: 1, ScannedItems: len(summaries), MatchedItems: len(summaries),
		Partial: truncated, Truncated: truncated, MoreAvailable: truncated, Reason: reason,
	}
}

type staleOverviewError struct{}

func (staleOverviewError) Error() string { return "stale cluster overview scope" }

func classifyOverviewFailure(ctx context.Context, err error) domain.SafeErrorClass {
	if _, stale := err.(staleOverviewError); stale {
		return domain.SafeErrorClassStaleScope
	}
	if err == ErrInvalidResourceRead {
		return domain.SafeErrorClassInvalidExternalResponse
	}
	return classifyFailure(ctx, err)
}

func trimOverviewData(data *clusterOverviewData, namespaceCount, keep int) {
	if data == nil {
		return
	}
	keptNamespaces := min(namespaceCount, keep)
	keptNodes := max(0, keep-keptNamespaces)
	data.Namespaces = data.Namespaces[:min(keptNamespaces, len(data.Namespaces))]
	data.Nodes = data.Nodes[:min(keptNodes, len(data.Nodes))]
	data.Truncated = true
}

func fitClusterOverviewResult(
	call BoundToolCall,
	observed time.Time,
	data clusterOverviewData,
	summaries []domain.ResourceSummary,
	evidence []domain.Evidence,
	warnings []domain.ToolResultWarning,
) ToolResult {
	dataGuard, _ := security.NewOutputGuard(domain.MaxToolResultBytes)
	completeGuard, guardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if guardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	for attempts := 0; attempts <= 2*domain.MaxResourceSummaries; attempts++ {
		raw, encodeErr := json.Marshal(data)
		encoded := ""
		if encodeErr == nil {
			encoded, encodeErr = dataGuard.CanonicalizeJSONObject(raw)
		}
		if encodeErr == nil {
			result := ToolResult{
				InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(),
				ObservedAt: observed, Status: domain.ToolResultStatusSuccess, DataJSON: encoded,
				Evidence: append([]domain.Evidence(nil), evidence...), ResourceSummaries: append([]domain.ResourceSummary(nil), summaries...),
				Warnings:   append([]domain.ToolResultWarning(nil), warnings...),
				Truncation: domain.ToolResultTruncation{Truncated: data.Truncated, ReturnedCount: len(summaries)},
			}
			if data.Truncated {
				result.Status = domain.ToolResultStatusPartial
				result.Truncation.Reason = itemLimitReason
			}
			measured, measureErr := measureResult(call, result)
			if measureErr == nil && completeGuard.Allows(measured.Truncation.ReturnedBytes) {
				return measured
			}
		}
		data.Truncated = true
		if len(data.Nodes) > 0 {
			data.Nodes = data.Nodes[:len(data.Nodes)-1]
		} else if len(data.Namespaces) > 0 {
			data.Namespaces = data.Namespaces[:len(data.Namespaces)-1]
		} else {
			return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
		}
		keep := len(data.Namespaces) + len(data.Nodes)
		if len(evidence) > keep {
			evidence = evidence[:keep]
		}
		if len(summaries) > keep {
			summaries = summaries[:keep]
		}
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
}
