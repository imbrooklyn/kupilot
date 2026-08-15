package kube

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestSecurityAssuranceKubernetesCredentialSourceMatrix(t *testing.T) {
	t.Run("kubeconfig bytes, path, and permissions", func(t *testing.T) {
		contentCanary := strings.Repeat("k", 43) + "-generated"
		pathCanary := strings.Repeat("p", 41) + "-generated"
		directory := filepath.Join(t.TempDir(), pathCanary)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("os.Mkdir() error = %v", err)
		}
		path := filepath.Join(directory, "config.yaml")
		raw := clientcmdapi.NewConfig()
		raw.CurrentContext = "selected"
		raw.Clusters["cluster"] = &clientcmdapi.Cluster{Server: "https://cluster.example.invalid"}
		raw.AuthInfos["user"] = &clientcmdapi.AuthInfo{}
		raw.Contexts["selected"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: "user"}
		writeKubeconfigFile(t, path, *raw)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("os.OpenFile() error = %v", err)
		}
		if _, err := fmt.Fprintf(file, "# %s\n", contentCanary); err != nil {
			_ = file.Close()
			t.Fatalf("append kubeconfig marker error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close kubeconfig error = %v", err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatalf("os.Chmod() error = %v", err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat(before) error = %v", err)
		}

		t.Setenv("KUBECONFIG", path)
		loader := NewConfigLoader()
		contexts, err := loader.Contexts(context.Background())
		if err != nil {
			t.Fatalf("Contexts() error = %v", err)
		}
		warnings, err := loader.Warnings(context.Background())
		if err != nil {
			t.Fatalf("Warnings() error = %v", err)
		}
		if len(warnings) != 1 || warnings[0] != KubeconfigWarningUnsafePermissions {
			t.Fatalf("Warnings() count or class is invalid")
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat(after) error = %v", err)
		}
		if before.Mode().Perm() != after.Mode().Perm() {
			t.Fatalf("kubeconfig permissions changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
		}
		if len(contexts) != 1 || contexts[0].Name != "selected" {
			t.Fatalf("Contexts() returned an invalid safe projection")
		}
		assertCredentialBoundaryExcludes(t, []string{contentCanary, pathCanary}, contexts, warnings)

		factory, err := NewClientFactory(loader, ExecCredentialsDeny)
		if err != nil {
			t.Fatalf("NewClientFactory() error = %v", err)
		}
		var clientConstructions atomic.Int32
		factory.newTypedClient = func(*rest.Config, *http.Client) (kubernetes.Interface, error) {
			clientConstructions.Add(1)
			return kubernetesfake.NewSimpleClientset(), nil
		}
		if got := clientConstructions.Load(); got != 0 {
			t.Fatalf("Kubernetes client constructions = %d, want 0", got)
		}
	})

	t.Run("bearer token", func(t *testing.T) {
		canary := strings.Repeat("b", 47) + "-generated"
		var serverCalls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			serverCalls.Add(1)
			assertExactPodGet(t, request, DefaultRequestTimeout)
			if request.Header.Get("Authorization") != "Bearer "+canary {
				t.Error("Kubernetes transport did not receive the selected bearer credential")
			}
			writePodResponse(writer)
		}))
		defer server.Close()

		auth := &clientcmdapi.AuthInfo{Token: canary}
		path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
		contextInfo, observation, err := readCredentialProtectedPod(t, path, ExecCredentialsDeny, nil)
		if err != nil {
			t.Fatalf("credential-protected Tool read error = %v", err)
		}
		if got := serverCalls.Load(); got != 1 {
			t.Fatalf("Kubernetes requests = %d, want 1", got)
		}
		assertCredentialBoundaryExcludes(t, []string{canary}, contextInfo, observation)
	})

	t.Run("client certificate and private key", func(t *testing.T) {
		certificateMarker := strings.Repeat("c", 43) + "-generated"
		certificatePEM, privateKeyPEM := generateClientCredential(t, certificateMarker)
		var serverCalls atomic.Int32
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			serverCalls.Add(1)
			assertExactPodGet(t, request, DefaultRequestTimeout)
			if request.TLS == nil || len(request.TLS.PeerCertificates) != 1 ||
				request.TLS.PeerCertificates[0].Subject.CommonName != certificateMarker {
				t.Error("Kubernetes transport did not receive the selected client certificate")
			}
			writePodResponse(writer)
		}))
		server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
		server.StartTLS()
		defer server.Close()

		auth := &clientcmdapi.AuthInfo{
			ClientCertificateData: certificatePEM,
			ClientKeyData:         privateKeyPEM,
		}
		path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
		contextInfo, observation, err := readCredentialProtectedPod(t, path, ExecCredentialsDeny, nil)
		if err != nil {
			t.Fatalf("credential-protected Tool read error = %v", err)
		}
		if got := serverCalls.Load(); got != 1 {
			t.Fatalf("Kubernetes requests = %d, want 1", got)
		}
		assertCredentialBoundaryExcludes(t, []string{certificateMarker, string(certificatePEM), string(privateKeyPEM)}, contextInfo, observation)
	})

	t.Run("exec standard output", func(t *testing.T) {
		canary := strings.Repeat("o", 47) + "-generated"
		modelKeyCanary := strings.Repeat("m", 49) + "-generated"
		var serverCalls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			serverCalls.Add(1)
			assertExactPodGet(t, request, DefaultRequestTimeout)
			if request.Header.Get("Authorization") != "Bearer "+canary {
				t.Error("Kubernetes transport did not receive the exec credential")
			}
			writePodResponse(writer)
		}))
		defer server.Close()

		auth := testExecAuth(os.Args[0], execHelperModeCredential, canary, "client.authentication.k8s.io/v1")
		path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
		var commandCalls atomic.Int32
		configure := func(factory *ClientFactory) {
			factory.environment = func() []string {
				return []string{"PATH=/usr/bin:/bin", config.ModelAPIKeyEnvironmentVariable + "=" + modelKeyCanary}
			}
			factory.commandContext = func(ctx context.Context, command string, arguments ...string) *exec.Cmd {
				commandCalls.Add(1)
				return exec.CommandContext(ctx, command, arguments...)
			}
		}
		contextInfo, observation, err := readCredentialProtectedPod(t, path, ExecCredentialsAllow, configure)
		if err != nil {
			t.Fatalf("credential-protected Tool read error = %v", err)
		}
		if got := commandCalls.Load(); got != 1 {
			t.Fatalf("exec credential launches = %d, want 1", got)
		}
		if got := serverCalls.Load(); got != 1 {
			t.Fatalf("Kubernetes requests = %d, want 1", got)
		}
		assertCredentialBoundaryExcludes(t, []string{canary, modelKeyCanary}, contextInfo, observation)
	})

	t.Run("exec standard error", func(t *testing.T) {
		canary := strings.Repeat("e", 47) + "-generated"
		var serverCalls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			serverCalls.Add(1)
			writePodResponse(writer)
		}))
		defer server.Close()

		auth := testExecAuth(os.Args[0], execHelperModeExit, "", "client.authentication.k8s.io/v1")
		auth.Exec.Env = append(auth.Exec.Env, clientcmdapi.ExecEnvVar{Name: execHelperCanaryEnvironment, Value: canary})
		path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
		var commandCalls atomic.Int32
		configure := func(factory *ClientFactory) {
			factory.commandContext = func(ctx context.Context, command string, arguments ...string) *exec.Cmd {
				commandCalls.Add(1)
				return exec.CommandContext(ctx, command, arguments...)
			}
		}
		contextInfo, observation, err := readCredentialProtectedPod(t, path, ExecCredentialsAllow, configure)
		assertKubeSafeError(t, err, ClassAuthenticationFailed, "kubernetes_exec_failed")
		if got := commandCalls.Load(); got != 1 {
			t.Fatalf("exec credential launches = %d, want 1", got)
		}
		if got := serverCalls.Load(); got != 0 {
			t.Fatalf("Kubernetes requests after exec failure = %d, want 0", got)
		}
		assertCredentialBoundaryExcludes(t, []string{canary}, contextInfo, observation, err)
	})
}

