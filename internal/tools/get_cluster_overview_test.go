package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestGetClusterOverviewUsesOnlyFixedNamespaceAndNodeLists(t *testing.T) {
	canary := strings.Repeat("credential", 5)
	namespace := resourceObservation(domain.ResourceKindNamespace, "team-a")
	namespace.Summary.Reference.Namespace = ""
	node := resourceObservation(domain.ResourceKindNode, "worker-a")
	node.Summary.Reference.Namespace = ""
	node.Summary.Status.Reason = "token=" + canary
	reader := &fakeResourceReader{listFn: func(_ context.Context, request ResourceListRequest) (ResourceObservationList, error) {
		if request.Namespace != "" || request.AllNamespaces || request.Limit != 1 {
			t.Fatalf("cluster overview request = %#v", request)
		}
		switch request.Kind {
		case domain.ResourceKindNamespace:
			return ResourceObservationList{Items: []ResourceObservation{namespace}}, nil
		case domain.ResourceKindNode:
			return ResourceObservationList{Items: []ResourceObservation{node}}, nil
		default:
			t.Fatalf("unexpected overview Kind %q", request.Kind)
			return ResourceObservationList{}, nil
		}
	}}
	tool, err := NewGetClusterOverviewTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewGetClusterOverviewTool() error = %v", err)
	}
	result := tool.Execute(
		context.Background(),
		boundClusterOverviewCall(t, testRunInput(t, 0), `{"limit":2,"purpose":"Inspect cluster health."}`),
	)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 2 ||
		len(result.ResourceSummaries) != 2 {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
	if _, listCalls := reader.counts(); listCalls != 2 {
		t.Fatalf("LIST calls = %d, want 2", listCalls)
	}
	if strings.Contains(result.DataJSON, canary) || !strings.Contains(result.DataJSON, "[REDACTED]") ||
		!strings.Contains(result.DataJSON, `"namespaces"`) || !strings.Contains(result.DataJSON, `"nodes"`) {
		t.Fatalf("safe cluster overview data = %s", result.DataJSON)
	}
	if result.ResourceSummaries[0].Reference.Kind != string(domain.ResourceKindNamespace) ||
		result.ResourceSummaries[1].Reference.Kind != string(domain.ResourceKindNode) {
		t.Fatalf("overview summaries = %#v", result.ResourceSummaries)
	}
}

func TestGetClusterOverviewSharesTheTotalLimitAcrossNamespacesAndNodes(t *testing.T) {
	namespaces := make([]ResourceObservation, 25)
	nodes := make([]ResourceObservation, 25)
	for index := range namespaces {
		namespaces[index] = resourceObservation(domain.ResourceKindNamespace, fmt.Sprintf("team-%02d", index))
		namespaces[index].Summary.Reference.Namespace = ""
		nodes[index] = resourceObservation(domain.ResourceKindNode, fmt.Sprintf("worker-%02d", index))
		nodes[index].Summary.Reference.Namespace = ""
	}
	reader := &fakeResourceReader{listFn: func(_ context.Context, request ResourceListRequest) (ResourceObservationList, error) {
		if request.Limit != 25 || request.Namespace != "" || request.AllNamespaces {
			t.Fatalf("cluster overview split request = %#v", request)
		}
		switch request.Kind {
		case domain.ResourceKindNamespace:
			return ResourceObservationList{Items: namespaces}, nil
		case domain.ResourceKindNode:
			return ResourceObservationList{Items: nodes}, nil
		default:
			t.Fatalf("unexpected overview Kind %q", request.Kind)
			return ResourceObservationList{}, nil
		}
	}}
	tool, err := NewGetClusterOverviewTool(testDependencies(reader, &sequenceScopeGuard{}))
	if err != nil {
		t.Fatalf("NewGetClusterOverviewTool() error = %v", err)
	}
	result := tool.Execute(
		context.Background(),
		boundClusterOverviewCall(t, testRunInput(t, 0), `{"limit":50,"purpose":"Inspect cluster health."}`),
	)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 50 ||
		len(result.ResourceSummaries) != 50 {
		t.Fatalf("bounded overview result = %#v, validation = %v", result, result.Validate())
	}
	if result.ResourceSummaries[24].Reference.Kind != string(domain.ResourceKindNamespace) ||
		result.ResourceSummaries[25].Reference.Kind != string(domain.ResourceKindNode) {
		t.Fatalf("overview split summaries = %#v", result.ResourceSummaries)
	}
}

func TestGetClusterOverviewRejectsStaleSecondReadWithoutEvidence(t *testing.T) {
	namespace := resourceObservation(domain.ResourceKindNamespace, "team-a")
	namespace.Summary.Reference.Namespace = ""
	node := resourceObservation(domain.ResourceKindNode, "worker-a")
	node.Summary.Reference.Namespace = ""
	reader := &fakeResourceReader{listFn: func(_ context.Context, request ResourceListRequest) (ResourceObservationList, error) {
		if request.Kind == domain.ResourceKindNamespace {
			return ResourceObservationList{Items: []ResourceObservation{namespace}}, nil
		}
		return ResourceObservationList{Items: []ResourceObservation{node}}, nil
	}}
	ids := &sequenceEvidenceIDs{}
	dependencies := testDependencies(reader, &sequenceScopeGuard{results: []bool{true, true, true, false}})
	dependencies.EvidenceIDs = ids
	tool, err := NewGetClusterOverviewTool(dependencies)
	if err != nil {
		t.Fatalf("NewGetClusterOverviewTool() error = %v", err)
	}
	result := tool.Execute(
		context.Background(),
		boundClusterOverviewCall(t, testRunInput(t, 0), `{"limit":2,"purpose":"Inspect cluster health."}`),
	)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusError ||
		result.Error == nil || result.Error.Class != domain.SafeErrorClassStaleScope || len(result.Evidence) != 0 {
		t.Fatalf("stale overview result = %#v, validation = %v", result, result.Validate())
	}
	if _, listCalls := reader.counts(); listCalls != 2 || ids.count() != 0 {
		t.Fatalf("LIST/Evidence ID calls = %d/%d, want 2/0", listCalls, ids.count())
	}
}
