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
		!strings.Contains(model.modelSetupView(), "Endpoint · model setup 1/4") ||
		strings.Contains(model.footerView(), "model") {
		t.Fatalf("initial model setup state = %#v prompt=%q footer=%q", model.modelSetup, model.modelSetupView(), model.footerView())
	}
	model = pasteAndSubmitSetup(t, model, "https://model.example.test/v1", modelSetupName)
	model = pasteAndSubmitSetup(t, model, "diagnostic-model", modelSetupStorage)
	assertModelSetupStorageDisclosure(t, model)
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.dialog.Open() || model.modelSetup.Stage != modelSetupStorage {
		t.Fatalf("storage disclosure close = %#v command=%v dialog=%v", model.modelSetup, cmd != nil, model.dialog.Open())
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	model, cmd = updateModel(t, model, ModelSetupResultMsg{Result: application.ModelSetupResult{
		RequestID: message.Request.RequestID, Model: "diagnostic-model",
		Origin: "https://model.example.test", Persisted: true,
	}})
	if !model.modelConfigured || model.modelSetup != nil || cmd != nil ||
		!transcriptContains(model, "Agent model configured.") ||
		strings.Contains(model.footerView(), "model") ||
		strings.Contains(model.render(), canary) || strings.Contains(model.TerminalTranscript(), canary) {
		t.Fatalf("configured model state = configured=%v setup=%#v footer=%q", model.modelConfigured, model.modelSetup, model.footerView())
	}
	if model.composer.PreviousHistory() {
		t.Fatal("credential was retained in composer history")
	}
}

func TestModelSetupKeepsAFieldLabelVisibleAfterTypingAtEveryStep(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		ModelConfiguredSet: true, ModelConfigured: false,
	})
	steps := []struct {
		label string
		value string
		want  modelSetupStage
	}{
		{label: "Endpoint · model setup 1/4", value: "https://model.example.test/v1", want: modelSetupName},
		{label: "Model · model setup 2/4", value: "diagnostic-model", want: modelSetupStorage},
		{label: "Storage · model setup 3/4", value: "session", want: modelSetupCredential},
	}
	for _, step := range steps {
		model.composer.SetValue(step.value)
		if label := model.inputLabelView(); !strings.Contains(label, step.label) ||
			!strings.Contains(model.render(), step.label) {
			t.Fatalf("typed %s field lost its persistent label: label=%q", step.label, label)
		}
		var cmd tea.Cmd
		model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil || model.modelSetup == nil || model.modelSetup.Stage != step.want {
			t.Fatalf("stage after %s = %#v command=%v", step.label, model.modelSetup, cmd != nil)
		}
		if step.want == modelSetupStorage {
			assertModelSetupStorageDisclosure(t, model)
			model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd != nil || model.dialog.Open() || model.modelSetup.Stage != modelSetupStorage {
				t.Fatalf("storage disclosure close = %#v command=%v", model.modelSetup, cmd != nil)
			}
		}
	}
	model.composer.SetValue("generated-labelled-key")
	if label := model.inputLabelView(); !strings.Contains(label, "API key · model setup 4/4 · masked") ||
		!strings.Contains(model.render(), "API key · model setup 4/4 · masked") ||
		strings.Contains(model.render(), "generated-labelled-key") {
		t.Fatalf("credential label or masking = label %q render %q", label, model.render())
	}
}

func TestModelSetupArrowKeysCannotRecallOrdinaryInputHistory(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.composer.RecordSubmission("ordinary diagnostic question")
	model.beginModelSetup()
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if cmd != nil || model.modelSetup == nil || model.modelSetup.Stage != modelSetupEndpoint ||
		model.composer.Value() != "" {
		t.Fatalf("model setup recalled ordinary history: setup=%#v draft=%q command=%v", model.modelSetup, model.composer.Value(), cmd != nil)
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if cmd != nil || model.composer.Value() != "" {
		t.Fatalf("explicit history shortcut reached model setup: draft=%q command=%v", model.composer.Value(), cmd != nil)
	}
}

func TestCtrlCCancelsModelSetupDraftAtEveryStepWithoutChangingRuntime(t *testing.T) {
	t.Parallel()

	for _, stage := range []modelSetupStage{modelSetupEndpoint, modelSetupName, modelSetupStorage, modelSetupCredential} {
		t.Run(modelSetupStageName(stage), func(t *testing.T) {
			model := NewModel(Config{
				Width: 80, Height: 24, Theme: ThemeNoColor,
				ModelEndpoint: "https://old.example.test/v1", ModelName: "old-model",
				ModelConfiguredSet: true, ModelConfigured: true,
			})
			model.beginModelSetup()
			model.modelSetup.Stage = stage
			model.composer.SetValue("generated-abandoned-value")
			if stage == modelSetupCredential {
				model.composer.SetSecretMode(true)
			}
			model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			if cmd != nil || model.modelSetup != nil || !model.modelConfigured || model.modelName != "old-model" ||
				model.modelEndpoint != "https://old.example.test/v1" || model.composer.Value() != "" ||
				strings.Contains(model.render(), "generated-abandoned-value") {
				t.Fatalf("Ctrl+C at %s = setup %#v configured=%v endpoint=%q name=%q draft=%q command=%v",
					modelSetupStageName(stage), model.modelSetup, model.modelConfigured, model.modelEndpoint,
					model.modelName, model.composer.Value(), cmd != nil)
			}
		})
	}

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		ModelConfiguredSet: true, ModelConfigured: false,
	})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || model.modelSetup != nil || model.modelConfigured {
		t.Fatalf("initial setup cancellation = setup %#v configured=%v command=%v", model.modelSetup, model.modelConfigured, cmd != nil)
	}
}

