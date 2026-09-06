package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
)

type modelSetupStage uint8

const (
	modelSetupEndpoint modelSetupStage = iota + 1
	modelSetupName
	modelSetupStorage
	modelSetupCredential
	modelSetupApplying
)

const modelSetupStorageDisclosure = "Choosing save writes models.agent.api_key and any existing file-sourced models.approval_reviewer.api_key, observability.prometheus.api_key, and observability.loki.api_key as plaintext (not encrypted) in KUPILOT_HOME/config.yaml. Choosing session keeps the new Agent key only in this process."

type modelSetupState struct {
	Stage      modelSetupStage
	Endpoint   string
	Model      string
	Persist    bool
	Cancelling bool
}

func (model *Model) beginModelSetup() {
	model.closePickers()
	model.slashMenu.Close()
	model.modelSetup = &modelSetupState{Stage: modelSetupEndpoint, Endpoint: model.modelEndpoint, Model: model.modelName}
	model.pendingModelSetupID = 0
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxModelSetupEndpointBytes)
	model.composer.SetPlaceholder("Enter an OpenAI-compatible endpoint")
	if model.modelEndpoint != "" {
		model.composer.SetValue(model.modelEndpoint)
	}
}

func (model *Model) beginMissingModelSetup() {
	model.beginModelSetup()
	if model.modelEndpoint != "" && model.modelName != "" {
		model.enterModelSetupStorage()
	}
}

func (model *Model) enterModelSetupStorage() {
	model.modelSetup.Stage = modelSetupStorage
	model.composer.Reset()
	model.composer.SetMaxBytes(16)
	model.composer.SetPlaceholder("save (plaintext) or session")
	model.showDialog("Plaintext credential storage", modelSetupStorageDisclosure)
}

func (model *Model) interruptModelSetup() (tea.Cmd, bool) {
	if model.modelSetup == nil {
		return nil, false
	}
	if model.pendingModelSetupID == 0 {
		model.clearModelSetupState()
		return nil, true
	}
	if model.modelSetup.Cancelling {
		return nil, true
	}
	model.modelSetup.Cancelling = true
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.SetPlaceholder("Cancelling model setup…")
	model.reflow()
	return applicationModelSetupCancel(model.pendingModelSetupID), true
}

func (model *Model) clearModelSetupState() {
	model.modelSetup = nil
	model.pendingModelSetupID = 0
	model.closeDialog()
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.ResetPlaceholder()
	model.reflow()
}

func (model Model) submitModelSetupDraft() (tea.Model, tea.Cmd) {
	if model.modelSetup == nil || model.pendingModelSetupID != 0 {
		return model, nil
	}
	draft := strings.TrimSpace(model.composer.Value())
	switch model.modelSetup.Stage {
	case modelSetupEndpoint:
		if draft == "" || len(draft) > application.MaxModelSetupEndpointBytes || strings.ContainsRune(draft, '\n') {
			model.showDialog("Endpoint required", "Enter one HTTPS endpoint, or explicit loopback HTTP endpoint.")
			return model, nil
		}
		model.modelSetup.Endpoint = draft
		model.modelSetup.Stage = modelSetupName
		model.composer.Reset()
		model.composer.SetMaxBytes(application.MaxModelSetupNameBytes)
		model.composer.SetPlaceholder("Enter the model identifier")
		if model.modelName != "" {
			model.composer.SetValue(model.modelName)
		}
	case modelSetupName:
		if draft == "" || len(draft) > application.MaxModelSetupNameBytes || strings.ContainsRune(draft, '\n') {
			model.showDialog("Model required", "Enter one bounded model identifier.")
			return model, nil
		}
		model.modelSetup.Model = draft
		model.enterModelSetupStorage()
	case modelSetupStorage:
		switch strings.ToLower(draft) {
		case "", "save":
			model.modelSetup.Persist = true
		case "session":
			model.modelSetup.Persist = false
		default:
			model.showDialog("Choose storage", "Type save or session.\n\n"+modelSetupStorageDisclosure)
			return model, nil
		}
		model.modelSetup.Stage = modelSetupCredential
		model.composer.Reset()
		model.composer.SetMaxBytes(application.MaxModelSetupSecretBytes)
		model.composer.SetPlaceholder("Enter the model API key")
		model.composer.SetSecretMode(true)
	case modelSetupCredential:
		secret, err := application.NewModelSetupSecret(model.composer.Value())
		model.composer.Reset()
		model.composer.SetSecretMode(false)
		if err != nil {
			model.composer.SetSecretMode(true)
			model.showDialog("API key required", "Enter one non-empty API key without spaces or control characters.")
			return model, nil
		}
		request := application.ModelSetupRequest{
			RequestID: model.nextUIRequestID(), Endpoint: model.modelSetup.Endpoint,
			Model: model.modelSetup.Model, Persist: model.modelSetup.Persist, Secret: secret,
		}
		if request.Validate() != nil {
			secret.Destroy()
			model.composer.SetSecretMode(true)
			model.showDialog("Model setup unavailable", "The model settings could not be submitted safely.")
			return model, nil
		}
		model.pendingModelSetupID = request.RequestID
		model.modelSetup.Stage = modelSetupApplying
		model.composer.SetMaxBytes(application.MaxQuestionBytes)
		model.composer.SetPlaceholder("Configuring model…")
		return model, applicationModelSetup(request)
	case modelSetupApplying:
		return model, nil
	}
	model.reflow()
	return model, nil
}

