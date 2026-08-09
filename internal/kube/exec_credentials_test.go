package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	execHelperModeEnvironment    = "KUPILOT_TEST_EXEC_MODE"
	execHelperTokenEnvironment   = "KUPILOT_TEST_EXEC_TOKEN"
	execHelperVersionEnvironment = "KUPILOT_TEST_EXEC_VERSION"
	execHelperReadyEnvironment   = "KUPILOT_TEST_EXEC_READY"
	execHelperCanaryEnvironment  = "KUPILOT_TEST_EXEC_CANARY"
)

func TestExecCredentialsStrictDenyPerformsZeroLaunches(t *testing.T) {
	t.Parallel()

	commandCanary := strings.Repeat("d", 37) + "-generated"
	auth := testExecAuth(commandCanary, execHelperModeCredential, "", "client.authentication.k8s.io/v1")
	path := writeSingleContextKubeconfig(t, "https://cluster.example.invalid", nil, auth)
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsDeny)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	var commandCalls atomic.Int32
	factory.commandContext = func(ctx context.Context, command string, args ...string) *exec.Cmd {
		commandCalls.Add(1)
		return exec.CommandContext(ctx, command, args...)
	}
	constructorCalls := 0
	factory.newTypedClient = func(*rest.Config, *http.Client) (kubernetes.Interface, error) {
		constructorCalls++
		return kubernetesfake.NewSimpleClientset(), nil
	}

	_, err = factory.Create(context.Background(), "selected")
	assertKubeSafeError(t, err, ClassPolicyDenied, "kubernetes_exec_credentials_denied")
	if got := commandCalls.Load(); got != 0 {
		t.Fatalf("exec command calls = %d, want 0", got)
	}
	if constructorCalls != 0 {
		t.Fatalf("typed client constructor calls = %d, want 0", constructorCalls)
	}
	if strings.Contains(err.Error(), commandCanary) {
		t.Fatal("strict-deny error contains the exec command")
	}
}

func TestStaticCredentialsTakePrecedenceWithoutExecLaunch(t *testing.T) {
	t.Parallel()

	auth := testExecAuth("synthetic-helper", execHelperModeCredential, "", "client.authentication.k8s.io/v1")
	auth.Token = strings.Repeat("s", 41) + "-generated"
	path := writeSingleContextKubeconfig(t, "https://cluster.example.invalid", nil, auth)
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsDeny)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	var commandCalls atomic.Int32
	factory.commandContext = func(ctx context.Context, command string, args ...string) *exec.Cmd {
		commandCalls.Add(1)
		return exec.CommandContext(ctx, command, args...)
	}
	fakeClient := kubernetesfake.NewSimpleClientset()
	factory.newTypedClient = func(config *rest.Config, _ *http.Client) (kubernetes.Interface, error) {
		if config.ExecProvider != nil {
			t.Error("typed client received a raw exec provider")
		}
		if config.BearerToken != auth.Token {
			t.Error("typed client did not retain the selected static credential")
		}
		return fakeClient, nil
	}

	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()
	if got := commandCalls.Load(); got != 0 {
		t.Fatalf("exec command calls = %d, want 0", got)
	}
	if actions := fakeClient.Actions(); len(actions) != 0 {
		t.Fatalf("factory construction performed %d Kubernetes actions, want 0", len(actions))
	}
}

func TestExecCredentialsAllowDirectLaunchAndRemoveModelKey(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("a", 43) + "-generated"
	modelKey := strings.Repeat("m", 53) + "-generated"
	authHeaders := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertExactPodGet(t, request, DefaultRequestTimeout)
		authHeaders <- request.Header.Get("Authorization")
		writePodResponse(writer)
	}))
	defer server.Close()

	auth := testExecAuth(os.Args[0], execHelperModeCredential, token, "client.authentication.k8s.io/v1")
	auth.Exec.Env = append(auth.Exec.Env, clientcmdapi.ExecEnvVar{
		Name:  config.ModelAPIKeyEnvironmentVariable,
		Value: modelKey + "-from-kubeconfig",
	})
	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	factory.environment = func() []string {
		return []string{
			"PATH=/usr/bin:/bin",
			config.ModelAPIKeyEnvironmentVariable + "=" + modelKey,
		}
	}
	bundle, err := factory.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer bundle.Close()

	if _, err := bundle.typed.CoreV1().Pods("default").Get(context.Background(), "synthetic", metav1.GetOptions{}); err != nil {
		t.Fatalf("typed Pod Get() error = %v", classifyKubernetesError("read_pod", err))
	}
	if got := <-authHeaders; got != "Bearer "+token {
		t.Fatal("API request did not receive the synthetic exec credential")
	}
}

