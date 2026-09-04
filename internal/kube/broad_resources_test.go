package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
)

type alwaysCurrentResourcePolicy struct{}

func (alwaysCurrentResourcePolicy) CurrentPolicyGeneration(context.Context, domain.PolicyGeneration) bool {
	return true
}

type sequenceResourcePolicy struct {
	mu      sync.Mutex
	results []bool
	calls   int
}

func (guard *sequenceResourcePolicy) CurrentPolicyGeneration(context.Context, domain.PolicyGeneration) bool {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.calls++
	if len(guard.results) == 0 {
		return true
	}
	result := guard.results[0]
	guard.results = guard.results[1:]
	return result
}

func TestBroadBuiltInQueryUsesExactServerSelectorsLimitAndInternalContinuation(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/namespaces/team-a/pods" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		assertBroadReadHeaders(t, request, false)
		query := request.URL.Query()
		writer.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			if got, want := query.Encode(), "fieldSelector=metadata.name%21%3Dignored&labelSelector=app.kubernetes.io%2Fname%3Dsample&limit=2&timeout=10s&timeoutSeconds=10"; got != want {
				t.Fatalf("first query = %q, want %q", got, want)
			}
			writeUnstructuredList(t, writer, "v1", "PodList", "runtime-next", []map[string]any{
				podObject("sample-a", "Running"), podObject("sample-b", "Pending"),
			})
		case 2:
			if got, want := query.Encode(), "continue=runtime-next&fieldSelector=metadata.name%21%3Dignored&labelSelector=app.kubernetes.io%2Fname%3Dsample&limit=2&timeout=10s&timeoutSeconds=10"; got != want {
				t.Fatalf("second query = %q, want %q", got, want)
			}
			writeUnstructuredList(t, writer, "v1", "PodList", "", []map[string]any{podObject("sample-c", "Running")})
		default:
			t.Fatalf("unexpected request %d", call)
		}
	}))
	defer server.Close()

	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "pods")
	request := broadListRequest(policy, 2, 4, 8, 3, 1<<20)
	request.Query.Filters = []domain.ResourceFilter{
		{Field: "app_name", Operator: domain.ResourceFilterEquals, Value: "sample"},
		{Field: "name", Operator: domain.ResourceFilterNotEquals, Value: "ignored"},
	}
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil {
		t.Fatalf("QueryResources() error = %v", err)
	}
	if result.Validate(request) != nil || result.Page.PagesRead != 2 || result.Page.ScannedItems != 3 ||
		result.Page.Partial || len(result.Items) != 3 || calls.Load() != 2 {
		t.Fatalf("result = %#v, validation = %v, calls = %d", result, result.Validate(request), calls.Load())
	}
	for index, want := range []string{"sample-a", "sample-b", "sample-c"} {
		if result.Items[index].Summary.Reference.Name != want || result.Items[index].Summary.EffectiveType() != policy.Type {
			t.Fatalf("item[%d] = %#v", index, result.Items[index])
		}
	}
}

