package tools

import (
	"encoding/json"
	"errors"
	"strings"

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
	Detail   ResourceDetail   `json:"detail"`
	Purpose  string           `json:"purpose"`
	Resource resourceArgument `json:"resource"`
}

type listResourcesArguments struct {
	HealthFilter string `json:"health_filter"`
	Kind         string `json:"kind"`
	Limit        int    `json:"limit"`
	NameQuery    string `json:"name_query,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	Purpose      string `json:"purpose"`
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

func decodeGetResourceCall(call BoundToolCall) (getResourceArguments, ResourceReadRequest, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameGetResource || call.Version() != agent.ToolCatalogVersion {
		return getResourceArguments{}, ResourceReadRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments getResourceArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() || !arguments.Detail.valid() {
		return getResourceArguments{}, ResourceReadRequest{}, ErrInvalidCanonicalArguments
	}
	request := ResourceReadRequest{
		Scope: call.Scope(),
		Reference: domain.ResourceRef{
			APIVersion: arguments.Resource.APIVersion,
			Kind:       arguments.Resource.Kind,
			Namespace:  arguments.Resource.Namespace,
			Name:       arguments.Resource.Name,
		},
		Detail: arguments.Detail,
	}
	if request.Validate() != nil {
		return getResourceArguments{}, ResourceReadRequest{}, ErrInvalidCanonicalArguments
	}
	return arguments, request, nil
}

func decodeListResourcesCall(call BoundToolCall) (listResourcesArguments, ResourceListRequest, error) {
	if call.Validate() != nil || call.Name() != domain.ToolNameListResources || call.Version() != agent.ToolCatalogVersion {
		return listResourcesArguments{}, ResourceListRequest{}, ErrInvalidCanonicalArguments
	}
	var arguments listResourcesArguments
	if json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() ||
		arguments.HealthFilter != "any" && arguments.HealthFilter != "abnormal" ||
		len(arguments.NameQuery) > 128 || strings.TrimSpace(arguments.NameQuery) != arguments.NameQuery {
		return listResourcesArguments{}, ResourceListRequest{}, ErrInvalidCanonicalArguments
	}
	effectiveLimit := min(arguments.Limit, call.Ceilings().MaxResourceItems, domain.MaxResourceSummaries)
	request := ResourceListRequest{
		Scope: call.Scope(), Kind: domain.ResourceKind(arguments.Kind), Namespace: arguments.Namespace,
		AllNamespaces: arguments.Namespace == "*", Limit: effectiveLimit,
	}
	if request.AllNamespaces {
		request.Namespace = ""
	}
	if request.Validate() != nil || arguments.Limit < 1 || arguments.Limit > domain.MaxResourceSummaries {
		return listResourcesArguments{}, ResourceListRequest{}, ErrInvalidCanonicalArguments
	}
	return arguments, request, nil
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