func TestExecCredentialsAcceptV1Beta1(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("b", 43) + "-generated"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertExactPodGet(t, request, DefaultRequestTimeout)
		writePodResponse(writer)
	}))
	defer server.Close()

	auth := testExecAuth(os.Args[0], execHelperModeCredential, token, "client.authentication.k8s.io/v1beta1")
	auth.Exec.InteractiveMode = clientcmdapi.IfAvailableExecInteractiveMode
	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
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
		t.Fatalf("typed Pod Get() with v1beta1 exec credential error = %v", classifyKubernetesError("read_pod", err))
	}
}

func TestExecCredentialsRejectUnsupportedProtocolBeforeLaunch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		apiVersion  string
		interactive clientcmdapi.ExecInteractiveMode
		code        string
	}{
		{
			name:        "unsupported API version",
			apiVersion:  "client.authentication.k8s.io/v1alpha1",
			interactive: clientcmdapi.NeverExecInteractiveMode,
			code:        "kubernetes_exec_version_unsupported",
		},
		{
			name:        "interactive required",
			apiVersion:  "client.authentication.k8s.io/v1",
			interactive: clientcmdapi.AlwaysExecInteractiveMode,
			code:        "kubernetes_exec_interactive_unsupported",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			auth := testExecAuth(os.Args[0], execHelperModeCredential, "", test.apiVersion)
			auth.Exec.InteractiveMode = test.interactive
			path := writeSingleContextKubeconfig(t, "https://cluster.example.invalid", nil, auth)
			factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
			if err != nil {
				t.Fatalf("NewClientFactory() error = %v", err)
			}
			var commandCalls atomic.Int32
			factory.commandContext = func(ctx context.Context, command string, args ...string) *exec.Cmd {
				commandCalls.Add(1)
				return exec.CommandContext(ctx, command, args...)
			}

			_, err = factory.Create(context.Background(), "selected")
			assertKubeSafeError(t, err, ClassUnsupported, test.code)
			if got := commandCalls.Load(); got != 0 {
				t.Fatalf("exec command calls = %d, want 0", got)
			}
		})
	}
}

func TestExecCredentialOutputFailureIsBoundedAndSafe(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("o", 59) + "-generated"
	var serverCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		serverCalls.Add(1)
		writePodResponse(writer)
	}))
	defer server.Close()

	auth := testExecAuth(os.Args[0], execHelperModeInvalid, "", "client.authentication.k8s.io/v1")
	auth.Exec.Env = append(auth.Exec.Env, clientcmdapi.ExecEnvVar{Name: execHelperCanaryEnvironment, Value: canary})
	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
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
	err = classifyKubernetesError("read_pod", rawErr)
	assertKubeSafeError(t, err, ClassInvalidExternalResponse, "kubernetes_exec_output_invalid")
	if strings.Contains(err.Error(), canary) {
		t.Fatal("safe exec error contains stdout or stderr content")
	}
	if got := serverCalls.Load(); got != 0 {
		t.Fatalf("Kubernetes calls after invalid exec output = %d, want 0", got)
	}
}

func TestExecCredentialLaunchFailuresAreSafe(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("f", 61) + "-generated"
	var serverCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		serverCalls.Add(1)
		writePodResponse(writer)
	}))
	defer server.Close()

	tests := []struct {
		name    string
		command string
		mode    string
		class   ErrorClass
		code    string
	}{
		{
			name:    "missing executable",
			command: "kupilot-" + canary,
			mode:    execHelperModeCredential,
			class:   ClassUnavailable,
			code:    "kubernetes_exec_unavailable",
		},
		{
			name:    "nonzero exit",
			command: os.Args[0],
			mode:    execHelperModeExit,
			class:   ClassAuthenticationFailed,
			code:    "kubernetes_exec_failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := testExecAuth(test.command, test.mode, "", "client.authentication.k8s.io/v1")
			auth.Exec.Env = append(auth.Exec.Env, clientcmdapi.ExecEnvVar{Name: execHelperCanaryEnvironment, Value: canary})
			path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
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
			err = classifyKubernetesError("read_pod", rawErr)
			assertKubeSafeError(t, err, test.class, test.code)
			if strings.Contains(err.Error(), canary) {
				t.Fatal("safe exec launch error contains a command or stderr canary")
			}
		})
	}
	if got := serverCalls.Load(); got != 0 {
		t.Fatalf("Kubernetes calls after exec launch failures = %d, want 0", got)
	}
}