func TestBroadBuiltInGetProjectsEveryExactPolicyFieldFromTypedObject(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/apis/autoscaling/v2/namespaces/team-a/horizontalpodautoscalers/sample-hpa" ||
			request.URL.Query().Encode() != "timeout=10s" {
			t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		assertBroadReadHeaders(t, request, false)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"apiVersion":"autoscaling/v2",
			"kind":"HorizontalPodAutoscaler",
			"metadata":{"name":"sample-hpa","namespace":"team-a","uid":"generated-hpa-uid","resourceVersion":"12"},
			"spec":{"scaleTargetRef":{"apiVersion":"apps/v1","kind":"Deployment","name":"sample"},"minReplicas":2,"maxReplicas":5,"metrics":[]},
			"status":{"currentReplicas":3,"desiredReplicas":4,"currentMetrics":[],"conditions":[]}
		}`))
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "horizontal-pod-autoscalers")
	request := toolcontract.ResourceQueryRequest{
		Policy: policy, Detail: toolcontract.ResourceDetailSummary,
		Query: domain.ResourceQuery{
			Scope: liveScope("team-a"), PolicyGeneration: 1, PolicyVersion: domain.ResourcePolicyVersion,
			Type: policy.Type, Verb: domain.ResourceVerbGet, View: domain.ResourceViewSummary,
			Namespace: "team-a", Name: "sample-hpa",
			Limits: domain.ResourceQueryLimits{
				MaxPages: 1, PageItems: 1, PageBytes: policy.Limits.PageBytes,
				MaxItems: 1, MaxBytes: policy.Limits.MaxBytes, MaxReturned: 1,
			},
		},
	}
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil || result.Validate(request) != nil || calls.Load() != 1 || len(result.Items) != 1 ||
		len(result.Items[0].Fields) != len(policy.Fields) {
		t.Fatalf("built-in GET = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(request), calls.Load())
	}
	values := make(map[string]string, len(result.Items[0].Fields))
	for _, field := range result.Items[0].Fields {
		if field.Present {
			values[field.Field] = field.Value.Value
		}
	}
	for field, want := range map[string]string{"minimum": "2", "maximum": "5", "current": "3", "desired": "4"} {
		if values[field] != want {
			t.Fatalf("field %q = %q, want %q; fields = %#v", field, values[field], want, result.Items[0].Fields)
		}
	}
}

func TestEveryBuiltInPolicyRecordsExactGetAndListHTTPRequests(t *testing.T) {
	tests := []struct {
		id           string
		apiVersion   string
		kind         string
		getPath      string
		listPath     string
		metadataOnly bool
	}{
		{id: "namespaces", apiVersion: "v1", kind: "Namespace", getPath: "/api/v1/namespaces/sample-resource", listPath: "/api/v1/namespaces"},
		{id: "nodes", apiVersion: "v1", kind: "Node", getPath: "/api/v1/nodes/sample-resource", listPath: "/api/v1/nodes"},
		{id: "pods", apiVersion: "v1", kind: "Pod", getPath: "/api/v1/namespaces/team-a/pods/sample-resource", listPath: "/api/v1/namespaces/team-a/pods"},
		{id: "services", apiVersion: "v1", kind: "Service", getPath: "/api/v1/namespaces/team-a/services/sample-resource", listPath: "/api/v1/namespaces/team-a/services"},
		{id: "persistent-volume-claims", apiVersion: "v1", kind: "PersistentVolumeClaim", getPath: "/api/v1/namespaces/team-a/persistentvolumeclaims/sample-resource", listPath: "/api/v1/namespaces/team-a/persistentvolumeclaims"},
		{id: "persistent-volumes", apiVersion: "v1", kind: "PersistentVolume", getPath: "/api/v1/persistentvolumes/sample-resource", listPath: "/api/v1/persistentvolumes"},
		{id: "config-maps", apiVersion: "v1", kind: "ConfigMap", getPath: "/api/v1/namespaces/team-a/configmaps/sample-resource", listPath: "/api/v1/namespaces/team-a/configmaps", metadataOnly: true},
		{id: "secrets", apiVersion: "v1", kind: "Secret", getPath: "/api/v1/namespaces/team-a/secrets/sample-resource", listPath: "/api/v1/namespaces/team-a/secrets", metadataOnly: true},
		{id: "deployments", apiVersion: "apps/v1", kind: "Deployment", getPath: "/apis/apps/v1/namespaces/team-a/deployments/sample-resource", listPath: "/apis/apps/v1/namespaces/team-a/deployments"},
		{id: "replica-sets", apiVersion: "apps/v1", kind: "ReplicaSet", getPath: "/apis/apps/v1/namespaces/team-a/replicasets/sample-resource", listPath: "/apis/apps/v1/namespaces/team-a/replicasets"},
		{id: "stateful-sets", apiVersion: "apps/v1", kind: "StatefulSet", getPath: "/apis/apps/v1/namespaces/team-a/statefulsets/sample-resource", listPath: "/apis/apps/v1/namespaces/team-a/statefulsets"},
		{id: "daemon-sets", apiVersion: "apps/v1", kind: "DaemonSet", getPath: "/apis/apps/v1/namespaces/team-a/daemonsets/sample-resource", listPath: "/apis/apps/v1/namespaces/team-a/daemonsets"},
		{id: "jobs", apiVersion: "batch/v1", kind: "Job", getPath: "/apis/batch/v1/namespaces/team-a/jobs/sample-resource", listPath: "/apis/batch/v1/namespaces/team-a/jobs"},
		{id: "cron-jobs", apiVersion: "batch/v1", kind: "CronJob", getPath: "/apis/batch/v1/namespaces/team-a/cronjobs/sample-resource", listPath: "/apis/batch/v1/namespaces/team-a/cronjobs"},
		{id: "ingresses", apiVersion: "networking.k8s.io/v1", kind: "Ingress", getPath: "/apis/networking.k8s.io/v1/namespaces/team-a/ingresses/sample-resource", listPath: "/apis/networking.k8s.io/v1/namespaces/team-a/ingresses"},
		{id: "horizontal-pod-autoscalers", apiVersion: "autoscaling/v2", kind: "HorizontalPodAutoscaler", getPath: "/apis/autoscaling/v2/namespaces/team-a/horizontalpodautoscalers/sample-resource", listPath: "/apis/autoscaling/v2/namespaces/team-a/horizontalpodautoscalers"},
		{id: "pod-disruption-budgets", apiVersion: "policy/v1", kind: "PodDisruptionBudget", getPath: "/apis/policy/v1/namespaces/team-a/poddisruptionbudgets/sample-resource", listPath: "/apis/policy/v1/namespaces/team-a/poddisruptionbudgets"},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			policy := resourcePolicy(t, test.id)
			if policy.Type.APIVersion() != test.apiVersion || policy.Type.Kind != test.kind {
				t.Fatalf("policy API identity = %#v", policy.Type)
			}
			metadata := map[string]any{"name": "sample-resource", "uid": "generated-resource-uid", "resourceVersion": "7"}
			if policy.Type.Namespaced() {
				metadata["namespace"] = "team-a"
			}
			object := map[string]any{"apiVersion": test.apiVersion, "kind": test.kind, "metadata": metadata}
			if test.metadataOnly {
				object["apiVersion"], object["kind"] = "meta.k8s.io/v1", "PartialObjectMetadata"
			}
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				call := calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				assertBroadReadHeaders(t, request, test.metadataOnly)
				switch call {
				case 1:
					if request.Method != http.MethodGet || request.URL.EscapedPath() != test.getPath || request.URL.Query().Encode() != "timeout=10s" {
						t.Fatalf("GET request = %s %s?%s", request.Method, request.URL.EscapedPath(), request.URL.RawQuery)
					}
					_ = json.NewEncoder(writer).Encode(object)
				case 2:
					if request.Method != http.MethodGet || request.URL.EscapedPath() != test.listPath || request.URL.Query().Encode() != "limit=2&timeout=10s&timeoutSeconds=10" {
						t.Fatalf("LIST request = %s %s?%s", request.Method, request.URL.EscapedPath(), request.URL.RawQuery)
					}
					listKind := test.kind + "List"
					listVersion := test.apiVersion
					if test.metadataOnly {
						listKind, listVersion = "PartialObjectMetadataList", "meta.k8s.io/v1"
					}
					_ = json.NewEncoder(writer).Encode(map[string]any{
						"apiVersion": listVersion, "kind": listKind,
						"metadata": map[string]any{"continue": ""}, "items": []map[string]any{object},
					})
				default:
					t.Fatalf("unexpected request %d", call)
				}
			}))
			defer server.Close()
			scope := liveScope("team-a")
			reader, closeReader := newHTTPToolResourceReaderForScope(t, server, scope, alwaysCurrentResourcePolicy{})
			defer closeReader()
			namespace := "team-a"
			if policy.Type.ClusterScoped() {
				namespace = ""
			}
			getRequest := toolcontract.ResourceQueryRequest{Policy: policy, Detail: toolcontract.ResourceDetailSummary, Query: domain.ResourceQuery{
				Scope: scope, PolicyGeneration: 1, PolicyVersion: domain.ResourcePolicyVersion,
				Type: policy.Type, Verb: domain.ResourceVerbGet, View: domain.ResourceViewSummary,
				Namespace: namespace, Name: "sample-resource",
				Limits: domain.ResourceQueryLimits{MaxPages: 1, PageItems: 1, PageBytes: 256 * 1024, MaxItems: 1, MaxBytes: 1 << 20, MaxReturned: 1},
			}}
			getResult, err := reader.QueryResources(context.Background(), getRequest)
			if err != nil || getResult.Validate(getRequest) != nil || len(getResult.Items) != 1 {
				t.Fatalf("GET result/error = %#v/%v", getResult, err)
			}
			listRequest := broadListRequest(policy, 2, 1, 2, 2, 1<<20)
			listRequest.Query.Namespace = namespace
			listResult, err := reader.QueryResources(context.Background(), listRequest)
			if err != nil || listResult.Validate(listRequest) != nil || len(listResult.Items) != 1 || calls.Load() != 2 {
				t.Fatalf("LIST result/error/calls = %#v/%v/%d", listResult, err, calls.Load())
			}
		})
	}
}

func TestClusterOverviewReaderRecordsExactNamespaceAndNodeHTTPRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		assertBroadReadHeaders(t, request, false)
		if request.Method != http.MethodGet || request.URL.Query().Encode() != "limit=1" {
			t.Fatalf("request %d = %s %s?%s", call, request.Method, request.URL.EscapedPath(), request.URL.RawQuery)
		}
		switch call {
		case 1:
			if request.URL.EscapedPath() != "/api/v1/namespaces" {
				t.Fatalf("Namespace path = %q", request.URL.EscapedPath())
			}
			_, _ = writer.Write([]byte(`{"apiVersion":"v1","kind":"NamespaceList","metadata":{"continue":""},"items":[{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"team-b","uid":"generated-namespace-uid","resourceVersion":"7"},"status":{"phase":"Active"}}]}`))
		case 2:
			if request.URL.EscapedPath() != "/api/v1/nodes" {
				t.Fatalf("Node path = %q", request.URL.EscapedPath())
			}
			_, _ = writer.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","metadata":{"continue":""},"items":[{"apiVersion":"v1","kind":"Node","metadata":{"name":"worker-a","uid":"generated-node-uid","resourceVersion":"9"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`))
		default:
			t.Fatalf("unexpected request %d", call)
		}
	}))
	defer server.Close()

	scope := liveScope("team-a")
	reader, closeReader := newHTTPToolResourceReaderForScope(t, server, scope, alwaysCurrentResourcePolicy{})
	defer closeReader()
	for _, kind := range []domain.ResourceKind{domain.ResourceKindNamespace, domain.ResourceKindNode} {
		request := toolcontract.ResourceListRequest{Scope: scope, Kind: kind, Limit: 1}
		result, err := reader.ListResources(context.Background(), request)
		if err != nil || result.Validate(request) != nil || len(result.Items) != 1 {
			t.Fatalf("%s result/error = %#v/%v", kind, result, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2", calls.Load())
	}
}

func TestBroadCRDGetDiscoversExactAPIThenProjectsOnlyPolicyFields(t *testing.T) {
	t.Parallel()
	canary := strings.Repeat("credential-canary", 4)
	requests := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/apis/example.test/v1":
			assertBroadReadHeaders(t, request, false)
			if request.Method != http.MethodGet || request.URL.Query().Encode() != "timeout=10s" {
				t.Fatalf("discovery request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			}
			_, _ = writer.Write([]byte(`{"apiVersion":"v1","groupVersion":"example.test/v1","kind":"APIResourceList","resources":[{"kind":"Widget","name":"widgets","namespaced":true,"singularName":"widget","verbs":["get","list"]}]}`))
		case "/apis/example.test/v1/namespaces/team-a/widgets/sample-widget":
			assertBroadReadHeaders(t, request, false)
			if request.Method != http.MethodGet || request.URL.Query().Encode() != "timeout=10s" {
				t.Fatalf("resource request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"apiVersion": "example.test/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "sample-widget", "namespace": "team-a", "uid": "generated-widget-uid", "resourceVersion": "12", "labels": map[string]any{"example.test/tenant": "blue"}},
				"spec":     map[string]any{"credential": canary},
				"status":   map[string]any{"state": "Ready", "replicas": float64(3)},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := widgetPolicy(t)
	request := toolcontract.ResourceQueryRequest{
		Policy: policy, Detail: toolcontract.ResourceDetailDiagnostic,
		Query: domain.ResourceQuery{
			Scope: liveScope("team-a"), PolicyGeneration: 1, PolicyVersion: domain.ResourcePolicyVersion,
			Type: policy.Type, Verb: domain.ResourceVerbGet, View: domain.ResourceViewDescribe,
			Namespace: "team-a", Name: "sample-widget",
			Limits: domain.ResourceQueryLimits{MaxPages: 1, PageItems: 1, PageBytes: 256 * 1024, MaxItems: 1, MaxBytes: 1 << 20, MaxReturned: 1},
		},
	}
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil {
		t.Fatalf("QueryResources() error = %v", err)
	}
	if result.Validate(request) != nil || len(result.Items) != 1 || len(result.Items[0].Fields) != 3 {
		t.Fatalf("result = %#v, validation = %v", result, result.Validate(request))
	}
	if strings.Contains(fmt.Sprintf("%#v", result), canary) {
		t.Fatal("projected CRD result contains an unlisted sensitive field")
	}
	if first, second := <-requests, <-requests; first != "/apis/example.test/v1" || second != "/apis/example.test/v1/namespaces/team-a/widgets/sample-widget" {
		t.Fatalf("request order = %q, %q", first, second)
	}
}

func TestBroadCRDListUsesExactDiscoveryAndPolicyOwnedLabelSelector(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		assertBroadReadHeaders(t, request, false)
		writer.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/apis/example.test/v1" || request.URL.Query().Encode() != "timeout=10s" {
				t.Fatalf("discovery request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			}
			_, _ = writer.Write([]byte(`{"apiVersion":"v1","groupVersion":"example.test/v1","kind":"APIResourceList","resources":[{"kind":"Widget","name":"widgets","namespaced":true,"singularName":"widget","verbs":["get","list"]}]}`))
		case 2:
			if request.Method != http.MethodGet || request.URL.Path != "/apis/example.test/v1/namespaces/team-a/widgets" ||
				request.URL.Query().Encode() != "labelSelector=example.test%2Ftenant%3Dblue&limit=2&timeout=10s&timeoutSeconds=10" {
				t.Fatalf("resource request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			}
			writeUnstructuredList(t, writer, "example.test/v1", "WidgetList", "", []map[string]any{{
				"apiVersion": "example.test/v1", "kind": "Widget",
				"metadata": map[string]any{"name": "sample-widget", "namespace": "team-a", "labels": map[string]any{"example.test/tenant": "blue"}},
				"status":   map[string]any{"state": "Ready", "replicas": float64(2)},
			}})
		default:
			t.Fatalf("unexpected request %d", call)
		}
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := widgetPolicy(t)
	request := broadListRequest(policy, 2, 2, 4, 2, 1<<20)
	request.Query.Limits.PageBytes = policy.Limits.PageBytes
	request.Query.Filters = []domain.ResourceFilter{{Field: "tenant", Operator: domain.ResourceFilterEquals, Value: "blue"}}
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil || result.Validate(request) != nil || calls.Load() != 2 || len(result.Items) != 1 {
		t.Fatalf("CRD list = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(request), calls.Load())
	}
	foundTenant := false
	for _, field := range result.Items[0].Fields {
		foundTenant = foundTenant || field.Field == "tenant" && field.Present && field.Value.Value == "blue"
	}
	if !foundTenant {
		t.Fatalf("CRD fields = %#v", result.Items[0].Fields)
	}
}

func TestBroadSecretReadUsesPartialObjectMetadataMediaType(t *testing.T) {
	t.Parallel()
	canary := strings.Repeat("secret-value", 8)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/namespaces/team-a/secrets/sample-secret" || request.Method != http.MethodGet {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		assertBroadReadHeaders(t, request, true)
		if request.URL.Query().Encode() != "timeout=10s" {
			t.Fatalf("query = %q", request.URL.Query().Encode())
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata","metadata":{"name":"sample-secret","namespace":"team-a","uid":"generated-secret-uid","resourceVersion":"7"}}`))
		if strings.Contains(request.URL.RawQuery, canary) {
			t.Fatal("secret value entered request")
		}
	}))
	defer server.Close()

	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "secrets")
	request := toolcontract.ResourceQueryRequest{Policy: policy, Detail: toolcontract.ResourceDetailSummary, Query: domain.ResourceQuery{
		Scope: liveScope("team-a"), PolicyGeneration: 1, PolicyVersion: domain.ResourcePolicyVersion,
		Type: policy.Type, Verb: domain.ResourceVerbGet, View: domain.ResourceViewSummary,
		Namespace: "team-a", Name: "sample-secret",
		Limits: domain.ResourceQueryLimits{MaxPages: 1, PageItems: 1, PageBytes: 256 * 1024, MaxItems: 1, MaxBytes: 1 << 20, MaxReturned: 1},
	}}
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil || result.Validate(request) != nil || len(result.Items) != 1 || result.Items[0].Summary.Reference.Kind != "Secret" {
		t.Fatalf("QueryResources() = %#v, %v, validation = %v", result, err, result.Validate(request))
	}
	if strings.Contains(fmt.Sprintf("%#v", result), canary) {
		t.Fatal("Secret value entered the metadata-only projection")
	}
}

func TestBroadConfigMapListUsesPartialObjectMetadataMediaType(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/namespaces/team-a/configmaps" ||
			request.URL.Query().Encode() != "limit=2&timeout=10s&timeoutSeconds=10" {
			t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		assertBroadReadHeaders(t, request, true)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadataList",
			"metadata": map[string]any{"continue": ""},
			"items": []map[string]any{{
				"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadata",
				"metadata": map[string]any{
					"name": "sample-config", "namespace": "team-a", "uid": "generated-config-uid",
					"resourceVersion": "7", "creationTimestamp": "2026-09-05T01:02:03Z",
				},
			}},
		})
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "config-maps")
	request := broadListRequest(policy, 2, 2, 4, 2, 1<<20)
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil || result.Validate(request) != nil || calls.Load() != 1 || len(result.Items) != 1 ||
		result.Items[0].Summary.Reference.Name != "sample-config" {
		t.Fatalf("ConfigMap metadata list = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(request), calls.Load())
	}
}

func TestBroadListReturnsVisiblePartialStateAtPageCeiling(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/namespaces/team-a/pods" || request.URL.Query().Encode() != "limit=1&timeout=10s&timeoutSeconds=10" {
			t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		assertBroadReadHeaders(t, request, false)
		writer.Header().Set("Content-Type", "application/json")
		writeUnstructuredList(t, writer, "v1", "PodList", "opaque-next", []map[string]any{podObject("sample-a", "Running")})
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "pods")
	request := broadListRequest(policy, 1, 1, 2, 2, 1<<20)
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil || result.Validate(request) != nil || !result.Page.Partial || !result.Page.Truncated ||
		!result.Page.MoreAvailable || result.Page.Reason != "page_limit" || calls.Load() != 1 {
		t.Fatalf("partial result = %#v, error = %v, validation = %v", result, err, result.Validate(request))
	}
}

func TestBroadListReturnsVisiblePartialStateAtItemCeiling(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/namespaces/team-a/pods" || request.URL.Query().Encode() != "limit=2&timeout=10s&timeoutSeconds=10" {
			t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		assertBroadReadHeaders(t, request, false)
		writer.Header().Set("Content-Type", "application/json")
		writeUnstructuredList(t, writer, "v1", "PodList", "opaque-next", []map[string]any{
			podObject("sample-a", "Running"),
			podObject("sample-b", "Pending"),
		})
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "pods")
	request := broadListRequest(policy, 2, 3, 2, 2, 1<<20)
	request.Query.Filters = []domain.ResourceFilter{{
		Field: "phase", Operator: domain.ResourceFilterEquals, Value: "Succeeded",
	}}
	result, err := reader.QueryResources(context.Background(), request)
	if err != nil || result.Validate(request) != nil || len(result.Items) != 0 || result.Page.ScannedItems != 2 || result.Page.MatchedItems != 0 ||
		!result.Page.Partial || !result.Page.Truncated || !result.Page.MoreAvailable || result.Page.Reason != "item_limit" || calls.Load() != 1 {
		t.Fatalf("item-limited result = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(request), calls.Load())
	}
}

func TestBroadClusterAndAllNamespaceListsUseExactPaths(t *testing.T) {
	t.Parallel()

	t.Run("cluster scoped", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			if request.Method != http.MethodGet || request.URL.Path != "/api/v1/nodes" || request.URL.Query().Encode() != "limit=3&timeout=10s&timeoutSeconds=10" {
				t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			}
			assertBroadReadHeaders(t, request, false)
			writer.Header().Set("Content-Type", "application/json")
			writeUnstructuredList(t, writer, "v1", "NodeList", "", []map[string]any{{
				"apiVersion": "v1", "kind": "Node",
				"metadata": map[string]any{"name": "sample-node", "uid": "node-uid", "resourceVersion": "4"},
				"spec":     map[string]any{"unschedulable": false},
				"status":   map[string]any{"conditions": []map[string]any{{"type": "Ready", "status": "True"}}},
			}})
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		policy := resourcePolicy(t, "nodes")
		request := broadListRequest(policy, 3, 2, 6, 3, 1<<20)
		request.Query.Namespace = ""
		result, err := reader.QueryResources(context.Background(), request)
		if err != nil || result.Validate(request) != nil || calls.Load() != 1 || len(result.Items) != 1 ||
			result.Items[0].Summary.Reference.Namespace != "" || result.Items[0].Summary.Status.Phase != "Ready" {
			t.Fatalf("cluster result = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(request), calls.Load())
		}
	})

	t.Run("all namespaces", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			if request.Method != http.MethodGet || request.URL.Path != "/api/v1/pods" || request.URL.Query().Encode() != "limit=2&timeout=10s&timeoutSeconds=10" {
				t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
			}
			assertBroadReadHeaders(t, request, false)
			writer.Header().Set("Content-Type", "application/json")
			item := podObject("sample-other", "Running")
			item["metadata"].(map[string]any)["namespace"] = "team-b"
			writeUnstructuredList(t, writer, "v1", "PodList", "", []map[string]any{item})
		}))
		defer server.Close()
		scope := liveScope("team-a")
		scope.NamespaceAccess = domain.NamespaceAccessAll
		reader, closeReader := newHTTPToolResourceReaderForScope(t, server, scope, alwaysCurrentResourcePolicy{})
		defer closeReader()
		policy := resourcePolicy(t, "pods")
		request := broadListRequest(policy, 2, 2, 4, 2, 1<<20)
		request.Query.Scope = scope
		request.Query.Namespace = ""
		request.Query.AllNamespaces = true
		result, err := reader.QueryResources(context.Background(), request)
		if err != nil || result.Validate(request) != nil || calls.Load() != 1 || len(result.Items) != 1 ||
			result.Items[0].Summary.Reference.Namespace != "team-b" {
			t.Fatalf("all-Namespace result = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(request), calls.Load())
		}
	})
}

