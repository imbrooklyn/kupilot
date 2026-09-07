package kube

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestClientFactoryBuildsFixedTypedBundleWithoutNetworkIO(t *testing.T) {
	t.Parallel()

	path := writeSingleContextKubeconfig(t, "https://cluster.example.invalid", nil, &clientcmdapi.AuthInfo{})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}

	fakeClient := kubernetesfake.NewSimpleClientset()
	var captured *rest.Config
	factory.newTypedClient = func(config *rest.Config, _ *http.Client) (kubernetes.Interface, error) {
		captured = rest.CopyConfig(config)
		return fakeClient, nil
	}

	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()

	if captured == nil {
		t.Fatal("typed client constructor was not called")
	}
	if captured.UserAgent != DefaultUserAgent {
		t.Errorf("UserAgent = %q, want %q", captured.UserAgent, DefaultUserAgent)
	}
	if captured.QPS != DefaultClientQPS {
		t.Errorf("QPS = %v, want %v", captured.QPS, DefaultClientQPS)
	}
	if captured.Burst != DefaultClientBurst {
		t.Errorf("Burst = %d, want %d", captured.Burst, DefaultClientBurst)
	}
	if captured.Timeout != DefaultRequestTimeout {
		t.Errorf("Timeout = %v, want %v", captured.Timeout, DefaultRequestTimeout)
	}
	if captured.Insecure {
		t.Fatal("typed client config enables insecure TLS")
	}
	if captured.ExecProvider != nil {
		t.Fatal("typed client received a raw exec provider")
	}
	if actions := fakeClient.Actions(); len(actions) != 0 {
		t.Fatalf("factory construction performed %d Kubernetes actions, want 0", len(actions))
	}
	if got := bundle.Context(); got != (ContextInfo{Name: "selected", Namespace: "default", Current: true}) {
		t.Fatalf("bundle.Context() = %#v", got)
	}
}

func TestClientFactoryAcceptsExactMaximumTimeoutAndRejectsOneOverWithoutNetworkIO(t *testing.T) {
	t.Parallel()

	loader := newConfigLoaderForPaths([]string{"unused"})
	factory, err := NewClientFactory(loader, ExecCredentialsAllow, MaxRequestTimeout)
	if err != nil || factory.requestTimeout != 180*time.Second {
		t.Fatalf("NewClientFactory(maximum) = %#v, %v", factory, err)
	}
	if _, err := NewClientFactory(loader, ExecCredentialsAllow, MaxRequestTimeout+time.Nanosecond); err == nil {
		t.Fatal("NewClientFactory(one over) error = nil")
	}
}

func TestClientFactoryRejectsUnsafeTLSBeforeClientConstruction(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("c", 41) + "-generated"
	tests := []struct {
		name    string
		cluster *clientcmdapi.Cluster
		class   ErrorClass
		code    string
	}{
		{
			name:    "insecure skip verification",
			cluster: &clientcmdapi.Cluster{Server: "https://cluster.example.invalid", InsecureSkipTLSVerify: true},
			class:   ClassPolicyDenied,
			code:    "kubernetes_insecure_tls_denied",
		},
		{
			name:    "plain HTTP",
			cluster: &clientcmdapi.Cluster{Server: "http://cluster.example.invalid"},
			class:   ClassPolicyDenied,
			code:    "kubernetes_transport_policy_denied",
		},
		{
			name: "invalid CA data",
			cluster: &clientcmdapi.Cluster{
				Server:                   "https://cluster.example.invalid",
				CertificateAuthorityData: []byte(canary),
			},
			class: ClassConfigurationInvalid,
			code:  "kubernetes_transport_invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := writeContextKubeconfig(t, test.cluster, &clientcmdapi.AuthInfo{})
			factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
			if err != nil {
				t.Fatalf("NewClientFactory() error = %v", err)
			}
			constructorCalls := 0
			factory.newTypedClient = func(*rest.Config, *http.Client) (kubernetes.Interface, error) {
				constructorCalls++
				return kubernetesfake.NewSimpleClientset(), nil
			}

			_, err = factory.Create(context.Background(), "selected")
			assertKubeSafeError(t, err, test.class, test.code)
			if constructorCalls != 0 {
				t.Fatalf("typed client constructor calls = %d, want 0", constructorCalls)
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatal("safe TLS error contains a CA canary")
			}
		})
	}
}

