package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestReadOnlyToolSchemasRemainExactStrictPolicyBoundAndPurposeBound(t *testing.T) {
	t.Parallel()

	specifications := agent.ToolSpecifications()
	wanted := map[domain.ToolName]bool{
		domain.ToolNameGetResource:         false,
		domain.ToolNameListResources:       false,
		domain.ToolNameGetEvents:           false,
		domain.ToolNameGetPodLogs:          false,
		domain.ToolNameGetPreviousPodLogs:  false,
		domain.ToolNameGetPodMetrics:       false,
		domain.ToolNameGetNodeMetrics:      false,
		domain.ToolNameQueryPrometheus:     false,
		domain.ToolNameQueryLoki:           false,
		domain.ToolNameGetRelatedResources: false,
		domain.ToolNameGetClusterOverview:  false,
		domain.ToolNamePodExec:             false,
		domain.ToolNameReadContainerFile:   false,
		domain.ToolNameRunDiagnosticPod:    false,
	}
	if len(specifications) != len(wanted) {
		t.Fatalf("fixed Tool specification count = %d, want %d", len(specifications), len(wanted))
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
		for _, prohibited := range []string{
			`"context"`, `"scope"`, `"gvr"`, `"raw_selector"`, `"hard_limit"`,
			`"continue"`, `"subresource"`, `"limit_bytes"`, `"watch"`, `"follow"`,
		} {
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

func TestToolCatalogBuildsExactlyElevenReadOnlyHandlersWhenRemoteDiagnosticsAreDisabled(t *testing.T) {
	t.Parallel()

	resourceReader := &fakeResourceReader{}
	eventReader := &fakeEventReader{}
	logReader := &fakePodLogReader{}
	metricReader := &fakeMetricReader{}
	prometheusReader := &fakePrometheusReader{}
	lokiReader := &fakeLokiReader{}
	relatedReader := &fakeRelatedResourceReader{}
	guard := &sequenceScopeGuard{}
	handlers, err := NewToolCatalog(ToolCatalogDependencies{
		Resources: testDependencies(resourceReader, guard),
		Events:    eventDependencies(eventReader, guard),
		Logs:      logDependencies(logReader, guard, LogPolicyAllowed),
		Metrics:   metricDependencies(metricReader, guard),
		Sources:   sourceDependencies(prometheusReader, lokiReader, guard),
		Related:   relatedDependencies(relatedReader, guard),
	})
	if err != nil || handlers.Validate() != nil {
		t.Fatalf("NewToolCatalog() handlers/error = %#v/%v", handlers, err)
	}
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
	for _, name := range want {
		if handler, err := handlers.Resolve(name); err != nil || handler == nil {
			t.Fatalf("Resolve(%q) handler/error = %#v/%v", name, handler, err)
		}
	}
	if _, err := handlers.Resolve(domain.ToolName("run_shell")); err == nil {
		t.Fatal("fixed handlers resolved an unknown Tool")
	}
	invalid := ToolCatalogDependencies{
		Resources: testDependencies(resourceReader, guard),
		Events:    eventDependencies(eventReader, guard),
		Logs:      logDependencies(logReader, guard, LogPolicyAllowed),
	}
	if _, err := NewToolCatalog(invalid); err == nil {
		t.Fatal("catalog accepted a missing related handler dependency")
	}
}

func TestToolAuthorityMatrixRejectsBeforeHandlerOrReaderAction(t *testing.T) {
	resourceReader := &fakeResourceReader{}
	eventReader := &fakeEventReader{}
	logReader := &fakePodLogReader{}
	metricReader := &fakeMetricReader{}
	prometheusReader := &fakePrometheusReader{}
	lokiReader := &fakeLokiReader{}
	relatedReader := &fakeRelatedResourceReader{}
	guard := &sequenceScopeGuard{}
	logPolicy := &staticLogPolicy{decision: LogPolicyAllowed}
	_, err := NewToolCatalog(ToolCatalogDependencies{
		Resources: testDependencies(resourceReader, guard),
		Events:    eventDependencies(eventReader, guard),
		Logs: LogToolDependencies{
			Reader: logReader, Targets: permissiveObservationTargetResolver{},
			Actions:    &permissiveObservationActionGate{now: func() time.Time { return testObservedAt }},
			ScopeGuard: guard, PolicyGuard: alwaysCurrentPolicyGuard{}, EvidenceIDs: &sequenceEvidenceIDs{},
			Text: security.NewRedactor(), Policy: logPolicy, Now: func() time.Time { return testObservedAt },
		},
		Metrics: metricDependencies(metricReader, guard),
		Sources: sourceDependencies(prometheusReader, lokiReader, guard),
		Related: relatedDependencies(relatedReader, guard),
	})
	if err != nil {
		t.Fatalf("NewToolCatalog() error = %v", err)
	}
	input := testRunInput(t, 0)
	tests := []agent.ToolSelection{
		{ID: "denied-get", Name: domain.ToolNameGetResource, ArgumentsJSON: `{"namespace":"other","purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`},
		{ID: "denied-list", Name: domain.ToolNameListResources, ArgumentsJSON: `{"kind":"Pod","purpose":"List.","raw_selector":"app=all"}`},
		{ID: "denied-events", Name: domain.ToolNameGetEvents, ArgumentsJSON: `{"purpose":"Events.","resource":{"kind":"Secret","name":"sample-secret"}}`},
		{ID: "denied-logs", Name: domain.ToolNameGetPodLogs, ArgumentsJSON: `{"namespace":"other","pod_name":"sample-pod","purpose":"Logs."}`},
		{ID: "denied-previous", Name: domain.ToolNameGetPreviousPodLogs, ArgumentsJSON: `{"pod_name":"sample-pod","previous":false,"purpose":"Previous logs."}`},
		{ID: "denied-related", Name: domain.ToolNameGetRelatedResources, ArgumentsJSON: `{"gvr":"v1/secrets","purpose":"Related.","resource":{"kind":"Pod","name":"sample-pod"}}`},
	}
	for index, selection := range tests {
		_, err := agent.BindToolCall(input, domain.ToolInvocationID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 20_000+index)), selection)
		if err == nil {
			t.Errorf("BindToolCall(%q) accepted prohibited authority", selection.Name)
		}
	}
	getCalls, listCalls := resourceReader.counts()
	if getCalls != 0 || listCalls != 0 || eventReader.count() != 0 || logReader.count() != 0 ||
		relatedReader.count() != 0 || logPolicy.count() != 0 {
		t.Fatalf("denied matrix actions resource=%d/%d event=%d log=%d related=%d policy=%d",
			getCalls, listCalls, eventReader.count(), logReader.count(), relatedReader.count(), logPolicy.count())
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

func TestResourceToolBindingCanonicalizesDefaultsWithPolicyBoundNamespace(t *testing.T) {
	t.Parallel()

	input := testRunInput(t, 0)
	getCall := boundGetCall(t, input, `{"purpose":"Inspect one Pod.","resource":{"kind":"Pod","name":"sample-pod"}}`)
	if got := getCall.ArgumentsJSON(); got != `{"detail":"describe","name":"sample-pod","namespace":"team-a","purpose":"Inspect one Pod.","resource_type":"pods"}` {
		t.Fatalf("canonical get_resource arguments = %s", got)
	}
	listCall := boundListCall(t, input, `{"kind":"Pod","purpose":"Find abnormal Pods."}`)
	if got := listCall.ArgumentsJSON(); got != `{"filters":[],"format":"list","limit":20,"namespace":"team-a","purpose":"Find abnormal Pods.","resource_type":"pods"}` {
		t.Fatalf("canonical list_resources arguments = %s", got)
	}
	for _, value := range []string{getCall.ArgumentsJSON(), listCall.ArgumentsJSON()} {
		lower := strings.ToLower(value)
		for _, prohibited := range []string{"context", "gvr", "selector", "max_result_bytes"} {
			if strings.Contains(lower, prohibited) {
				t.Fatalf("canonical arguments contain runtime authority %q: %s", prohibited, value)
			}
		}
	}
}