func TestBroadLocallyDecidableDenialsPerformZeroKubernetesCalls(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	basePolicy := resourcePolicy(t, "pods")

	tests := []struct {
		name   string
		mutate func(*toolcontract.ResourceQueryRequest)
	}{
		{name: "unknown API identity", mutate: func(request *toolcontract.ResourceQueryRequest) {
			request.Query.Type.Resource = "unknowns"
		}},
		{name: "unadmitted verb", mutate: func(request *toolcontract.ResourceQueryRequest) {
			request.Policy.Verbs = []domain.ResourceVerb{domain.ResourceVerbGet}
		}},
		{name: "subresource", mutate: func(request *toolcontract.ResourceQueryRequest) {
			request.Policy.Type.Resource = "pods/status"
			request.Query.Type = request.Policy.Type
		}},
		{name: "current policy namespace expansion", mutate: func(request *toolcontract.ResourceQueryRequest) {
			request.Query.Namespace = "team-b"
		}},
		{name: "sensitive field predicate", mutate: func(request *toolcontract.ResourceQueryRequest) {
			request.Policy.Fields = append(request.Policy.Fields, domain.ResourceFieldPolicy{
				ID: "credential", Path: "spec.credential", Scalar: domain.ResourceScalarString,
				DataClass: domain.ResourceDataSensitive, SelectorSource: domain.ResourceSelectorNone,
				Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals},
			})
			request.Query.Filters = []domain.ResourceFilter{{Field: "credential", Operator: domain.ResourceFilterEquals, Value: "blocked"}}
		}},
		{name: "invalid page byte ceiling", mutate: func(request *toolcontract.ResourceQueryRequest) {
			request.Query.Limits.PageBytes = 0
		}},
		{name: "invalid typed predicate", mutate: func(request *toolcontract.ResourceQueryRequest) {
			request.Policy.Fields = append(request.Policy.Fields, domain.ResourceFieldPolicy{
				ID: "test_count", Path: "status.testCount", Scalar: domain.ResourceScalarInteger,
				DataClass: domain.ResourceDataStatus, SelectorSource: domain.ResourceSelectorNone,
				Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals}, Evidence: true,
			})
			request.Query.Filters = []domain.ResourceFilter{{Field: "test_count", Operator: domain.ResourceFilterEquals, Value: "many"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := broadListRequest(basePolicy, 2, 2, 4, 2, 1<<20)
			test.mutate(&request)
			_, err := reader.QueryResources(context.Background(), request)
			assertKubeErrorClass(t, err, ClassPolicyDenied)
			if calls.Load() != 0 {
				t.Fatalf("Kubernetes calls = %d, want 0", calls.Load())
			}
		})
	}
}

