package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestToolCatalogIsExactStrictAndScopeFree(t *testing.T) {
	specifications := ToolSpecifications()
	want := []domain.ToolName{
		domain.ToolNameGetResource,
		domain.ToolNameListResources,
		domain.ToolNameGetEvents,
		domain.ToolNameGetPodLogs,
		domain.ToolNameGetPreviousPodLogs,
		domain.ToolNameGetRelatedResources,
	}
	if len(specifications) != len(want) {
		t.Fatalf("Tool specification count = %d, want %d", len(specifications), len(want))
	}
	for index, specification := range specifications {
		if specification.Name != want[index] {
			t.Fatalf("Tool[%d] = %q, want %q", index, specification.Name, want[index])
		}
		if specification.Version != ToolCatalogVersion || specification.Validate() != nil {
			t.Fatalf("invalid Tool specification: %#v", specification)
		}
		lowerSchema := strings.ToLower(specification.InputSchemaJSON)
		for _, prohibited := range []string{`"context"`, `"namespace"`, `"scope"`, `"gvr"`, `"endpoint"`, `"credential"`, `"kubeconfig"`, `"deadline"`, `"max_bytes"`, `"max_items"`} {
			if strings.Contains(lowerSchema, prohibited) {
				t.Fatalf("Tool %q schema contains prohibited field %s", specification.Name, prohibited)
			}
		}
	}

	copyOfCatalog := ToolSpecifications()
	copyOfCatalog[0].Name = "changed"
	if ToolSpecifications()[0].Name != domain.ToolNameGetResource {
		t.Fatal("ToolSpecifications() returned mutable catalog storage")
	}
}

func TestToolCallPolicyRejectsUnknownForbiddenAndInvalidBeforeHandler(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	tool := &fakeTool{result: func(call BoundToolCall) domain.ToolResult {
		return testToolResult(t, call, testEvidenceID, input.Scope().ActivatedAt)
	}}
	handlers := testToolHandlers(tool)
	tests := []struct {
		name string
		call domain.ModelToolCall
	}{
		{name: "unknown Tool", call: domain.ModelToolCall{ID: "call-1", Name: "run_shell", ArgumentsJSON: `{}`}},
		{name: "scope field", call: domain.ModelToolCall{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"context":"other","purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`}},
		{name: "hard limit field", call: domain.ModelToolCall{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"max_bytes":999999,"purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`}},
		{name: "duplicate nested field", call: domain.ModelToolCall{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":"Inspect.","resource":{"kind":"Pod","kind":"Secret","name":"sample-pod"}}`}},
		{name: "extra field", call: domain.ModelToolCall{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"},"surprise":true}`}},
		{name: "Secret Kind", call: domain.ModelToolCall{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":"Inspect.","resource":{"kind":"Secret","name":"sample-secret"}}`}},
		{name: "wrong type", call: domain.ModelToolCall{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":7,"resource":{"kind":"Pod","name":"sample-pod"}}`}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			bound, err := BindToolCall(input, testInvocationID, current.call)
			if err == nil {
				handler, resolveErr := handlers.Resolve(bound.Name())
				if resolveErr == nil {
					_ = handler.Execute(context.Background(), bound)
				}
				t.Fatal("BindToolCall() error = nil")
			}
			if tool.Calls() != 0 {
				t.Fatalf("handler calls = %d, want 0", tool.Calls())
			}
		})
	}
}

func TestToolCallBindingInjectsScopeCeilingsAndCanonicalDefaults(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	if call.Scope() != input.Scope() || call.Scope().Namespace != "test-namespace" {
		t.Fatalf("bound scope = %#v", call.Scope())
	}
	if strings.Contains(strings.ToLower(call.ArgumentsJSON()), `"namespace"`) {
		t.Fatalf("canonical arguments contain scope: %s", call.ArgumentsJSON())
	}
	if !strings.Contains(call.ArgumentsJSON(), `"api_version":"v1"`) || !strings.Contains(call.ArgumentsJSON(), `"detail":"diagnostic"`) {
		t.Fatalf("canonical arguments did not inject defaults: %s", call.ArgumentsJSON())
	}
	ceilings := call.Ceilings()
	if ceilings.RequestTimeout != maxToolRequestDuration ||
		ceilings.MaxResultBytes != domain.MaxToolResultBytes ||
		ceilings.MaxEvidenceItems != domain.MaxEvidenceItemsPerResult ||
		ceilings.MaxResourceItems != 50 || ceilings.MaxEventItems != 50 ||
		ceilings.MaxLogLines != 200 || ceilings.MaxLogWindow != 15*time.Minute ||
		ceilings.MaxRelationshipHops != 2 || ceilings.MaxRelationshipNodes != 25 || ceilings.MaxRelationshipEdges != 40 {
		t.Fatalf("bound ceilings = %#v", ceilings)
	}

	boundedList, err := BindToolCall(input, invocationID(1), domain.ModelToolCall{
		ID:            "call-2",
		Name:          domain.ToolNameListResources,
		ArgumentsJSON: `{"health_filter":"any","kind":"Pod","limit":5,"purpose":"Find bounded Pod candidates."}`,
	})
	if err != nil {
		t.Fatalf("BindToolCall(list with request limit) error = %v", err)
	}
	if !strings.Contains(boundedList.ArgumentsJSON(), `"limit":5`) {
		t.Fatalf("canonical list arguments = %s", boundedList.ArgumentsJSON())
	}
}