func TestExecCredentialChildTerminatesWithBundleOwner(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for helper barrier: %v", err)
	}
	defer listener.Close()
	if tcp, ok := listener.(*net.TCPListener); ok {
		if err := tcp.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatalf("set helper barrier deadline: %v", err)
		}
	}

	var serverCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		serverCalls.Add(1)
		writePodResponse(writer)
	}))
	defer server.Close()
	auth := testExecAuth(os.Args[0], execHelperModeBlock, "", "client.authentication.k8s.io/v1")
	auth.Exec.Env = append(auth.Exec.Env, clientcmdapi.ExecEnvVar{Name: execHelperReadyEnvironment, Value: listener.Addr().String()})
	path := writeSingleContextKubeconfig(t, server.URL, testServerCAData(server), auth)
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
	connection, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept helper barrier: %v", err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set helper barrier read deadline: %v", err)
	}
	ready := make([]byte, 1)
	if _, err := io.ReadFull(connection, ready); err != nil {
		t.Fatalf("read helper barrier: %v", err)
	}

	cancelOwner()
	select {
	case rawErr := <-result:
		err := classifyKubernetesError("read_pod", rawErr)
		assertKubeSafeError(t, err, ClassCancelled, "kubernetes_request_cancelled")
	case <-time.After(3 * time.Second):
		t.Fatal("bundle owner cancellation did not reap the exec credential child")
	}
	if got := serverCalls.Load(); got != 0 {
		t.Fatalf("Kubernetes calls before exec credential completion = %d, want 0", got)
	}
}

const (
	execHelperModeCredential = "credential"
	execHelperModeInvalid    = "invalid"
	execHelperModeBlock      = "block"
	execHelperModeExit       = "exit"
)

func testExecAuth(command, mode, token, apiVersion string) *clientcmdapi.AuthInfo {
	return &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			Command:    command,
			Args:       []string{"-test.run=^TestExecCredentialHelperProcess$"},
			APIVersion: apiVersion,
			Env: []clientcmdapi.ExecEnvVar{
				{Name: execHelperModeEnvironment, Value: mode},
				{Name: execHelperTokenEnvironment, Value: token},
				{Name: execHelperVersionEnvironment, Value: apiVersion},
			},
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
		},
	}
}

func TestExecCredentialHelperProcess(t *testing.T) {
	mode := os.Getenv(execHelperModeEnvironment)
	if mode == "" {
		return
	}
	if _, present := os.LookupEnv(config.ModelAPIKeyEnvironmentVariable); present {
		os.Exit(41)
	}

	switch mode {
	case execHelperModeCredential:
		response := struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Status     struct {
				Token string `json:"token"`
			} `json:"status"`
		}{
			APIVersion: os.Getenv(execHelperVersionEnvironment),
			Kind:       "ExecCredential",
		}
		response.Status.Token = os.Getenv(execHelperTokenEnvironment)
		if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
			os.Exit(42)
		}
		os.Exit(0)
	case execHelperModeInvalid:
		canary := os.Getenv(execHelperCanaryEnvironment)
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat(canary, 2048))
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat(canary, 2048))
		os.Exit(0)
	case execHelperModeBlock:
		connection, err := net.DialTimeout("tcp", os.Getenv(execHelperReadyEnvironment), 2*time.Second)
		if err != nil {
			os.Exit(43)
		}
		_, _ = connection.Write([]byte{1})
		_, _ = io.Copy(io.Discard, connection)
		os.Exit(44)
	case execHelperModeExit:
		_, _ = fmt.Fprint(os.Stderr, os.Getenv(execHelperCanaryEnvironment))
		os.Exit(46)
	default:
		os.Exit(45)
	}
}
