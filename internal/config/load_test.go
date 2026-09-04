package config

import (
	"bytes"
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

func TestLoadUsesDefaultNamespaceWithoutSelectingContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configFile := filepath.Join(root, "config.yaml")
	writePrivateFile(t, configFile, []byte("version: 1\n"))
	paths := testPaths(root)
	paths.ConfigFile = configFile
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Context != "" || loaded.Namespace != DefaultNamespace {
		t.Fatalf("loaded scope defaults = Context %q Namespace %q", loaded.Context, loaded.Namespace)
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
			"KUPILOT_MODEL_REASONING_EFFORT":        "none",
			"KUPILOT_MODEL_TEMPERATURE":             "0.2",
			"KUPILOT_MODEL_MAX_OUTPUT_TOKENS":       "1024",
			"KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS": "30",
			"KUPILOT_LOG_ENABLED":                   "false",
		}),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Models.Agent.ReasoningEffort != ModelReasoningEffortNone || got.Models.Agent.Temperature != 0.2 ||
		got.Models.Agent.MaxOutputTokens != 1024 || got.Models.Agent.RequestTimeoutSeconds != 30 {
		t.Errorf("typed model environment values = %#v", got.Models.Agent)
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
	if got.Models.Agent.Temperature != 0 {
		t.Fatalf("Temperature = %v, want 0", got.Models.Agent.Temperature)
	}
}

