package tools

import (
	"encoding/json"
	"strings"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type resourceArgument struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
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
	Purpose      string `json:"purpose"`
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
			Namespace:  call.Scope().Namespace,
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
	request := ResourceListRequest{Scope: call.Scope(), Kind: domain.ResourceKind(arguments.Kind), Limit: effectiveLimit}
	if request.Validate() != nil || arguments.Limit < 1 || arguments.Limit > domain.MaxResourceSummaries {
		return listResourcesArguments{}, ResourceListRequest{}, ErrInvalidCanonicalArguments
	}
	return arguments, request, nil
}
