package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const helpText = `/help                 Show commands and key bindings
/context [filter]     Select the Kubernetes Context
/namespace [filter]   Select the Kubernetes Namespace
/resource [filter]    Select or clear the target resource
/status               Show the current safe status
/new                  Start a new Session
/resume [filter]      Resume a local Session
/rename [title]       Rename the current Session
/privacy              Show model data-sharing information
/cancel               Cancel the active AgentRun
/quit                 Exit KuPilot

Enter sends. Ctrl+J inserts a newline. Tab completes a command.`

// Update reduces one message into pure UI state and deferred typed commands.
func (model Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		model.width = max(1, message.Width)
		model.height = max(1, message.Height)
		model.reflow()
		return model, nil
	case ApplicationEventMsg:
		cmd := model.acceptApplicationEvent(message.Event)
		model.reflow()
		if model.quitAfterCancel && !model.run.Active && model.run.Terminal {
			model.quitAfterCancel = false
			return model, quitCommand()
		}
		return model, cmd
	case ApprovalExpiryMsg:
		cmd := model.acceptApprovalExpiry(message)
		model.reflow()
		return model, cmd
	case CompletionResultMsg:
		model.acceptCompletionResult(message.Result)
		model.reflow()
		return model, nil
	case ResumeResultMsg:
		cmd := model.stageResumeResult(message.Result)
		model.reflow()
		return model, cmd
	case ScopeResultMsg:
		model.applyScopeResult(message.Result)
		model.reflow()
		return model, nil
	case ResourceSelectionResultMsg:
		model.acceptResourceSelectionResult(message.Result)
		model.reflow()
		return model, nil
	case CommandResultMsg:
		model.acceptCommandOutcome(message.Result)
		model.reflow()
		return model, nil
	case ApplicationFailureMsg:
		if !model.acceptApplicationFailure(message) {
			return model, nil
		}
		body := sanitizeExternalText(message.Message, 512)
		if body == "" {
			body = "The requested operation could not be completed safely."
		}
		model.showDialog("Operation unavailable", body)
		if model.resumeOrigin == resumeOriginTopLevel && !model.startup.Ready {
			model.startup.Failed = true
		}
		model.reflow()
		return model, nil
	case tea.BlurMsg:
		model.terminalFocused = false
		model.composer.Blur()
		return model, nil
	case tea.FocusMsg:
		model.terminalFocused = true
		if !model.dialog.Open() && !model.scopeConflict.Open() && !model.approvalDialog.Open() {
			_ = model.composer.Focus()
			model.focus = FocusComposer
		}
		return model, nil
	case tea.PasteMsg:
		if model.dialog.Open() || model.scopeConflict.Open() || model.approvalDialog.Open() || !model.terminalFocused {
			return model, nil
		}
		return model.updatePaste(message)
	case tea.KeyPressMsg:
		return model.updateKey(message)
	default:
		if model.dialog.Open() || model.scopeConflict.Open() || model.approvalDialog.Open() {
			return model, nil
		}
		updated, cmd, err := model.composer.Update(msg)
		if err == nil {
			model.composer = updated
		}
		return model, cmd
	}
}

func (model *Model) acceptApplicationFailure(message ApplicationFailureMsg) bool {
	if message.Command == application.UICommandApproveRestart || message.Command == application.UICommandRejectRestart ||
		message.Command == application.UICommandExpireRestart {
		if model.pendingApproval == nil || model.pendingApprovalID == 0 || message.RequestID != model.pendingApprovalID ||
			message.RunID != model.pendingApproval.RunID || message.ScopeGeneration != model.pendingApproval.Scope.Generation ||
			message.ApprovalID != model.pendingApproval.RequestID ||
			!message.ApprovalDigest.Equal(model.pendingApproval.Digest) ||
			message.ApprovalSequence != model.pendingApproval.Sequence {
			return false
		}
		model.clearApproval()
		return true
	}
	if message.Command == application.UICommandSubmitQuestion {
		if model.pendingSubmitID == 0 || message.RequestID != model.pendingSubmitID ||
			message.ScopeGeneration != model.scope.Generation {
			return false
		}
		model.pendingSubmitID = 0
		return true
	}
	switch message.Command {
	case application.UICommandShowPrivacy, application.UICommandAcceptPrivacy,
		application.UICommandRejectPrivacy, application.UICommandRevokePrivacy,
		application.UICommandToggleLogs, application.UICommandCancelPrivacy:
		if model.pendingPrivacyID == 0 || message.RequestID != model.pendingPrivacyID {
			return false
		}
		model.pendingPrivacyID = 0
		model.privacyReview = nil
		model.privacyPending = false
		return true
	}
	switch {
	case message.Query != "":
		if model.pendingCompletion.RequestID == 0 || message.RequestID != model.pendingCompletion.RequestID ||
			message.Query != model.pendingCompletion.Kind || message.ScopeGeneration != model.pendingCompletion.ScopeGeneration ||
			message.ScopeGeneration != model.scope.Generation || message.Query != model.activePicker {
			return false
		}
		model.pendingCompletion = application.UICompletionQuery{}
		model.setPickerFailed(message.Query)
	case message.Resume != "":
		if model.pendingResume.RequestID == 0 || message.RequestID != model.pendingResume.RequestID ||
			message.Resume != model.pendingResume.Mode {
			return false
		}
		model.pendingResume = application.UIResumeRequest{}
		model.pendingResumed = nil
		if model.resumeOrigin == resumeOriginInTUI {
			model.startup.Ready = true
		}
	case message.Command != "":
		switch message.Command {
		case application.UICommandAcceptResume, application.UICommandSelectContext,
			application.UICommandSelectNamespace, application.UICommandActivateScope:
			if model.pendingScopeID == 0 || message.RequestID != model.pendingScopeID ||
				message.ScopeGeneration != model.scope.Generation {
				return false
			}
			model.pendingScopeID = 0
			model.scope.Switching = false
			if message.Command == application.UICommandAcceptResume {
				model.pendingResumed = nil
				if model.resumeOrigin == resumeOriginInTUI {
					model.startup.Ready = true
				}
			}
		case application.UICommandSelectResource:
			if model.pendingResourceID == 0 || message.RequestID != model.pendingResourceID ||
				message.ScopeGeneration != model.scope.Generation {
				return false
			}
			model.pendingResourceID = 0
			model.pendingResource = ResourceView{}
		case application.UICommandCancelRun:
			if !model.run.Active || message.RunID != model.run.RunID || message.ScopeGeneration != model.run.ScopeGeneration {
				return false
			}
		case application.UICommandNewSession, application.UICommandRenameSession, application.UICommandShowStatus:
		case application.UICommandCancelResume, application.UICommandResumeSession:
			return false
		default:
			return false
		}
	default:
		return false
	}
	return true
}

