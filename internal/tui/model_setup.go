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

type modelSetupState struct {
	Stage    modelSetupStage
	Endpoint string
	Model    string
	Persist  bool
}

func (model *Model) beginModelSetup() {
	model.closePickers()
	model.slashMenu.Close()
	model.modelSetup = &modelSetupState{Stage: modelSetupEndpoint, Endpoint: model.modelEndpoint, Model: model.modelName}
	model.pendingModelSetupID = 0
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxModelSetupEndpointBytes)
	model.composer.SetPlaceholder("OpenAI-compatible endpoint")
	if model.modelEndpoint != "" {
		model.composer.SetValue(model.modelEndpoint)
	}
}

func (model *Model) beginMissingModelSetup() {
	model.beginModelSetup()
	if model.modelEndpoint != "" && model.modelName != "" {
		model.modelSetup.Stage = modelSetupStorage
		model.composer.Reset()
		model.composer.SetMaxBytes(16)
		model.composer.SetPlaceholder("save (default) or session")
	}
}

func (model *Model) cancelOptionalModelSetup() bool {
	if model.modelSetup == nil || model.pendingModelSetupID != 0 || !model.modelConfigured {
		return false
	}
	model.modelSetup = nil
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.ResetPlaceholder()
	model.reflow()
	return true
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
		model.composer.SetPlaceholder("Model identifier")
		if model.modelName != "" {
			model.composer.SetValue(model.modelName)
		}
	case modelSetupName:
		if draft == "" || len(draft) > application.MaxModelSetupNameBytes || strings.ContainsRune(draft, '\n') {
			model.showDialog("Model required", "Enter one bounded model identifier.")
			return model, nil
		}
		model.modelSetup.Model = draft
		model.modelSetup.Stage = modelSetupStorage
		model.composer.Reset()
		model.composer.SetMaxBytes(16)
		model.composer.SetPlaceholder("save (default) or session")
	case modelSetupStorage:
		switch strings.ToLower(draft) {
		case "", "save":
			model.modelSetup.Persist = true
		case "session":
			model.modelSetup.Persist = false
		default:
			model.showDialog("Choose storage", "Type save to store the key as plaintext in KUPILOT_HOME, or session to keep it only for this run.")
			return model, nil
		}
		model.modelSetup.Stage = modelSetupCredential
		model.composer.Reset()
		model.composer.SetMaxBytes(application.MaxModelSetupSecretBytes)
		model.composer.SetPlaceholder("Model API key (masked)")
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

func (model *Model) acceptModelSetupResult(result application.ModelSetupResult) {
	if model.modelSetup == nil || model.pendingModelSetupID == 0 || result.RequestID != model.pendingModelSetupID || result.Validate() != nil {
		return
	}
	model.modelEndpoint = model.modelSetup.Endpoint
	model.modelName = sanitizeExternalText(result.Model, application.MaxModelSetupNameBytes)
	model.modelConfigured = true
	model.pendingModelSetupID = 0
	model.modelSetup = nil
	model.composer.Reset()
	model.composer.SetSecretMode(false)
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.ResetPlaceholder()
	if result.Persisted {
		model.transcript.AppendNotice("Model configured. The API key was saved as plaintext in KUPILOT_HOME/config.yaml.")
	} else {
		model.transcript.AppendNotice("Model configured for this KuPilot process only.")
	}
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
	model.composer.SetPlaceholder("OpenAI-compatible endpoint")
	model.composer.SetValue(model.modelSetup.Endpoint)
	return true
}

func (model Model) modelSetupView() string {
	if model.modelSetup == nil {
		return ""
	}
	switch model.modelSetup.Stage {
	case modelSetupEndpoint:
		return "Model setup 1/4 — Enter the OpenAI-compatible endpoint."
	case modelSetupName:
		return "Model setup 2/4 — Enter the model identifier."
	case modelSetupStorage:
		return "Model setup 3/4 — Type save (default, plaintext and not encrypted) or session."
	case modelSetupCredential:
		return "Model setup 4/4 — Enter the API key. Input is masked and is not added to history."
	case modelSetupApplying:
		return "Model setup — Validating and constructing the single model runtime…"
	default:
		return ""
	}
}