func TestBroadQueryMapsExactRBACDenialWithoutRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/namespaces/team-a/pods" || request.URL.Query().Encode() != "limit=2&timeout=10s&timeoutSeconds=10" {
			t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		assertBroadReadHeaders(t, request, false)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403,"message":"synthetic forbidden detail"}`))
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "pods")
	request := broadListRequest(policy, 2, 2, 4, 2, 1<<20)
	_, err := reader.QueryResources(context.Background(), request)
	assertKubeErrorClass(t, err, ClassPermissionDenied)
	if calls.Load() != 1 || strings.Contains(err.Error(), "synthetic forbidden detail") {
		t.Fatalf("RBAC calls/error = %d/%v", calls.Load(), err)
	}
}

func TestBroadCRDDiscoveryCannotExpandUnknownAPI(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/apis/example.test/v1" || request.URL.Query().Encode() != "timeout=10s" {
			t.Fatalf("request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		assertBroadReadHeaders(t, request, false)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"apiVersion":"v1","groupVersion":"example.test/v1","kind":"APIResourceList","resources":[{"kind":"Gadget","name":"gadgets","namespaced":true,"verbs":["get","list"]}]}`))
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := widgetPolicy(t)
	request := toolcontract.ResourceQueryRequest{Policy: policy, Detail: toolcontract.ResourceDetailSummary, Query: domain.ResourceQuery{
		Scope: liveScope("team-a"), PolicyGeneration: 1, PolicyVersion: domain.ResourcePolicyVersion,
		Type: policy.Type, Verb: domain.ResourceVerbGet, View: domain.ResourceViewSummary,
		Namespace: "team-a", Name: "sample-widget",
		Limits: domain.ResourceQueryLimits{MaxPages: 1, PageItems: 1, PageBytes: 256 * 1024, MaxItems: 1, MaxBytes: 1 << 20, MaxReturned: 1},
	}}
	_, err := reader.QueryResources(context.Background(), request)
	assertKubeErrorClass(t, err, ClassUnsupported)
	if calls.Load() != 1 {
		t.Fatalf("Kubernetes calls = %d, want discovery only", calls.Load())
	}
}