func (model Model) updatePaste(message tea.PasteMsg) (tea.Model, tea.Cmd) {
	message.Content = sanitizeExternalText(message.Content, 0)
	updated, cmd, err := model.composer.Update(message)
	if err != nil {
		model.showDialog("Input limit", "The draft is too long. The maximum is 65536 bytes.")
		return model, nil
	}
	model.composer = updated
	queryCmd := model.syncSuggestionsAfterEdit()
	model.reflow()
	return model, combineCommands(cmd, queryCmd)
}

func (model Model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	message.Text = sanitizeExternalText(message.Text, 0)
	if !model.terminalFocused {
		return model, nil
	}
	if model.approvalDialog.Open() {
		return model.updateApprovalDialogKey(message)
	}
	if key.Matches(message, model.keymap.Quit) {
		if model.run.Active {
			model.quitAfterCancel = true
			return model, model.cancelRunCommand()
		}
		return model, quitCommand()
	}
	if model.scopeConflict.Open() {
		return model.updateScopeConflictKey(message)
	}
	if model.dialog.Open() {
		if model.privacyReview != nil {
			return model.updatePrivacyDialogKey(message)
		}
		if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
			quitFailedResume := model.resumeOrigin == resumeOriginTopLevel && model.startup.Failed && !model.startup.Ready
			model.closeDialog()
			if quitFailedResume {
				return model, quitCommand()
			}
			if model.resumeOrigin == resumeOriginInTUI && model.startup.Ready {
				model.resumeOrigin = resumeOriginNone
			}
		}
		return model, nil
	}

	if key.Matches(message, model.keymap.Close) {
		if model.pickerOpen() {
			return model.cancelPicker()
		}
		if model.slashMenu.Open() {
			model.slashMenu.Close()
			model.reflow()
		}
		return model, nil
	}
	if key.Matches(message, model.keymap.Cancel) {
		if model.run.Active {
			return model, model.cancelRunCommand()
		}
		return model, nil
	}
	if model.pickerOpen() {
		switch {
		case key.Matches(message, model.keymap.Reverse), key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
			model.movePicker(-1)
			return model, nil
		case key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
			model.movePicker(1)
			return model, nil
		case key.Matches(message, model.keymap.Complete):
			model.completePickerSelection()
			model.reflow()
			return model, nil
		case key.Matches(message, model.keymap.Submit):
			cmd := model.selectPickerCandidate()
			model.reflow()
			return model, cmd
		}
	}
	if model.slashMenu.Open() {
		switch {
		case key.Matches(message, model.keymap.Reverse), key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
			model.slashMenu.Move(-1)
			return model, nil
		case key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
			model.slashMenu.Move(1)
			return model, nil
		case key.Matches(message, model.keymap.Complete):
			model.completeSlashSelection()
			queryCmd := model.syncSuggestionsAfterEdit()
			model.reflow()
			return model, queryCmd
		}
	}
	if key.Matches(message, model.keymap.Complete) {
		return model, nil
	}
	if key.Matches(message, model.keymap.Submit) {
		return model.submitDraft()
	}
	if key.Matches(message, model.keymap.TranscriptUp) {
		model.transcript.PageUp()
		return model, nil
	}
	if key.Matches(message, model.keymap.TranscriptDown) {
		model.transcript.PageDown()
		return model, nil
	}
	if key.Matches(message, model.keymap.Previous) && model.composer.HistoryEligible() {
		model.composer.PreviousHistory()
		queryCmd := model.syncSuggestionsAfterEdit()
		model.reflow()
		return model, queryCmd
	}
	if key.Matches(message, model.keymap.Next) && model.composer.NextHistory() {
		queryCmd := model.syncSuggestionsAfterEdit()
		model.reflow()
		return model, queryCmd
	}

	updated, cmd, err := model.composer.Update(message)
	if err != nil {
		model.showDialog("Input limit", "The draft is too long. The maximum is 65536 bytes.")
		return model, nil
	}
	model.composer = updated
	queryCmd := model.syncSuggestionsAfterEdit()
	model.reflow()
	return model, combineCommands(cmd, queryCmd)
}

func (model Model) updateApprovalDialogKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.approvalDialog.Terminal() &&
		(key.Matches(message, model.keymap.Submit) || key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Quit)) {
		model.approvalDialog.Close()
		if model.terminalFocused && !model.dialog.Open() && !model.scopeConflict.Open() {
			_ = model.composer.Focus()
			model.focus = FocusComposer
		}
		return model, nil
	}
	switch {
	case key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.Next),
		key.Matches(message, model.keymap.PreviousAlt), key.Matches(message, model.keymap.NextAlt),
		key.Matches(message, model.keymap.Complete), key.Matches(message, model.keymap.Reverse):
		model.approvalDialog.Move(1)
		return model, nil
	case key.Matches(message, model.keymap.Submit):
		return model.submitApprovalDecision(false)
	case key.Matches(message, model.keymap.Close), key.Matches(message, model.keymap.Quit):
		return model.submitApprovalDecision(true)
	default:
		return model, nil
	}
}

