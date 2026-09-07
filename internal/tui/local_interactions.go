package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const MaxClipboardAnswerBytes = 64 * 1024

func (model Model) requestDoctor() (Model, tea.Cmd) {
	model.composer.Reset()
	model.slashMenu.Close()
	if model.pendingDoctorID != 0 {
		model.showDialog("Doctor busy", "Wait for the current local diagnostic check to finish.")
		return model, nil
	}
	requestID := model.nextUIRequestID()
	command := application.UICommand{Kind: application.UICommandShowDoctor, RequestID: requestID}
	if command.Validate() != nil {
		model.showDialog("Doctor unavailable", "The local diagnostic request could not be constructed safely.")
		return model, nil
	}
	model.pendingDoctorID = requestID
	model.showDialog("Running local doctor", "Checking only redacted configuration, storage, recovery, feature, and terminal capability state.")
	return model, applicationCommand(command)
}

func (model *Model) showDoctor(result application.UIDoctorResult) {
	capabilities := model.terminalCapabilities
	storage := "healthy"
	if result.PersistenceDegraded || result.Storage.ProtectedActivity > 0 || result.Storage.PendingRecoveryRuns > 0 {
		storage = "degraded"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Schema: %s\nApplication: %s\nConfiguration: %s\nProvider: %s\nOrigin hash: %s\n",
		result.SchemaVersion, result.ApplicationVersion, result.ConfigurationSchema, result.ProviderKind, result.AgentOriginHash)
	fmt.Fprintf(&builder, "Model configured: %t\nStorage: %s · schema %d · Sessions %d · protected activity %d · future activity %d · pending recovery %d\n",
		result.ModelConfigured, storage, result.Storage.SchemaRevision, result.Storage.SessionCount,
		result.Storage.ProtectedActivity, result.Storage.FutureActivity, result.Storage.PendingRecoveryRuns)
	fmt.Fprintf(&builder, "Model boundary: %s %s · %s %s · %s · live conformance %s\n",
		result.ModelCompatibility.Runtime, result.ModelCompatibility.RuntimeVersion,
		result.ModelCompatibility.Adapter, result.ModelCompatibility.AdapterVersion,
		result.ModelCompatibility.Protocol, result.ModelCompatibility.LiveConformance)
	fmt.Fprintf(&builder, "Terminal: native clipboard %s · OSC 52 %s · multiplexer %s · remote session %s · title %s · notification %s · color %s · alternate screen %s · reduced motion %t · scrollback %s\n",
		capabilities.NativeClipboard, capabilities.OSC52, capabilities.Multiplexer, capabilities.RemoteSession,
		capabilities.Title, capabilities.Notification, capabilities.Color, capabilities.AlternateScreen, capabilities.ReducedMotion, capabilities.Scrollback)
	fmt.Fprintf(&builder, "Stream recovery: %s\n", result.ModelCompatibility.StreamContinuation)
	builder.WriteString("Checks are local and redacted. No model, Kubernetes, Tool, Reviewer, approval, process, or executor call was made.")
	model.showDialog("Doctor", builder.String())
}

func (model Model) copyLatestCommittedAnswer() (Model, tea.Cmd) {
	model.composer.Reset()
	model.slashMenu.Close()
	if !model.terminalClipboard {
		model.showDialog("Clipboard unavailable", "Terminal-native clipboard support could not be established. No clipboard command was emitted.")
		return model, nil
	}
	answer, ok := model.transcript.LatestCommittedAssistantFinal()
	if !ok {
		model.showDialog("Nothing to copy", "There is no committed assistant final answer in this Session transcript.")
		return model, nil
	}
	answer = sanitizeExternalText(answer, 0)
	if answer == "" || len(answer) > MaxClipboardAnswerBytes {
		model.showDialog("Copy unavailable", "The committed answer is empty after terminal-safety normalization or exceeds the exact 65536-byte clipboard limit. Nothing was copied.")
		return model, nil
	}
	model.transcript.AppendNotice("The latest committed assistant final answer was sent to the terminal-native clipboard sink.")
	return model, tea.SetClipboard(answer)
}