func TestBroadMalformedContinuationStopsBeforeAnotherKubernetesCall(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/namespaces/team-a/pods" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		writeUnstructuredList(t, writer, "v1", "PodList", "malformed\ncontinuation", []map[string]any{podObject("sample-a", "Running")})
	}))
	defer server.Close()
	reader, closeReader := newHTTPToolResourceReader(t, server)
	defer closeReader()
	policy := resourcePolicy(t, "pods")
	request := broadListRequest(policy, 1, 3, 3, 3, 1<<20)
	_, err := reader.QueryResources(context.Background(), request)
	assertKubeErrorClass(t, err, ClassInvalidExternalResponse)
	if calls.Load() != 1 {
		t.Fatalf("Kubernetes calls = %d, want 1 before rejecting the server continuation", calls.Load())
	}
}

func TestBroadQueryCancellationStalePolicyAndOversizeFailClosed(t *testing.T) {
	t.Parallel()
	policy := resourcePolicy(t, "pods")
	request := broadListRequest(policy, 2, 2, 4, 2, 512)

	t.Run("cancelled before request", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := reader.QueryResources(ctx, request)
		assertKubeErrorClass(t, err, ClassCancelled)
		if calls.Load() != 0 {
			t.Fatalf("Kubernetes calls = %d, want 0", calls.Load())
		}
	})

	t.Run("cancelled in flight", func(t *testing.T) {
		var calls atomic.Int32
		started := make(chan struct{})
		server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			close(started)
			<-request.Context().Done()
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := reader.QueryResources(ctx, request)
			result <- err
		}()
		<-started
		cancel()
		err := <-result
		assertKubeErrorClass(t, err, ClassCancelled)
		if calls.Load() != 1 {
			t.Fatalf("Kubernetes calls = %d, want 1", calls.Load())
		}
	})

	t.Run("deadline before request", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		defer cancel()
		_, err := reader.QueryResources(ctx, request)
		assertKubeErrorClass(t, err, ClassTimeout)
		if calls.Load() != 0 {
			t.Fatalf("Kubernetes calls = %d, want 0", calls.Load())
		}
	})

	t.Run("stale policy before request", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReaderWithGuard(t, server, &sequenceResourcePolicy{results: []bool{false}})
		defer closeReader()
		_, err := reader.QueryResources(context.Background(), request)
		assertKubeErrorClass(t, err, ClassStaleScope)
		if calls.Load() != 0 {
			t.Fatalf("Kubernetes calls = %d, want 0", calls.Load())
		}
	})

	t.Run("stale policy after response", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writeUnstructuredList(t, writer, "v1", "PodList", "", []map[string]any{podObject("sample-a", "Running")})
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReaderWithGuard(t, server, &sequenceResourcePolicy{results: []bool{true, true, false}})
		defer closeReader()
		_, err := reader.QueryResources(context.Background(), request)
		assertKubeErrorClass(t, err, ClassStaleScope)
		if calls.Load() != 1 {
			t.Fatalf("Kubernetes calls = %d, want 1", calls.Load())
		}
	})

	t.Run("cumulative byte limit after complete page", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			call := calls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			if call == 1 {
				writeUnstructuredList(t, writer, "v1", "PodList", "next-page", []map[string]any{podObject("sample-a", "Running")})
				return
			}
			if request.URL.Query().Get("continue") != "next-page" {
				t.Fatalf("continuation = %q", request.URL.Query().Get("continue"))
			}
			writeUnstructuredList(t, writer, "v1", "PodList", "later-page", []map[string]any{
				podObject("sample-b", strings.Repeat("x", 2000)),
			})
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		byteRequest := broadListRequest(policy, 2, 3, 6, 4, 700)
		result, err := reader.QueryResources(context.Background(), byteRequest)
		if err != nil || result.Validate(byteRequest) != nil || calls.Load() != 2 || len(result.Items) != 1 ||
			!result.Page.Partial || result.Page.Reason != "byte_limit" || !result.Page.MoreAvailable || result.Page.PagesRead != 1 {
			t.Fatalf("byte-limited result = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(byteRequest), calls.Load())
		}
	})

	t.Run("per-page byte limit after complete page", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			call := calls.Add(1)
			assertBroadReadHeaders(t, request, false)
			writer.Header().Set("Content-Type", "application/json")
			if call == 1 {
				writeUnstructuredList(t, writer, "v1", "PodList", "next-page", []map[string]any{podObject("sample-a", "Running")})
				return
			}
			if request.URL.Query().Get("continue") != "next-page" {
				t.Fatalf("continuation = %q", request.URL.Query().Get("continue"))
			}
			writeUnstructuredList(t, writer, "v1", "PodList", "later-page", []map[string]any{
				podObject("sample-b", strings.Repeat("x", 1000)),
			})
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		pageRequest := broadListRequest(policy, 2, 3, 6, 4, 4*1024)
		pageRequest.Query.Limits.PageBytes = 512
		result, err := reader.QueryResources(context.Background(), pageRequest)
		if err != nil || result.Validate(pageRequest) != nil || calls.Load() != 2 || len(result.Items) != 1 ||
			!result.Page.Partial || result.Page.Reason != "byte_limit" || !result.Page.MoreAvailable || result.Page.PagesRead != 1 ||
			result.Page.ObservedBytes >= pageRequest.Query.Limits.MaxBytes {
			t.Fatalf("page-byte-limited result = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(pageRequest), calls.Load())
		}
	})

	t.Run("oversize first page", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writeUnstructuredList(t, writer, "v1", "PodList", "", []map[string]any{
				podObject("sample-"+strings.Repeat("a", 200), strings.Repeat("x", 600)),
			})
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		_, err := reader.QueryResources(context.Background(), request)
		assertKubeErrorClass(t, err, ClassBudgetExhausted)
		if calls.Load() != 1 {
			t.Fatalf("Kubernetes calls = %d, want 1", calls.Load())
		}
	})

	t.Run("truncated predicate field reports partial even when item is filtered out", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writeUnstructuredList(t, writer, "v1", "PodList", "", []map[string]any{
				podObject("sample-a", strings.Repeat("x", maxBroadResourceFieldBytes)+"wanted-suffix"),
			})
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		limited := broadListRequest(policy, 2, 2, 4, 2, 32*1024)
		limited.Query.Limits.PageBytes = 16 * 1024
		limited.Query.Filters = []domain.ResourceFilter{{Field: "phase", Operator: domain.ResourceFilterContains, Value: "wanted-suffix"}}
		result, err := reader.QueryResources(context.Background(), limited)
		if err != nil || result.Validate(limited) != nil || calls.Load() != 1 || len(result.Items) != 0 ||
			!result.Page.Partial || result.Page.Reason != "field_limit" {
			t.Fatalf("field-limited result = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(limited), calls.Load())
		}
	})

	t.Run("return ceiling preserves bounded matched count", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writeUnstructuredList(t, writer, "v1", "PodList", "", []map[string]any{
				podObject("sample-a", "Running"), podObject("sample-b", "Running"), podObject("sample-c", "Running"),
			})
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		limited := broadListRequest(policy, 3, 2, 4, 2, 32*1024)
		result, err := reader.QueryResources(context.Background(), limited)
		if err != nil || result.Validate(limited) != nil || calls.Load() != 1 || len(result.Items) != 2 ||
			result.Page.MatchedItems != 3 || !result.Page.Partial || !result.Page.MoreAvailable || result.Page.Reason != "return_limit" {
			t.Fatalf("return-limited result = %#v, error = %v, validation = %v, calls = %d", result, err, result.Validate(limited), calls.Load())
		}
	})

	t.Run("discovery consumes aggregate budget before resource request", func(t *testing.T) {
		var calls atomic.Int32
		discovery := []byte(`{"apiVersion":"v1","groupVersion":"example.test/v1","kind":"APIResourceList","resources":[{"kind":"Widget","name":"widgets","namespaced":true,"verbs":["get","list"]}]}`)
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			if request.URL.Path != "/apis/example.test/v1" {
				t.Fatalf("unexpected resource request %s", request.URL.Path)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(discovery)
		}))
		defer server.Close()
		reader, closeReader := newHTTPToolResourceReader(t, server)
		defer closeReader()
		customPolicy := widgetPolicy(t)
		limited := toolcontract.ResourceQueryRequest{Policy: customPolicy, Detail: toolcontract.ResourceDetailSummary, Query: domain.ResourceQuery{
			Scope: liveScope("team-a"), PolicyGeneration: 1, PolicyVersion: domain.ResourcePolicyVersion,
			Type: customPolicy.Type, Verb: domain.ResourceVerbGet, View: domain.ResourceViewSummary,
			Namespace: "team-a", Name: "sample-widget",
			Limits: domain.ResourceQueryLimits{MaxPages: 1, PageItems: 1, PageBytes: len(discovery), MaxItems: 1, MaxBytes: len(discovery), MaxReturned: 1},
		}}
		_, err := reader.QueryResources(context.Background(), limited)
		assertKubeErrorClass(t, err, ClassBudgetExhausted)
		if calls.Load() != 1 {
			t.Fatalf("Kubernetes calls = %d, want discovery only", calls.Load())
		}
	})
}