func (model Model) submitApprovalDecision(forceReject bool) (tea.Model, tea.Cmd) {
	if model.pendingApproval == nil || model.pendingApprovalID != 0 || !model.approvalDialog.MarkSubmitted() {
		return model, nil
	}
	request := *model.pendingApproval
	kind := application.UICommandRejectRestart
	if !forceReject && model.approvalDialog.ApproveSelected() {
		kind = application.UICommandApproveRestart
	}
	requestID := model.nextUIRequestID()
	command := application.UICommand{
		Kind: kind, RequestID: requestID, RunID: request.RunID,
		ExpectedScopeGeneration: request.Scope.Generation,
		ApprovalID:              request.RequestID, ApprovalDigest: request.Digest,
		ApprovalNonce: request.Nonce, ApprovalSequence: request.Sequence,
	}
	if command.Validate() != nil {
		model.clearApproval()
		model.showDialog("Approval unavailable", "The approval decision could not be submitted safely.")
		return model, nil
	}
	model.pendingApprovalID = requestID
	return model, applicationCommand(command)
}

func (model Model) submitDraft() (tea.Model, tea.Cmd) {
	draft := model.composer.Value()
	historyDraft := draft
	if strings.TrimSpace(draft) == "" {
		model.showDialog("Nothing to send", "Enter a diagnostic question or a fixed Slash command.")
		return model, nil
	}
	if strings.HasPrefix(draft, "!") {
		model.showDialog("Command unavailable", "Shell and bang commands are not supported.")
		return model, nil
	}

	parsed := ParseSlashDraft(draft)
	switch parsed.Mode {
	case DraftUnknownSlash:
		if model.slashMenu.Open() {
			if _, ok := model.slashMenu.SelectedCandidate(); ok {
				model.completeSlashSelection()
				queryCmd := model.syncSuggestionsAfterEdit()
				model.reflow()
				return model, queryCmd
			}
		}
		model.showDialog("Unknown command", "Unknown Slash command. Type /help for available commands.")
		return model, nil
	case DraftSlash:
		if parsed.Command == nil {
			model.completeSlashSelection()
			queryCmd := model.syncSuggestionsAfterEdit()
			model.reflow()
			return model, queryCmd
		}
		return model.executeSlash(*parsed.Command, parsed.Argument)
	case DraftEscapedChat:
		draft = strings.TrimPrefix(draft, "/")
	}

	if model.run.Active {
		model.showDialog("Run active", "Cancel the active AgentRun before sending another question.")
		return model, nil
	}
	if model.scope.Switching {
		model.showDialog("Scope switching", "Wait for scope activation to finish before sending a question.")
		return model, nil
	}
	if model.scope.Generation < 1 || !model.scope.ReadOnly {
		model.showDialog("Scope required", "Select and verify one Context and Namespace before sending a question.")
		return model, nil
	}
	if !model.startup.Ready {
		model.showDialog("Session loading", "Finish or cancel the explicit Session resume before sending a question.")
		return model, nil
	}
	command := application.UICommand{
		Kind:                    application.UICommandSubmitQuestion,
		RequestID:               model.nextUIRequestID(),
		Text:                    draft,
		ExpectedScopeGeneration: model.scope.Generation,
	}
	if command.Validate() != nil {
		model.showDialog("Cannot send", "The question could not be submitted safely.")
		return model, nil
	}
	model.transcript.AppendUser(draft)
	model.composer.RecordSubmission(historyDraft)
	model.composer.Reset()
	model.slashMenu.Close()
	model.pendingSubmitID = command.RequestID
	model.reflow()
	return model, applicationCommand(command)
}

func (model Model) executeSlash(command SlashCommand, argument string) (tea.Model, tea.Cmd) {
	if argument != "" && !command.AcceptsArgument {
		model.showDialog("Invalid command", "This Slash command does not accept an argument.")
		return model, nil
	}
	if enabled, reason := model.slashAvailability(command); !enabled {
		model.showDialog("Command unavailable", reason)
		return model, nil
	}
	if command.Name == "resource" && argument == "clear" {
		requestID := model.nextUIRequestID()
		intent := application.UICommand{
			Kind: application.UICommandSelectResource, RequestID: requestID,
			Text: "clear", ExpectedScopeGeneration: model.scope.Generation,
		}
		if intent.Validate() != nil {
			model.showDialog("Cannot continue", "The Resource selection could not be cleared safely.")
			return model, nil
		}
		model.pendingResourceID = requestID
		model.pendingResource = ResourceView{}
		model.composer.Reset()
		model.slashMenu.Close()
		return model, applicationCommand(intent)
	}
	if kind := completionKindForCommand(command.Name); kind != "" {
		prefix := "/" + command.Name + " "
		model.composer.SetValue(prefix + argument)
		origin := resumeOriginNone
		if kind == application.UICompletionSession {
			origin = resumeOriginInTUI
		}
		queryCmd := model.openCompletion(kind, argument, origin)
		return model, queryCmd
	}

	switch command.action {
	case slashHelp:
		model.composer.Reset()
		model.slashMenu.Close()
		model.showDialog("Help", helpText)
		return model, nil
	case slashStatus:
		model.composer.Reset()
		model.slashMenu.Close()
		model.reflow()
		return model, applicationCommand(application.UICommand{Kind: application.UICommandShowStatus})
	case slashQuit:
		model.composer.Reset()
		model.slashMenu.Close()
		return model, quitCommand()
	case slashApplication:
		intent := application.UICommand{
			Kind:                    command.commandKind,
			Text:                    argument,
			ExpectedScopeGeneration: model.scope.Generation,
		}
		if command.commandKind == application.UICommandCancelRun {
			intent.RunID = model.run.RunID
			intent.Text = ""
			intent.ExpectedScopeGeneration = model.run.ScopeGeneration
		}
		if command.commandKind == application.UICommandShowPrivacy {
			intent.RequestID = model.nextUIRequestID()
			model.pendingPrivacyID = intent.RequestID
		}
		if command.commandKind == application.UICommandNewSession || command.commandKind == application.UICommandRenameSession ||
			command.commandKind == application.UICommandShowPrivacy {
			intent.ExpectedScopeGeneration = 0
		}
		if intent.Validate() != nil {
			model.showDialog("Cannot continue", "The command could not be dispatched safely.")
			return model, nil
		}
		model.composer.Reset()
		model.slashMenu.Close()
		model.reflow()
		return model, applicationCommand(intent)
	default:
		model.showDialog("Command unavailable", "The command is not available.")
		return model, nil
	}
}

