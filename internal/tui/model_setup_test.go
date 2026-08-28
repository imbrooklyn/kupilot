package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func TestUnconfiguredModelSetupMasksCredentialAndEmitsOneTypedRequest(t *testing.T) {
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		ModelConfiguredSet: true, ModelConfigured: false,
	})
	if model.modelSetup == nil || model.modelSetup.Stage != modelSetupEndpoint ||
		!strings.Contains(model.footerView(), "model/unconfigured") {
		t.Fatalf("initial model setup state = %#v footer=%q", model.modelSetup, model.footerView())
	}
	model = pasteAndSubmitSetup(t, model, "https://model.example.test/v1", modelSetupName)
	model = pasteAndSubmitSetup(t, model, "diagnostic-model", modelSetupStorage)
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.modelSetup.Stage != modelSetupCredential {
		t.Fatalf("default storage selection = %#v command=%v", model.modelSetup, cmd != nil)
	}
	canary := strings.Repeat("z", 43) + "-generated"
	model, _ = updateModel(t, model, tea.PasteMsg{Content: canary})
	if strings.Contains(model.render(), canary) || !strings.Contains(model.render(), "••••") {
		t.Fatal("masked credential was exposed or not visibly masked")
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || model.modelSetup.Stage != modelSetupApplying || model.composer.Value() != "" {
		t.Fatalf("credential submission state = %#v command=%v draft=%q", model.modelSetup, cmd != nil, model.composer.Value())
	}
	message, ok := cmd().(ApplicationModelSetupMsg)
	if !ok || message.Request.Validate() != nil || !message.Request.Persist ||
		message.Request.Endpoint != "https://model.example.test/v1" || message.Request.Model != "diagnostic-model" {
		t.Fatalf("model setup request = %#v", message)
	}
	if err := message.Request.Secret.Use(func(value string) {
		if value != canary {
			t.Fatalf("submitted credential changed after masked rendering")
		}
	}); err != nil {
		t.Fatalf("submitted credential unavailable: %v", err)
	}
	if strings.Contains(model.render(), canary) {
		t.Fatal("submitted credential entered render state")
	}
	message.Request.Secret.Destroy()
	model, _ = updateModel(t, model, ModelSetupResultMsg{Result: application.ModelSetupResult{
		RequestID: message.Request.RequestID, Model: "diagnostic-model",
		Origin: "https://model.example.test", Persisted: true,
	}})
	if !model.modelConfigured || model.modelSetup != nil || !strings.Contains(model.footerView(), "model/diagnostic-model") ||
		strings.Contains(model.render(), canary) {
		t.Fatalf("configured model state = configured=%v setup=%#v footer=%q", model.modelConfigured, model.modelSetup, model.footerView())
	}
	if model.composer.PreviousHistory() {
		t.Fatal("credential was retained in composer history")
	}
}

func TestUnconfiguredModelDoesNotPreemptExplicitResumeStartup(t *testing.T) {
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent:        application.UIStartIntent{Kind: application.UIStartResumePicker},
		ModelConfiguredSet: true, ModelConfigured: false,
	})
	if model.modelSetup != nil || model.startup.Ready ||
		!strings.Contains(model.footerView(), "model/unconfigured") {
		t.Fatalf("unconfigured resume state = setup=%#v startup=%#v footer=%q", model.modelSetup, model.startup, model.footerView())
	}
	cmd := model.Init()
	if cmd == nil {
		t.Fatal("unconfigured resume did not preserve its initial Session query")
	}
	message, ok := cmd().(ApplicationQueryMsg)
	if !ok || message.Query.Kind != application.UICompletionSession {
		t.Fatalf("unconfigured resume initialization = %#v", message)
	}
}

func TestModelSlashReconfiguresAndFailureRestartsEditableFlow(t *testing.T) {
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		ModelEndpoint: "https://old.example.test/v1", ModelName: "old-model",
		ModelConfiguredSet: true, ModelConfigured: true,
	})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/model"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.modelSetup == nil || model.modelSetup.Stage != modelSetupEndpoint {
		t.Fatalf("/model state = %#v command=%v", model.modelSetup, cmd != nil)
	}
	model.composer.SetValue("https://new.example.test/v1")
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	model.composer.SetValue("new-model")
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	model.composer.SetValue("session")
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	model.composer.SetValue("generated-session-key")
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	message := cmd().(ApplicationModelSetupMsg)
	if message.Request.Persist {
		t.Fatal("session storage choice became a local persistence request")
	}
	message.Request.Secret.Destroy()
	model, _ = updateModel(t, model, ApplicationFailureMsg{
		RequestID: message.Request.RequestID, ModelSetup: true,
	})
	if model.modelSetup == nil || model.modelSetup.Stage != modelSetupEndpoint ||
		model.composer.Value() != "https://new.example.test/v1" ||
		!model.dialog.Open() {
		t.Fatalf("failed model setup state = %#v draft=%q dialog=%v", model.modelSetup, model.composer.Value(), model.dialog.Open())
	}
}

func TestInvalidModelCredentialKeepsComposerMaskedAndOutOfHistory(t *testing.T) {
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		ModelEndpoint: "https://model.example.test/v1", ModelName: "diagnostic-model",
		ModelConfiguredSet: true, ModelConfigured: false,
	})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.modelSetup == nil || model.modelSetup.Stage != modelSetupCredential {
		t.Fatalf("storage submission state = %#v", model.modelSetup)
	}
	model.composer.SetValue("invalid key")
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.modelSetup.Stage != modelSetupCredential || !model.dialog.Open() {
		t.Fatalf("invalid credential state = %#v command=%v dialog=%v", model.modelSetup, cmd != nil, model.dialog.Open())
	}
	model.dialog.Close()
	canary := "generated-retry-key"
	model.composer.SetValue(canary)
	model.composer.RecordSubmission(canary)
	if strings.Contains(model.render(), canary) || model.composer.PreviousHistory() {
		t.Fatal("retried credential was rendered or entered composer history")
	}
}

func TestOptionalModelSetupCanBeCancelledWithoutChangingRuntime(t *testing.T) {
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		ModelEndpoint: "https://old.example.test/v1", ModelName: "old-model",
		ModelConfiguredSet: true, ModelConfigured: true,
	})
	model.beginModelSetup()
	model.modelSetup.Stage = modelSetupCredential
	model.composer.Reset()
	model.composer.SetSecretMode(true)
	model.composer.SetValue("generated-abandoned-key")
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil || model.modelSetup != nil || !model.modelConfigured || model.modelName != "old-model" ||
		model.composer.Value() != "" || strings.Contains(model.render(), "generated-abandoned-key") {
		t.Fatalf("cancelled model setup = setup %#v configured=%v name=%q draft=%q command=%v",
			model.modelSetup, model.modelConfigured, model.modelName, model.composer.Value(), cmd != nil)
	}
}

func pasteAndSubmitSetup(t *testing.T, model Model, value string, want modelSetupStage) Model {
	t.Helper()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: value})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.modelSetup == nil || model.modelSetup.Stage != want {
		t.Fatalf("setup stage after %q = %#v command=%v", value, model.modelSetup, cmd != nil)
	}
	return model
}