func (model Model) beginTranscriptSearch(query, returnDraft string) (Model, tea.Cmd) {
	if model.modelSetup != nil || model.sessionExport != nil || model.pickerOpen() || model.dialog.Open() ||
		model.approvalDialog.Open() || model.evidenceDialog.Open() || model.transcript.EvidenceSelecting() {
		model.showDialog("Find unavailable", "Finish the current local interaction before searching the committed transcript.")
		return model, nil
	}
	model.closePickers()
	model.slashMenu.Close()
	model.searchMode = true
	model.searchReturnDraft = returnDraft
	model.composer.Reset()
	model.composer.SetMaxBytes(components.MaxTranscriptSearchQueryBytes)
	model.composer.SetPlaceholder("Find committed transcript text")
	if query != "" {
		query = sanitizeExternalText(query, 0)
		if len(query) > components.MaxTranscriptSearchQueryBytes {
			model.endTranscriptSearch()
			model.showDialog("Find limit", "The query exceeds the exact 512-byte search limit.")
			return model, nil
		}
		model.composer.SetValue(query)
		model.syncTranscriptSearch()
	}
	model.reflow()
	return model, nil
}

func (model *Model) syncTranscriptSearch() {
	if !model.searchMode {
		return
	}
	query := model.composer.Value()
	if query == "" {
		model.transcript.EndSearch()
		return
	}
	_, err := model.transcript.BeginSearch(query)
	if err == nil {
		return
	}
	model.transcript.EndSearch()
	if errors.Is(err, components.ErrTranscriptSearchLimit) {
		model.showDialog("Find limit", "The committed transcript search exceeded its exact entry, byte, match, or 50 ms time bound. No partial result was retained.")
		return
	}
	model.showDialog("Find unavailable", "The search query is empty, invalid, or exceeds the exact 512-byte limit.")
}

func (model *Model) endTranscriptSearch() {
	returnDraft := model.searchReturnDraft
	model.searchMode = false
	model.searchReturnDraft = ""
	model.transcript.EndSearch()
	model.composer.Reset()
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.ResetPlaceholder()
	if returnDraft != "" {
		model.composer.SetValue(returnDraft)
	}
	model.reflow()
}

func (model Model) updateTranscriptSearchKey(message tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(message, model.keymap.Close), key.Matches(message, model.keymap.Quit), key.Matches(message, model.keymap.Find):
		model.endTranscriptSearch()
		return model, nil
	case key.Matches(message, model.keymap.Submit), key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
		model.transcript.MoveSearch(1)
		model.reflow()
		return model, nil
	case key.Matches(message, model.keymap.Reverse), key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
		model.transcript.MoveSearch(-1)
		model.reflow()
		return model, nil
	case key.Matches(message, model.keymap.TranscriptUp):
		model.transcript.PageUp()
		return model, nil
	case key.Matches(message, model.keymap.TranscriptDown):
		model.transcript.PageDown()
		return model, nil
	}
	updated, command, err := model.composer.Update(message)
	if err != nil {
		model.showDialog("Find limit", "The query exceeds the exact 512-byte search limit.")
		return model, nil
	}
	model.composer = updated
	model.syncTranscriptSearch()
	model.reflow()
	return model, command
}