func (model Model) slashAvailability(command SlashCommand) (bool, string) {
	if model.scope.Switching {
		switch command.Name {
		case "context", "namespace", "resource":
			return false, "Wait for the current scope activation to finish."
		}
	}
	switch command.Name {
	case "cancel":
		if !model.run.Active {
			return false, "There is no active AgentRun to cancel."
		}
	case "new", "resume", "quit":
		if model.run.Active {
			return false, "Cancel the active AgentRun first."
		}
	}
	return true, ""
}

func (model *Model) syncSlashMenu() {
	draft := model.composer.Value()
	parsed := ParseSlashDraft(draft)
	if parsed.Mode != DraftSlash && parsed.Mode != DraftUnknownSlash || strings.ContainsRune(draft, '\n') || strings.Contains(draft, " ") {
		model.slashMenu.Close()
		return
	}
	query := strings.TrimPrefix(draft, "/")
	commands := FilterSlashCommands(query)
	candidates := make([]components.SlashCandidate, 0, len(commands))
	for _, command := range commands {
		enabled, reason := model.slashAvailability(command)
		candidates = append(candidates, components.SlashCandidate{
			Name: command.Name, Usage: command.Usage, Summary: command.Summary,
			Enabled: enabled, DisabledReason: reason,
		})
	}
	model.slashMenu.SetCandidates(candidates)
}

func (model *Model) completeSlashSelection() {
	candidate, ok := model.slashMenu.SelectedCandidate()
	if !ok {
		return
	}
	command, ok := findSlashCommand(candidate.Name)
	if !ok {
		return
	}
	value := "/" + command.Name
	if command.AcceptsArgument {
		value += " "
	}
	model.composer.SetValue(value)
}

func (model *Model) showDialog(title, body string) {
	model.dialog.Show(title, body)
	model.composer.Blur()
	model.focus = FocusModal
}

func (model *Model) closeDialog() {
	model.dialog.Close()
	if model.terminalFocused && !model.scopeConflict.Open() && !model.approvalDialog.Open() {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
}

func quitCommand() tea.Cmd {
	return func() tea.Msg { return tea.Quit() }
}

func combineCommands(commands ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(commands))
	for _, command := range commands {
		if command != nil {
			filtered = append(filtered, command)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Batch(filtered...)
	}
}

func (model Model) cancelRunCommand() tea.Cmd {
	command := application.UICommand{
		Kind: application.UICommandCancelRun, RunID: model.run.RunID,
		ExpectedScopeGeneration: model.run.ScopeGeneration,
	}
	if command.Validate() != nil {
		return nil
	}
	return applicationCommand(command)
}

func (model Model) cancelPicker() (tea.Model, tea.Cmd) {
	isSession := model.activePicker == application.UICompletionSession
	topLevel := isSession && model.resumeOrigin == resumeOriginTopLevel && !model.startup.Ready
	model.closePickers()
	if isSession {
		model.composer.Reset()
		model.resumeOrigin = resumeOriginNone
		if !topLevel {
			model.startup.Ready = true
		}
	}
	model.reflow()
	if topLevel {
		return model, quitCommand()
	}
	return model, nil
}

func (model Model) updateScopeConflictKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(message, model.keymap.Close):
		topLevel := model.resumeOrigin == resumeOriginTopLevel
		requestID := uint64(0)
		if model.pendingResumed != nil {
			requestID = model.pendingResumed.ResumeRequestID
		}
		model.scopeConflict.Close()
		model.pendingResumed = nil
		model.composer.Reset()
		model.resumeOrigin = resumeOriginNone
		if model.terminalFocused {
			_ = model.composer.Focus()
		}
		model.focus = FocusComposer
		if topLevel {
			return model, quitCommand()
		}
		model.startup.Ready = true
		if requestID == 0 {
			return model, nil
		}
		return model, applicationCommand(application.UICommand{Kind: application.UICommandCancelResume, RequestID: requestID})
	case key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.Next),
		key.Matches(message, model.keymap.PreviousAlt), key.Matches(message, model.keymap.NextAlt),
		key.Matches(message, model.keymap.Complete), key.Matches(message, model.keymap.Reverse):
		model.scopeConflict.Move(1)
		return model, nil
	case key.Matches(message, model.keymap.Submit):
		if model.pendingResumed == nil {
			return model, nil
		}
		resumed := *model.pendingResumed
		topLevel := model.resumeOrigin == resumeOriginTopLevel
		useSaved := model.scopeConflict.UseSavedScope()
		requestID := resumed.ResumeRequestID
		command := application.UICommand{
			Kind: application.UICommandAcceptResume, RequestID: requestID,
			ExpectedScopeGeneration: model.scope.Generation,
		}
		if useSaved && resumed.SavedScope != nil {
			scope := *resumed.SavedScope
			command.Scope = &scope
		} else if topLevel && !model.scope.ReadOnly && model.scope.Context != "" && model.scope.Namespace != "" {
			scope := domain.ScopeCandidate{Context: model.scope.Context, Namespace: model.scope.Namespace}
			command.Scope = &scope
		}
		if command.Validate() != nil {
			model.showDialog("Resume unavailable", "The Session choice could not be accepted safely.")
			return model, nil
		}
		model.pendingScopeID = requestID
		model.scope.Switching = true
		model.scopeConflict.Close()
		return model, applicationCommand(command)
	default:
		return model, nil
	}
}

