package config

import (
	"context"
	"strings"
	"testing"
)

func TestValidateModelEndpointPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		endpoint   string
		wantBase   string
		wantOrigin string
		wantCode   string
	}{
		{name: "public HTTPS", endpoint: "https://MODEL.EXAMPLE.test:443/v1/", wantBase: "https://model.example.test/v1", wantOrigin: "https://model.example.test"},
		{name: "private HTTPS hostname", endpoint: "https://model.internal/v1", wantBase: "https://model.internal/v1", wantOrigin: "https://model.internal"},
		{name: "private HTTPS address", endpoint: "https://10.0.0.7:8443/v1", wantBase: "https://10.0.0.7:8443/v1", wantOrigin: "https://10.0.0.7:8443"},
		{name: "loopback HTTP hostname", endpoint: "http://localhost:8080/v1", wantBase: "http://localhost:8080/v1", wantOrigin: "http://localhost:8080"},
		{name: "loopback HTTP IPv4", endpoint: "http://127.0.0.1:8080/v1", wantBase: "http://127.0.0.1:8080/v1", wantOrigin: "http://127.0.0.1:8080"},
		{name: "loopback HTTP IPv6", endpoint: "http://[::1]:8080/v1", wantBase: "http://[::1]:8080/v1", wantOrigin: "http://[::1]:8080"},
		{name: "non-loopback HTTP", endpoint: "http://model.example.test/v1", wantCode: "config_model_endpoint_invalid"},
		{name: "private HTTP", endpoint: "http://10.0.0.7/v1", wantCode: "config_model_endpoint_invalid"},
		{name: "userinfo", endpoint: "https://user:model.example.test@model.example.test/v1", wantCode: "config_model_endpoint_invalid"},
		{name: "query", endpoint: "https://model.example.test/v1?token=value", wantCode: "config_model_endpoint_invalid"},
		{name: "fragment", endpoint: "https://model.example.test/v1#fragment", wantCode: "config_model_endpoint_invalid"},
		{name: "unsupported scheme", endpoint: "ftp://model.example.test/v1", wantCode: "config_model_endpoint_invalid"},
		{name: "escaped path", endpoint: "https://model.example.test/%76%31", wantCode: "config_model_endpoint_invalid"},
		{name: "path traversal", endpoint: "https://model.example.test/v1/../admin", wantCode: "config_model_endpoint_invalid"},
		{name: "missing host", endpoint: "https:///v1", wantCode: "config_model_endpoint_invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := Defaults()
			config.Model.Endpoint = tt.endpoint
			config.Model.Model = "diagnostic-model"
			err := Validate(&config)
			if tt.wantCode != "" {
				assertSafeError(t, err, ClassConfigurationInvalid, tt.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if config.Model.Endpoint != tt.wantBase {
				t.Errorf("Endpoint = %q, want %q", config.Model.Endpoint, tt.wantBase)
			}
			if config.Model.Origin != tt.wantOrigin {
				t.Errorf("Origin = %q, want %q", config.Model.Origin, tt.wantOrigin)
			}
		})
	}
}

