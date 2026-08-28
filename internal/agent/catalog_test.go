package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	listDescription := specifications[1].Description
	for _, required := range []string{
		"Pod, Deployment, ReplicaSet, Job, or Service",
		"Never use this Tool for Namespace discovery",
		"Node inventory",
		"cluster-wide inventory",
	} {
		if !strings.Contains(listDescription, required) {
			t.Fatalf("list_resources description missing %q", required)
		}
	}

	copyOfCatalog := ToolSpecifications()
	copyOfCatalog[0].Name = "changed"
	if ToolSpecifications()[0].Name != domain.ToolNameGetResource {
		t.Fatal("ToolSpecifications() returned mutable catalog storage")
	}
}

func TestToolCatalogNullableDefaultsCanonicalizeLocally(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	tests := []struct {
		name      string
		tool      domain.ToolName
		arguments string
		want      []string
	}{
		{
			name:      "get resource",
			tool:      domain.ToolNameGetResource,
			arguments: `{"detail":null,"purpose":"Inspect the selected Pod.","resource":{"api_version":null,"kind":"Pod","name":"sample-pod"}}`,
			want:      []string{`"api_version":"v1"`, `"detail":"diagnostic"`},
		},
		{
			name:      "list resources",
			tool:      domain.ToolNameListResources,
			arguments: `{"health_filter":null,"kind":"Pod","limit":null,"name_query":null,"purpose":"Find bounded Pod candidates."}`,
			want:      []string{`"health_filter":"abnormal"`, `"limit":20`},
		},
		{
			name:      "get events",
			tool:      domain.ToolNameGetEvents,
			arguments: `{"limit":null,"purpose":"Inspect recent Pod events.","resource":{"api_version":null,"kind":"Pod","name":"sample-pod","uid":null},"since_seconds":null}`,
			want:      []string{`"api_version":"v1"`, `"limit":30`, `"since_seconds":3600`},
		},
		{
			name:      "get Pod logs",
			tool:      domain.ToolNameGetPodLogs,
			arguments: `{"container":null,"pod_name":"sample-pod","purpose":"Inspect current Pod logs.","since_seconds":null,"tail_lines":null}`,
			want:      []string{`"since_seconds":900`, `"tail_lines":200`},
		},
		{
			name:      "get previous Pod logs",
			tool:      domain.ToolNameGetPreviousPodLogs,
			arguments: `{"container":null,"pod_name":"sample-pod","purpose":"Inspect previous Pod logs.","since_seconds":null,"tail_lines":null}`,
			want:      []string{`"since_seconds":900`, `"tail_lines":200`},
		},
		{
			name:      "get related resources",
			tool:      domain.ToolNameGetRelatedResources,
			arguments: `{"include":null,"purpose":"Inspect bounded Pod relationships.","relation_depth":null,"resource":{"api_version":null,"kind":"Pod","name":"sample-pod","uid":null}}`,
			want:      []string{`"include":["owners","service_endpoints","services"]`, `"relation_depth":2`},
		},
	}

	for index, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			bound, err := BindToolCall(input, invocationID(index+20), domain.ModelToolCall{
				ID:            "nullable-call",
				Name:          current.tool,
				ArgumentsJSON: current.arguments,
			})
			if err != nil {
				t.Fatalf("BindToolCall() error = %v", err)
			}
			if strings.Contains(bound.ArgumentsJSON(), "null") {
				t.Fatalf("canonical arguments retained null: %s", bound.ArgumentsJSON())
			}
			for _, wanted := range current.want {
				if !strings.Contains(bound.ArgumentsJSON(), wanted) {
					t.Fatalf("canonical arguments = %s, missing %s", bound.ArgumentsJSON(), wanted)
				}
			}
		})
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
		{name: "null required field", call: domain.ModelToolCall{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"detail":null,"purpose":null,"resource":{"api_version":null,"kind":"Pod","name":"sample-pod"}}`}},
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

func TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	canary := strings.Join([]string{"synthetic", "model", "binding", "canary", "4401"}, "-")
	purposeJSON, err := json.Marshal("Inspect the selected Pod; token=" + canary)
	if err != nil {
		t.Fatalf("json.Marshal(purpose) error = %v", err)
	}
	bound, err := BindToolCall(input, testInvocationID, domain.ModelToolCall{
		ID:            "call-1",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"purpose":` + string(purposeJSON) + `,"resource":{"kind":"Pod","name":"sample-pod"}}`,
	})
	if err != nil {
		t.Fatalf("BindToolCall(sensitive replacement) error = %v", err)
	}
	if strings.Contains(bound.Purpose(), canary) || strings.Contains(bound.ArgumentsJSON(), canary) ||
		!strings.Contains(bound.Purpose(), "[REDACTED]") || !strings.Contains(bound.ArgumentsJSON(), "[REDACTED]") {
		t.Fatalf("sanitized BoundToolCall = %#v", bound)
	}

	controlJSON, err := json.Marshal("\x1b[31mInspect the selected Pod.\x1b[0m\u202e")
	if err != nil {
		t.Fatalf("json.Marshal(control purpose) error = %v", err)
	}
	_, err = BindToolCall(input, invocationID(4), domain.ModelToolCall{
		ID:            "call-4",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"purpose":` + string(controlJSON) + `,"resource":{"kind":"Pod","name":"sample-pod"}}`,
	})
	if !errors.Is(err, ErrToolPolicyDenied) {
		t.Fatalf("BindToolCall(control purpose) error = %v, want %v", err, ErrToolPolicyDenied)
	}

	queryJSON, err := json.Marshal("token=" + canary)
	if err != nil {
		t.Fatalf("json.Marshal(name query) error = %v", err)
	}
	listed, err := BindToolCall(input, invocationID(2), domain.ModelToolCall{
		ID:            "call-2",
		Name:          domain.ToolNameListResources,
		ArgumentsJSON: `{"kind":"Pod","name_query":` + string(queryJSON) + `,"purpose":"Find matching Pods."}`,
	})
	if err != nil || strings.Contains(listed.ArgumentsJSON(), canary) || !strings.Contains(listed.ArgumentsJSON(), "[REDACTED]") {
		t.Fatalf("sanitized list_resources call/error = %#v/%v", listed, err)
	}

	blockedCanary := strings.Join([]string{"synthetic", "blocked", "binding", "canary", "4402"}, "-")
	blockedPurpose := strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\n" +
		blockedCanary + "\n" + strings.Join([]string{"-----END", "PRIVATE", "KEY-----"}, " ")
	blockedJSON, err := json.Marshal(blockedPurpose)
	if err != nil {
		t.Fatalf("json.Marshal(blocked purpose) error = %v", err)
	}
	_, err = BindToolCall(input, invocationID(3), domain.ModelToolCall{
		ID:            "call-3",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"purpose":` + string(blockedJSON) + `,"resource":{"kind":"Pod","name":"sample-pod"}}`,
	})
	if !errors.Is(err, ErrSensitiveModelTextBlocked) ||
		strings.Contains(err.Error(), blockedCanary) || strings.Contains(err.Error(), blockedPurpose) {
		t.Fatalf("blocked BindToolCall error = %v", err)
	}
}