func (model *Model) stageResumeResult(result application.UIResumeResult) tea.Cmd {
	if result.Validate() != nil || model.pendingResume.RequestID == 0 ||
		result.RequestID != model.pendingResume.RequestID || result.Mode != model.pendingResume.Mode ||
		result.Mode == application.UIResumeExact && result.Session != nil && result.Session.Session.ID != model.pendingResume.SessionID {
		return nil
	}
	model.pendingResume = application.UIResumeRequest{}
	if result.Failure != "" {
		topLevel := model.resumeOrigin == resumeOriginTopLevel
		model.startup.Failed = topLevel
		if !topLevel {
			model.startup.Ready = true
		}
		model.showDialog("Resume unavailable", resumeUIFailureText(result.Failure))
		return nil
	}
	resumed := sanitizedResumedSession(*result.Session)
	model.pendingResumed = &resumed
	explicitTopLevelScope := model.resumeOrigin == resumeOriginTopLevel && model.startup.Intent.ExplicitScope
	if !explicitTopLevelScope && resumed.SavedScope != nil &&
		(resumed.SavedScope.Context != model.scope.Context || resumed.SavedScope.Namespace != model.scope.Namespace) {
		model.scopeConflict.Show(scopeLabel(model.scope.Context, model.scope.Namespace), scopeLabel(resumed.SavedScope.Context, resumed.SavedScope.Namespace))
		model.composer.Blur()
		model.focus = FocusModal
		return nil
	}
	command := application.UICommand{
		Kind: application.UICommandAcceptResume, RequestID: resumed.ResumeRequestID,
		ExpectedScopeGeneration: model.scope.Generation,
	}
	if resumed.SavedScope != nil && !explicitTopLevelScope {
		scope := *resumed.SavedScope
		command.Scope = &scope
	}
	if command.Validate() != nil {
		model.showDialog("Resume unavailable", "The Session choice could not be accepted safely.")
		return nil
	}
	model.pendingScopeID = command.RequestID
	model.scope.Switching = true
	return applicationCommand(command)
}

func (model *Model) acceptCommandOutcome(result application.UICommandOutcome) {
	if result.Validate() != nil {
		return
	}
	switch result.Command {
	case application.UICommandAcceptResume:
		if model.pendingResumed == nil || model.pendingResumed.ResumeRequestID != result.RequestID ||
			model.pendingScopeID != result.RequestID {
			return
		}
		if result.Failure != "" {
			model.pendingScopeID = 0
			model.scope.Switching = false
			model.pendingResumed = nil
			model.showDialog("Resume unavailable", resumeUIFailureText(result.Failure))
			return
		}
		model.applyAcceptedResume(*result.Resumed)
		model.applyScopeResult(*result.Scope)
		if result.Scope.Failure == "" && result.Resource != nil {
			model.pendingResourceID = result.Resource.RequestID
			model.acceptResourceSelectionResult(*result.Resource)
		}
	case application.UICommandSelectContext, application.UICommandSelectNamespace, application.UICommandActivateScope:
		model.applyScopeResult(*result.Scope)
	case application.UICommandSelectResource:
		model.acceptResourceSelectionResult(*result.Resource)
	case application.UICommandNewSession:
		model.session = SessionView{ID: result.Session.ID, Title: result.Session.Title}
		model.resource = ResourceView{}
		model.pendingResumed = nil
		model.resumeOrigin = resumeOriginNone
		model.startup.Ready = true
		model.resetTranscript()
		model.transcript.AppendNotice("A new Session was started. The verified scope remains active; no model request was sent.")
	case application.UICommandRenameSession:
		if result.Failure != "" {
			model.showDialog("Rename unavailable", "The current Session title could not be updated safely.")
			return
		}
		model.session = SessionView{ID: result.Session.ID, Title: result.Session.Title, Resumed: result.Session.Resumed}
		model.transcript.AppendNotice("The current Session title was updated.")
	case application.UICommandShowStatus:
		model.transcript.AppendNotice(statusText(*result.Status))
	case application.UICommandSubmitQuestion:
		if model.pendingSubmitID == 0 || result.RequestID != model.pendingSubmitID {
			return
		}
		model.pendingSubmitID = 0
		if result.Failure == application.UIQueryConsentRequired && result.Privacy != nil {
			model.pendingPrivacyID = result.RequestID
			model.showPrivacyReview(*result.Privacy)
			return
		}
		if result.Failure != "" {
			model.showDialog("Model transfer unavailable", "The question could not start under the current safe state.")
		}
	case application.UICommandShowPrivacy, application.UICommandToggleLogs:
		if model.pendingPrivacyID == 0 || result.RequestID != model.pendingPrivacyID || result.Privacy == nil {
			return
		}
		model.privacyPending = false
		model.showPrivacyReview(*result.Privacy)
	case application.UICommandAcceptPrivacy:
		if !model.finishPrivacyAction(result.RequestID) {
			return
		}
		model.transcript.AppendNotice("Model data-sharing consent was accepted for the displayed destination and categories.")
	case application.UICommandRejectPrivacy:
		if !model.finishPrivacyAction(result.RequestID) {
			return
		}
		model.transcript.AppendNotice("Model data sharing remains blocked.")
	case application.UICommandRevokePrivacy:
		if !model.finishPrivacyAction(result.RequestID) {
			return
		}
		model.transcript.AppendNotice("Model data-sharing consent was revoked; any active AgentRun was cancelled.")
	case application.UICommandCancelPrivacy:
		model.finishPrivacyAction(result.RequestID)
	case application.UICommandApproveRestart, application.UICommandRejectRestart, application.UICommandExpireRestart:
		if model.pendingApproval == nil || model.pendingApprovalID == 0 || result.RequestID != model.pendingApprovalID ||
			result.Approval.RequestID != model.pendingApproval.RequestID ||
			result.Approval.RunID != model.pendingApproval.RunID ||
			result.Approval.ScopeGeneration != model.pendingApproval.Scope.Generation ||
			result.Approval.Sequence != model.pendingApproval.Sequence ||
			!result.Approval.Digest.Equal(model.pendingApproval.Digest) {
			return
		}
		state := result.Approval.State
		switch state {
		case domain.ApprovalStateApproved:
			model.pendingApprovalID = 0
			model.approvalState = domain.ApprovalStateApproved
			model.approvalDialog.Close()
			if model.terminalFocused && !model.dialog.Open() && !model.scopeConflict.Open() {
				_ = model.composer.Focus()
				model.focus = FocusComposer
			}
			model.transcript.AppendNotice("The restart request was approved but not executed.")
		case domain.ApprovalStateRejected:
			model.clearApproval()
			model.transcript.AppendNotice("The restart request was rejected. No operation was executed.")
		case domain.ApprovalStateExpired:
			model.clearApproval()
			model.transcript.AppendNotice("The restart approval expired. No operation was executed.")
		case domain.ApprovalStateConsumed:
			status := restartExecutionStatus(*result.Approval.Execution)
			if model.approvalDialog.Open() {
				model.approvalDialog.SetStatus(status, true)
			}
			model.pendingApproval = nil
			model.pendingApprovalID = 0
			model.approvalState = ""
			model.transcript.AppendNotice(status)
		default:
			model.clearApproval()
			model.transcript.AppendNotice("The restart approval was invalidated. No operation was executed.")
		}
	case application.UICommandResumeSession:
		model.showDialog("Command unavailable", "The command is not available in the current flow.")
	}
}

