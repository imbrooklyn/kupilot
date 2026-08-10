package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestResourceToolSchemasRemainStrictScopeFreeAndPurposeBound(t *testing.T) {
	t.Parallel()

	specifications := agent.ToolSpecifications()
	wanted := map[domain.ToolName]bool{
		domain.ToolNameGetResource:   false,
		domain.ToolNameListResources: false,
	}
	for _, specification := range specifications {
		if _, relevant := wanted[specification.Name]; !relevant {
			continue
		}
		wanted[specification.Name] = true
		if specification.Version != agent.ToolCatalogVersion || specification.Validate() != nil || specification.Description == "" {
			t.Fatalf("invalid resource Tool specification: %#v", specification)
		}
		schema := strings.ToLower(specification.InputSchemaJSON)
		for _, required := range []string{`"additionalproperties":false`, `"purpose"`} {
			if !strings.Contains(schema, required) {
				t.Fatalf("Tool %q schema is missing %s", specification.Name, required)
			}
		}
		for _, prohibited := range []string{`"context"`, `"namespace"`, `"scope"`, `"gvr"`, `"raw_selector"`, `"hard_limit"`, `"continue"`} {
			if strings.Contains(schema, prohibited) {
				t.Fatalf("Tool %q schema contains prohibited authority field %s", specification.Name, prohibited)
			}
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("fixed catalog is missing %q", name)
		}
	}
}

func TestResourceToolResultSupportsCanonicalInvocationAuditContract(t *testing.T) {
	t.Parallel()

	reader := &fakeResourceReader{getFn: func(context.Context, ResourceReadRequest) (ResourceObservation, error) {
		return resourceObservation(domain.ResourceKindPod, "sample-pod"), nil
	}}
	tool, err := NewGetResourceTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewGetResourceTool() error = %v", err)
	}
	call := boundGetCall(t, testRunInput(t, 0), `{"purpose":"Inspect one Pod.","resource":{"kind":"Pod","name":"sample-pod"}}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil {
		t.Fatalf("Execute() result validation = %v", result.Validate())
	}
	purpose := call.Purpose()
	startedAt := call.Scope().ActivatedAt
	finishedAt := result.ObservedAt
	invocation := domain.ToolInvocation{
		ID:              call.InvocationID(),
		RunID:           call.RunID(),
		Sequence:        1,
		Name:            call.Name(),
		Version:         call.Version(),
		Purpose:         &purpose,
		Scope:           call.Scope().Snapshot(),
		ArgumentsJSON:   call.ArgumentsJSON(),
		ArgumentsDigest: call.Identity().ArgumentsDigest,
		Status:          domain.ToolInvocationStatusSucceeded,
		ReturnedBytes:   result.Truncation.ReturnedBytes,
		EvidenceCount:   len(result.Evidence),
		Truncated:       result.Truncation.Truncated,
		StartedAt:       &startedAt,
		FinishedAt:      &finishedAt,
	}
	if invocation.Validate() != nil {
		t.Fatalf("ToolInvocation audit derivative = %#v, validation = %v", invocation, invocation.Validate())
	}
}

func TestResourceToolBindingCanonicalizesDefaultsWithoutScopeOrHardCeilings(t *testing.T) {
	t.Parallel()

	input := testRunInput(t, 0)
	getCall := boundGetCall(t, input, `{"purpose":"Inspect one Pod.","resource":{"kind":"Pod","name":"sample-pod"}}`)
	if got := getCall.ArgumentsJSON(); got != `{"detail":"diagnostic","purpose":"Inspect one Pod.","resource":{"api_version":"v1","kind":"Pod","name":"sample-pod"}}` {
		t.Fatalf("canonical get_resource arguments = %s", got)
	}
	listCall := boundListCall(t, input, `{"kind":"Pod","purpose":"Find abnormal Pods."}`)
	if got := listCall.ArgumentsJSON(); got != `{"health_filter":"abnormal","kind":"Pod","limit":20,"purpose":"Find abnormal Pods."}` {
		t.Fatalf("canonical list_resources arguments = %s", got)
	}
	for _, value := range []string{getCall.ArgumentsJSON(), listCall.ArgumentsJSON()} {
		lower := strings.ToLower(value)
		for _, prohibited := range []string{"context", "namespace", "gvr", "selector", "max_result_bytes"} {
			if strings.Contains(lower, prohibited) {
				t.Fatalf("canonical arguments contain runtime authority %q: %s", prohibited, value)
			}
		}
	}
}