func TestResourceResponseBudgetAllowsExactCeilingAndRejectsOneOver(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		body      string
		exhausted bool
	}{
		{name: "exact ceiling", body: "abcd", exhausted: false},
		{name: "one over", body: "abcde", exhausted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := &responseByteBudget{remaining: 4}
			body := &budgetedResponseBody{base: io.NopCloser(strings.NewReader(test.body)), budgets: []*responseByteBudget{budget}}
			read, err := io.ReadAll(body)
			_, exhausted := budget.snapshot()
			if test.exhausted {
				if !errors.Is(err, errResourceResponseLimit) || !exhausted || string(read) != "abcd" {
					t.Fatalf("one-over read = %q/%v, exhausted=%t", read, err, exhausted)
				}
				return
			}
			if err != nil || exhausted || string(read) != test.body {
				t.Fatalf("exact read = %q/%v, exhausted=%t", read, err, exhausted)
			}
		})
	}
}

func TestBroadIntegerPredicatesRetainInt64Precision(t *testing.T) {
	t.Parallel()

	field := toolcontract.ResourceFieldObservation{
		Field: "replicas", Scalar: domain.ResourceScalarInteger, Present: true,
		Value: toolcontract.ExternalText{Value: "9007199254740993"},
	}
	if !resourceFieldMatches(field, domain.ResourceFilter{
		Field: "replicas", Operator: domain.ResourceFilterGreaterThan, Value: "9007199254740992",
	}) {
		t.Fatal("integer comparison lost precision above the exact float64 range")
	}
	if resourceFieldMatches(field, domain.ResourceFilter{
		Field: "replicas", Operator: domain.ResourceFilterEquals, Value: "9007199254740992",
	}) {
		t.Fatal("distinct int64 values compared equal")
	}
}

