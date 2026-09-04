package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestToolCatalogIsExactStrictAndPolicyBound(t *testing.T) {
	specifications := ToolSpecifications()
	want := []domain.ToolName{
		domain.ToolNameGetResource,
		domain.ToolNameListResources,
		domain.ToolNameGetEvents,
		domain.ToolNameGetPodLogs,
		domain.ToolNameGetPreviousPodLogs,
		domain.ToolNameGetPodMetrics,
		domain.ToolNameGetNodeMetrics,
		domain.ToolNameQueryPrometheus,
		domain.ToolNameQueryLoki,
		domain.ToolNameGetRelatedResources,
		domain.ToolNameGetClusterOverview,
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
		for _, prohibited := range []string{`"context"`, `"scope"`, `"gvr"`, `"endpoint"`, `"credential"`, `"kubeconfig"`, `"deadline"`, `"max_bytes"`, `"max_items"`} {
			if strings.Contains(lowerSchema, prohibited) {
				t.Fatalf("Tool %q schema contains prohibited field %s", specification.Name, prohibited)
			}
		}
	}
	listDescription := specifications[1].Description
	for _, required := range []string{
		"local resource_type ID",
		"code-defined field/operator predicates",
		"Raw selectors and continuation tokens are not accepted",
		"'*' requires namespace_access=all",
	} {
		if !strings.Contains(listDescription, required) {
			t.Fatalf("list_resources description missing %q", required)
		}
	}
	if !strings.Contains(specifications[10].Description, "which Nodes and/or Namespaces exist") {
		t.Fatalf("get_cluster_overview description = %q", specifications[10].Description)
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
			arguments: `{"detail":null,"name":"sample-pod","namespace":null,"purpose":"Inspect the selected Pod.","resource_type":"pods"}`,
			want:      []string{`"detail":"describe"`, `"namespace":"test-namespace"`, `"resource_type":"pods"`},
		},
		{
			name:      "list resources",
			tool:      domain.ToolNameListResources,
			arguments: `{"filters":null,"format":null,"limit":null,"namespace":null,"purpose":"Find bounded Pod candidates.","resource_type":"pods"}`,
			want:      []string{`"filters":[]`, `"format":"list"`, `"limit":20`, `"resource_type":"pods"`},
		},
		{
			name:      "get events",
			tool:      domain.ToolNameGetEvents,
			arguments: `{"limit":null,"purpose":"Inspect recent Pod events.","reason":null,"resource":{"api_version":null,"kind":"Pod","name":"sample-pod","namespace":null,"uid":null},"since_seconds":null,"type":null}`,
			want:      []string{`"api_version":"v1"`, `"limit":30`, `"since_seconds":3600`},
		},
		{
			name:      "get Pod logs",
			tool:      domain.ToolNameGetPodLogs,
			arguments: `{"container":null,"container_mode":null,"include_ephemeral":null,"include_init":null,"namespace":null,"pod_name":"sample-pod","purpose":"Inspect current Pod logs.","search":null,"since_seconds":null,"tail_lines":null}`,
			want:      []string{`"since_seconds":900`, `"tail_lines":200`},
		},
		{
			name:      "get previous Pod logs",
			tool:      domain.ToolNameGetPreviousPodLogs,
			arguments: `{"container":null,"container_mode":null,"include_ephemeral":null,"include_init":null,"namespace":null,"pod_name":"sample-pod","purpose":"Inspect previous Pod logs.","search":null,"since_seconds":null,"tail_lines":null}`,
			want:      []string{`"since_seconds":900`, `"tail_lines":200`},
		},
		{
			name:      "get related resources",
			tool:      domain.ToolNameGetRelatedResources,
			arguments: `{"include":null,"purpose":"Inspect bounded Pod relationships.","relation_depth":null,"resource":{"api_version":null,"kind":"Pod","name":"sample-pod","uid":null}}`,
			want:      []string{`"include":["owners","service_endpoints","services"]`, `"relation_depth":2`},
		},
		{
			name:      "get cluster overview",
			tool:      domain.ToolNameGetClusterOverview,
			arguments: `{"limit":null,"purpose":"Inspect cluster health."}`,
			want:      []string{`"limit":20`},
		},
	}

	for index, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			bound, err := BindToolCall(input, invocationID(index+20), ToolSelection{
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
		call ToolSelection
	}{
		{name: "unknown Tool", call: ToolSelection{ID: "call-1", Name: "run_shell", ArgumentsJSON: `{}`}},
		{name: "scope field", call: ToolSelection{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"context":"other","purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`}},
		{name: "hard limit field", call: ToolSelection{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"max_bytes":999999,"purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`}},
		{name: "duplicate nested field", call: ToolSelection{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":"Inspect.","resource":{"kind":"Pod","kind":"Secret","name":"sample-pod"}}`}},
		{name: "extra field", call: ToolSelection{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"},"surprise":true}`}},
		{name: "Secret Kind", call: ToolSelection{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":"Inspect.","resource":{"kind":"Secret","name":"sample-secret"}}`}},
		{name: "wrong type", call: ToolSelection{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"purpose":7,"resource":{"kind":"Pod","name":"sample-pod"}}`}},
		{name: "null required field", call: ToolSelection{ID: "call-1", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"detail":null,"purpose":null,"resource":{"api_version":null,"kind":"Pod","name":"sample-pod"}}`}},
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

func TestToolBindingClassifiesMalformedArgumentsSeparatelyFromCorrectablePolicyDenial(t *testing.T) {
	input := testRunInput(t, "Inspect cluster resources.")
	_, malformedErr := BindToolCall(input, invocationID(30), ToolSelection{
		ID:            "call-malformed-known-tool",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":"Inspect one Pod.","resource_type":"pods","surprise":true}`,
	})
	if !errors.Is(malformedErr, ErrToolPolicyDenied) || !errors.Is(malformedErr, ErrToolArgumentsRejected) {
		t.Fatalf("malformed known-Tool error = %v", malformedErr)
	}

	_, policyErr := BindToolCall(input, invocationID(31), ToolSelection{
		ID:            "call-correctable-policy",
		Name:          domain.ToolNameListResources,
		ArgumentsJSON: `{"filters":[],"format":"list","limit":20,"namespace":"test-namespace","purpose":"List Nodes.","resource_type":"nodes"}`,
	})
	if !errors.Is(policyErr, ErrToolPolicyDenied) || errors.Is(policyErr, ErrToolArgumentsRejected) {
		t.Fatalf("correctable policy error = %v", policyErr)
	}
}

func TestBroadResourceBindingDenialsCallNoHandler(t *testing.T) {
	base := testRunInput(t, "Inspect cluster resources.")
	sensitive := testRunInputWithSensitiveResourcePolicy(t, base)
	tool := &fakeTool{result: func(call BoundToolCall) domain.ToolResult {
		return testToolResult(t, call, testEvidenceID, base.Scope().ActivatedAt)
	}}
	handlers := testToolHandlers(tool)
	tests := []struct {
		name      string
		input     RunInput
		tool      domain.ToolName
		arguments string
	}{
		{name: "missing required namespace", input: base, tool: domain.ToolNameGetResource, arguments: `{"detail":"describe","name":"sample-pod","purpose":"Inspect one Pod.","resource_type":"pods"}`},
		{name: "duplicate resource type", input: base, tool: domain.ToolNameGetResource, arguments: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":"Inspect one Pod.","resource_type":"pods","resource_type":"nodes"}`},
		{name: "duplicate nested filter", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[{"field":"phase","field":"name","operator":"equals","value":"Running"}],"format":"list","limit":20,"namespace":null,"purpose":"List Pods.","resource_type":"pods"}`},
		{name: "missing filter value", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[{"field":"phase","operator":"equals"}],"format":"list","limit":20,"namespace":null,"purpose":"List Pods.","resource_type":"pods"}`},
		{name: "unknown resource type", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","limit":20,"namespace":null,"purpose":"List resources.","resource_type":"invented"}`},
		{name: "model selected group", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","group":"example.test","limit":20,"namespace":null,"purpose":"List resources.","resource_type":"pods"}`},
		{name: "model selected verb", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","limit":20,"namespace":null,"purpose":"List resources.","resource_type":"pods","verb":"watch"}`},
		{name: "model selected subresource", input: base, tool: domain.ToolNameGetResource, arguments: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":"Inspect one Pod.","resource_type":"pods","subresource":"status"}`},
		{name: "raw selector", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","limit":20,"namespace":null,"purpose":"List resources.","raw_selector":"metadata.name=sample-pod","resource_type":"pods"}`},
		{name: "raw JSONPath", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","jsonpath":"{.items[*]}","limit":20,"namespace":null,"purpose":"List resources.","resource_type":"pods"}`},
		{name: "malformed continuation", input: base, tool: domain.ToolNameListResources, arguments: `{"continuation":"!!!","filters":[],"format":"list","limit":20,"namespace":null,"purpose":"List resources.","resource_type":"pods"}`},
		{name: "cross namespace under current policy", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","limit":20,"namespace":"other-namespace","purpose":"List Pods.","resource_type":"pods"}`},
		{name: "all namespaces under current policy", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","limit":20,"namespace":"*","purpose":"List Pods.","resource_type":"pods"}`},
		{name: "namespace on cluster resource", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[],"format":"list","limit":20,"namespace":"test-namespace","purpose":"List Nodes.","resource_type":"nodes"}`},
		{name: "unknown projected field", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[{"field":"spec","operator":"equals","value":"raw"}],"format":"list","limit":20,"namespace":null,"purpose":"List Pods.","resource_type":"pods"}`},
		{name: "invalid typed predicate", input: base, tool: domain.ToolNameListResources, arguments: `{"filters":[{"field":"desired","operator":"greater_than","value":"many"}],"format":"list","limit":20,"namespace":null,"purpose":"List Deployments.","resource_type":"deployments"}`},
		{name: "sensitive projected field", input: sensitive, tool: domain.ToolNameListResources, arguments: `{"filters":[{"field":"credential_ref","operator":"equals","value":"sample"}],"format":"list","limit":20,"namespace":null,"purpose":"List Widgets.","resource_type":"widgets"}`},
	}
	for index, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			bound, err := BindToolCall(current.input, invocationID(index+40), ToolSelection{
				ID: "denied-broad-read", Name: current.tool, ArgumentsJSON: current.arguments,
			})
			if err == nil {
				handler, resolveErr := handlers.Resolve(bound.Name())
				if resolveErr == nil {
					_ = handler.Execute(context.Background(), bound)
				}
				t.Fatal("BindToolCall() error = nil")
			}
			if !errors.Is(err, ErrToolPolicyDenied) || tool.Calls() != 0 {
				t.Fatalf("binding error/handler calls = %v/%d", err, tool.Calls())
			}
		})
	}
}

func testRunInputWithSensitiveResourcePolicy(t *testing.T, base RunInput) RunInput {
	t.Helper()
	entries := base.ResourcePolicies().Entries()
	entries = append(entries, domain.ResourcePolicy{
		Type: domain.ResourceType{
			ID: "widgets", Group: "example.test", Version: "v1", Resource: "widgets",
			Kind: "Widget", Scope: domain.ResourceScopeNamespaced,
		},
		Verbs: []domain.ResourceVerb{domain.ResourceVerbGet, domain.ResourceVerbList},
		Fields: []domain.ResourceFieldPolicy{
			{ID: "name", Path: "metadata.name", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataMetadata, SelectorSource: domain.ResourceSelectorField, SelectorKey: "metadata.name", Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals}},
			{ID: "credential_ref", Path: "spec.credentialRef", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataSensitive, SelectorSource: domain.ResourceSelectorNone, Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals}},
		},
		Limits: domain.ResourceQueryLimits{MaxPages: 2, PageItems: 20, PageBytes: 256 * 1024, MaxItems: 40, MaxBytes: 1 << 20, MaxReturned: 20},
	})
	catalog, err := domain.NewResourcePolicyCatalog(domain.ResourcePolicyVersion, entries)
	if err != nil {
		t.Fatalf("NewResourcePolicyCatalog() error = %v", err)
	}
	input, err := NewRunInputWithPolicyContext(
		base.RunID(), base.SessionID(), base.RequestMessageID(), base.Question(), base.Scope(), base.Resource(),
		base.BudgetLimits(), base.Conversation(), catalog, base.PolicyGeneration(),
	)
	if err != nil {
		t.Fatalf("NewRunInputWithPolicyContext() error = %v", err)
	}
	return input
}

func TestToolCallBindingInjectsScopeCeilingsAndCanonicalDefaults(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	call := testBoundCall(t, input, testInvocationID, "sample-pod")
	if call.Scope() != input.Scope() || call.Scope().Namespace != "test-namespace" {
		t.Fatalf("bound scope = %#v", call.Scope())
	}
	if !strings.Contains(call.ArgumentsJSON(), `"namespace":"test-namespace"`) {
		t.Fatalf("canonical arguments do not contain the runtime-resolved Namespace: %s", call.ArgumentsJSON())
	}
	if !strings.Contains(call.ArgumentsJSON(), `"resource_type":"pods"`) || !strings.Contains(call.ArgumentsJSON(), `"detail":"describe"`) {
		t.Fatalf("canonical arguments did not inject defaults: %s", call.ArgumentsJSON())
	}
	ceilings := call.Ceilings()
	if ceilings.RequestTimeout != input.BudgetLimits().ToolRequestTimeout ||
		ceilings.MaxResultBytes != domain.MaxToolResultBytes ||
		ceilings.MaxEvidenceItems != domain.MaxEvidenceItemsPerResult ||
		ceilings.MaxResourceItems != input.BudgetLimits().ResourceReturnedItems ||
		ceilings.MaxResourceScannedItems != input.BudgetLimits().ResourceScannedItems ||
		ceilings.MaxResourcePages != input.BudgetLimits().ResourcePages ||
		ceilings.MaxResourcePageItems != input.BudgetLimits().ResourcePageItems ||
		ceilings.MaxResourcePageBytes != input.BudgetLimits().ResourcePageBytes ||
		ceilings.MaxResourceBytes != input.BudgetLimits().ResourceBytes || ceilings.MaxEventItems != 50 ||
		ceilings.MaxEventPages != input.BudgetLimits().EventPages || ceilings.MaxEventPageItems != input.BudgetLimits().EventPageItems ||
		ceilings.MaxEventPageBytes != input.BudgetLimits().EventPageBytes || ceilings.MaxEventBytes != input.BudgetLimits().EventBytes ||
		ceilings.MaxLogLines != input.BudgetLimits().DataSourceLines || ceilings.MaxLogContainers != input.BudgetLimits().LogContainers ||
		ceilings.MaxLogBytes != input.BudgetLimits().LogBytes || ceilings.MaxLogWindow != input.BudgetLimits().DataSourceWindow ||
		ceilings.MaxMetricContainers != input.BudgetLimits().MetricContainers || ceilings.MaxMetricBytes != input.BudgetLimits().MetricBytes ||
		ceilings.MaxDataSourcePages != input.BudgetLimits().DataSourcePages || ceilings.MaxDataSourceSeries != input.BudgetLimits().DataSourceSeries ||
		ceilings.MaxDataSourceSamples != input.BudgetLimits().DataSourceSamples || ceilings.MaxDataSourceLines != input.BudgetLimits().DataSourceLines ||
		ceilings.MaxDataSourceBytes != input.BudgetLimits().DataSourceBytes || ceilings.MaxDataSourceWindow != input.BudgetLimits().DataSourceWindow ||
		ceilings.MaxDataSourceStep != input.BudgetLimits().DataSourceStep ||
		ceilings.MaxRelationshipHops != 2 || ceilings.MaxRelationshipNodes != 25 || ceilings.MaxRelationshipEdges != 40 {
		t.Fatalf("bound ceilings = %#v", ceilings)
	}

	boundedList, err := BindToolCall(input, invocationID(1), ToolSelection{
		ID:            "call-2",
		Name:          domain.ToolNameListResources,
		ArgumentsJSON: `{"filters":[],"format":"list","limit":5,"namespace":null,"purpose":"Find bounded Pod candidates.","resource_type":"pods"}`,
	})
	if err != nil {
		t.Fatalf("BindToolCall(list with request limit) error = %v", err)
	}
	if !strings.Contains(boundedList.ArgumentsJSON(), `"limit":5`) {
		t.Fatalf("canonical list arguments = %s", boundedList.ArgumentsJSON())
	}
}

func TestToolCallBindingCanonicalizesNeutralProviderJSON(t *testing.T) {
	t.Parallel()

	input := testRunInput(t, "Inspect the selected Pod.")
	selection := ToolSelection{
		ID:            "call-noncanonical",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: ` { "resource_type": "pods", "purpose": "Inspect the selected Pod.", "namespace": null, "name": "sample-pod", "detail": null } `,
	}
	bound, err := BindToolCall(input, testInvocationID, selection)
	if err != nil {
		t.Fatalf("BindToolCall(noncanonical provider JSON) error = %v", err)
	}
	if got := bound.ArgumentsJSON(); strings.HasPrefix(got, " ") || !strings.Contains(got, `"name":"sample-pod"`) {
		t.Fatalf("bound arguments were not canonicalized: %q", got)
	}
	if err := bound.Validate(); err != nil {
		t.Fatalf("canonical BoundToolCall validation error = %v", err)
	}
}

func TestToolCallBindingPreservesSingleItemListsAndRequiresTwoItemOverview(t *testing.T) {
	input := testRunInput(t, "Inspect Kubernetes resources.")
	list, err := BindToolCall(input, invocationID(1), ToolSelection{
		ID:   "call-list-one",
		Name: domain.ToolNameListResources,
		ArgumentsJSON: `{"filters":[],"format":"list","limit":1,"namespace":null,` +
			`"purpose":"Inspect one Pod.","resource_type":"pods"}`,
	})
	if err != nil || !strings.Contains(list.ArgumentsJSON(), `"limit":1`) {
		t.Fatalf("BindToolCall(single list) = %#v, %v", list, err)
	}
	if _, err = BindToolCall(input, invocationID(2), ToolSelection{
		ID:            "call-overview-one",
		Name:          domain.ToolNameGetClusterOverview,
		ArgumentsJSON: `{"limit":1,"purpose":"Inspect cluster health."}`,
	}); err != ErrToolPolicyDenied {
		t.Fatalf("BindToolCall(single overview) error = %v, want %v", err, ErrToolPolicyDenied)
	}
}

func TestToolCallBindingSanitizesOrBlocksModelFreeTextBeforeHandler(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	canary := strings.Join([]string{"synthetic", "model", "binding", "canary", "4401"}, "-")
	purposeJSON, err := json.Marshal("Inspect the selected Pod; token=" + canary)
	if err != nil {
		t.Fatalf("json.Marshal(purpose) error = %v", err)
	}
	bound, err := BindToolCall(input, testInvocationID, ToolSelection{
		ID:            "call-1",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":` + string(purposeJSON) + `,"resource_type":"pods"}`,
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
	_, err = BindToolCall(input, invocationID(4), ToolSelection{
		ID:            "call-4",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":` + string(controlJSON) + `,"resource_type":"pods"}`,
	})
	if !errors.Is(err, ErrToolPolicyDenied) {
		t.Fatalf("BindToolCall(control purpose) error = %v, want %v", err, ErrToolPolicyDenied)
	}

	queryJSON, err := json.Marshal("token=" + canary)
	if err != nil {
		t.Fatalf("json.Marshal(name query) error = %v", err)
	}
	listed, err := BindToolCall(input, invocationID(2), ToolSelection{
		ID:            "call-2",
		Name:          domain.ToolNameListResources,
		ArgumentsJSON: `{"filters":[{"field":"phase","operator":"contains","value":` + string(queryJSON) + `}],"format":"list","limit":20,"namespace":null,"purpose":"Find matching Pods.","resource_type":"pods"}`,
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
	_, err = BindToolCall(input, invocationID(3), ToolSelection{
		ID:            "call-3",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":` + string(blockedJSON) + `,"resource_type":"pods"}`,
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
		{name: string(domain.ToolNameGetResource), arguments: `{"detail":"describe","name":"sample-pod","namespace":null,"purpose":"Inspect one Pod.","resource_type":"pods"}`},
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
		selection := ToolSelection{
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
			`"context":`, `"scope":`, `"gvr":`, `"selector":`,
			`"endpoint":`, `"credential":`, `"kubeconfig":`, `"deadline":`,
			`"max_bytes":`, `"max_items":`,
		} {
			if strings.Contains(canonical, prohibited) {
				t.Fatalf("canonical Tool arguments contain prohibited authority %s: %s", prohibited, canonical)
			}
		}
		if validation := (ToolSelection{
			ID: selection.ID, Name: bound.Name(), ArgumentsJSON: canonical,
		}).Validate(); validation != nil {
			t.Fatalf("bound canonical Tool arguments are invalid: %v", validation)
		}
	})
}