func TestClientFactorySendsFixedUserAgentOverVerifiedTLS(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("t", 43) + "-generated"
	headers := make(chan http.Header, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertExactPodGet(t, request, DefaultRequestTimeout)
		headers <- request.Header.Clone()
		writePodResponse(writer)
	}))
	defer server.Close()

	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), &clientcmdapi.AuthInfo{Token: token})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()

	if _, err := bundle.typed.CoreV1().Pods("default").Get(context.Background(), "synthetic", metav1.GetOptions{}); err != nil {
		t.Fatalf("typed Pod Get() error = %v", err)
	}
	got := <-headers
	if got.Get("User-Agent") != DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got.Get("User-Agent"), DefaultUserAgent)
	}
	if got.Get("Authorization") != "Bearer "+token {
		t.Fatal("Authorization header does not contain the selected synthetic credential")
	}
}

func TestClientFactoryResolvesRelativeKubeconfigCAFile(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertExactPodGet(t, request, DefaultRequestTimeout)
		writePodResponse(writer)
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cluster-ca.pem"), testServerCAData(server), 0o600); err != nil {
		t.Fatalf("write relative CA fixture: %v", err)
	}
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = "selected"
	raw.Clusters["cluster"] = &clientcmdapi.Cluster{
		Server:               server.URL,
		CertificateAuthority: "cluster-ca.pem",
	}
	raw.AuthInfos["user"] = &clientcmdapi.AuthInfo{}
	raw.Contexts["selected"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: "user"}
	path := filepath.Join(root, "config.yaml")
	writeKubeconfigFile(t, path, *raw)

	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()

	if _, err := bundle.typed.CoreV1().Pods("default").Get(context.Background(), "synthetic", metav1.GetOptions{}); err != nil {
		t.Fatalf("typed Pod Get() error = %v", err)
	}
}

func TestClientFactoryRejectsRedirectWithoutForwardingCredential(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("r", 47) + "-generated"
	var redirectedCalls atomic.Int32
	redirected := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedCalls.Add(1)
		writePodResponse(writer)
	}))
	defer redirected.Close()

	firstHeaders := make(chan http.Header, 1)
	first := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertExactPodGet(t, request, DefaultRequestTimeout)
		firstHeaders <- request.Header.Clone()
		http.Redirect(writer, request, redirected.URL+"/api/v1/namespaces/default/pods/synthetic", http.StatusTemporaryRedirect)
	}))
	defer first.Close()

	path := writeSingleContextKubeconfig(t, first.URL, testServerCAData(first), &clientcmdapi.AuthInfo{Token: token})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()

	_, rawErr := bundle.typed.CoreV1().Pods("default").Get(context.Background(), "synthetic", metav1.GetOptions{})
	err = classifyKubernetesError("read_namespace", rawErr)
	assertKubeSafeError(t, err, ClassPolicyDenied, "kubernetes_redirect_denied")
	if strings.Contains(err.Error(), token) {
		t.Fatal("redirect error contains the credential canary")
	}
	if got := (<-firstHeaders).Get("Authorization"); got != "Bearer "+token {
		t.Fatal("initial same-origin request did not receive its synthetic credential")
	}
	if got := redirectedCalls.Load(); got != 0 {
		t.Fatalf("redirect target calls = %d, want 0", got)
	}
}

func TestClientBundleOwnerCancellationStopsInFlightRequest(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		assertExactPodGet(t, request, DefaultRequestTimeout)
		started <- struct{}{}
		<-request.Context().Done()
	}))
	defer server.Close()

	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), &clientcmdapi.AuthInfo{})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	owner, cancelOwner := context.WithCancel(context.Background())
	bundle, err := factory.Create(owner, "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()

	result := make(chan error, 1)
	go func() {
		_, err := bundle.typed.CoreV1().Pods("default").Get(context.Background(), "synthetic", metav1.GetOptions{})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Kubernetes request did not start")
	}
	cancelOwner()
	select {
	case rawErr := <-result:
		err := classifyKubernetesError("read_namespace", rawErr)
		assertKubeSafeError(t, err, ClassCancelled, "kubernetes_request_cancelled")
	case <-time.After(3 * time.Second):
		t.Fatal("owner cancellation did not stop the Kubernetes request")
	}
}

func TestClientBundleEnforcesRequestTimeout(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		assertExactPodGet(t, request, 50*time.Millisecond)
		started <- struct{}{}
		<-request.Context().Done()
	}))
	defer server.Close()

	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), &clientcmdapi.AuthInfo{})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	factory.requestTimeout = 50 * time.Millisecond
	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()

	result := make(chan error, 1)
	go func() {
		_, err := bundle.typed.CoreV1().Pods("default").Get(context.Background(), "synthetic", metav1.GetOptions{})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("Kubernetes request did not start")
	}
	select {
	case rawErr := <-result:
		err := classifyKubernetesError("read_namespace", rawErr)
		assertKubeSafeError(t, err, ClassTimeout, "kubernetes_request_timeout")
	case <-time.After(3 * time.Second):
		t.Fatal("request timeout did not stop the Kubernetes request")
	}
}

