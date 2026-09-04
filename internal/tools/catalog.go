package tools

import (
	"encoding/json"
	"errors"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	// ErrInvalidReadOnlyToolCatalog reports incomplete dependencies for the
	// compile-time built-in catalog.
	ErrInvalidReadOnlyToolCatalog = errors.New("the fixed read-only Tool catalog dependencies are invalid")
)

// ReadOnlyToolCatalogDependencies contain the active dependencies for the
// built-in handlers. They are concrete and cannot register a dynamically named
// Tool.
type ReadOnlyToolCatalogDependencies struct {
	Resources ResourceToolDependencies
	Events    EventToolDependencies
	Logs      LogToolDependencies
	Metrics   MetricToolDependencies
	Sources   DataSourceToolDependencies
	Related   RelatedToolDependencies
}

// NewReadOnlyToolCatalog constructs the compile-time fixed handler table used
// by the Agent adapter. Schema authority remains in the neutral Agent catalog.
func NewReadOnlyToolCatalog(dependencies ReadOnlyToolCatalogDependencies) (agent.ToolHandlers, error) {
	getResource, err := NewGetResourceTool(dependencies.Resources)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	listResources, err := NewListResourcesTool(dependencies.Resources)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	getEvents, err := NewGetEventsTool(dependencies.Events)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	getPodLogs, err := NewGetPodLogsTool(dependencies.Logs)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	getPreviousPodLogs, err := NewGetPreviousPodLogsTool(dependencies.Logs)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	getPodMetrics, err := NewGetPodMetricsTool(dependencies.Metrics)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	getNodeMetrics, err := NewGetNodeMetricsTool(dependencies.Metrics)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	queryPrometheus, err := NewQueryPrometheusTool(dependencies.Sources)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	queryLoki, err := NewQueryLokiTool(dependencies.Sources)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	getRelatedResources, err := NewGetRelatedResourcesTool(dependencies.Related)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	getClusterOverview, err := NewGetClusterOverviewTool(dependencies.Resources)
	if err != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	handlers := agent.ToolHandlers{
		GetResource: getResource, ListResources: listResources, GetEvents: getEvents,
		GetPodLogs: getPodLogs, GetPreviousPodLogs: getPreviousPodLogs, GetRelatedResources: getRelatedResources,
		GetPodMetrics: getPodMetrics, GetNodeMetrics: getNodeMetrics, QueryPrometheus: queryPrometheus, QueryLoki: queryLoki,
		GetClusterOverview: getClusterOverview,
	}
	if handlers.Validate() != nil {
		return agent.ToolHandlers{}, ErrInvalidReadOnlyToolCatalog
	}
	return handlers, nil
}

type resourceArgument struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

type getResourceArguments struct {
	Detail       string `json:"detail"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace,omitempty"`
	Purpose      string `json:"purpose"`
	ResourceType string `json:"resource_type"`
}

type resourceFilterArgument struct {
	Field    string                        `json:"field"`
	Operator domain.ResourceFilterOperator `json:"operator"`
	Value    string                        `json:"value,omitempty"`
}

type listResourcesArguments struct {
	Filters      []resourceFilterArgument `json:"filters"`
	Format       domain.ResourceView      `json:"format"`
	Limit        int                      `json:"limit"`
	Namespace    string                   `json:"namespace,omitempty"`
	Purpose      string                   `json:"purpose"`
	ResourceType string                   `json:"resource_type"`
}

type relatedResourceArgument struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	UID        string `json:"uid,omitempty"`
}

type getRelatedResourcesArguments struct {
	Include       []RelatedInclude        `json:"include"`
	Purpose       string                  `json:"purpose"`
	RelationDepth int                     `json:"relation_depth"`
	Resource      relatedResourceArgument `json:"resource"`
}

func decodeGetResourceCall(call BoundToolCall) (getResourceArguments, ResourceQueryRequest, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameGetResource || call.Version() != agent.ToolCatalogVersion {
		return getResourceArguments{}, ResourceQueryRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments getResourceArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() {
		return getResourceArguments{}, ResourceQueryRequest{}, ErrInvalidCanonicalArguments
	}
	detail := ResourceDetailSummary
	view := domain.ResourceViewSummary
	if arguments.Detail == string(domain.ResourceViewDescribe) {
		detail = ResourceDetailDiagnostic
		view = domain.ResourceViewDescribe
	} else if arguments.Detail != string(domain.ResourceViewSummary) {
		return getResourceArguments{}, ResourceQueryRequest{}, ErrInvalidCanonicalArguments
	}
	policy := call.ResourcePolicy()
	limits := boundedResourceQueryLimits(call, policy, 1)
	request := ResourceQueryRequest{Policy: policy, Detail: detail, Query: domain.ResourceQuery{
		Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), PolicyVersion: domain.ResourcePolicyVersion,
		Type: policy.Type, Verb: domain.ResourceVerbGet, View: view, Namespace: arguments.Namespace,
		Name: arguments.Name, Limits: limits,
	}}
	if request.Validate() != nil {
		return getResourceArguments{}, ResourceQueryRequest{}, ErrInvalidCanonicalArguments
	}
	return arguments, request, nil
}