func (model Model) executeQueueCommand(argument string) (Model, tea.Cmd) {
	fields := strings.Fields(argument)
	if len(fields) == 1 && fields[0] == "clear" {
		if model.conversationStatus.Editable == 0 || model.conversationRevision < 1 {
			model.showDialog("Queue empty", "There are no editable queued, rejected, or recovered inputs to clear.")
			return model, nil
		}
		model.composer.Reset()
		model.slashMenu.Close()
		model.queueClearConfirmation = &queueClearConfirmation{
			Revision: model.conversationRevision, ScopeGeneration: model.scope.Generation,
			PolicyGeneration: model.permission.PolicyGeneration, Editable: model.conversationStatus.Editable,
		}
		model.dialog.ShowWithHint(
			"Clear editable queue?",
			fmt.Sprintf("Remove %d editable queued, rejected, or recovered input(s) at queue revision %d? Pending, committing, and committed steer entries cannot be removed.", model.conversationStatus.Editable, model.conversationRevision),
			"Y or Enter confirms · N, Esc, or Ctrl+C keeps every item",
		)
		return model, nil
	}
	if len(fields) == 2 && fields[0] == "cancel" {
		itemID := domain.MessageID(fields[1])
		if !itemID.Valid() || model.conversationRevision < 1 {
			model.showDialog("Queue item unavailable", "Use the exact full item ID shown beside an editable queued, rejected, or recovered input.")
			return model, nil
		}
		command := application.UICommand{
			Kind: application.UICommandCancelFollowUp, RequestID: model.nextUIRequestID(), ItemID: itemID,
			ExpectedQueueRevision: model.conversationRevision, ExpectedScopeGeneration: model.scope.Generation,
			ExpectedPolicyGeneration: model.permission.PolicyGeneration,
		}
		return model.dispatchQueueMutation(command)
	}
	model.showDialog("Invalid queue command", "Use /queue cancel <item-id> or /queue clear.")
	return model, nil
}

func (model Model) updateQueueClearConfirmationKey(message tea.KeyPressMsg) (Model, tea.Cmd) {
	confirmation := model.queueClearConfirmation
	if confirmation == nil {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Quit) || strings.EqualFold(message.Text, "n") {
		model.queueClearConfirmation = nil
		model.closeDialog()
		return model, nil
	}
	if !key.Matches(message, model.keymap.Submit) && !strings.EqualFold(message.Text, "y") {
		return model, nil
	}
	command := application.UICommand{
		Kind: application.UICommandClearFollowUps, RequestID: model.nextUIRequestID(),
		ExpectedQueueRevision: confirmation.Revision, ExpectedScopeGeneration: confirmation.ScopeGeneration,
		ExpectedPolicyGeneration: confirmation.PolicyGeneration,
	}
	model.queueClearConfirmation = nil
	model.closeDialog()
	return model.dispatchQueueMutation(command)
}

func (model Model) dispatchQueueMutation(command application.UICommand) (Model, tea.Cmd) {
	if model.pendingConversation != nil || command.Validate() != nil {
		model.showDialog("Queue change unavailable", "The queue change could not be submitted under the current revision and generation.")
		return model, nil
	}
	model.pendingConversation = &pendingConversationInput{
		RequestID: command.RequestID, Kind: command.Kind, ScopeGeneration: command.ExpectedScopeGeneration,
		PolicyGeneration: command.ExpectedPolicyGeneration,
	}
	model.composer.Reset()
	model.slashMenu.Close()
	return model, applicationCommand(command)
}

func (model Model) requestManualCompaction() (Model, tea.Cmd) {
	if model.pendingCompactionID != 0 {
		model.showDialog("Compaction busy", "Wait for the current manual compaction request to finish.")
		return model, nil
	}
	requestID := model.nextUIRequestID()
	command := application.UICommand{
		Kind: application.UICommandCompactContext, RequestID: requestID,
		ExpectedScopeGeneration: model.scope.Generation, ExpectedPolicyGeneration: model.permission.PolicyGeneration,
	}
	if command.Validate() != nil {
		model.showDialog("Compaction unavailable", "The compaction request could not be bound to the current scope and policy.")
		return model, nil
	}
	model.pendingCompactionID = requestID
	model.composer.Reset()
	model.slashMenu.Close()
	return model, applicationCommand(command)
}

func (model Model) executePlanCommand(argument string) (Model, tea.Cmd) {
	if model.pendingPlanID != 0 {
		model.showDialog("Plan mode busy", "Wait for the current plan-mode change to finish.")
		return model, nil
	}
	kind := application.UICommandArmPlan
	if argument == "off" {
		kind = application.UICommandCancelPlan
	} else if argument != "" {
		model.showDialog("Invalid plan command", "Use /plan or /plan off.")
		return model, nil
	}
	model.composer.Reset()
	model.slashMenu.Close()
	requestID := model.nextUIRequestID()
	model.pendingPlanID = requestID
	return model, applicationCommand(application.UICommand{Kind: kind, RequestID: requestID})
}
