package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestListResourcesSummarizesSortsAndCreatesDeterministicEvidence(t *testing.T) {
	canary := strings.Repeat("generated", 5)
	healthy := resourceObservation(domain.ResourceKindPod, "healthy-pod")
	abnormalB := resourceObservation(domain.ResourceKindPod, "broken-b")
	abnormalB.Summary.Status.Phase = "Pending"
	abnormalB.Summary.Status.Ready = domain.Count(0)
	abnormalB.Summary.Status.Reason = "token=" + canary
	abnormalA := resourceObservation(domain.ResourceKindPod, "broken-a")
	abnormalA.Summary.Status.Phase = "Running"
	abnormalA.Summary.Status.Ready = domain.Count(0)
	reader := &fakeResourceReader{listFn: func(_ context.Context, request ResourceListRequest) (ResourceObservationList, error) {
		if request.Scope.Namespace != "team-a" || request.Kind != domain.ResourceKindPod || request.Limit != 20 {
			t.Fatalf("ListResources() request = %#v", request)
		}
		return ResourceObservationList{Items: []ResourceObservation{healthy, abnormalB, abnormalA}}, nil
	}}
	tool, err := NewListResourcesTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, testRunInput(t, 0), `{"health_filter":"abnormal","kind":"Pod","purpose":"Find abnormal Pods."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 3 ||
		len(result.ResourceSummaries) != 3 {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
	if strings.Contains(result.DataJSON, canary) || !strings.Contains(result.DataJSON, "[REDACTED]") ||
		!strings.Contains(result.DataJSON, `"matched_count":3`) || !strings.Contains(result.DataJSON, `"returned_count":3`) ||
		!strings.Contains(result.DataJSON, `"source_trust":"untrusted_external_data"`) {
		t.Fatalf("safe list data = %s", result.DataJSON)
	}
	var decoded struct {
		Items []struct {
			Reference struct {
				Name string `json:"name"`
			} `json:"reference"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(result.DataJSON), &decoded); err != nil {
		t.Fatalf("json.Unmarshal(DataJSON) error = %v", err)
	}
	if len(decoded.Items) != 3 {
		t.Fatalf("decoded items = %#v", decoded.Items)
	}
	names := make([]string, len(decoded.Items))
	for index, item := range decoded.Items {
		names[index] = item.Reference.Name
	}
	if !sort.StringsAreSorted(names) || strings.Join(names, ",") != "broken-a,broken-b,healthy-pod" {
		t.Fatalf("returned names = %#v", names)
	}
	for index, evidence := range result.Evidence {
		if evidence.Category != domain.EvidenceCategoryResourceStatus || evidence.Resource.Name != names[index] ||
			evidence.SourcePath == nil || *evidence.SourcePath != "status.phase" || strings.Contains(evidence.Fact, canary) {
			t.Fatalf("Evidence[%d] = %#v", index, evidence)
		}
		if summary := result.ResourceSummaries[index]; summary.Reference != evidence.Resource ||
			strings.Contains(fmt.Sprintf("%#v", summary), canary) {
			t.Fatalf("ResourceSummaries[%d] = %#v", index, summary)
		}
	}
	message, measured, err := agent.BuildToolResultContent(result)
	if err != nil || measured != result.Truncation.ReturnedBytes || measured > call.Ceilings().MaxResultBytes {
		t.Fatalf("BuildToolResultContent() bytes/error = %d/%v, result = %#v", measured, err, result.Truncation)
	}
	if strings.Contains(message, "resource_summaries") {
		t.Fatalf("local presentation summaries entered the model envelope: %s", message)
	}
}