func broadListRequest(policy domain.ResourcePolicy, pageItems, pages, scanned, returned, bytes int) toolcontract.ResourceQueryRequest {
	return toolcontract.ResourceQueryRequest{Policy: policy, Query: domain.ResourceQuery{
		Scope: liveScope("team-a"), PolicyGeneration: 1, PolicyVersion: domain.ResourcePolicyVersion,
		Type: policy.Type, Verb: domain.ResourceVerbList, View: domain.ResourceViewList, Namespace: "team-a",
		Limits: domain.ResourceQueryLimits{MaxPages: pages, PageItems: pageItems, PageBytes: min(bytes, domain.MaxResourcePageBytes), MaxItems: scanned, MaxBytes: bytes, MaxReturned: returned},
	}}
}

func resourcePolicy(t *testing.T, id string) domain.ResourcePolicy {
	t.Helper()
	policy, found := domain.DefaultResourcePolicyCatalog().Resolve(id)
	if !found {
		t.Fatalf("resource policy %q is missing", id)
	}
	return policy
}

func widgetPolicy(t *testing.T) domain.ResourcePolicy {
	t.Helper()
	policy := domain.ResourcePolicy{
		Type:  domain.ResourceType{ID: "widgets", Group: "example.test", Version: "v1", Resource: "widgets", Kind: "Widget", Scope: domain.ResourceScopeNamespaced},
		Verbs: []domain.ResourceVerb{domain.ResourceVerbGet, domain.ResourceVerbList},
		Fields: []domain.ResourceFieldPolicy{
			{ID: "replicas", Path: "status.replicas", Scalar: domain.ResourceScalarInteger, DataClass: domain.ResourceDataStatus, SelectorSource: domain.ResourceSelectorNone, Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals}, Evidence: true},
			{ID: "state", Path: "status.state", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataStatus, SelectorSource: domain.ResourceSelectorNone, Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals}, Evidence: true},
			{ID: "tenant", Path: "metadata.labels.tenant", Scalar: domain.ResourceScalarString, DataClass: domain.ResourceDataMetadata, SelectorSource: domain.ResourceSelectorLabel, SelectorKey: "example.test/tenant", Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals, domain.ResourceFilterExists}},
		},
		Limits: domain.ResourceQueryLimits{MaxPages: 4, PageItems: 20, PageBytes: 256 * 1024, MaxItems: 80, MaxBytes: 1 << 20, MaxReturned: 20},
	}
	if policy.Validate() != nil {
		t.Fatalf("widget policy is invalid: %#v", policy)
	}
	return policy
}