func (model *Model) acceptApprovalExpiry(message ApprovalExpiryMsg) tea.Cmd {
	if model.pendingApproval == nil || model.pendingApprovalID != 0 ||
		message.RequestID != model.pendingApproval.RequestID || message.RunID != model.pendingApproval.RunID ||
		message.ScopeGeneration != model.pendingApproval.Scope.Generation || message.Sequence != model.pendingApproval.Sequence ||
		!message.Digest.Equal(model.pendingApproval.Digest) {
		return nil
	}
	now := model.now().UTC().Truncate(time.Millisecond)
	if now.IsZero() || now.UnixMilli() < 0 || now.Before(model.pendingApproval.ExpiresAt) {
		return nil
	}
	return model.expirePendingApproval()
}

func (model *Model) expirePendingApproval() tea.Cmd {
	if model.pendingApproval == nil || model.pendingApprovalID != 0 {
		return nil
	}
	request := *model.pendingApproval
	requestID := model.nextUIRequestID()
	command := application.UICommand{
		Kind: application.UICommandExpireRestart, RequestID: requestID, RunID: request.RunID,
		ExpectedScopeGeneration: request.Scope.Generation,
		ApprovalID:              request.RequestID, ApprovalDigest: request.Digest,
		ApprovalNonce: request.Nonce, ApprovalSequence: request.Sequence,
	}
	if command.Validate() != nil {
		model.clearApproval()
		return nil
	}
	model.pendingApprovalID = requestID
	model.approvalDialog.Close()
	return applicationCommand(command)
}

func (model *Model) clearApproval() {
	model.approvalDialog.Close()
	model.pendingApproval = nil
	model.pendingApprovalID = 0
	model.approvalState = ""
	if model.terminalFocused && !model.dialog.Open() && !model.scopeConflict.Open() {
		_ = model.composer.Focus()
		model.focus = FocusComposer
	}
}

func (model *Model) showPrivacyReview(review application.PrivacyReview) {
	if review.Validate() != nil {
		model.showDialog("Privacy unavailable", "Model data-sharing information could not be displayed safely.")
		return
	}
	copy := review
	copy.Categories = append([]application.PrivacyCategoryReview(nil), review.Categories...)
	copy.NeverEligible = append([]string(nil), review.NeverEligible...)
	model.privacyReview = &copy
	model.privacyPending = false
	model.showDialog("Model data sharing", privacyReviewText(copy))
}

func (model *Model) finishPrivacyAction(requestID uint64) bool {
	if model.pendingPrivacyID == 0 || requestID != model.pendingPrivacyID {
		return false
	}
	model.pendingPrivacyID = 0
	model.privacyReview = nil
	model.privacyPending = false
	model.closeDialog()
	return true
}

func privacyReviewText(review application.PrivacyReview) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Destination: %s\nConsent policy: %s\nDecision: %s\n\nEligible data categories:\n",
		sanitizeExternalText(review.Origin, 2048), review.PolicyVersion, review.Decision)
	for _, category := range review.Categories {
		state := "disabled"
		if category.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(&builder, "- [%s] %s: %s\n", state, category.ID, category.Description)
	}
	builder.WriteString("\nNever eligible:\n")
	for _, value := range review.NeverEligible {
		fmt.Fprintf(&builder, "- %s\n", value)
	}
	builder.WriteString("\nA accept | L toggle container output | R reject/revoke | Esc cancel")
	return builder.String()
}

func (model Model) updatePrivacyDialogKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.privacyPending || model.privacyReview == nil || model.pendingPrivacyID == 0 {
		return model, nil
	}
	review := *model.privacyReview
	command := application.UICommand{
		RequestID: model.pendingPrivacyID, PrivacyRevision: review.Revision,
	}
	switch {
	case key.Matches(message, model.keymap.Close), key.Matches(message, model.keymap.Submit):
		command.Kind = application.UICommandCancelPrivacy
		model.dialog.Close()
		model.privacyReview = nil
	case message.Code == 'a' || message.Code == 'A':
		command.Kind = application.UICommandAcceptPrivacy
		model.privacyPending = true
	case message.Code == 'l' || message.Code == 'L':
		command.Kind = application.UICommandToggleLogs
		enabled := !review.LogsEnabled
		command.LogsEnabled = &enabled
		model.privacyPending = true
	case message.Code == 'r' || message.Code == 'R':
		command.Kind = application.UICommandRejectPrivacy
		if review.Decision == application.PrivacyDecisionAccepted {
			command.Kind = application.UICommandRevokePrivacy
		}
		model.privacyPending = true
	default:
		return model, nil
	}
	if command.Validate() != nil {
		model.privacyPending = false
		return model, nil
	}
	return model, applicationCommand(command)
}

