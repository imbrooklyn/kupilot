package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestLoadPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		environment   map[string]string
		overrides     Overrides
		wantContext   string
		wantNamespace string
		wantNoColor   bool
	}{
		{
			name:          "file overrides defaults",
			wantContext:   "file-context",
			wantNamespace: "file-namespace",
			wantNoColor:   true,
		},
		{
			name: "environment overrides file",
			environment: map[string]string{
				"KUPILOT_CONTEXT":   "environment-context",
				"KUPILOT_NAMESPACE": "environment-namespace",
				"KUPILOT_NO_COLOR":  "false",
			},
			wantContext:   "environment-context",
			wantNamespace: "environment-namespace",
			wantNoColor:   false,
		},
		{
			name: "CLI overrides environment",
			environment: map[string]string{
				"KUPILOT_CONTEXT":   "environment-context",
				"KUPILOT_NAMESPACE": "environment-namespace",
				"KUPILOT_NO_COLOR":  "false",
			},
			overrides: Overrides{
				Context:   StringOverride{Set: true, Value: "cli-context"},
				Namespace: StringOverride{Set: true, Value: "cli-namespace"},
				NoColor:   BoolOverride{Set: true, Value: true},
			},
			wantContext:   "cli-context",
			wantNamespace: "cli-namespace",
			wantNoColor:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			configFile := filepath.Join(root, "config.yaml")
			writePrivateFile(t, configFile, []byte(`version: 1
context: file-context
namespace: file-namespace
no_color: true
model:
  endpoint: https://model.example.test/v1
  model: diagnostic-model
`))

			paths := testPaths(root)
			paths.ConfigFile = configFile
			got, err := Load(context.Background(), LoadOptions{
				Paths:     paths,
				Overrides: tt.overrides,
				LookupEnv: lookupMap(tt.environment),
			})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.Context != tt.wantContext {
				t.Errorf("Context = %q, want %q", got.Context, tt.wantContext)
			}
			if got.Namespace != tt.wantNamespace {
				t.Errorf("Namespace = %q, want %q", got.Namespace, tt.wantNamespace)
			}
			if got.NoColor != tt.wantNoColor {
				t.Errorf("NoColor = %t, want %t", got.NoColor, tt.wantNoColor)
			}
		})
	}
}

func TestLoadSelectsConfigurationFileByPrecedence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	defaultFile := filepath.Join(root, "default.yaml")
	environmentFile := filepath.Join(root, "environment.yaml")
	cliFile := filepath.Join(root, "cli.yaml")
	for path, contextName := range map[string]string{
		defaultFile:     "default-file",
		environmentFile: "environment-file",
		cliFile:         "cli-file",
	} {
		writePrivateFile(t, path, []byte(fmt.Sprintf("version: 1\ncontext: %s\n", contextName)))
	}

	tests := []struct {
		name        string
		environment map[string]string
		overrides   Overrides
		want        string
	}{
		{name: "fixed Home default", want: "default-file"},
		{
			name:        "environment file",
			environment: map[string]string{"KUPILOT_CONFIG_FILE": environmentFile},
			want:        "environment-file",
		},
		{
			name:        "CLI file",
			environment: map[string]string{"KUPILOT_CONFIG_FILE": environmentFile},
			overrides: Overrides{
				ConfigFile: StringOverride{Set: true, Value: cliFile},
			},
			want: "cli-file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			paths := testPaths(root)
			paths.ConfigFile = defaultFile
			got, err := Load(context.Background(), LoadOptions{
				Paths:     paths,
				Overrides: tt.overrides,
				LookupEnv: lookupMap(tt.environment),
			})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.Context != tt.want {
				t.Fatalf("Context = %q, want %q", got.Context, tt.want)
			}
		})
	}
}