func TestRedirectPolicyAllowsOnlyCanonicalOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		origin string
		target string
		want   bool
	}{
		{name: "same HTTPS origin", origin: "https://model.example.test", target: "https://model.example.test/v1/chat/completions", want: true},
		{name: "same origin default port", origin: "https://model.example.test", target: "https://model.example.test:443/v1", want: true},
		{name: "different host", origin: "https://model.example.test", target: "https://other.example.test/v1"},
		{name: "different port", origin: "https://model.example.test:8443", target: "https://model.example.test:9443/v1"},
		{name: "scheme downgrade", origin: "https://model.example.test", target: "http://model.example.test/v1"},
		{name: "userinfo rejected", origin: "https://model.example.test", target: "https://user@model.example.test/v1"},
		{name: "query rejected", origin: "https://model.example.test", target: "https://model.example.test/v1?value=1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RedirectAllowed(tt.origin, tt.target); got != tt.want {
				t.Fatalf("RedirectAllowed() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestValidateConfigurationFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
		code   string
	}{
		{name: "unsupported version", mutate: func(c *Config) { c.Version = 2 }, code: "config_version_unsupported"},
		{name: "context control character", mutate: func(c *Config) { c.Context = "context\nname" }, code: "config_context_invalid"},
		{name: "context too long", mutate: func(c *Config) { c.Context = strings.Repeat("c", MaxContextBytes+1) }, code: "config_context_invalid"},
		{name: "namespace uppercase", mutate: func(c *Config) { c.Namespace = "Default" }, code: "config_namespace_invalid"},
		{name: "namespace all", mutate: func(c *Config) { c.Namespace = "*" }, code: "config_namespace_invalid"},
		{name: "provider kind", mutate: func(c *Config) { c.Model.ProviderKind = "another_provider" }, code: "config_provider_invalid"},
		{name: "temperature below zero", mutate: func(c *Config) { c.Model.Temperature = -0.01 }, code: "config_temperature_invalid"},
		{name: "temperature above maximum", mutate: func(c *Config) { c.Model.Temperature = 0.21 }, code: "config_temperature_invalid"},
		{name: "output tokens zero", mutate: func(c *Config) { c.Model.MaxOutputTokens = 0 }, code: "config_output_limit_invalid"},
		{name: "output tokens above maximum", mutate: func(c *Config) { c.Model.MaxOutputTokens = MaxModelOutputTokens + 1 }, code: "config_output_limit_invalid"},
		{name: "request timeout zero", mutate: func(c *Config) { c.Model.RequestTimeoutSeconds = 0 }, code: "config_model_timeout_invalid"},
		{name: "request timeout above maximum", mutate: func(c *Config) { c.Model.RequestTimeoutSeconds = MaxModelRequestTimeoutSeconds + 1 }, code: "config_model_timeout_invalid"},
		{name: "streaming disabled", mutate: func(c *Config) { c.Model.Streaming = false }, code: "config_model_capability_invalid"},
		{name: "tool calling disabled", mutate: func(c *Config) { c.Model.ToolCallingRequired = false }, code: "config_model_capability_invalid"},
		{name: "unknown exec policy", mutate: func(c *Config) { c.Kubernetes.ExecCredentials = "prompt" }, code: "config_exec_credentials_invalid"},
		{name: "debug logging", mutate: func(c *Config) { c.Logging.Level = "debug" }, code: "config_log_level_invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := Defaults()
			tt.mutate(&config)
			err := Validate(&config)
			assertSafeError(t, err, ClassConfigurationInvalid, tt.code)
		})
	}
}

func TestModelConfigurationRequiresEndpointAndModel(t *testing.T) {
	t.Parallel()

	config := Defaults()
	_, err := config.ValidatedModel()
	assertSafeError(t, err, ClassConfigurationInvalid, "config_model_required")

	config.Model.Endpoint = "https://model.example.test/v1"
	config.Model.Model = "diagnostic-model"
	model, err := config.ValidatedModel()
	if err != nil {
		t.Fatalf("ValidatedModel() error = %v", err)
	}
	if model.Origin != "https://model.example.test" {
		t.Fatalf("Origin = %q, want canonical origin", model.Origin)
	}
}

func TestValidateAcceptsPartialModelConfigurationForInteractiveCompletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		endpoint   string
		model      string
		wantOrigin string
	}{
		{
			name:       "endpoint only",
			endpoint:   "https://MODEL.EXAMPLE.test:443/v1/",
			wantOrigin: "https://model.example.test",
		},
		{
			name:  "model only",
			model: "diagnostic-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := Defaults()
			config.Model.Endpoint = tt.endpoint
			config.Model.Model = tt.model
			if err := Validate(&config); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if config.Model.Origin != tt.wantOrigin {
				t.Fatalf("Origin = %q, want %q", config.Model.Origin, tt.wantOrigin)
			}
		})
	}
}

func TestLoadInvalidEnvironmentDoesNotEchoValue(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("v", 39) + "-generated"
	_, err := Load(context.Background(), LoadOptions{
		Paths: testPaths(t.TempDir()),
		LookupEnv: lookupMap(map[string]string{
			"KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS": canary,
		}),
	})
	assertSafeError(t, err, ClassConfigurationInvalid, "config_schema_invalid")
	if strings.Contains(err.Error(), canary) {
		t.Fatal("safe error contains an invalid environment value")
	}
}