func resumeUIFailureText(code application.UIQueryFailureCode) string {
	if code == application.UIQueryNotResumable {
		return "This Session uses minimal persistence and cannot be resumed."
	}
	return resumeFailureText(code)
}

func (model *Model) applyAcceptedResume(resumed application.UIResumedSession) {
	model.session = SessionView{ID: resumed.Session.ID, Title: resumed.Session.Title, Resumed: true}
	model.startup.Ready = true
	model.startup.Failed = false
	model.resource = ResourceView{}
	model.pendingResourceID = 0
	model.pendingResource = ResourceView{}
	model.pendingResumed = nil
	model.resumeOrigin = resumeOriginNone
	model.closePickers()
	model.composer.Reset()
	model.scopeConflict.Close()
	model.resetTranscript()
	for _, message := range resumed.History {
		text := sanitizeExternalText(message.Content, application.MaxQuestionBytes)
		if text == "" {
			continue
		}
		switch message.Role {
		case domain.MessageRoleUser:
			model.transcript.AppendUser(text)
		case domain.MessageRoleAssistant:
			model.transcript.StartAgent()
			model.transcript.FinishAgent(text)
		default:
			model.transcript.AppendNotice(text)
		}
	}
	model.transcript.AppendNotice("Session resumed. Historic Evidence is display-only and cannot support facts in a new AgentRun.")
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
}

func (model *Model) resetTranscript() {
	model.transcript = components.NewTranscript(model.styles.transcript, model.styles.toolSteps)
}

func (model *Model) applyScopeResult(result application.UIScopeResult) {
	if result.Validate() != nil || model.pendingScopeID == 0 || result.RequestID != model.pendingScopeID ||
		result.ExpectedGeneration != model.scope.Generation {
		return
	}
	model.pendingScopeID = 0
	model.scope.Switching = false
	changed := result.ScopeGeneration > result.ExpectedGeneration
	if result.Failure != "" {
		if changed {
			model.clearApproval()
			model.finishRunForScopeChange()
			model.resource = ResourceView{}
			model.pendingResourceID = 0
			model.pendingResource = ResourceView{}
			model.closePickers()
			model.scope = ScopeView{Generation: result.ScopeGeneration}
		}
		model.showDialog("Scope unavailable", scopeFailureText(result.Failure))
		return
	}
	if changed {
		model.clearApproval()
		model.finishRunForScopeChange()
		model.resource = ResourceView{}
		model.pendingResourceID = 0
		model.pendingResource = ResourceView{}
		model.closePickers()
	}
	model.scope = ScopeView{
		Context: sanitizeExternalText(result.Context, 253), Namespace: sanitizeExternalText(result.Namespace, 63),
		Generation: result.ScopeGeneration, ReadOnly: result.ReadOnly,
	}
	if changed {
		model.transcript.AppendNotice("Scope changed. The selected Resource and old-generation Picker results were cleared.")
	}
}

func (model *Model) finishRunForScopeChange() {
	if !model.run.Active {
		return
	}
	const cancellation = "The AgentRun was cancelled because the scope changed."
	model.run.Active = false
	model.run.Terminal = true
	model.run.Status = "cancelled"
	model.run.StreamedText = cancellation
	model.transcript.FinishAgent(cancellation)
}

func statusText(status application.UIStatusResult) string {
	contextName := status.Context
	if contextName == "" {
		contextName = "unavailable"
	}
	namespace := status.Namespace
	if namespace == "" {
		namespace = "unavailable"
	}
	access := "scope unverified"
	if status.ReadOnly {
		access = "read-only"
	}
	run := "idle"
	if status.RunActive {
		run = "run active"
	}
	return fmt.Sprintf("Context: %s · Namespace: %s · %s · %s", contextName, namespace, access, run)
}