func TestLoadParsesAdmittedTypedEnvironmentValues(t *testing.T) {
	t.Parallel()

	got, err := Load(context.Background(), LoadOptions{
		Paths: testPaths(t.TempDir()),
		LookupEnv: lookupMap(map[string]string{
			"KUPILOT_MODEL_ENDPOINT":                "https://model.example.test/v1",
			"KUPILOT_MODEL":                         "diagnostic-model",
			"KUPILOT_MODEL_TEMPERATURE":             "0.2",
			"KUPILOT_MODEL_MAX_OUTPUT_TOKENS":       "1024",
			"KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS": "30",
			"KUPILOT_LOG_ENABLED":                   "false",
		}),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Model.Temperature != 0.2 || got.Model.MaxOutputTokens != 1024 || got.Model.RequestTimeoutSeconds != 30 {
		t.Errorf("typed model environment values = %#v", got.Model)
	}
	if got.Logging.Enabled {
		t.Fatal("logging.enabled = true, want false")
	}
}

func TestLoadAcceptsZeroModelTemperature(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writePrivateFile(t, path, []byte("version: 1\nmodel:\n  endpoint: https://model.example.test/v1\n  model: diagnostic-model\n  temperature: 0\n"))
	paths := testPaths(root)
	paths.ConfigFile = path
	got, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Model.Temperature != 0 {
		t.Fatalf("Temperature = %v, want 0", got.Model.Temperature)
	}
}

func TestLoadRejectsUnknownOrSensitiveFileFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "unknown root field", content: "version: 1\nfuture_setting: true\n"},
		{name: "empty file", content: ""},
		{name: "missing version", content: "context: development\n"},
		{name: "duplicate root field", content: "version: 1\nversion: 1\n"},
		{name: "wrong root type", content: "version: \"1\"\n"},
		{name: "wrong nested type", content: "version: 1\nlogging:\n  enabled: \"true\"\n"},
		{name: "wrong integer type", content: "version: 1\nmodel:\n  max_output_tokens: 2048.0\n"},
		{name: "null value", content: "version: 1\ncontext: null\n"},
		{name: "alias value", content: "version: 1\ncontext: &context development\nnamespace: *context\n"},
		{name: "multiple documents", content: "version: 1\n---\nversion: 1\n"},
		{name: "unknown nested field", content: "version: 1\nlogging:\n  backend: remote\n"},
		{name: "retired paths", content: "version: 1\npaths:\n  state_dir: /tmp/state\n"},
		{name: "retired API key source", content: "version: 1\nmodel:\n  api_key_source: environment\n"},
		{name: "authorization header", content: "version: 1\nmodel:\n  authorization: prohibited\n"},
		{name: "insecure TLS override", content: "version: 1\nmodel:\n  insecure_skip_verify: true\n"},
		{name: "redirect override", content: "version: 1\nmodel:\n  allow_cross_origin_redirects: true\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			path := filepath.Join(root, "config.yaml")
			writePrivateFile(t, path, []byte(tt.content))
			paths := testPaths(root)
			paths.ConfigFile = path

			_, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
			assertSafeError(t, err, ClassConfigurationInvalid, "config_schema_invalid")
			if strings.Contains(err.Error(), "prohibited") {
				t.Fatal("safe error contains a configuration value")
			}
		})
	}
}

func TestLoadAcceptsUserManagedConfigurationPermissionsAndRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()

	t.Run("non-owner permissions", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "config.yaml")
		if err := os.WriteFile(path, []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		paths := testPaths(root)
		paths.ConfigFile = path
		loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if len(loaded.Warnings) != 1 {
			t.Fatalf("warnings = %#v, want one permissions warning", loaded.Warnings)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.yaml")
		link := filepath.Join(root, "config.yaml")
		writePrivateFile(t, target, []byte("version: 1\n"))
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}

		paths := testPaths(root)
		paths.ConfigFile = link
		_, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
		assertSafeError(t, err, ClassConfigurationInvalid, "config_file_unsafe")
	})

	t.Run("oversized", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "config.yaml")
		writePrivateFile(t, path, []byte(strings.Repeat("#", MaxConfigFileBytes+1)))

		paths := testPaths(root)
		paths.ConfigFile = path
		_, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
		assertSafeError(t, err, ClassConfigurationInvalid, "config_file_too_large")
	})
}

func TestLoadHonorsCancellationBeforeFileIO(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	root := t.TempDir()
	paths := testPaths(root)
	paths.ConfigFile = filepath.Join(root, "missing.yaml")
	_, err := Load(ctx, LoadOptions{
		Paths: paths,
		Overrides: Overrides{
			ConfigFile: StringOverride{Set: true, Value: paths.ConfigFile},
		},
		LookupEnv: lookupMap(nil),
	})
	assertSafeError(t, err, ClassCancelled, "config_load_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled configuration error does not preserve cancellation semantics")
	}
}

func TestConfigSerializationNeverContainsEnvironmentAPIKey(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("s", 41) + "-generated"
	root := t.TempDir()
	apiKeyLookedUp := false
	environment := lookupMap(map[string]string{ModelAPIKeyEnvironmentVariable: canary})
	got, err := Load(context.Background(), LoadOptions{
		Paths: testPaths(root),
		LookupEnv: func(key string) (string, bool) {
			if key == ModelAPIKeyEnvironmentVariable {
				apiKeyLookedUp = true
			}
			return environment(key)
		},
		Unsetenv: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !apiKeyLookedUp || !got.Credential.IsSet() || got.CredentialSource != CredentialSourceEnvironment {
		t.Fatal("configuration loading did not consume the environment credential override")
	}
	defer got.Credential.Destroy()

	jsonEncoded, err := json.Marshal(got.Config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	yamlEncoded, err := yaml.Marshal(got.Config)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	for name, value := range map[string]string{
		"formatted Config": fmt.Sprintf("%v %#v", got.Config, got.Config),
		"JSON Config":      string(jsonEncoded),
		"YAML Config":      string(yamlEncoded),
	} {
		if strings.Contains(value, canary) {
			t.Fatalf("%s contains the API key canary", name)
		}
	}
}

func TestExampleConfigurationMatchesStrictSchema(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("read example configuration: %v", err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writePrivateFile(t, path, content)
	paths := testPaths(root)
	paths.ConfigFile = path
	if _, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)}); err != nil {
		t.Fatalf("Load(example) error = %v", err)
	}
}

func testPaths(root string) Paths {
	return pathsForHome(root)
}

func lookupMap(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func writePrivateFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSafeError(t *testing.T, err error, class ErrorClass, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want class %q and code %q", class, code)
	}
	safe, ok := err.(*SafeError)
	if !ok {
		t.Fatalf("error type = %T, want *SafeError", err)
	}
	if safe.Class() != class {
		t.Errorf("error class = %q, want %q", safe.Class(), class)
	}
	if safe.Code() != code {
		t.Errorf("error code = %q, want %q", safe.Code(), code)
	}
}