func (model *Model) acceptModelSetupResult(result application.ModelSetupResult) tea.Cmd {
	if model.modelSetup == nil || model.pendingModelSetupID == 0 || result.RequestID != model.pendingModelSetupID || result.Validate() != nil {
		return nil
	}
	model.modelEndpoint = model.modelSetup.Endpoint
	model.modelName = sanitizeExternalText(result.Model, application.MaxModelSetupNameBytes)
	model.modelConfigured = true
	model.pendingModelSetupID = 0
	model.modelSetup = nil
	model.closeDialog()
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.ResetPlaceholder()
	if result.Persisted {
		model.transcript.AppendNotice("Agent model configured. The agent API key, plus any existing file-sourced approval_reviewer, Prometheus, and Loki keys, was saved as plaintext in KUPILOT_HOME/config.yaml.")
	} else {
		model.transcript.AppendNotice("Model configured for this Kupilot process only.")
	}
	if model.scopeSelectionRequired && model.startup.Ready && !model.scope.Verified {
		return model.beginRequiredScopeSelection()
	}
	return nil
}

func (model *Model) acceptCancelledModelSetupFailure(message ApplicationFailureMsg) bool {
	if !message.ModelSetup || model.modelSetup == nil || !model.modelSetup.Cancelling ||
		model.pendingModelSetupID == 0 || message.RequestID != model.pendingModelSetupID {
		return false
	}
	model.clearModelSetupState()
	return true
}

func (model *Model) rejectModelSetupCancellation(message ModelSetupCancelRejectedMsg) bool {
	if model.modelSetup == nil || !model.modelSetup.Cancelling || model.pendingModelSetupID == 0 ||
		message.RequestID != model.pendingModelSetupID {
		return false
	}
	model.modelSetup.Cancelling = false
	model.composer.SetPlaceholder("Configuring model…")
	return true
}

func (model *Model) acceptModelSetupFailure(message ApplicationFailureMsg) bool {
	if !message.ModelSetup || model.modelSetup == nil || model.pendingModelSetupID == 0 || message.RequestID != model.pendingModelSetupID {
		return false
	}
	model.pendingModelSetupID = 0
	model.modelSetup.Stage = modelSetupEndpoint
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxModelSetupEndpointBytes)
	model.composer.SetPlaceholder("Enter an OpenAI-compatible endpoint")
	model.composer.SetValue(model.modelSetup.Endpoint)
	return true
}

func (model Model) modelSetupView() string {
	if model.modelSetup == nil {
		return ""
	}
	switch model.modelSetup.Stage {
	case modelSetupEndpoint:
		return "Endpoint · model setup 1/4"
	case modelSetupName:
		return "Model · model setup 2/4"
	case modelSetupStorage:
		return "Storage · model setup 3/4"
	case modelSetupCredential:
		return "API key · model setup 4/4 · masked"
	case modelSetupApplying:
		if model.modelSetup.Cancelling {
			return "Model setup · cancelling…"
		}
		return "Model setup · applying…"
	default:
		return ""
	}
}