func (model *Model) acceptApplicationEvent(event application.UIEvent) tea.Cmd {
	if event.Validate() != nil || model.scope.Switching || event.ScopeGeneration != model.scope.Generation {
		return nil
	}
	if event.Kind == application.UIEventRestartExecution {
		if model.pendingApproval == nil || event.RestartExecution == nil ||
			event.RestartExecution.RequestID != model.pendingApproval.RequestID ||
			event.RunID != model.pendingApproval.RunID || event.Sequence != model.pendingApproval.Sequence ||
			!event.RestartExecution.Digest.Equal(model.pendingApproval.Digest) ||
			event.RestartExecution.EventIndex != model.approvalDialog.ExecutionIndex()+1 ||
			!model.approvalDialog.Open() || !model.approvalDialog.Submitted() {
			return nil
		}
		model.approvalDialog.SetExecutionStatus(
			event.RestartExecution.EventIndex, restartExecutionStatus(*event.RestartExecution),
			event.RestartExecution.State.Terminal(),
		)
		return nil
	}
	if event.Kind == application.UIEventApprovalClosed {
		if model.pendingApproval == nil || event.ApprovalResult.RequestID != model.pendingApproval.RequestID ||
			event.RunID != model.pendingApproval.RunID || event.Sequence != model.pendingApproval.Sequence ||
			!event.ApprovalResult.Digest.Equal(model.pendingApproval.Digest) {
			return nil
		}
		state := event.ApprovalResult.State
		model.clearApproval()
		if state == domain.ApprovalStateExpired {
			model.transcript.AppendNotice("The restart approval expired. No operation was executed.")
		} else {
			model.transcript.AppendNotice("The restart approval was cancelled or invalidated. No operation was executed.")
		}
		return nil
	}
	if event.Kind == application.UIEventRunStarted {
		if event.Sequence != 1 || model.run.Active || !model.startup.Ready || !model.scope.ReadOnly {
			return nil
		}
		model.run = RunView{
			RunID: event.RunID, ScopeGeneration: event.ScopeGeneration,
			LastSequence: event.Sequence, Active: true, Status: "active",
		}
		model.transcript.StartAgent()
		return nil
	}
	if !model.run.Active || model.run.Terminal || event.RunID != model.run.RunID ||
		event.ScopeGeneration != model.run.ScopeGeneration || event.Sequence != model.run.LastSequence+1 {
		return nil
	}
	if event.Kind == application.UIEventApprovalRequested {
		if model.pendingApproval != nil || model.approvalDialog.Open() {
			return nil
		}
		request := *event.Approval
		content := components.ApprovalDialogContent{
			Operation: string(request.Operation),
			Scope: fmt.Sprintf("%s / %s / generation %d",
				sanitizeExternalText(request.Scope.Context, 253),
				sanitizeExternalText(request.Scope.Namespace, 63), request.Scope.Generation),
			Resource: fmt.Sprintf("%s %s %s/%s (UID %s)",
				sanitizeExternalText(request.Target.APIVersion, 253),
				sanitizeExternalText(request.Target.Kind, 63),
				sanitizeExternalText(request.Target.Namespace, 63),
				sanitizeExternalText(request.Target.Name, 253),
				sanitizeExternalText(request.Target.UID, 1024)),
			Current:  sanitizeExternalText(request.CurrentSummary, 4096),
			Proposed: sanitizeExternalText(request.ProposedSummary, 4096),
			Reason:   sanitizeExternalText(request.ReasonSummary, domain.MaxApprovalReasonSummaryBytes),
			Risk:     sanitizeExternalText(request.RiskSummary, 4096),
			Digest:   string(request.Digest), ExpiresAt: request.ExpiresAt,
		}
		now := model.now().UTC().Truncate(time.Millisecond)
		if now.IsZero() || now.UnixMilli() < 0 {
			return nil
		}
		model.pendingApproval = &request
		model.pendingApprovalID = 0
		model.approvalState = domain.ApprovalStatePending
		model.run.LastSequence = event.Sequence
		if !now.Before(request.ExpiresAt) {
			return model.expirePendingApproval()
		}
		model.approvalDialog.Show(content, now)
		if !model.approvalDialog.Open() {
			model.clearApproval()
			return nil
		}
		model.composer.Blur()
		model.focus = FocusModal
		return approvalExpiry(request, now)
	}

	switch event.Kind {
	case application.UIEventTextDelta:
		remaining := application.MaxQuestionBytes - len(model.run.StreamedText)
		if remaining < 0 {
			remaining = 0
		}
		text := ""
		if remaining > 0 {
			text = sanitizeExternalText(event.Text, remaining)
		}
		model.run.StreamedText += text
		model.transcript.AppendAgent(text)
	case application.UIEventPersistenceDegraded:
		text := sanitizeExternalText(event.Text, application.MaxQuestionBytes)
		if text == "" {
			text = "Local persistence is degraded; this run may not be resumable."
		}
		model.run.PersistenceDegraded = true
		model.transcript.AppendNotice(text)
	case application.UIEventToolStep:
		step := event.ToolStep
		model.transcript.UpsertToolStep(components.ToolStep{
			InvocationID:  string(step.InvocationID),
			Name:          string(step.Name),
			Purpose:       sanitizeExternalText(step.Purpose, 1024),
			Status:        string(step.Status),
			Summary:       sanitizeExternalText(step.Summary, 4096),
			EvidenceCount: step.EvidenceCount,
			Truncated:     step.Truncated,
		})
	case application.UIEventRunCompleted, application.UIEventRunFailed, application.UIEventRunCancelled:
		model.clearApproval()
		text := sanitizeExternalText(event.Text, application.MaxQuestionBytes)
		if text == "" {
			switch event.Kind {
			case application.UIEventRunCompleted:
				text = "The AgentRun completed without displayable text."
			case application.UIEventRunCancelled:
				text = "The AgentRun was cancelled."
			default:
				text = "The AgentRun failed safely."
			}
		}
		model.run.StreamedText = text
		model.run.Active = false
		model.run.Terminal = true
		switch event.Kind {
		case application.UIEventRunCompleted:
			model.run.Status = "completed"
		case application.UIEventRunCancelled:
			model.run.Status = "cancelled"
		default:
			model.run.Status = "failed"
		}
		model.transcript.FinishAgent(text)
	default:
		return nil
	}
	model.run.LastSequence = event.Sequence
	return nil
}

func restartExecutionStatus(execution application.UIRestartExecution) string {
	switch execution.State {
	case application.UIRestartPatchAccepted:
		return fmt.Sprintf("PATCH accepted. Observing Deployment generation %d with %d target replicas.", execution.TargetGeneration, execution.TargetReplicas)
	case application.UIRestartPatchFailed:
		return "The PATCH failed and will not be retried."
	case application.UIRestartPatchOutcomeUnknown:
		return "The PATCH outcome is unknown. It will not be retried automatically."
	case application.UIRestartNotAttempted:
		return "The approved PATCH was not attempted. The approval cannot be reused."
	case application.UIRestartRolloutProgress:
		return fmt.Sprintf(
			"Rollout progress: observed generation %d/%d; updated %d/%d; available %d/%d.",
			execution.ObservedGeneration, execution.TargetGeneration,
			execution.UpdatedReplicas, execution.TargetReplicas,
			execution.AvailableReplicas, execution.TargetReplicas,
		)
	case application.UIRestartRolloutSucceeded:
		return fmt.Sprintf(
			"Rollout verified: generation %d observed; updated %d/%d; available %d/%d.",
			execution.ObservedGeneration, execution.UpdatedReplicas, execution.TargetReplicas,
			execution.AvailableReplicas, execution.TargetReplicas,
		)
	case application.UIRestartRolloutTimedOut:
		return "PATCH accepted; rollout verification timed out without claiming success or PATCH failure."
	case application.UIRestartRolloutFailed:
		return "PATCH accepted; rollout failed (" + string(execution.FailureCode) + ")."
	case application.UIRestartRolloutUnavailable:
		return "PATCH accepted; rollout verification became unavailable. The PATCH will not be retried."
	case application.UIRestartResultAuditFailed:
		return "High-priority error: the write result audit could not be stored after bounded retries."
	default:
		return "The restart result is unavailable."
	}
}