func TestLoadSensitiveDiagnosticsIsExplicitAndWarned(t *testing.T) {
	t.Parallel()

	defaults, err := Load(context.Background(), LoadOptions{
		Paths: testPaths(t.TempDir()), LookupEnv: lookupMap(nil),
	})
	if err != nil {
		t.Fatalf("Load(defaults) error = %v", err)
	}
	if defaults.Logging.SensitiveDiagnostics {
		t.Fatal("sensitive diagnostics defaulted to enabled")
	}

	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	writePrivateFile(t, path, []byte("version: 1\nlogging:\n  sensitive_diagnostics: true\n"))
	paths := testPaths(root)
	paths.ConfigFile = path
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load(opt-in) error = %v", err)
	}
	if !loaded.Logging.SensitiveDiagnostics {
		t.Fatal("sensitive diagnostics opt-in was not loaded")
	}
	if len(loaded.Warnings) != 2 || !strings.Contains(strings.Join(loaded.Warnings, "\n"), "Sensitive model diagnostics") {
		t.Fatalf("sensitive diagnostics warnings = %q", loaded.Warnings)
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
		{name: "wrong reasoning effort type", content: "version: 1\nmodel:\n  reasoning_effort: true\n"},
		{name: "wrong sensitive diagnostics type", content: "version: 1\nlogging:\n  sensitive_diagnostics: \"true\"\n"},
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
		if len(loaded.Warnings) != 2 || !strings.Contains(strings.Join(loaded.Warnings, "\n"), "accessible beyond its owner") {
			t.Fatalf("warnings = %#v, want migration and permissions warnings", loaded.Warnings)
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
	if !apiKeyLookedUp || !got.Credentials.Agent.Value.IsSet() || got.Credentials.Agent.Source != CredentialSourceEnvironment {
		t.Fatal("configuration loading did not consume the environment credential override")
	}
	defer got.Credentials.Destroy()

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

func TestLoadVersion2NamedProfilesAndIndependentCredentials(t *testing.T) {
	t.Parallel()

	t.Run("reviewer inherits agent origin model settings and credential buffer", func(t *testing.T) {
		root := t.TempDir()
		paths := testPaths(root)
		canary := "agent-inherited-key-generated"
		writePrivateFile(t, paths.ConfigFile, []byte(version2Config(`
    api_key: `+canary, `
  approval_reviewer:
    name: reviewer
    role: approval_reviewer
    inherit_agent: true
    credential_ref: agent
    model: reviewer-model`)))
		loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		defer loaded.Credentials.Destroy()
		if loaded.SourceVersion != CurrentVersion || loaded.Models.ApprovalReviewer == nil {
			t.Fatalf("loaded named profiles = %#v source version=%d", loaded.Models, loaded.SourceVersion)
		}
		reviewer := loaded.Models.ApprovalReviewer
		if reviewer.Origin != loaded.Models.Agent.Origin || reviewer.Model != "reviewer-model" ||
			reviewer.Streaming || reviewer.ToolCallingRequired || reviewer.Role != ModelRoleApprovalReviewer {
			t.Fatalf("resolved reviewer profile = %#v", reviewer)
		}
		if loaded.Credentials.ApprovalReviewer == nil ||
			loaded.Credentials.ApprovalReviewer.Source != CredentialSourceInherited {
			t.Fatalf("reviewer credential metadata = %#v", loaded.Credentials.ApprovalReviewer)
		}
		loaded.Credentials.Agent.Value.Destroy()
		matched := false
		if err := loaded.Credentials.ApprovalReviewer.Value.Use(func(value string) { matched = value == canary }); err != nil || !matched {
			t.Fatalf("reviewer did not own an independent inherited credential: %v", err)
		}
	})

	t.Run("reviewer uses a distinct origin model and environment credential", func(t *testing.T) {
		root := t.TempDir()
		paths := testPaths(root)
		writePrivateFile(t, paths.ConfigFile, []byte(version2Config(`
    api_key: file-agent-key-generated`, `
  approval_reviewer:
    name: reviewer
    role: approval_reviewer
    inherit_agent: false
    credential_ref: approval_reviewer
    provider_kind: openai_compatible
    endpoint: https://reviewer.example.test/v1
    model: reviewer-model
    temperature: 0
    max_output_tokens: 256
    request_timeout_seconds: 20
    streaming: false
    tool_calling_required: false
    api_key: file-reviewer-key-generated`)))
		environment := map[string]string{ApprovalReviewerAPIKeyEnvironmentVariable: "environment-reviewer-key-generated"}
		unset := 0
		loaded, err := Load(context.Background(), LoadOptions{
			Paths: paths, LookupEnv: lookupMap(environment),
			Unsetenv: func(name string) error { unset++; delete(environment, name); return nil },
		})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		defer loaded.Credentials.Destroy()
		if loaded.Models.ApprovalReviewer == nil || loaded.Models.ApprovalReviewer.Origin != "https://reviewer.example.test" ||
			loaded.Credentials.ApprovalReviewer == nil || loaded.Credentials.ApprovalReviewer.Source != CredentialSourceEnvironment || unset != 1 {
			t.Fatalf("distinct reviewer load = profile %#v credential %#v unset=%d", loaded.Models.ApprovalReviewer, loaded.Credentials.ApprovalReviewer, unset)
		}
	})

	t.Run("missing optional reviewer credential remains visibly unavailable", func(t *testing.T) {
		root := t.TempDir()
		paths := testPaths(root)
		writePrivateFile(t, paths.ConfigFile, []byte(version2Config("", `
  approval_reviewer:
    name: reviewer
    role: approval_reviewer
    inherit_agent: false
    credential_ref: approval_reviewer
    provider_kind: openai_compatible
    endpoint: https://reviewer.example.test/v1
    model: reviewer-model
    temperature: 0
    max_output_tokens: 256
    request_timeout_seconds: 20
    streaming: false
    tool_calling_required: false`)))
		loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		defer loaded.Credentials.Destroy()
		if loaded.Credentials.ApprovalReviewer == nil || loaded.Credentials.ApprovalReviewer.Value.IsSet() ||
			loaded.Credentials.ApprovalReviewer.Source != CredentialSourceNone {
			t.Fatalf("missing reviewer credential = %#v", loaded.Credentials.ApprovalReviewer)
		}
	})
}

func TestLoadVersionMigrationAndVersion2StrictFailures(t *testing.T) {
	t.Parallel()

	t.Run("version 1 migrates in memory without rewriting", func(t *testing.T) {
		root := t.TempDir()
		paths := testPaths(root)
		original := []byte("version: 1\nmodel:\n  endpoint: https://legacy.example.test/v1\n  model: legacy-model\n")
		writePrivateFile(t, paths.ConfigFile, original)
		loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		defer loaded.Credentials.Destroy()
		current, readErr := os.ReadFile(paths.ConfigFile)
		if readErr != nil || !bytes.Equal(current, original) {
			t.Fatalf("legacy file was rewritten: %q, %v", current, readErr)
		}
		if loaded.SourceVersion != LegacyVersion || loaded.Version != CurrentVersion ||
			loaded.Models.Agent.Name != "agent" || loaded.Models.Agent.Role != ModelRoleAgent ||
			loaded.Models.Agent.MaxOutputTokens != LegacyDefaultMaxModelOutputTokens || len(loaded.Warnings) == 0 {
			t.Fatalf("legacy migration = version %d source %d profile %#v warnings=%q", loaded.Version, loaded.SourceVersion, loaded.Models.Agent, loaded.Warnings)
		}
	})

	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "missing agent profile", content: "version: 2\nmodels: {}\n"},
		{name: "partial agent profile", content: "version: 2\nmodels:\n  agent:\n    name: agent\n    role: agent\n"},
		{name: "wrong reviewer type", content: version2Config("", "\n  approval_reviewer: enabled")},
		{name: "duplicate profile field", content: strings.Replace(version2Config("", ""), "    name: agent", "    name: agent\n    name: duplicate", 1)},
		{name: "unknown profile field", content: strings.Replace(version2Config("", ""), "    role: agent", "    role: agent\n    route: fallback", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			paths := testPaths(root)
			writePrivateFile(t, paths.ConfigFile, []byte(test.content))
			_, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
			assertSafeError(t, err, ClassConfigurationInvalid, "config_schema_invalid")
		})
	}
}