func podObject(name, phase string) map[string]any {
	return map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": "team-a", "uid": "uid-" + name, "resourceVersion": "1",
			"labels": map[string]any{"app.kubernetes.io/name": "sample"},
		},
		"status": map[string]any{"phase": phase},
	}
}

func writeUnstructuredList(t *testing.T, writer http.ResponseWriter, apiVersion, kind, continuation string, items []map[string]any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"continue": continuation}, "items": items,
	}); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
}

func assertBroadReadHeaders(t *testing.T, request *http.Request, metadataOnly bool) {
	t.Helper()
	if request.Header.Get("Content-Type") != "" {
		t.Fatalf("GET Content-Type = %q, want empty", request.Header.Get("Content-Type"))
	}
	accept := request.Header.Get("Accept")
	if metadataOnly {
		if !strings.Contains(accept, "PartialObjectMetadata") {
			t.Fatalf("metadata Accept = %q", accept)
		}
	} else if !strings.Contains(accept, "application/json") {
		t.Fatalf("Accept = %q", accept)
	}
}

func newHTTPToolResourceReaderWithGuard(
	t *testing.T,
	server *httptest.Server,
	guard toolcontract.PolicyGenerationGuard,
) (*ToolResourceReader, func()) {
	return newHTTPToolResourceReaderForScope(t, server, liveScope("team-a"), guard)
}

func newHTTPToolResourceReaderForScope(
	t *testing.T,
	server *httptest.Server,
	scope domain.ClusterScope,
	guard toolcontract.PolicyGenerationGuard,
) (*ToolResourceReader, func()) {
	t.Helper()
	path := writeNamespacedKubeconfig(t, server.URL, testServerCAData(server), scope.Namespace)
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	gateway, err := NewGateway(factory)
	if err != nil {
		t.Fatalf("NewGateway() error = %v", err)
	}
	client, err := gateway.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	reader, err := NewToolResourceReader(gateway, client, scope, guard)
	if err != nil {
		client.Close()
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	return reader, client.Close
}