func TestClosedClientBundlePerformsNoRequest(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writePodResponse(writer)
	}))
	defer server.Close()

	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), &clientcmdapi.AuthInfo{})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	bundle.Close()
	bundle.Close()

	_, rawErr := bundle.typed.CoreV1().Pods("default").Get(context.Background(), "synthetic", metav1.GetOptions{})
	err = classifyKubernetesError("read_namespace", rawErr)
	assertKubeSafeError(t, err, ClassCancelled, "kubernetes_request_cancelled")
	if got := calls.Load(); got != 0 {
		t.Fatalf("requests after Close() = %d, want 0", got)
	}
}

func TestClientFactoryHonorsCancellationBeforeConstruction(t *testing.T) {
	t.Parallel()

	path := writeSingleContextKubeconfig(t, "https://cluster.example.invalid", nil, &clientcmdapi.AuthInfo{})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	constructorCalls := 0
	factory.newTypedClient = func(*rest.Config, *http.Client) (kubernetes.Interface, error) {
		constructorCalls++
		return kubernetesfake.NewSimpleClientset(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = factory.Create(ctx, "selected")
	assertKubeSafeError(t, err, ClassCancelled, "kubernetes_client_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled factory error does not preserve cancellation semantics")
	}
	if constructorCalls != 0 {
		t.Fatalf("typed client constructor calls = %d, want 0", constructorCalls)
	}
}

func TestClientFactoryDiscardsBundleCancelledDuringConstruction(t *testing.T) {
	t.Parallel()

	path := writeSingleContextKubeconfig(t, "https://cluster.example.invalid", nil, &clientcmdapi.AuthInfo{})
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	factory.newTypedClient = func(*rest.Config, *http.Client) (kubernetes.Interface, error) {
		close(started)
		<-release
		return kubernetesfake.NewSimpleClientset(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		bundle *ClientBundle
		err    error
	}, 1)
	go func() {
		bundle, err := factory.Create(ctx, "selected")
		result <- struct {
			bundle *ClientBundle
			err    error
		}{bundle: bundle, err: err}
	}()
	<-started
	cancel()
	close(release)

	got := <-result
	if got.bundle != nil {
		got.bundle.Close()
		t.Fatal("Create() returned a bundle after owner cancellation")
	}
	assertKubeSafeError(t, got.err, ClassCancelled, "kubernetes_client_cancelled")
}

func TestLifecycleRoundTripperKeepsResponseBodyUsable(t *testing.T) {
	t.Parallel()

	roundTripper := &lifecycleRoundTripper{
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &contextAwareBody{ctx: request.Context(), content: []byte("ok")},
				Header:     make(http.Header),
				Request:    request,
			}, nil
		}),
		closed: make(chan struct{}),
	}
	response, err := roundTripper.RoundTrip(httptest.NewRequest(http.MethodGet, "https://cluster.example.invalid", nil))
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(content) != "ok" {
		t.Fatalf("response body = %q, want %q", content, "ok")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	roundTripper.closeAndWait()
}

func writeSingleContextKubeconfig(t *testing.T, server string, caData []byte, auth *clientcmdapi.AuthInfo) string {
	t.Helper()
	return writeContextKubeconfig(t, &clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: caData,
	}, auth)
}

func writeContextKubeconfig(t *testing.T, cluster *clientcmdapi.Cluster, auth *clientcmdapi.AuthInfo) string {
	t.Helper()
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = "selected"
	raw.Clusters["cluster"] = cluster
	raw.AuthInfos["user"] = auth
	raw.Contexts["selected"] = &clientcmdapi.Context{
		Cluster:  "cluster",
		AuthInfo: "user",
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeKubeconfigFile(t, path, *raw)
	return path
}

func testServerCAData(server *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}

func writePodResponse(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"synthetic","namespace":"default"}}`))
}

func assertExactPodGet(t *testing.T, request *http.Request, timeout time.Duration) {
	t.Helper()
	if request.Method != http.MethodGet {
		t.Errorf("Kubernetes request method = %q, want GET", request.Method)
	}
	if request.URL.EscapedPath() != "/api/v1/namespaces/default/pods/synthetic" {
		t.Errorf("Kubernetes request path = %q", request.URL.EscapedPath())
	}
	query := request.URL.Query()
	if len(query) != 1 || query.Get("timeout") != timeout.String() {
		t.Errorf("Kubernetes request query = %q, want timeout=%s", request.URL.RawQuery, timeout)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

type contextAwareBody struct {
	ctx     context.Context
	content []byte
	read    bool
}

func (body *contextAwareBody) Read(destination []byte) (int, error) {
	if err := body.ctx.Err(); err != nil {
		return 0, err
	}
	if body.read {
		return 0, io.EOF
	}
	body.read = true
	return copy(destination, body.content), nil
}

func (*contextAwareBody) Close() error {
	return nil
}
