package config

import (
	"context"
	"strings"
	"testing"
)

func TestInitialModelSchemaRejectsRemovedSwitches(t *testing.T) {
	for _, field := range []string{
		"name: custom", "role: agent", "provider_kind: openai",
		"provider_kind: ollama", "credential_ref: agent", "inherit_agent: true",
		"streaming: true", "tool_calling_required: true",
	} {
		t.Run(field, func(t *testing.T) {
			paths := testPaths(t.TempDir())
			writePrivateFile(t, paths.ConfigFile, []byte(version1Config("\n    "+field, "")))
			loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
			defer loaded.Credentials.Destroy()
			assertSafeError(t, err, ClassConfigurationInvalid, "config_schema_invalid")
		})
	}
}

func TestInitialReviewerDoesNotInheritAgentSettingsOrCredential(t *testing.T) {
	paths := testPaths(t.TempDir())
	writePrivateFile(t, paths.ConfigFile, []byte(version1Config("\n    api_key: synthetic-agent-key\n    reasoning_effort: high", `
  approval_reviewer:
    endpoint: https://reviewer.example.test/v1
    model: reviewer-model
`)))
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Credentials.Destroy()
	reviewer := loaded.Models.ApprovalReviewer
	if reviewer == nil || reviewer.Name != string(ModelRoleApprovalReviewer) || reviewer.Role != ModelRoleApprovalReviewer ||
		reviewer.CredentialReference != ModelCredentialApprovalReviewer || reviewer.ReasoningEffort != "" ||
		reviewer.Temperature != nil || reviewer.Streaming || reviewer.ToolCallingRequired ||
		loaded.Credentials.ApprovalReviewer.Value.IsSet() || !loaded.Credentials.Agent.Value.IsSet() {
		t.Fatal("Reviewer inherited Agent configuration or authority")
	}
}

func TestInitialModelDefaultsDeriveFixedBindings(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		paths := testPaths(t.TempDir())
		document := strings.Replace(version1Config("\n    api_protocol: "+protocol, ""), "    temperature: 0.1\n", "", 1)
		writePrivateFile(t, paths.ConfigFile, []byte(document))
		loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
		if err != nil {
			t.Fatal(err)
		}
		loaded.Credentials.Destroy()
		profile := loaded.Models.Agent
		if profile.Name != "agent" || profile.Role != ModelRoleAgent || profile.ProviderKind != ProviderOpenAI ||
			profile.CredentialReference != ModelCredentialAgent || profile.Temperature != nil ||
			profile.Streaming != (protocol == "chat_completions") || !profile.ToolCallingRequired {
			t.Fatal("Fixed role or protocol bindings changed")
		}
	}
}