func TestListResourcesUsesExactCRDEvidenceMapping(t *testing.T) {
	resourceType := domain.ResourceType{
		ID: "widgets", Group: "ops.example.com", Version: "v1", Resource: "widgets", Kind: "Widget",
		Scope: domain.ResourceScopeNamespaced,
	}
	policy := domain.ResourcePolicy{
		Type: resourceType, Verbs: []domain.ResourceVerb{domain.ResourceVerbGet, domain.ResourceVerbList},
		Fields: []domain.ResourceFieldPolicy{
			{ID: "name", Path: "metadata.name", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataMetadata, SelectorSource: domain.ResourceSelectorField, SelectorKey: "metadata.name", Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals}},
			{ID: "note", Path: "spec.note", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataSpec, SelectorSource: domain.ResourceSelectorNone},
			{ID: "state", Path: "status.state", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataStatus, SelectorSource: domain.ResourceSelectorNone, Evidence: true},
		},
		Limits: domain.ResourceQueryLimits{MaxPages: 2, PageItems: 10, PageBytes: 32 * 1024, MaxItems: 20, MaxBytes: 64 * 1024, MaxReturned: 10},
	}
	catalog, err := domain.NewResourcePolicyCatalog(domain.ResourcePolicyVersion, []domain.ResourcePolicy{policy})
	if err != nil {
		t.Fatalf("NewResourcePolicyCatalog() error = %v", err)
	}
	conversation, err := agent.NewConversationContext(testSessionID, nil, nil)
	if err != nil {
		t.Fatalf("NewConversationContext() error = %v", err)
	}
	input, err := agent.NewRunInputWithPolicyContext(
		testRunID, testSessionID, testMessageID, "Inspect approved custom resources.",
		domain.ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: testActivatedAt},
		nil, agent.DefaultRunBudgetLimits(), conversation, catalog, 9,
	)
	if err != nil {
		t.Fatalf("NewRunInputWithPolicyContext() error = %v", err)
	}
	reader := &fakeResourceReader{queryFn: func(_ context.Context, request ResourceQueryRequest) (ResourceQueryObservation, error) {
		if request.Policy.Type != resourceType || request.Query.Namespace != "team-a" || request.Query.Verb != domain.ResourceVerbList {
			t.Fatalf("QueryResources() request = %#v", request)
		}
		summary := domain.ResourceSummary{Type: resourceType, Reference: domain.ResourceRef{
			APIVersion: "ops.example.com/v1", Kind: "Widget", Namespace: "team-a", Name: "sample-widget",
		}}
		observation := ResourceObservation{Summary: summary, Fields: []ResourceFieldObservation{
			{Field: "note", Path: "spec.note", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataSpec, Value: ExternalText{Value: "ordinary"}, Present: true},
			{Field: "state", Path: "status.state", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataStatus, Value: ExternalText{Value: "Ready"}, Present: true},
		}}
		return ResourceQueryObservation{
			Page:  domain.ResourcePage{Type: resourceType, Items: []domain.ResourceSummary{summary}, PagesRead: 1, ScannedItems: 1, MatchedItems: 1},
			Items: []ResourceObservation{observation},
		}, nil
	}}
	tool, err := NewListResourcesTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, input, `{"filters":[],"format":"table","limit":1,"namespace":"team-a","purpose":"Compare approved Widgets.","resource_type":"widgets"}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 1 {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
	evidence := result.Evidence[0]
	if evidence.ResourceType != resourceType || evidence.PolicyVersion != domain.ResourcePolicyVersion || evidence.PolicyGeneration != 9 ||
		evidence.SourcePath == nil || *evidence.SourcePath != "status.state" || !strings.Contains(evidence.Fact, "state is Ready") ||
		strings.Contains(evidence.Fact, "ordinary") {
		t.Fatalf("Evidence = %#v", evidence)
	}
}

func TestListResourcesEmptyIsSuccessfulAndPerformsOneBoundedRead(t *testing.T) {
	reader := &fakeResourceReader{listFn: func(context.Context, ResourceListRequest) (ResourceObservationList, error) {
		return ResourceObservationList{Items: []ResourceObservation{}}, nil
	}}
	tool, err := NewListResourcesTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, testRunInput(t, 0), `{"health_filter":"any","kind":"Service","limit":7,"purpose":"Find Service candidates."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 0 ||
		!strings.Contains(result.DataJSON, `"items":[]`) || !strings.Contains(result.DataJSON, `"returned_count":0`) {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
	_, listCalls := reader.counts()
	if listCalls != 1 || reader.lastListRequest().Limit != 7 {
		t.Fatalf("list calls/request = %d/%#v", listCalls, reader.lastListRequest())
	}
}

func TestListResourcesMarksKubernetesContinuationAsItemLimited(t *testing.T) {
	item := resourceObservation(domain.ResourceKindPod, "sample-pod")
	reader := &fakeResourceReader{listFn: func(context.Context, ResourceListRequest) (ResourceObservationList, error) {
		return ResourceObservationList{Items: []ResourceObservation{item}, Truncated: true}, nil
	}}
	tool, err := NewListResourcesTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, testRunInput(t, 0), `{"health_filter":"any","kind":"Pod","purpose":"Find bounded Pod candidates."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusPartial ||
		result.Truncation.Reason != itemLimitReason || result.Truncation.ReturnedCount != 1 || len(result.Evidence) != 1 {
		t.Fatalf("Execute() continuation result = %#v, validation = %v", result, result.Validate())
	}
}

func TestListResourcesOmitsSensitiveIdentityFromDataAndEvidence(t *testing.T) {
	canary := strings.Repeat("identity-canary", 3)
	item := resourceObservation(domain.ResourceKindPod, "sample-pod")
	item.Summary.Reference.UID = "token=" + canary
	item.Summary.Reference.ResourceVersion = "Bearer " + canary
	reader := &fakeResourceReader{listFn: func(context.Context, ResourceListRequest) (ResourceObservationList, error) {
		return ResourceObservationList{Items: []ResourceObservation{item}}, nil
	}}
	tool, err := NewListResourcesTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, testRunInput(t, 0), `{"health_filter":"any","kind":"Pod","purpose":"Find one safe Pod summary."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusPartial || result.Truncation.Reason != fieldLimitReason ||
		strings.Contains(result.DataJSON, canary) || len(result.Evidence) != 1 || result.Evidence[0].Resource.UID != "" ||
		result.Evidence[0].Resource.ResourceVersion != "" || len(result.ResourceSummaries) != 1 ||
		result.ResourceSummaries[0].Reference.UID != "" || result.ResourceSummaries[0].Reference.ResourceVersion != "" ||
		strings.Contains(fmt.Sprintf("%#v", result.ResourceSummaries), canary) {
		t.Fatalf("Execute() identity result = %#v, validation = %v", result, result.Validate())
	}
}

func TestListResourcesMapsForbiddenWithoutLeakingRawError(t *testing.T) {
	canary := strings.Repeat("forbidden-canary", 4)
	reader := &fakeResourceReader{listFn: func(context.Context, ResourceListRequest) (ResourceObservationList, error) {
		return ResourceObservationList{}, &fakeClassifiedError{class: domain.SafeErrorClassPermissionDenied, message: canary}
	}}
	tool, err := NewListResourcesTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, testRunInput(t, 0), `{"kind":"Pod","purpose":"Find abnormal Pods."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusError || result.Error == nil ||
		result.Error.Class != domain.SafeErrorClassPermissionDenied || len(result.Evidence) != 0 ||
		strings.Contains(fmt.Sprintf("%#v", result), canary) {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
	_, listCalls := reader.counts()
	if listCalls != 1 {
		t.Fatalf("reader calls = %d, want 1", listCalls)
	}
}

func TestListResourcesRejectsDuplicateExternalIdentitiesAfterOneRead(t *testing.T) {
	first := resourceObservation(domain.ResourceKindPod, "sample-pod")
	second := first
	second.Summary.Reference.UID = "generated-recreated-uid"
	second.Summary.Reference.ResourceVersion = "18"
	reader := &fakeResourceReader{listFn: func(context.Context, ResourceListRequest) (ResourceObservationList, error) {
		return ResourceObservationList{Items: []ResourceObservation{first, second}}, nil
	}}
	tool, err := NewListResourcesTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, testRunInput(t, 0), `{"health_filter":"any","kind":"Pod","purpose":"Find Pods."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusError || result.Error == nil ||
		result.Error.Class != domain.SafeErrorClassInvalidExternalResponse || len(result.Evidence) != 0 {
		t.Fatalf("Execute() duplicate result = %#v, validation = %v", result, result.Validate())
	}
	_, listCalls := reader.counts()
	if listCalls != 1 {
		t.Fatalf("reader actions = %d, want 1", listCalls)
	}
}

func TestListResourcesEnforcesRequestedAndHardItemLimitsWithPartialMetadata(t *testing.T) {
	items := make([]ResourceObservation, 0, domain.MaxResourceSummaries)
	for index := domain.MaxResourceSummaries - 1; index >= 0; index-- {
		item := resourceObservation(domain.ResourceKindPod, fmt.Sprintf("sample-pod-%02d", index))
		item.Summary.Status.Phase = "Pending"
		item.Summary.Status.Ready = domain.Count(0)
		items = append(items, item)
	}
	reader := &fakeResourceReader{listFn: func(_ context.Context, request ResourceListRequest) (ResourceObservationList, error) {
		if request.Limit != domain.MaxResourceSummaries {
			t.Fatalf("reader limit = %d, want %d", request.Limit, domain.MaxResourceSummaries)
		}
		return ResourceObservationList{Items: items, Truncated: true}, nil
	}}
	dependencies := testDependencies(reader, &sequenceScopeGuard{})
	identifierSource := dependencies.EvidenceIDs.(*sequenceEvidenceIDs)
	tool, err := NewListResourcesTool(dependencies)
	if err != nil {
		t.Fatalf("NewListResourcesTool() error = %v", err)
	}
	call := boundListCall(t, testRunInput(t, 8192), `{"health_filter":"abnormal","kind":"Pod","limit":50,"purpose":"Find bounded Pod candidates."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusPartial || !result.Truncation.Truncated ||
		result.Truncation.Reason != "output_limit" || result.Truncation.ReturnedCount > domain.MaxResourceSummaries ||
		len(result.Evidence) != result.Truncation.ReturnedCount || len(result.ResourceSummaries) != len(result.Evidence) {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
	_, measured, err := agent.BuildToolResultContent(result)
	if err != nil || measured > 8192 || measured != result.Truncation.ReturnedBytes {
		t.Fatalf("measured bytes/error = %d/%v, truncation = %#v", measured, err, result.Truncation)
	}
	if identifierSource.count() != len(result.Evidence) {
		t.Fatalf("Evidence ID allocations = %d, returned Evidence = %d", identifierSource.count(), len(result.Evidence))
	}
}

func TestListResourcesStaleAndCancelledPathsUseZeroOrOneReaderAction(t *testing.T) {
	tests := []struct {
		name        string
		context     func() context.Context
		guard       *sequenceScopeGuard
		wantClass   domain.SafeErrorClass
		wantActions int
	}{
		{
			name:      "stale before list",
			context:   func() context.Context { return context.Background() },
			guard:     &sequenceScopeGuard{results: []bool{false}},
			wantClass: domain.SafeErrorClassStaleScope,
		},
		{
			name: "cancelled before list",
			context: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			guard:     &sequenceScopeGuard{},
			wantClass: domain.SafeErrorClassCancelled,
		},
		{
			name: "deadline before list",
			context: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
				cancel()
				return ctx
			},
			guard:     &sequenceScopeGuard{},
			wantClass: domain.SafeErrorClassTimeout,
		},
		{
			name:        "stale after list",
			context:     func() context.Context { return context.Background() },
			guard:       &sequenceScopeGuard{results: []bool{true, false}},
			wantClass:   domain.SafeErrorClassStaleScope,
			wantActions: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeResourceReader{listFn: func(context.Context, ResourceListRequest) (ResourceObservationList, error) {
				return ResourceObservationList{Items: []ResourceObservation{resourceObservation(domain.ResourceKindPod, "sample-pod")}}, nil
			}}
			tool, err := NewListResourcesTool(testDependencies(reader, test.guard))
			if err != nil {
				t.Fatalf("NewListResourcesTool() error = %v", err)
			}
			call := boundListCall(t, testRunInput(t, 0), `{"health_filter":"any","kind":"Pod","purpose":"Find Pods."}`)
			result := tool.Execute(test.context(), call)
			if result.Validate() != nil || result.Error == nil || result.Error.Class != test.wantClass || len(result.Evidence) != 0 {
				t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
			}
			_, listCalls := reader.counts()
			if listCalls != test.wantActions {
				t.Fatalf("reader actions = %d, want %d", listCalls, test.wantActions)
			}
		})
	}
}

func TestListResourcesRejectsSelectorScopeAndLimitAuthorityBeforeReader(t *testing.T) {
	input := testRunInput(t, 0)
	reader := &fakeResourceReader{}
	tests := []struct {
		name      string
		arguments string
	}{
		{name: "raw selector", arguments: `{"kind":"Pod","purpose":"Find Pods.","raw_selector":"app=sample"}`},
		{name: "Namespace", arguments: `{"kind":"Pod","namespace":"other","purpose":"Find Pods."}`},
		{name: "hard cap", arguments: `{"kind":"Pod","limit":51,"purpose":"Find Pods."}`},
		{name: "Secret", arguments: `{"kind":"Secret","purpose":"Find Secrets."}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := agent.BindToolCall(input, testInvocationID, agent.ToolSelection{
				ID:            "call-denied",
				Name:          domain.ToolNameListResources,
				ArgumentsJSON: test.arguments,
			})
			if !errors.Is(err, agent.ErrToolPolicyDenied) {
				t.Fatalf("BindToolCall() error = %v", err)
			}
			getCalls, listCalls := reader.counts()
			if getCalls != 0 || listCalls != 0 {
				t.Fatalf("denied reader calls = get %d/list %d", getCalls, listCalls)
			}
		})
	}
}