func TestCtrlCCancelsApplyingModelSetupWithCorrelationAndStaleSafety(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		ModelEndpoint: "https://old.example.test/v1", ModelName: "old-model",
		ModelConfiguredSet: true, ModelConfigured: true,
	})
	model.beginModelSetup()
	model.modelSetup.Stage = modelSetupApplying
	model.pendingModelSetupID = 44
	model.composer.SetPlaceholder("Configuring model…")
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil || model.modelSetup == nil || !model.modelSetup.Cancelling ||
		model.pendingModelSetupID != 44 || !strings.Contains(model.inputLabelView(), "cancelling") {
		t.Fatalf("applying cancellation state = setup %#v pending=%d command=%v", model.modelSetup, model.pendingModelSetupID, cmd != nil)
	}
	message, ok := cmd().(ApplicationModelSetupCancelMsg)
	if !ok || message.RequestID != 44 {
		t.Fatalf("applying cancellation request = %#v", message)
	}
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "must-not-edit"})
	model, _ = updateModel(t, model, keyText("x"))
	if model.composer.Value() != "" {
		t.Fatalf("applying setup accepted edits: %q", model.composer.Value())
	}
	model, _ = updateModel(t, model, ApplicationFailureMsg{RequestID: 43, ModelSetup: true})
	if model.modelSetup == nil || !model.modelSetup.Cancelling || model.pendingModelSetupID != 44 {
		t.Fatal("stale setup failure cleared the correlated cancellation")
	}
	model, _ = updateModel(t, model, ApplicationFailureMsg{RequestID: 44, ModelSetup: true})
	if model.modelSetup != nil || model.pendingModelSetupID != 0 || model.dialog.Open() ||
		!model.modelConfigured || model.modelName != "old-model" {
		t.Fatalf("cancelled setup completion = setup %#v pending=%d dialog=%v configured=%v name=%q",
			model.modelSetup, model.pendingModelSetupID, model.dialog.Open(), model.modelConfigured, model.modelName)
	}
}

func TestRejectedModelSetupCancellationRemainsRetryable(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.beginModelSetup()
	model.modelSetup.Stage = modelSetupApplying
	model.pendingModelSetupID = 45
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model, _ = updateModel(t, model, ModelSetupCancelRejectedMsg{RequestID: 45})
	if model.modelSetup == nil || model.modelSetup.Cancelling || model.pendingModelSetupID != 45 ||
		!model.dialog.Open() || !strings.Contains(model.render(), "Cancellation unavailable") {
		t.Fatalf("rejected cancellation state = setup %#v pending=%d dialog=%v", model.modelSetup, model.pendingModelSetupID, model.dialog.Open())
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil || model.modelSetup == nil || !model.modelSetup.Cancelling ||
		cmd().(ApplicationModelSetupCancelMsg).RequestID != 45 {
		t.Fatal("rejected setup cancellation could not be retried")
	}
}

func modelSetupStageName(stage modelSetupStage) string {
	switch stage {
	case modelSetupEndpoint:
		return "endpoint"
	case modelSetupName:
		return "model"
	case modelSetupStorage:
		return "storage"
	case modelSetupCredential:
		return "credential"
	case modelSetupApplying:
		return "applying"
	default:
		return "unknown"
	}
}

func transcriptContains(model Model, value string) bool {
	for _, entry := range model.transcript.Entries() {
		if strings.Contains(entry.Text, value) {
			return true
		}
	}
	return false
}

func TestUnconfiguredModelDoesNotPreemptExplicitResumeStartup(t *testing.T) {
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent:        application.UIStartIntent{Kind: application.UIStartResumePicker},
		ModelConfiguredSet: true, ModelConfigured: false,
	})
	if model.modelSetup != nil || model.startup.Ready ||
		strings.Contains(model.footerView(), "model") {
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
	if !model.dialog.Open() {
		t.Fatal("/model did not disclose plaintext storage before selection")
	}
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
	assertModelSetupStorageDisclosure(t, model)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
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

func assertModelSetupStorageDisclosure(t *testing.T, model Model) {
	t.Helper()
	rendered := model.render()
	for _, value := range []string{
		"Plaintext credential storage",
		"models.agent.api_key",
		"models.approval_reviewer.api_key",
		"observability.prometheus.api_key",
		"observability.loki.api_key",
		"plaintext",
		"(not",
		"encrypted)",
		"KUPILOT_HOME/config.yaml",
	} {
		if !strings.Contains(rendered, value) {
			t.Fatalf("storage disclosure is missing %q: %q", value, rendered)
		}
	}
	if !model.dialog.Open() || model.modelSetup == nil || model.modelSetup.Stage != modelSetupStorage {
		t.Fatalf("storage disclosure state = setup %#v dialog=%v", model.modelSetup, model.dialog.Open())
	}
}