func FuzzBindToolCallStrictSchema(f *testing.F) {
	for _, seed := range []struct {
		name      string
		arguments string
	}{
		{name: string(domain.ToolNameGetResource), arguments: `{"purpose":"Inspect one Pod.","resource":{"kind":"Pod","name":"sample-pod"}}`},
		{name: string(domain.ToolNameGetResource), arguments: `{"namespace":"other","purpose":"Inspect one Pod.","resource":{"kind":"Pod","name":"sample-pod"}}`},
		{name: string(domain.ToolNameGetResource), arguments: `{"purpose":"Inspect one Pod.","resource":{"kind":"Pod","kind":"Secret","name":"sample-pod"}}`},
		{name: string(domain.ToolNameListResources), arguments: `{"kind":"Pod","limit":51,"purpose":"Find Pods."}`},
		{name: "run_shell", arguments: `{}`},
		{name: string(domain.ToolNameGetPodLogs), arguments: `{"pod_name":"sample-pod","purpose":"Inspect logs.","tail_lines":200}`},
	} {
		f.Add(seed.name, seed.arguments)
	}

	f.Fuzz(func(t *testing.T, name, arguments string) {
		if len(name) > domain.MaxModelToolNameBytes+1 || len(arguments) > domain.MaxModelToolArgumentsBytes+1 {
			return
		}
		input := testRunInput(t, "Inspect the selected Pod.")
		selection := domain.ModelToolCall{
			ID: "fuzz-call", Name: domain.ToolName(name), ArgumentsJSON: arguments,
		}
		bound, err := BindToolCall(input, testInvocationID, selection)
		if err != nil {
			return
		}
		if bound.Validate() != nil || bound.Scope() != input.Scope() || bound.Name() != selection.Name {
			t.Fatalf("successful binding violated runtime invariants: %#v", bound)
		}
		canonical := bound.ArgumentsJSON()
		for _, prohibited := range []string{
			`"context":`, `"namespace":`, `"scope":`, `"gvr":`, `"selector":`,
			`"endpoint":`, `"credential":`, `"kubeconfig":`, `"deadline":`,
			`"max_bytes":`, `"max_items":`,
		} {
			if strings.Contains(canonical, prohibited) {
				t.Fatalf("canonical Tool arguments contain prohibited authority %s: %s", prohibited, canonical)
			}
		}
		if validation := (domain.ModelToolCall{
			ID: selection.ID, Name: bound.Name(), ArgumentsJSON: canonical,
		}).Validate(); validation != nil {
			t.Fatalf("bound canonical Tool arguments are invalid: %v", validation)
		}
	})
}
