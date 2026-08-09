package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestGatewayListsContextsAndProvidesBoundedNamespaceReads(t *testing.T) {
	objects := make([]runtime.Object, 0, 61)
	for index := 0; index < 60; index++ {
		objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("team-%02d", index)}})
	}
	objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}})
	gateway, client, fakeClient := newFakeGateway(t, "team-a", objects...)
	defer client.Close()

	contexts, err := gateway.Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts() error = %v", err)
	}
	wantContext := application.ContextCandidate{
		Name:             "selected",
		DefaultNamespace: "team-a",
		Current:          true,
	}
	if !equalContextCandidates(contexts, []application.ContextCandidate{wantContext}) {
		t.Fatalf("Contexts() = %#v, want %#v", contexts, []application.ContextCandidate{wantContext})
	}
	if actions := fakeClient.Actions(); len(actions) != 0 {
		t.Fatalf("Contexts/Create performed %d Kubernetes actions, want 0", len(actions))
	}

	if err := gateway.VerifyNamespace(context.Background(), client, "team-a"); err != nil {
		t.Fatalf("VerifyNamespace() error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "namespaces", "")
	fakeClient.ClearActions()

	list, err := gateway.ListNamespaces(context.Background(), client, domain.MaxResourceSummaries)
	if err != nil {
		t.Fatalf("ListNamespaces() error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "list", "", "v1", "namespaces", "")
	if len(list.Items) != domain.MaxResourceSummaries || !list.Truncated {
		t.Fatalf("namespace result count/truncated = %d/%v, want %d/true", len(list.Items), list.Truncated, domain.MaxResourceSummaries)
	}
	if !sort.SliceIsSorted(list.Items, func(left, right int) bool { return list.Items[left].Name < list.Items[right].Name }) {
		t.Fatal("Namespace summaries are not sorted by name")
	}
}

func TestGatewayNamespaceValidationAndForbiddenErrorsFailClosed(t *testing.T) {
	gateway, client, fakeClient := newFakeGateway(t, "team-a", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}})
	defer client.Close()

	err := gateway.VerifyNamespace(context.Background(), client, "")
	assertKubeSafeError(t, err, ClassInvalidInput, "kubernetes_namespace_invalid")
	if actions := fakeClient.Actions(); len(actions) != 0 {
		t.Fatalf("invalid Namespace performed %d actions, want 0", len(actions))
	}

	fakeClient.PrependReactor("get", "namespaces", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "team-b", errors.New("generated denial"))
	})
	err = gateway.VerifyNamespace(context.Background(), client, "team-b")
	assertKubeSafeError(t, err, ClassPermissionDenied, "kubernetes_permission_denied")
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "namespaces", "")
}

func TestGatewayListResourcesSendsFixedHTTPPathAndLimit(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.Clone(context.Background())
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"next-page"},"items":[{"metadata":{"name":"sample-pod","namespace":"team-a"},"status":{"phase":"Pending"}}]}`))
	}))
	defer server.Close()

	path := writeNamespacedKubeconfig(t, server.URL, testServerCAData(server), "team-a")
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
	defer client.Close()
	scope := domain.ClusterScope{
		Context:     "selected",
		Namespace:   "team-a",
		Generation:  1,
		ActivatedAt: testActivationTime(),
	}
	list, err := gateway.ListResources(context.Background(), client, scope, domain.ResourceKindPod, 17)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(list.Items) != 1 || !list.Truncated {
		t.Fatalf("ListResources() = %#v, want one truncated item", list)
	}

	select {
	case request := <-requests:
		if request.Method != http.MethodGet {
			t.Errorf("request method = %q, want GET", request.Method)
		}
		if request.URL.EscapedPath() != "/api/v1/namespaces/team-a/pods" {
			t.Errorf("request path = %q", request.URL.EscapedPath())
		}
		query := request.URL.Query()
		if query.Get("limit") != "17" {
			t.Errorf("request query = %q, want limit=17", request.URL.RawQuery)
		}
		for _, forbidden := range []string{"labelSelector", "fieldSelector", "watch", "continue"} {
			if _, exists := query[forbidden]; exists {
				t.Errorf("request unexpectedly contains %s", forbidden)
			}
		}
	default:
		t.Fatal("Kubernetes HTTP request was not captured")
	}
}

func newFakeGateway(t *testing.T, namespace string, objects ...runtime.Object) (*Gateway, application.ScopeClient, *kubernetesfake.Clientset) {
	t.Helper()
	path := writeNamespacedKubeconfig(t, "https://cluster.example.invalid", nil, namespace)
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	fakeClient := kubernetesfake.NewSimpleClientset(objects...)
	factory.newTypedClient = func(*rest.Config, *http.Client) (kubernetes.Interface, error) {
		return fakeClient, nil
	}
	gateway, err := NewGateway(factory)
	if err != nil {
		t.Fatalf("NewGateway() error = %v", err)
	}
	client, err := gateway.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return gateway, client, fakeClient
}

func writeNamespacedKubeconfig(t *testing.T, server string, caData []byte, namespace string) string {
	t.Helper()
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = "selected"
	raw.Clusters["cluster"] = &clientcmdapi.Cluster{Server: server, CertificateAuthorityData: caData}
	raw.AuthInfos["user"] = &clientcmdapi.AuthInfo{}
	raw.Contexts["selected"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: "user", Namespace: namespace}
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeKubeconfigFile(t, path, *raw)
	return path
}

func equalContextCandidates(left, right []application.ContextCandidate) bool {
	return fmt.Sprintf("%#v", left) == fmt.Sprintf("%#v", right)
}

func testActivationTime() time.Time {
	return time.UnixMilli(1).UTC()
}

func assertNoCanary(t *testing.T, value any, canary string) {
	t.Helper()
	if strings.Contains(fmt.Sprintf("%#v", value), canary) {
		t.Fatal("safe projection contains a prohibited canary")
	}
}