func decodeListResourcesCall(call BoundToolCall) (listResourcesArguments, ResourceQueryRequest, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameListResources || call.Version() != agent.ToolCatalogVersion {
		return listResourcesArguments{}, ResourceQueryRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments listResourcesArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() ||
		arguments.Format != domain.ResourceViewList && arguments.Format != domain.ResourceViewCount && arguments.Format != domain.ResourceViewTable {
		return listResourcesArguments{}, ResourceQueryRequest{}, ErrInvalidCanonicalArguments
	}
	policy := call.ResourcePolicy()
	filters := make([]domain.ResourceFilter, len(arguments.Filters))
	for index, filter := range arguments.Filters {
		filters[index] = domain.ResourceFilter{Field: filter.Field, Operator: filter.Operator, Value: filter.Value}
	}
	allNamespaces := arguments.Namespace == "*"
	namespace := arguments.Namespace
	if allNamespaces {
		namespace = ""
	}
	request := ResourceQueryRequest{Policy: policy, Query: domain.ResourceQuery{
		Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), PolicyVersion: domain.ResourcePolicyVersion,
		Type: policy.Type, Verb: domain.ResourceVerbList, View: arguments.Format, Namespace: namespace,
		AllNamespaces: allNamespaces, Filters: filters, Limits: boundedResourceQueryLimits(call, policy, arguments.Limit),
	}}
	if request.Validate() != nil || arguments.Limit < 1 || arguments.Limit > domain.MaxResourceSummaries {
		return listResourcesArguments{}, ResourceQueryRequest{}, ErrInvalidCanonicalArguments
	}
	return arguments, request, nil
}

func boundedResourceQueryLimits(call BoundToolCall, policy domain.ResourcePolicy, returned int) domain.ResourceQueryLimits {
	ceilings := call.Ceilings()
	if returned < 1 {
		returned = 1
	}
	limits := domain.ResourceQueryLimits{
		MaxPages:    min(policy.Limits.MaxPages, ceilings.MaxResourcePages),
		PageItems:   min(policy.Limits.PageItems, ceilings.MaxResourcePageItems),
		PageBytes:   min(policy.Limits.PageBytes, ceilings.MaxResourcePageBytes),
		MaxItems:    min(policy.Limits.MaxItems, ceilings.MaxResourceScannedItems),
		MaxBytes:    min(policy.Limits.MaxBytes, ceilings.MaxResourceBytes),
		MaxReturned: min(policy.Limits.MaxReturned, ceilings.MaxResourceItems, returned),
	}
	if returned == 1 && call.Name() == domain.ToolNameGetResource {
		limits.MaxPages, limits.PageItems, limits.MaxItems, limits.MaxReturned = 1, 1, 1, 1
	}
	return limits
}

func decodeRelatedResourcesCall(call BoundToolCall) (getRelatedResourcesArguments, RelatedReadRequest, bool, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameGetRelatedResources || call.Version() != agent.ToolCatalogVersion {
		return getRelatedResourcesArguments{}, RelatedReadRequest{}, false, ErrInvalidCanonicalArguments
	}
	var arguments getRelatedResourcesArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() ||
		arguments.RelationDepth < 1 || arguments.RelationDepth > 2 {
		return getRelatedResourcesArguments{}, RelatedReadRequest{}, false, ErrInvalidCanonicalArguments
	}
	effectiveDepth := min(arguments.RelationDepth, call.Ceilings().MaxRelationshipHops, 2)
	request := RelatedReadRequest{
		Scope: call.Scope(),
		Reference: domain.ResourceRef{
			APIVersion: arguments.Resource.APIVersion,
			Kind:       arguments.Resource.Kind,
			Namespace:  arguments.Resource.Namespace,
			Name:       arguments.Resource.Name,
			UID:        arguments.Resource.UID,
		},
		Depth: effectiveDepth, Includes: append([]RelatedInclude(nil), arguments.Include...),
		MaxNodes: min(call.Ceilings().MaxRelationshipNodes, 25),
		MaxEdges: min(call.Ceilings().MaxRelationshipEdges, 40),
	}
	if request.Validate() != nil {
		return getRelatedResourcesArguments{}, RelatedReadRequest{}, false, ErrInvalidCanonicalArguments
	}
	return arguments, request, effectiveDepth < arguments.RelationDepth, nil
}
