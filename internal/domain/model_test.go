package domain

import (
	"strings"
	"testing"
	"time"
)

func TestModelConfigurationRequiresOneExplicitResponseFormat(t *testing.T) {
	t.Parallel()

	configuration := ModelConfiguration{
		ProfileName: "agent", Role: ModelRoleAgent, ProviderKind: ModelProviderOpenAI,
		Endpoint: "http://127.0.0.1:11434/v1", Origin: "http://127.0.0.1:11434", Model: "fixture-model",
		ResponseFormat: ModelResponseFormatPrompt, APIKeySource: ModelAPIKeySourceRuntime,
		Temperature: 0.1, RequestTimeout: time.Minute, StreamingRequired: true, ToolCallingRequired: true,
		TransportPolicy: ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP,
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate(prompt) error = %v", err)
	}
	configuration.ResponseFormat = ModelResponseFormatJSONObject
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate(json_object) error = %v", err)
	}
	configuration.ResponseFormat = "automatic"
	if err := configuration.Validate(); err == nil {
		t.Fatal("Validate(unknown response format) error = nil")
	}
}

func TestModelConfigurationSeparatesOpenAIAndNativeOllamaAuthority(t *testing.T) {
	t.Parallel()

	native := ModelConfiguration{
		ProfileName: "agent", Role: ModelRoleAgent, ProviderKind: ModelProviderOllama,
		Endpoint: "http://127.0.0.1:11434", Origin: "http://127.0.0.1:11434", Model: "fixture-model",
		ResponseFormat: ModelResponseFormatJSONObject, APIKeySource: ModelAPIKeySourceNone,
		Temperature: 0.1, RequestTimeout: 10 * time.Minute, StreamingRequired: true, ToolCallingRequired: true,
		TransportPolicy: ModelTransportPolicyLoopbackHTTPNoRedirect,
	}
	if err := native.Validate(); err != nil {
		t.Fatalf("Validate(native Ollama) error = %v", err)
	}
	for _, mutate := range []func(*ModelConfiguration){
		func(value *ModelConfiguration) {
			value.Endpoint, value.Origin = "http://model.example.test:11434", "http://model.example.test:11434"
		},
		func(value *ModelConfiguration) {
			value.Endpoint, value.Origin = "https://127.0.0.1:11434", "https://127.0.0.1:11434"
		},
		func(value *ModelConfiguration) { value.Endpoint = "http://127.0.0.1:11434/api" },
		func(value *ModelConfiguration) { value.APIKeySource = ModelAPIKeySourceRuntime },
		func(value *ModelConfiguration) {
			value.TransportPolicy = ModelTransportPolicyVerifiedHTTPSOrLoopbackHTTP
		},
	} {
		candidate := native
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("Validate(unsafe native Ollama) accepted %#v", candidate)
		}
	}
	retired := native
	retired.ProviderKind = "openai_compatible"
	if err := retired.Validate(); err == nil {
		t.Fatal("Validate() accepted the retired provider kind")
	}
}

func TestValidModelTextEnforcesCallerSelectedBoundsAndTerminalSafety(t *testing.T) {
	if !ValidModelText(strings.Repeat("a", MaxModelMessageBytes), MaxModelMessageBytes, false) {
		t.Fatal("ValidModelText() rejected assistant text at the exact limit")
	}
	if ValidModelText(strings.Repeat("a", MaxModelMessageBytes+1), MaxModelMessageBytes, false) {
		t.Fatal("ValidModelText() accepted assistant text over the limit")
	}
	if ValidModelText("unsafe\x1btext", MaxModelInputMessageBytes, false) ||
		ValidModelText("unsafe"+string(rune(0x202e))+"text", MaxModelInputMessageBytes, false) {
		t.Fatal("ValidModelText() accepted terminal or bidirectional control text")
	}
}

func TestValidModelToolArgumentsAcceptsNeutralJSONAndRejectsUnsafeShapes(t *testing.T) {
	t.Parallel()

	valid := " { \"resource\": {\"name\": \"sample-pod\", \"kind\": \"Pod\"}, \"purpose\": \"Inspect the selected Pod.\" } "
	if !ValidModelToolArguments(valid) {
		t.Fatal("ValidModelToolArguments() rejected noncanonical provider JSON")
	}

	tests := []struct {
		name      string
		arguments string
	}{
		{name: "duplicate nested field", arguments: `{"purpose":"Inspect.","resource":{"kind":"Pod","kind":"Secret","name":"sample-pod"}}`},
		{name: "runtime authority", arguments: `{"purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"},"scope":{"namespace":"other"}}`},
		{name: "non-object root", arguments: `["get_resource"]`},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			if ValidModelToolArguments(current.arguments) {
				t.Fatal("ValidModelToolArguments() accepted an unsafe shape")
			}
		})
	}
}

func TestValidStrictModelToolSchemaRequiresClosedObjects(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		valid  bool
	}{
		{
			name:   "nullable field is required",
			schema: `{"additionalProperties":false,"properties":{"optional":{"type":["string","null"]}},"required":["optional"],"type":"object"}`,
			valid:  true,
		},
		{
			name:   "empty strict object",
			schema: `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
			valid:  true,
		},
		{
			name:   "missing root required",
			schema: `{"additionalProperties":false,"properties":{"value":{"type":"string"}},"type":"object"}`,
		},
		{
			name:   "nullable root object",
			schema: `{"additionalProperties":false,"properties":{},"required":[],"type":["object","null"]}`,
		},
		{
			name:   "incomplete root required",
			schema: `{"additionalProperties":false,"properties":{"optional":{"type":["string","null"]},"value":{"type":"string"}},"required":["value"],"type":"object"}`,
		},
		{
			name:   "missing nested required",
			schema: `{"additionalProperties":false,"properties":{"resource":{"additionalProperties":false,"properties":{"name":{"type":"string"}},"type":"object"}},"required":["resource"],"type":"object"}`,
		},
		{
			name:   "duplicate required field",
			schema: `{"additionalProperties":false,"properties":{"value":{"type":"string"}},"required":["value","value"],"type":"object"}`,
		},
		{
			name:   "unsupported unique items",
			schema: `{"additionalProperties":false,"properties":{"values":{"items":{"type":"string"},"type":"array","uniqueItems":true}},"required":["values"],"type":"object"}`,
		},
	}

	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			if got := ValidStrictModelToolSchema(current.schema); got != current.valid {
				t.Fatalf("ValidStrictModelToolSchema() = %t, want %t", got, current.valid)
			}
		})
	}
}