func TestLoadRejectsAmbiguousAgentCredentialAliasesAfterUnsettingBoth(t *testing.T) {
	t.Parallel()
	environment := map[string]string{
		AgentAPIKeyEnvironmentVariable: "new-agent-key-generated",
		ModelAPIKeyEnvironmentVariable: "legacy-agent-key-generated",
	}
	unset := 0
	_, err := Load(context.Background(), LoadOptions{
		Paths: testPaths(t.TempDir()), LookupEnv: lookupMap(environment),
		Unsetenv: func(name string) error { unset++; delete(environment, name); return nil },
	})
	assertSafeError(t, err, ClassConfigurationInvalid, "model_api_key_ambiguous")
	if unset != 2 || len(environment) != 0 {
		t.Fatalf("credential aliases were not both removed: unset=%d remaining=%v", unset, environment)
	}
}

func TestLoadUnsetsEveryRoleCredentialBeforeOtherConfigurationFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		paths func(t *testing.T) Paths
		code  string
	}{
		{
			name: "invalid configuration path",
			paths: func(t *testing.T) Paths {
				paths := testPaths(t.TempDir())
				paths.ConfigFile = "relative-config.yaml"
				return paths
			},
			code: "config_file_path_invalid",
		},
		{
			name: "invalid configuration schema",
			paths: func(t *testing.T) Paths {
				paths := testPaths(t.TempDir())
				writePrivateFile(t, paths.ConfigFile, []byte("version: 2\nmodels: {}\n"))
				return paths
			},
			code: "config_schema_invalid",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := map[string]string{
				AgentAPIKeyEnvironmentVariable:            "agent-key-failure-canary-generated",
				ApprovalReviewerAPIKeyEnvironmentVariable: "reviewer-key-failure-canary-generated",
			}
			unset := 0
			_, err := Load(context.Background(), LoadOptions{
				Paths: test.paths(t), LookupEnv: lookupMap(environment),
				Unsetenv: func(name string) error { unset++; delete(environment, name); return nil },
			})
			assertSafeError(t, err, ClassConfigurationInvalid, test.code)
			if unset != 2 || len(environment) != 0 {
				t.Fatalf("role credentials survived failed load: unset=%d remaining=%v", unset, environment)
			}
			if strings.Contains(err.Error(), "failure-canary") {
				t.Fatal("safe configuration error disclosed a role credential")
			}
		})
	}
}

func version2Config(agentExtra, reviewer string) string {
	return `version: 2
models:
  agent:
    name: agent
    role: agent
    credential_ref: agent
    provider_kind: openai_compatible
    endpoint: https://agent.example.test/v1
    model: agent-model
    temperature: 0.1
    max_output_tokens: 2048
    request_timeout_seconds: 60
    streaming: true
    tool_calling_required: true` + agentExtra + reviewer + `
`
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