func readCredentialProtectedPod(
	t *testing.T,
	path string,
	policy ExecCredentialPolicy,
	configure func(*ClientFactory),
) (ContextInfo, toolcontract.ResourceObservation, error) {
	t.Helper()
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), policy)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	if configure != nil {
		configure(factory)
	}
	gateway, err := NewGateway(factory)
	if err != nil {
		t.Fatalf("NewGateway() error = %v", err)
	}
	client, err := gateway.Create(context.Background(), "selected")
	if err != nil {
		return ContextInfo{}, toolcontract.ResourceObservation{}, err
	}
	defer client.Close()
	scope := liveScope("default")
	reader, err := NewToolResourceReader(gateway, client, scope)
	if err != nil {
		return client.(*scopeClient).bundle.Context(), toolcontract.ResourceObservation{}, err
	}
	observation, readErr := reader.ReadResource(context.Background(), toolcontract.ResourceReadRequest{
		Scope: scope,
		Reference: domain.ResourceRef{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  "default",
			Name:       "synthetic",
		},
		Detail: toolcontract.ResourceDetailSummary,
	})
	return client.(*scopeClient).bundle.Context(), observation, readErr
}

func generateClientCredential(t *testing.T, commonName string) ([]byte, []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Unix(1, 0).UTC(),
		NotAfter:     time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("x509.CreateCertificate() error = %v", err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("x509.MarshalPKCS8PrivateKey() error = %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
}

func assertCredentialBoundaryExcludes(t *testing.T, canaries []string, values ...any) {
	t.Helper()
	formatted := make([]string, 0, len(values)*6+1)
	for _, value := range values {
		formatted = append(formatted,
			fmt.Sprint(value),
			fmt.Sprintf("%s", value),
			fmt.Sprintf("%q", value),
			fmt.Sprintf("%v", value),
			fmt.Sprintf("%+v", value),
		)
		boundaryError, ok := value.(error)
		if !ok || boundaryError == nil {
			continue
		}
		wrapped := fmt.Errorf("Kubernetes boundary: %w", boundaryError)
		joined := errors.Join(wrapped, context.Canceled)
		formatted = append(formatted, wrapped.Error(), joined.Error())
		var safe *SafeError
		if !errors.As(wrapped, &safe) || !errors.As(joined, &safe) || safe.Class() == "" || safe.Code() == "" {
			t.Fatal("wrapped or joined Kubernetes error lost its stable safe classification")
		}
	}
	childEnvironment := config.FilterChildEnvironment([]string{
		"PATH=/usr/bin:/bin",
		config.ModelAPIKeyEnvironmentVariable + "=" + strings.Repeat("z", 43) + "-generated",
	})
	formatted = append(formatted, strings.Join(childEnvironment, "\x00"))
	for _, canary := range canaries {
		if canary == "" {
			t.Fatal("credential assurance canary is empty")
		}
		for _, sink := range formatted {
			if strings.Contains(sink, canary) {
				t.Fatal("a Kubernetes credential canary crossed the project-owned adapter boundary")
			}
		}
	}
}
