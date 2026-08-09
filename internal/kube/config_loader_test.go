package kube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestConfigLoaderUsesStandardKUBECONFIGMerge(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.yaml")
	secondPath := filepath.Join(root, "second.yaml")
	missingPath := filepath.Join(root, "missing.yaml")

	first := clientcmdapi.NewConfig()
	first.CurrentContext = "shared"
	first.Clusters["first-cluster"] = &clientcmdapi.Cluster{Server: "https://first.example.invalid"}
	first.AuthInfos["first-user"] = &clientcmdapi.AuthInfo{}
	first.Contexts["shared"] = &clientcmdapi.Context{
		Cluster:   "first-cluster",
		AuthInfo:  "first-user",
		Namespace: "first-namespace",
	}
	writeKubeconfigFile(t, firstPath, *first)

	second := clientcmdapi.NewConfig()
	second.CurrentContext = "second"
	second.Clusters["second-cluster"] = &clientcmdapi.Cluster{Server: "https://second.example.invalid"}
	second.AuthInfos["second-user"] = &clientcmdapi.AuthInfo{
		Exec: &clientcmdapi.ExecConfig{
			Command:         "synthetic-helper",
			APIVersion:      "client.authentication.k8s.io/v1",
			InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
		},
	}
	second.Contexts["shared"] = &clientcmdapi.Context{
		Cluster:   "second-cluster",
		AuthInfo:  "second-user",
		Namespace: "must-not-win",
	}
	second.Contexts["second"] = &clientcmdapi.Context{
		Cluster:  "second-cluster",
		AuthInfo: "second-user",
	}
	writeKubeconfigFile(t, secondPath, *second)

	t.Setenv(clientcmd.RecommendedConfigPathEnvVar, strings.Join(
		[]string{firstPath, missingPath, secondPath, firstPath},
		string(os.PathListSeparator),
	))
	t.Setenv("HOME", root)

	loader := NewConfigLoader()
	contexts, err := loader.Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts() error = %v", err)
	}
	want := []ContextInfo{
		{Name: "second", Namespace: "default", ExecCredentials: true},
		{Name: "shared", Namespace: "first-namespace", Current: true},
	}
	if len(contexts) != len(want) {
		t.Fatalf("Contexts() length = %d, want %d", len(contexts), len(want))
	}
	for index := range want {
		if contexts[index] != want[index] {
			t.Errorf("Contexts()[%d] = %#v, want %#v", index, contexts[index], want[index])
		}
	}

	current, err := loader.CurrentContext(context.Background())
	if err != nil {
		t.Fatalf("CurrentContext() error = %v", err)
	}
	if current != want[1] {
		t.Fatalf("CurrentContext() = %#v, want %#v", current, want[1])
	}
}

func TestConfigLoaderRejectsMissingAndInvalidSourcesSafely(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("k", 31) + "-generated"
	tests := []struct {
		name  string
		setup func(*testing.T) []string
		code  string
	}{
		{
			name: "all sources missing",
			setup: func(t *testing.T) []string {
				return []string{filepath.Join(t.TempDir(), canary+".yaml")}
			},
			code: "kubeconfig_missing",
		},
		{
			name: "invalid source",
			setup: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "invalid.yaml")
				if err := os.WriteFile(path, []byte("contexts: ["+canary), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{path}
			},
			code: "kubeconfig_invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loader := newConfigLoaderForPaths(test.setup(t))
			_, err := loader.Contexts(context.Background())
			assertKubeSafeError(t, err, ClassConfigurationInvalid, test.code)
			if strings.Contains(err.Error(), canary) {
				t.Fatal("safe error contains a kubeconfig canary")
			}
		})
	}
}

func TestConfigLoaderHonorsCancellationBeforeLoading(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loader := newConfigLoaderForPaths([]string{filepath.Join(t.TempDir(), "missing.yaml")})
	_, err := loader.Contexts(ctx)
	assertKubeSafeError(t, err, ClassCancelled, "kubeconfig_load_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled loader error does not preserve cancellation semantics")
	}
}

func TestConfigLoaderReportsUnsafePermissionsWithoutChangingOrExposingPath(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("p", 29) + "-generated"
	tests := []struct {
		name        string
		mode        os.FileMode
		wantWarning bool
	}{
		{name: "owner only", mode: 0o600},
		{name: "group readable", mode: 0o640, wantWarning: true},
		{name: "world readable", mode: 0o604, wantWarning: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := clientcmdapi.NewConfig()
			raw.CurrentContext = "selected"
			raw.Clusters["cluster"] = &clientcmdapi.Cluster{Server: "https://cluster.example.invalid"}
			raw.Contexts["selected"] = &clientcmdapi.Context{Cluster: "cluster"}
			path := filepath.Join(t.TempDir(), canary+".yaml")
			writeKubeconfigFile(t, path, *raw)
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}

			loader := newConfigLoaderForPaths([]string{path})
			warnings, err := loader.Warnings(context.Background())
			if err != nil {
				t.Fatalf("Warnings() error = %v", err)
			}
			if test.wantWarning {
				if len(warnings) != 1 || warnings[0] != KubeconfigWarningUnsafePermissions {
					t.Fatalf("Warnings() = %#v", warnings)
				}
				if strings.Contains(string(warnings[0]), canary) {
					t.Fatal("kubeconfig warning contains a source path")
				}
			} else if len(warnings) != 0 {
				t.Fatalf("Warnings() = %#v, want none", warnings)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != test.mode {
				t.Fatalf("kubeconfig permissions = %o, want unchanged %o", got, test.mode)
			}
		})
	}
}

func TestConfigLoaderRejectsUnsafeContextTextWithoutEcho(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("u", 37) + "-generated"
	name := "unsafe\n" + canary
	raw := clientcmdapi.NewConfig()
	raw.CurrentContext = name
	raw.Clusters["cluster"] = &clientcmdapi.Cluster{Server: "https://cluster.example.invalid"}
	raw.Contexts[name] = &clientcmdapi.Context{Cluster: "cluster"}
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeKubeconfigFile(t, path, *raw)

	loader := newConfigLoaderForPaths([]string{path})
	_, err := loader.Contexts(context.Background())
	assertKubeSafeError(t, err, ClassInvalidExternalResponse, "kubeconfig_context_invalid")
	if strings.Contains(err.Error(), canary) {
		t.Fatal("safe error contains unsafe Context text")
	}
}

func writeKubeconfigFile(t *testing.T, path string, config clientcmdapi.Config) {
	t.Helper()
	if err := clientcmd.WriteToFile(config, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod kubeconfig: %v", err)
	}
}

func newConfigLoaderForPaths(paths []string) *ConfigLoader {
	seen := make(map[string]struct{}, len(paths))
	precedence := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		precedence = append(precedence, path)
	}
	return &ConfigLoader{rules: clientcmd.ClientConfigLoadingRules{
		Precedence:       precedence,
		WarnIfAllMissing: true,
		Warner:           func(error) {},
	}}
}
