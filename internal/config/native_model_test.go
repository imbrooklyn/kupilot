package config

import (
	"context"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestNativeModelProtocolAndOptionalTemperatureConfiguration(t *testing.T) {
	cases := []struct {
		name, extra                  string
		removeTemperature, nonstream bool
		code                         string
	}{
		{name: "legacy default"},
		{name: "Responses", extra: "\n    api_protocol: responses\n    reasoning_effort: medium", nonstream: true, removeTemperature: true},
		{name: "Responses streaming denied", extra: "\n    api_protocol: responses", code: "config_model_capability_invalid"},
		{name: "Chat nonstream denied", nonstream: true, code: "config_model_capability_invalid"},
		{name: "unknown protocol", extra: "\n    api_protocol: unknown", code: "config_api_protocol_invalid"},
		{name: "omitted temperature", removeTemperature: true},
		{name: "null protocol", extra: "\n    api_protocol: null", code: "config_schema_invalid"},
		{name: "null temperature", extra: "\n    temperature: null", removeTemperature: true, code: "config_schema_invalid"},
		{name: "duplicate protocol", extra: "\n    api_protocol: responses\n    api_protocol: responses", nonstream: true, code: "config_schema_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := version1Config(tc.extra, "")
			if tc.removeTemperature {
				doc = strings.Replace(doc, "    temperature: 0.1\n", "", 1)
			}
			if tc.nonstream {
				doc = strings.Replace(doc, "streaming: true", "streaming: false", 1)
			}
			paths := pathsForHome(t.TempDir())
			writePrivateFile(t, paths.ConfigFile, []byte(doc))
			loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
			cfg := loaded.Config
			defer loaded.Credentials.Destroy()
			if tc.code != "" {
				assertSafeError(t, err, ClassConfigurationInvalid, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (cfg.Models.Agent.Temperature == nil) != tc.removeTemperature {
				t.Fatal("Sampling presence changed")
			}
			if tc.name == "Responses" && (cfg.Models.Agent.APIProtocol != string(domain.ModelAPIProtocolResponses) || cfg.Models.Agent.ReasoningEffort != "medium") {
				t.Fatal("Native model settings changed")
			}
		})
	}
}

func TestSaveNativeResponsesProfilePreservesOmissionAndReasoning(t *testing.T) {
	paths := pathsForHome(t.TempDir())
	key, err := NewSecretValue("synthetic-private-key")
	if err != nil {
		t.Fatal(err)
	}
	defer key.Destroy()
	base := Defaults()
	base.Models.Agent.APIProtocol = "responses"
	base.Models.Agent.Streaming = false
	base.Models.Agent.Temperature = nil
	base.Models.Agent.ReasoningEffort = "high"
	if err := SaveModelProfile(context.Background(), paths, base, ModelProfile{ProviderKind: ProviderOpenAI, Endpoint: "https://model.example.test/v1", Model: "synthetic-model"}, &key); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Credentials.Destroy()
	if loaded.Models.Agent.APIProtocol != "responses" || loaded.Models.Agent.Streaming || loaded.Models.Agent.Temperature != nil || loaded.Models.Agent.ReasoningEffort != "high" {
		t.Fatal("Native settings did not round trip")
	}
	if err := SaveModelProfile(context.Background(), paths, loaded.Config, ModelProfile{ProviderKind: ProviderOllama, Endpoint: "http://127.0.0.1:11434", Model: "synthetic-model"}, nil); err != nil {
		t.Fatal(err)
	}
	switched, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer switched.Credentials.Destroy()
	if switched.Models.Agent.APIProtocol != "" || !switched.Models.Agent.Streaming || switched.Models.Agent.Temperature == nil || switched.Models.Agent.ReasoningEffort != "" {
		t.Fatal("Explicit native provider setup was invalid")
	}
}

func testTemperature(value float64) *float64 { return &value }
