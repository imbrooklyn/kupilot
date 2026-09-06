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
/model                Configure the single model runtime
/context [filter]     Select the Kubernetes Context
/namespace [filter]   Select the Kubernetes Namespace
/resource [filter]    Select or clear the target resource
/permissions          Review or change the permission profile
/status               Show the current safe status
/new                  Start a new Session
/resume [filter]      Resume a local Session
/rename [title]       Rename the current Session
/privacy              Show privacy, retention, and Session controls
/cancel               Cancel the active diagnostic run
/quit                 Exit Kupilot

Enter sends when idle and steers an active run at its next model boundary. During an active run, Tab queues one follow-up.
Alt+Up retrieves the newest editable queued, rejected, or recovered follow-up when the composer is empty.
Shift+Enter or Alt+Enter inserts a newline; Ctrl+J also works when distinguishable. Idle Tab completes a command.
Up and Down recall submitted input at composer boundaries. Page Up and Page Down review the retained transcript.
Ctrl+E opens supporting observation details. Esc interrupts an active run when no local interaction owns it.
Ctrl+C cancels the active local interaction; otherwise it clears a draft before cancelling a run or quitting.`

// Update reduces one message into pure UI state. The TerminalRuntime wrapper,
// not this reducer, owns renderer-only history insertion.
func (model Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return model.update(msg)
}

func (model Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.BackgroundColorMsg:
		if message.Color == nil || model.theme == ThemeANSI16 || model.theme == ThemeNoColor {
			return model, nil
		}
		resolved := model.theme
		if resolved == ThemeAuto {
			resolved = ThemeLight
			if message.IsDark() {
				resolved = ThemeDark
			}
		}
		model.applyStyleSet(newStyleSetForBackground(resolved, message.IsDark(), message.Color))
		model.reflow()
		return model, nil
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
			return model, model.prepareQuit()
		}
		return model, cmd
	case ApprovalExpiryMsg:
		cmd := model.acceptApprovalExpiry(message)
		model.reflow()
		return model, cmd
	case WorkingTickMsg:
		cmd := model.acceptWorkingTick(message)
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
	case EvidenceDetailResultMsg:
		model.acceptEvidenceDetailResult(message.Result)
		model.reflow()
		return model, nil
	case ScopeResultMsg:
		model.applyScopeResult(message.Result)
		model.reflow()
		return model, nil
	case ResourceSelectionResultMsg:
		model.acceptResourceSelectionResult(message.Result)
		model.reflow()
		return model, nil
	case CommandResultMsg:
		resumeScopeActivation := (message.Result.Command == application.UICommandActivateScope ||
			message.Result.Command == application.UICommandSelectContext ||
			message.Result.Command == application.UICommandSelectNamespace) &&
			model.pendingResumed != nil && model.pendingResumed.ResumeRequestID == message.Result.RequestID
		model.acceptCommandOutcome(message.Result)
		model.reflow()
		if resumeScopeActivation {
			return model, model.finishResumeScopeActivation(message.Result)
		}
		return model, nil
	case ModelSetupResultMsg:
		cmd := model.acceptModelSetupResult(message.Result)
		model.reflow()
		return model, cmd
	case ModelSetupCancelRejectedMsg:
		if model.rejectModelSetupCancellation(message) {
			model.showDialog("Cancellation unavailable", "The model setup cancellation request could not be queued. The current setup operation is still running.")
			model.reflow()
		}
		return model, nil
	case ApplicationFailureMsg:
		if model.acceptCancelledModelSetupFailure(message) {
			model.reflow()
			return model, nil
		}
		if model.acceptModelSetupFailure(message) {
			model.showDialog("Model setup unavailable", "The model settings were not applied and no replacement runtime was activated. Review the endpoint and model, then enter the API key again.")
			model.reflow()
			return model, nil
		}
		if model.acceptEvidenceFailure(message) {
			model.reflow()
			return model, nil
		}
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
		if !model.dialog.Open() && !model.approvalDialog.Open() &&
			!model.evidenceDialog.Open() && !model.transcript.EvidenceSelecting() {
			_ = model.composer.Focus()
			model.focus = FocusComposer
		}
		return model, nil
	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg:
		return model, nil
	case tea.PasteMsg:
		if model.dialog.Open() || model.approvalDialog.Open() ||
			model.evidenceDialog.Open() || model.transcript.EvidenceSelecting() || !model.terminalFocused {
			return model, nil
		}
		return model.updatePaste(message)
	case tea.KeyPressMsg:
		return model.updateKey(message)
	default:
		if model.dialog.Open() || model.approvalDialog.Open() ||
			model.evidenceDialog.Open() || model.transcript.EvidenceSelecting() {
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
	if message.Command == application.UICommandSubmitSteer ||
		message.Command == application.UICommandEnqueueFollowUp ||
		message.Command == application.UICommandPopFollowUp {
		pending := model.pendingConversation
		if pending == nil || message.RequestID != pending.RequestID || message.Command != pending.Kind ||
			message.RunID != pending.RunID || message.ScopeGeneration != pending.ScopeGeneration ||
			message.PolicyGeneration != pending.PolicyGeneration {
			return false
		}
		if pending.Kind != application.UICommandPopFollowUp && model.composer.Value() == "" {
			model.composer.SetValue(pending.Draft)
		}
		model.pendingConversation = nil
		return true
	}
	if message.Command == application.UICommandApproveAction || message.Command == application.UICommandRejectAction ||
		message.Command == application.UICommandCancelAction || message.Command == application.UICommandExpireAction {
		if model.pendingApproval == nil || model.pendingApprovalID == 0 || message.RequestID != model.pendingApprovalID ||
			message.RunID != model.pendingApproval.RunID || message.ScopeGeneration != model.pendingApproval.Scope.Generation ||
			message.PolicyGeneration != model.pendingApproval.PolicyGeneration ||
			message.ApprovalID != model.pendingApproval.RequestID ||
			!message.ApprovalDigest.Equal(model.pendingApproval.Digest) ||
			message.ApprovalSequence != model.pendingApproval.Sequence {
			return false
		}
		model.clearApproval()
		return true
	}
	if message.Command == application.UICommandCreateSessionRule {
		if model.pendingApproval == nil || model.pendingPermissionID == 0 || message.RequestID != model.pendingPermissionID ||
			message.RunID != model.pendingApproval.RunID || message.ScopeGeneration != model.pendingApproval.Scope.Generation ||
			message.PolicyGeneration != model.pendingApproval.PolicyGeneration ||
			message.ApprovalID != model.pendingApproval.RequestID ||
			!message.ApprovalDigest.Equal(model.pendingApproval.Digest) ||
			message.ApprovalSequence != model.pendingApproval.Sequence {
			return false
		}
		model.pendingPermissionID = 0
		model.clearApproval()
		return true
	}
	if message.Command == application.UICommandShowPermissions || message.Command == application.UICommandChangePermission ||
		message.Command == application.UICommandCreateSessionRule {
		if model.pendingPermissionID == 0 || message.RequestID != model.pendingPermissionID ||
			message.Command != application.UICommandShowPermissions && message.PolicyGeneration != model.permission.PolicyGeneration {
			return false
		}
		model.pendingPermissionID = 0
		return true
	}
	if message.Command == application.UICommandSubmitQuestion {
		if model.pendingSubmitID == 0 || message.RequestID != model.pendingSubmitID ||
			message.ScopeGeneration != model.scope.Generation {
			return false
		}
		model.pendingSubmitID = 0
		if model.composer.Value() == "" && model.pendingSubmitDraft != "" {
			model.composer.SetValue(model.pendingSubmitDraft)
		}
		model.pendingSubmitDraft = ""
		return true
	}
	switch message.Command {
	case application.UICommandShowPrivacy, application.UICommandAcceptPrivacy,
		application.UICommandRejectPrivacy, application.UICommandRevokePrivacy,
		application.UICommandToggleLogs, application.UICommandCancelPrivacy,
		application.UICommandTightenRetention, application.UICommandSetPersistenceMode:
		if model.pendingPrivacyID == 0 || message.RequestID != model.pendingPrivacyID {
			return false
		}
		model.pendingPrivacyID = 0
		model.privacyReview = nil
		model.lifecycleReview = nil
		model.privacyPending = false
		return true
	case application.UICommandDeleteSession:
		if model.pendingDeleteID == 0 || message.RequestID != model.pendingDeleteID || model.sessionDelete == nil {
			return false
		}
		model.pendingDeleteID = 0
		model.pendingPrivacyID = 0
		model.privacyReview = nil
		model.lifecycleReview = nil
		model.sessionDelete = nil
		model.privacyPending = false
		model.showDialog("Session not deleted", "The Session was not deleted. No partial deletion was reported.")
		model.reflow()
		return false
	case application.UICommandClearHistory, application.UICommandDeleteAllLocalState:
		if model.pendingDeleteID == 0 || message.RequestID != model.pendingDeleteID || model.localDeletion == nil {
			return false
		}
		model.pendingDeleteID = 0
		model.pendingPrivacyID = 0
		model.privacyReview = nil
		model.lifecycleReview = nil
		model.localDeletion = nil
		model.privacyPending = false
		model.showDialog("Local data not deleted", "No committed deletion result was reported.")
		model.reflow()
		return false
	case application.UICommandExportSession:
		if model.pendingExportID == 0 || message.RequestID != model.pendingExportID || model.sessionExport == nil {
			return false
		}
		model.clearSessionExportState()
		model.showDialog("Session not exported", "The Session summary was not published. No successful export was reported.")
		model.reflow()
		return false
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
		model.resumeScopeSelection = false
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
				model.resumeScopeSelection = false
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

func (model *Model) acceptEvidenceFailure(message ApplicationFailureMsg) bool {
	if message.Evidence.EvidenceID == "" || model.pendingEvidence.RequestID == 0 ||
		message.RequestID != model.pendingEvidence.RequestID ||
		!sameUIEvidenceIdentity(message.Evidence, model.pendingEvidence.Reference) ||
		model.evidenceGeneration != model.scope.Generation {
		return false
	}
	model.pendingEvidence = application.UIEvidenceDetailQuery{}
	model.evidenceGeneration = 0
	model.evidenceDialog.ShowUnavailable(string(message.Evidence.EvidenceID))
	return true
}

func (model Model) updatePaste(message tea.PasteMsg) (tea.Model, tea.Cmd) {
	message.Content = sanitizeExternalText(message.Content, 0)
	if model.modelSetup != nil && model.modelSetup.Stage == modelSetupApplying {
		return model, nil
	}
	if model.pendingConversation != nil {
		return model, nil
	}
	if model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetEntry {
		if strings.ContainsRune(message.Content, '\n') || len(model.composer.Value())+len(message.Content) > 4096 {
			model.showSessionExportTargetError("The export target must be one bounded single-line path.")
			return model, nil
		}
		updated, cmd, err := model.composer.Update(message)
		if err != nil {
			model.showSessionExportTargetError("The export target exceeds the fixed path limit.")
			return model, nil
		}
		model.composer = updated
		return model, cmd
	}
	updated, cmd, err := model.composer.Update(message)
	if err != nil {
		model.showComposerLimit()
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
		if command, handled := model.interruptModelSetup(); handled {
			return model, command
		}
		if model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetEntry && !model.dialog.Open() {
			model.cancelSessionExport()
			return model, nil
		}
		if model.evidenceDialog.Open() || model.transcript.EvidenceSelecting() {
			model.closeEvidenceInteraction()
			return model, nil
		}
		if model.dialog.Open() {
			if model.quitAfterLocalDeletion {
				model.quitAfterLocalDeletion = false
				model.closeDialog()
				return model, model.prepareQuit()
			}
			switch {
			case model.permissionConfirmation != nil:
				return model.updatePermissionConfirmationKey(message)
			case model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetError:
				return model.updateSessionExportTargetErrorKey(message)
			case model.sessionExport != nil && model.sessionExport.Stage == sessionExportConfirmation:
				return model.updateSessionExportConfirmationKey(message)
			case model.sessionDelete != nil:
				return model.updateSessionDeleteKey(message)
			case model.localDeletion != nil:
				return model.updateLocalDeletionKey(message)
			case model.privacyReview != nil:
				return model.updatePrivacyDialogKey(message)
			default:
				model.closeDialog()
				return model, nil
			}
		}
		if model.pickerOpen() {
			return model.cancelPicker()
		}
		if model.clearComposerForInterrupt() {
			return model, nil
		}
		if model.run.Active {
			model.quitAfterCancel = true
			return model, model.cancelRunCommand()
		}
		return model, model.prepareQuit()
	}
	if model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetEntry && !model.dialog.Open() {
		return model.updateSessionExportTargetKey(message)
	}
	if model.evidenceDialog.Open() {
		if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) ||
			key.Matches(message, model.keymap.Evidence) {
			model.closeEvidenceInteraction()
		}
		return model, nil
	}
	if model.dialog.Open() {
		if model.quitAfterLocalDeletion {
			if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
				model.quitAfterLocalDeletion = false
				model.closeDialog()
				return model, model.prepareQuit()
			}
			return model, nil
		}
		if model.permissionConfirmation != nil {
			return model.updatePermissionConfirmationKey(message)
		}
		if model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetError {
			return model.updateSessionExportTargetErrorKey(message)
		}
		if model.sessionExport != nil && model.sessionExport.Stage == sessionExportConfirmation {
			return model.updateSessionExportConfirmationKey(message)
		}
		if model.sessionDelete != nil {
			return model.updateSessionDeleteKey(message)
		}
		if model.localDeletion != nil {
			return model.updateLocalDeletionKey(message)
		}
		if model.privacyReview != nil {
			return model.updatePrivacyDialogKey(message)
		}
		if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
			quitFailedResume := model.resumeOrigin == resumeOriginTopLevel && model.startup.Failed && !model.startup.Ready
			model.closeDialog()
			if quitFailedResume {
				return model, model.prepareQuit()
			}
			if model.resumeOrigin == resumeOriginInTUI && model.startup.Ready {
				model.resumeOrigin = resumeOriginNone
			}
		}
		return model, nil
	}
	if key.Matches(message, model.keymap.Evidence) {
		if model.transcript.EvidenceSelecting() {
			model.closeEvidenceInteraction()
			return model, nil
		}
		if model.transcript.BeginEvidenceSelection() {
			model.closePickers()
			model.slashMenu.Close()
			model.composer.Blur()
			model.focus = FocusTranscript
			model.reflow()
		}
		return model, nil
	}
	if model.transcript.EvidenceSelecting() {
		switch {
		case key.Matches(message, model.keymap.Close):
			model.closeEvidenceInteraction()
		case key.Matches(message, model.keymap.Cancel):
			if model.run.Active {
				return model, model.cancelRunCommand()
			}
		case key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
			model.transcript.MoveEvidence(-1)
		case key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
			model.transcript.MoveEvidence(1)
		case key.Matches(message, model.keymap.TranscriptUp):
			model.transcript.PageUp()
		case key.Matches(message, model.keymap.TranscriptDown):
			model.transcript.PageDown()
		case key.Matches(message, model.keymap.Submit):
			return model.openEvidenceDetail()
		}
		return model, nil
	}

	if key.Matches(message, model.keymap.Close) {
		if model.modelSetup != nil {
			command, _ := model.interruptModelSetup()
			return model, command
		}
		if model.pickerOpen() {
			return model.cancelPicker()
		}
		if model.slashMenu.Open() {
			model.slashMenu.Close()
			model.reflow()
			return model, nil
		}
		if model.run.Active {
			return model, model.cancelRunCommand()
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
		case model.activePicker == application.UICompletionSession && isSessionPickerDeleteKey(message):
			model.beginSelectedSessionDelete()
			model.reflow()
			return model, nil
		case key.Matches(message, model.keymap.Reverse), key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
			model.movePicker(-1)
			return model, nil
		case key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
			model.movePicker(1)
			return model, nil
		case key.Matches(message, model.keymap.Complete):
			if model.permissionPicker.Open() {
				return model, nil
			}
			model.completePickerSelection()
			model.reflow()
			return model, nil
		case key.Matches(message, model.keymap.Submit):
			if model.permissionPicker.Open() {
				return model.selectPermissionCandidate()
			}
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
	if model.modelSetup != nil && model.modelSetup.Stage == modelSetupApplying {
		return model, nil
	}
	if model.pendingConversation != nil {
		return model, nil
	}
	if key.Matches(message, model.keymap.EditFollowUp) {
		return model.editLastConversationInput()
	}
	if model.run.Active && key.Matches(message, model.keymap.Complete) {
		return model.queueActiveDraft()
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
	ordinaryComposer := model.modelSetup == nil && model.sessionExport == nil
	if ordinaryComposer &&
		key.Matches(message, model.keymap.Previous) && model.composer.ArrowHistoryEligible() {
		model.composer.PreviousHistory()
		queryCmd := model.syncSuggestionsAfterEdit()
		model.reflow()
		return model, queryCmd
	}
	if ordinaryComposer &&
		key.Matches(message, model.keymap.Next) && model.composer.ArrowHistoryEligible() {
		model.composer.NextHistory()
		queryCmd := model.syncSuggestionsAfterEdit()
		model.reflow()
		return model, queryCmd
	}
	updated, cmd, err := model.composer.Update(message)
	if err != nil {
		model.showComposerLimit()
		return model, nil
	}
	model.composer = updated
	queryCmd := model.syncSuggestionsAfterEdit()
	model.reflow()
	return model, combineCommands(cmd, queryCmd)
}

func (model *Model) showComposerLimit() {
	if model.modelSetup != nil {
		model.showDialog("Input limit", "The current model setup value exceeds its fixed byte limit.")
		return
	}
	model.showDialog("Input limit", "The draft is too long. The maximum is 65536 bytes.")
}

func (model Model) updateApprovalDialogKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.approvalDialog.Terminal() &&
		(key.Matches(message, model.keymap.Submit) || key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Quit)) {
		model.approvalDialog.Close()
		if model.terminalFocused && !model.dialog.Open() {
			_ = model.composer.Focus()
			model.focus = FocusComposer
		}
		return model, nil
	}
	approvalWidth, approvalHeight := model.approvalReviewSize()
	if !model.approvalDialog.Reviewable(approvalWidth, approvalHeight) {
		switch {
		case key.Matches(message, model.keymap.Submit):
			model.approvalDialog.SelectDeny()
			return model.submitApprovalDecision(false)
		case key.Matches(message, model.keymap.Close), key.Matches(message, model.keymap.Quit):
			return model.submitApprovalDecision(true)
		default:
			return model, nil
		}
	}
	switch {
	case key.Matches(message, model.keymap.TranscriptUp):
		model.approvalDialog.Scroll(-8)
		return model, nil
	case key.Matches(message, model.keymap.TranscriptDown):
		model.approvalDialog.Scroll(8)
		return model, nil
	case key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt),
		key.Matches(message, model.keymap.Reverse):
		model.approvalDialog.Move(-1)
		return model, nil
	case key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt),
		key.Matches(message, model.keymap.Complete):
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

func (model Model) submitApprovalDecision(forceCancel bool) (tea.Model, tea.Cmd) {
	if model.pendingApproval == nil || model.pendingApprovalID != 0 || model.pendingPermissionID != 0 ||
		!model.approvalDialog.MarkSubmitted() {
		return model, nil
	}
	request := *model.pendingApproval
	kind := application.UICommandRejectAction
	if forceCancel {
		kind = application.UICommandCancelAction
	} else {
		switch model.approvalDialog.Choice() {
		case components.ApprovalCancel:
			kind = application.UICommandCancelAction
		case components.ApprovalOnce:
			kind = application.UICommandApproveAction
		case components.ApprovalSessionRule:
			kind = application.UICommandCreateSessionRule
		}
	}
	requestID := model.nextUIRequestID()
	command := application.UICommand{
		Kind: kind, RequestID: requestID, RunID: request.RunID,
		ExpectedScopeGeneration:  request.Scope.Generation,
		ExpectedPolicyGeneration: request.PolicyGeneration,
		ApprovalID:               request.RequestID, ApprovalDigest: request.Digest,
		ApprovalNonce: request.Nonce, ApprovalSequence: request.Sequence,
	}
	if command.Validate() != nil {
		model.clearApproval()
		model.showDialog("Approval unavailable", "The approval decision could not be submitted safely.")
		return model, nil
	}
	if kind == application.UICommandCreateSessionRule {
		model.pendingPermissionID = requestID
	} else {
		model.pendingApprovalID = requestID
	}
	return model, applicationCommand(command)
}

func (model Model) submitDraft() (tea.Model, tea.Cmd) {
	if model.modelSetup != nil {
		return model.submitModelSetupDraft()
	}
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
		return model.dispatchActiveConversationInput(application.UICommandSubmitSteer, draft, historyDraft)
	}
	if model.scope.Switching {
		model.showDialog("Scope switching", "Wait for scope activation to finish before sending a question.")
		return model, nil
	}
	if !model.modelConfigured {
		model.beginMissingModelSetup()
		model.showDialog("Model required", "Configure the model endpoint, model identifier, and API key before sending a question.")
		return model, nil
	}
	if !model.scope.Verified || model.scope.Generation < 1 || !model.scope.ReadOnly {
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
	model.composer.Reset()
	model.composer.RecordSubmission(historyDraft)
	model.slashMenu.Close()
	model.pendingSubmitID = command.RequestID
	model.pendingSubmitDraft = historyDraft
	model.reflow()
	return model, applicationCommand(command)
}

func (model Model) queueActiveDraft() (tea.Model, tea.Cmd) {
	draft := model.composer.Value()
	if strings.TrimSpace(draft) == "" {
		return model, nil
	}
	if strings.HasPrefix(draft, "!") {
		model.showDialog("Command unavailable", "Shell and bang commands are not supported.")
		return model, nil
	}
	parsed := ParseSlashDraft(draft)
	switch parsed.Mode {
	case DraftEscapedChat:
		return model.dispatchActiveConversationInput(
			application.UICommandEnqueueFollowUp,
			strings.TrimPrefix(draft, "/"),
			draft,
		)
	case DraftChat:
		return model.dispatchActiveConversationInput(application.UICommandEnqueueFollowUp, draft, draft)
	default:
		// Fixed and unknown Slash syntax remains local and never enters the
		// follow-up queue. Visible completion already has higher priority.
		return model, nil
	}
}

func (model Model) dispatchActiveConversationInput(
	kind application.UICommandKind,
	text string,
	historyDraft string,
) (tea.Model, tea.Cmd) {
	if !model.activeConversationInputAvailable() {
		if model.run.Active && (model.pendingApproval != nil || model.actionPresentation != nil) {
			model.showDialog("Input paused", "Finish the current approval or Reviewer interaction before steering or queueing input.")
		}
		return model, nil
	}
	command := application.UICommand{
		Kind: kind, RequestID: model.nextUIRequestID(), Text: text, RunID: model.run.RunID,
		ExpectedScopeGeneration:  model.run.ScopeGeneration,
		ExpectedPolicyGeneration: model.run.PolicyGeneration,
	}
	if command.Validate() != nil {
		model.showDialog("Cannot accept input", "The conversation input could not be submitted safely.")
		return model, nil
	}
	model.pendingConversation = &pendingConversationInput{
		RequestID: command.RequestID, Kind: kind, RunID: command.RunID,
		ScopeGeneration: command.ExpectedScopeGeneration, PolicyGeneration: command.ExpectedPolicyGeneration,
		Draft: historyDraft,
	}
	model.composer.Reset()
	model.slashMenu.Close()
	model.reflow()
	return model, applicationCommand(command)
}

func (model Model) editLastConversationInput() (tea.Model, tea.Cmd) {
	if model.composer.Value() != "" || !model.activeConversationInputAvailable() ||
		model.conversationStatus.Editable == 0 {
		return model, nil
	}
	command := application.UICommand{
		Kind: application.UICommandPopFollowUp, RequestID: model.nextUIRequestID(), RunID: model.run.RunID,
		ExpectedScopeGeneration:  model.run.ScopeGeneration,
		ExpectedPolicyGeneration: model.run.PolicyGeneration,
	}
	if command.Validate() != nil {
		return model, nil
	}
	model.pendingConversation = &pendingConversationInput{
		RequestID: command.RequestID, Kind: command.Kind, RunID: command.RunID,
		ScopeGeneration: command.ExpectedScopeGeneration, PolicyGeneration: command.ExpectedPolicyGeneration,
	}
	return model, applicationCommand(command)
}

func (model Model) activeConversationInputAvailable() bool {
	return model.run.Active && !model.run.Terminal && model.pendingConversation == nil &&
		model.pendingApproval == nil && model.actionPresentation == nil && !model.approvalDialog.Open() &&
		!model.dialog.Open() && !model.pickerOpen() && !model.slashMenu.Open() &&
		!model.evidenceDialog.Open() && !model.transcript.EvidenceSelecting()
}

func editableConversationInput(value string) string {
	if strings.HasPrefix(value, "/") {
		return "/" + value
	}
	return value
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
	case slashModel:
		model.beginModelSetup()
		model.reflow()
		return model, nil
	case slashStatus:
		model.composer.Reset()
		model.slashMenu.Close()
		model.reflow()
		return model, applicationCommand(application.UICommand{Kind: application.UICommandShowStatus})
	case slashPermissions:
		model.composer.Reset()
		model.slashMenu.Close()
		requestID := model.nextUIRequestID()
		model.pendingPermissionID = requestID
		model.reflow()
		return model, applicationCommand(application.UICommand{
			Kind: application.UICommandShowPermissions, RequestID: requestID,
		})
	case slashQuit:
		model.composer.Reset()
		model.slashMenu.Close()
		return model, model.prepareQuit()
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
			return false, "There is no active diagnostic run to cancel."
		}
	case "new", "resume", "quit":
		if model.run.Active {
			return false, "Cancel the active diagnostic run first."
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
	model.closeEvidenceInteraction()
	model.dialog.Show(title, body)
	model.composer.Blur()
	model.focus = FocusModal
}

func (model *Model) closeDialog() {
	model.dialog.Close()
	if model.terminalFocused && !model.approvalDialog.Open() {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
}

func (model Model) openEvidenceDetail() (tea.Model, tea.Cmd) {
	index, selected := model.transcript.SelectedEvidence()
	if !selected || model.scope.Switching || index < 0 || index >= len(model.evidenceReferences) ||
		model.pendingEvidence.RequestID != 0 {
		return model, nil
	}
	reference := model.evidenceReferences[index]
	query := application.UIEvidenceDetailQuery{RequestID: model.nextUIRequestID(), Reference: reference}
	if query.Validate() != nil {
		model.closeEvidenceInteraction()
		return model, nil
	}
	model.pendingEvidence = query
	model.evidenceGeneration = model.scope.Generation
	model.evidenceDialog.ShowLoading(string(reference.EvidenceID))
	model.composer.Blur()
	model.focus = FocusModal
	return model, applicationEvidenceDetail(query)
}

func (model *Model) acceptEvidenceDetailResult(result application.UIEvidenceDetailResult) {
	if model.scope.Switching {
		model.closeEvidenceInteraction()
		return
	}
	if result.Validate() != nil || model.pendingEvidence.RequestID == 0 ||
		result.RequestID != model.pendingEvidence.RequestID ||
		!sameUIEvidenceIdentity(result.Reference, model.pendingEvidence.Reference) ||
		model.evidenceGeneration != model.scope.Generation {
		return
	}
	model.pendingEvidence = application.UIEvidenceDetailQuery{}
	model.evidenceGeneration = 0
	switch result.Reference.State {
	case application.UIEvidenceDetailExpired:
		model.evidenceDialog.ShowExpired(string(result.Reference.EvidenceID))
	case application.UIEvidenceDetailUnavailable:
		model.evidenceDialog.ShowUnavailable(string(result.Reference.EvidenceID))
	case application.UIEvidenceDetailAvailable, application.UIEvidenceDetailPartial:
		detail := result.Detail
		if detail == nil {
			model.evidenceDialog.ShowUnavailable(string(result.Reference.EvidenceID))
			return
		}
		partial := result.Reference.State == application.UIEvidenceDetailPartial
		status := "complete"
		if partial || detail.Truncated {
			status = "partial"
		}
		resourceName := sanitizeExternalText(detail.Resource.Name, 253)
		if detail.Resource.Namespace != "" {
			resourceName = sanitizeExternalText(detail.Resource.Namespace, 63) + "/" + resourceName
		}
		resourceIdentity := fmt.Sprintf("%s %s (%s %s, %s)",
			sanitizeExternalText(detail.Resource.Kind, 63), resourceName,
			sanitizeExternalText(detail.Resource.Type.APIVersion(), 317),
			sanitizeExternalText(detail.Resource.Type.Resource, 253), detail.Resource.Type.Scope)
		model.evidenceDialog.ShowDetail(components.EvidenceDetailContent{
			Category: string(detail.Category),
			Scope: fmt.Sprintf("%s / %s",
				sanitizeExternalText(result.Reference.Scope.Context, 253),
				sanitizeExternalText(result.Reference.Scope.Namespace, 63)),
			Resource:          resourceIdentity,
			ObservedAt:        detail.ObservedAt.UTC().Format(time.RFC3339Nano),
			Status:            status,
			SensitiveFiltered: detail.SensitiveFilter == application.UIEvidenceSensitiveFilterApplied,
			Projection:        sanitizeExternalText(detail.Projection, application.MaxUIEvidenceProjectionBytes),
		}, partial)
	}
}

func (model *Model) appendEvidenceReferences(references []application.UIEvidenceReference) {
	if len(references) == 0 || len(references) > 100 {
		return
	}
	for _, reference := range references {
		if reference.Validate() != nil {
			return
		}
	}
	display := make([]components.EvidenceReference, 0, len(references))
	for _, reference := range references {
		index := len(model.evidenceReferences)
		model.evidenceReferences = append(model.evidenceReferences, reference)
		display = append(display, components.EvidenceReference{
			Index: index, ID: string(reference.EvidenceID), State: string(reference.State),
		})
	}
	model.transcript.SetAgentEvidence(display)
}

func (model *Model) closeEvidenceInteraction() {
	model.pendingEvidence = application.UIEvidenceDetailQuery{}
	model.evidenceGeneration = 0
	model.evidenceDialog.Close()
	model.transcript.EndEvidenceSelection()
	if model.terminalFocused && !model.dialog.Open() && !model.approvalDialog.Open() {
		_ = model.composer.Focus()
		model.focus = FocusComposer
	}
}

func sameUIEvidenceIdentity(left, right application.UIEvidenceReference) bool {
	return left.EvidenceID == right.EvidenceID && left.RunID == right.RunID &&
		left.Scope == right.Scope && left.Sequence == right.Sequence
}

func quitCommand() tea.Cmd {
	return func() tea.Msg { return tea.Quit() }
}

func (model *Model) clearComposerForInterrupt() bool {
	if model.composer.Value() == "" {
		return false
	}
	model.composer.Reset()
	model.closePickers()
	model.slashMenu.Close()
	model.reflow()
	return true
}

func (model *Model) prepareQuit() tea.Cmd {
	model.closePickers()
	model.slashMenu.Close()
	model.composer.Blur()
	return quitCommand()
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
	resumeScope := model.resumeScopeSelection && model.pendingResumed != nil &&
		(model.activePicker == application.UICompletionContext || model.activePicker == application.UICompletionNamespace)
	topLevel := isSession && model.resumeOrigin == resumeOriginTopLevel && !model.startup.Ready
	if resumeScope {
		topLevel = model.resumeOrigin == resumeOriginTopLevel
	}
	requestID := uint64(0)
	if resumeScope {
		requestID = model.pendingResumed.ResumeRequestID
	}
	model.closePickers()
	model.resumeScopeSelection = false
	if isSession {
		model.composer.Reset()
		model.resumeOrigin = resumeOriginNone
		if !topLevel {
			model.startup.Ready = true
		}
	}
	model.reflow()
	if topLevel {
		model.pendingResumed = nil
		model.resumeOrigin = resumeOriginNone
		model.resumeScopeSelection = false
		return model, model.prepareQuit()
	}
	if resumeScope {
		model.pendingResumed = nil
		model.resumeOrigin = resumeOriginNone
		model.resumeScopeSelection = false
		model.startup.Ready = true
		model.composer.Reset()
		if requestID != 0 {
			return model, applicationCommand(application.UICommand{Kind: application.UICommandCancelResume, RequestID: requestID})
		}
	}
	return model, nil
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
		return model.beginResumeScopeSelection(resumed)
	}
	if !model.scope.Verified {
		// A configured or historic candidate is not current authority. Resume
		// itself performs no Kubernetes I/O; only an explicit picker selection
		// may start the normal independent activation path.
		return model.beginResumeScopeSelection(resumed)
	}
	command := model.resumeDecisionCommand(resumed)
	if command.Validate() != nil {
		model.showDialog("Resume unavailable", "The Session choice could not be accepted safely.")
		return nil
	}
	model.closeEvidenceInteraction()
	model.pendingScopeID = command.RequestID
	model.scope.Switching = true
	return applicationCommand(command)
}

func (model Model) resumeDecisionCommand(
	resumed application.UIResumedSession,
) application.UICommand {
	return application.UICommand{
		Kind: application.UICommandAcceptResume, RequestID: resumed.ResumeRequestID,
		ExpectedScopeGeneration: model.scope.Generation,
	}
}

func (model *Model) finishResumeScopeActivation(result application.UICommandOutcome) tea.Cmd {
	if result.Scope == nil || model.pendingResumed == nil ||
		model.pendingResumed.ResumeRequestID != result.RequestID {
		return nil
	}
	requestID := result.RequestID
	if result.Scope.Failure != "" {
		model.resumeScopeSelection = false
		model.pendingScopeID = 0
		model.scope.Switching = false
		return model.beginResumeScopeSelection(*model.pendingResumed)
	}
	model.resumeScopeSelection = false
	command := application.UICommand{
		Kind: application.UICommandAcceptResume, RequestID: requestID,
		ExpectedScopeGeneration: model.scope.Generation,
	}
	if command.Validate() != nil {
		model.pendingResumed = nil
		model.resumeOrigin = resumeOriginNone
		model.resumeScopeSelection = false
		model.showDialog("Resume unavailable", "The Session choice could not be accepted safely.")
		return applicationCommand(application.UICommand{Kind: application.UICommandCancelResume, RequestID: requestID})
	}
	model.pendingScopeID = requestID
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
			model.resumeScopeSelection = false
			model.showDialog("Resume unavailable", resumeUIFailureText(result.Failure))
			return
		}
		model.applyAcceptedResume(*result.Resumed)
		model.pendingScopeID = 0
		model.scope.Switching = false
		if result.Scope.Failure != "" {
			model.scope = ScopeView{Generation: result.Scope.ScopeGeneration}
			model.showDialog("Scope required", "The Session resumed, but no current Kubernetes scope could be verified. Select one before asking a question.")
		} else {
			model.scope = ScopeView{
				Context: sanitizeExternalText(result.Scope.Context, 253), Namespace: sanitizeExternalText(result.Scope.Namespace, 63),
				Generation: result.Scope.ScopeGeneration, ReadOnly: result.Scope.ReadOnly, Verified: true,
			}
		}
	case application.UICommandSelectContext, application.UICommandSelectNamespace, application.UICommandActivateScope:
		model.applyScopeResult(*result.Scope)
	case application.UICommandSelectResource:
		model.acceptResourceSelectionResult(*result.Resource)
	case application.UICommandNewSession:
		model.session = SessionView{ID: result.Session.ID, Title: result.Session.Title}
		model.privacyMode = result.Session.PrivacyMode
		model.resource = ResourceView{}
		model.pendingResumed = nil
		model.resumeOrigin = resumeOriginNone
		model.resumeScopeSelection = false
		model.scopeSelectionRequired = !model.scope.Verified
		model.startup.Ready = true
		model.resetTranscript()
		if model.scope.Verified {
			model.transcript.AppendNotice("A new Session was started. The verified scope remains active; no model request was sent.")
		} else {
			model.transcript.AppendNotice("A new Session was started without a verified Kubernetes scope. Select one before asking a question; no model request was sent.")
		}
	case application.UICommandRenameSession:
		if result.Failure != "" {
			model.showDialog("Rename unavailable", "The current Session title could not be updated safely.")
			return
		}
		model.session = SessionView{ID: result.Session.ID, Title: result.Session.Title, Resumed: result.Session.Resumed}
		model.transcript.AppendNotice("The current Session title was updated.")
	case application.UICommandShowStatus:
		model.transcript.AppendNotice(statusText(*result.Status, model.modelName))
	case application.UICommandShowPermissions, application.UICommandChangePermission, application.UICommandCreateSessionRule:
		if model.pendingPermissionID == 0 || result.RequestID != model.pendingPermissionID || result.Permissions == nil {
			return
		}
		model.pendingPermissionID = 0
		if result.Command == application.UICommandShowPermissions {
			model.showPermissionPicker(*result.Permissions)
			return
		}
		previousGeneration := model.permission.PolicyGeneration
		model.permission = result.Permissions.Permission
		model.permissionReviewer = result.Permissions.Reviewer
		model.permissionConfirmation = nil
		model.clearActionPresentation()
		model.clearApproval()
		if model.run.Active && previousGeneration != model.permission.PolicyGeneration {
			model.run.Active = false
			model.run.Terminal = true
			model.run.Status = "cancelled"
			model.run.StreamedText = "The diagnostic run was cancelled because the permission policy changed."
			model.transcript.FinishAgentWithDuration(model.run.StreamedText, model.currentRunElapsed())
		}
		model.transcript.AppendNotice(permissionChangeNotice(*result.Permissions))
	case application.UICommandSubmitQuestion:
		if model.pendingSubmitID == 0 || result.RequestID != model.pendingSubmitID {
			return
		}
		model.pendingSubmitID = 0
		if result.Failure == application.UIQueryConsentRequired && result.Privacy != nil {
			if model.composer.Value() == "" && model.pendingSubmitDraft != "" {
				model.composer.SetValue(model.pendingSubmitDraft)
			}
			model.pendingSubmitDraft = ""
			model.pendingPrivacyID = result.RequestID
			model.showPrivacyReview(*result.Privacy, nil)
			return
		}
		if result.Failure == application.UIQueryModelRequired {
			if model.composer.Value() == "" && model.pendingSubmitDraft != "" {
				model.composer.SetValue(model.pendingSubmitDraft)
			}
			model.pendingSubmitDraft = ""
			model.modelConfigured = false
			model.beginMissingModelSetup()
			model.showDialog("Model required", "Configure the model before sending another question.")
			return
		}
		if result.Failure != "" {
			if model.composer.Value() == "" && model.pendingSubmitDraft != "" {
				model.composer.SetValue(model.pendingSubmitDraft)
			}
			model.pendingSubmitDraft = ""
			model.showDialog("Model transfer unavailable", "The question could not start under the current safe state.")
		}
	case application.UICommandSubmitSteer, application.UICommandEnqueueFollowUp, application.UICommandPopFollowUp:
		pending := model.pendingConversation
		if pending == nil || result.RequestID != pending.RequestID || result.Command != pending.Kind ||
			result.RunID != pending.RunID || result.ConversationInput == nil {
			return
		}
		model.pendingConversation = nil
		if result.Command == application.UICommandPopFollowUp {
			if model.composer.Value() == "" && !model.pickerOpen() && !model.dialog.Open() &&
				!model.approvalDialog.Open() && model.pendingApproval == nil && model.actionPresentation == nil {
				model.composer.SetValue(editableConversationInput(result.ConversationInput.Text))
			}
			return
		}
		model.composer.RecordSubmission(pending.Draft)
	case application.UICommandShowPrivacy, application.UICommandToggleLogs, application.UICommandTightenRetention:
		if model.pendingPrivacyID == 0 || result.RequestID != model.pendingPrivacyID || result.Privacy == nil || result.Lifecycle == nil {
			return
		}
		model.privacyPending = false
		model.showPrivacyReview(*result.Privacy, result.Lifecycle)
	case application.UICommandSetPersistenceMode:
		if model.pendingPrivacyID == 0 || result.RequestID != model.pendingPrivacyID || result.Privacy == nil ||
			result.Lifecycle == nil || result.Session == nil {
			return
		}
		model.session = SessionView{ID: result.Session.ID, Title: result.Session.Title}
		model.privacyMode = result.Session.PrivacyMode
		model.resource = ResourceView{}
		model.pendingResumed = nil
		model.resumeOrigin = resumeOriginNone
		model.resumeScopeSelection = false
		model.resetTranscript()
		model.transcript.AppendNotice("A new Session was started with the selected persistence mode. No model request was sent.")
		model.privacyPending = false
		model.showPrivacyReview(*result.Privacy, result.Lifecycle)
	case application.UICommandDeleteSession:
		if model.pendingDeleteID == 0 || result.RequestID != model.pendingDeleteID || model.sessionDelete == nil {
			return
		}
		state := *model.sessionDelete
		if result.Failure == "" && (result.Deletion == nil || result.Deletion.SessionID != state.SessionID ||
			result.Deletion.WasCurrent != state.ExpectedCurrent) {
			return
		}
		model.pendingDeleteID = 0
		model.pendingPrivacyID = 0
		model.privacyReview = nil
		model.lifecycleReview = nil
		model.sessionDelete = nil
		model.privacyPending = false
		if result.Failure != "" {
			model.showDialog("Session not deleted", "The Session was not deleted. The transaction did not report a committed deletion.")
			return
		}
		if result.Deletion.WasCurrent {
			model.closeDialog()
			model.closePickers()
			model.composer.Reset()
			model.session = SessionView{}
			model.privacyMode = domain.PrivacyModeStandard
			model.resource = ResourceView{}
			model.resetTranscript()
		} else {
			model.sessionPicker.Remove(string(result.Deletion.SessionID))
			model.showDialog("Session deleted", "The selected Session was deleted logically in one committed transaction. This is not forensic erasure.")
		}
		model.transcript.AppendNotice("The Session was deleted logically in one committed transaction. This is not forensic erasure.")
	case application.UICommandClearHistory:
		if model.pendingDeleteID == 0 || result.RequestID != model.pendingDeleteID || model.localDeletion == nil ||
			model.localDeletion.Kind != clearLocalHistory {
			return
		}
		model.clearLocalDeletionFlow()
		if result.Failure != "" {
			model.showDialog("History not cleared", "No committed all-Session deletion was reported. Settings and consent were not changed by this request.")
			return
		}
		model.resetAfterHistoryDeletion()
		model.transcript.AppendNotice("All Session history was deleted logically. Settings and valid model data-sharing consent were preserved. This is not forensic erasure.")
		model.showDialog("Session history cleared", "All Session graphs and associated audit were deleted. Settings and valid model data-sharing consent remain. This is not forensic erasure.")
	case application.UICommandDeleteAllLocalState:
		if model.pendingDeleteID == 0 || result.RequestID != model.pendingDeleteID || model.localDeletion == nil ||
			model.localDeletion.Kind != deleteAllLocalState || result.LocalStateDeletion == nil {
			return
		}
		local := *result.LocalStateDeletion
		model.clearLocalDeletionFlow()
		if local.StorageClosed {
			model.resetAfterHistoryDeletion()
			model.quitAfterLocalDeletion = true
			if result.Failure != "" {
				model.showDialog("Local database deletion incomplete", "Kupilot closed local storage but could not remove every validated database file. The application cannot continue. Inspect only the fixed KUPILOT_HOME/state directory and known database sidecars after exit.\n\nPress Esc, Ctrl+C, or Enter to exit.")
				return
			}
			model.showDialog("Local database state deleted", "Kupilot closed local storage and removed the validated database plus known SQLite sidecars. Exported summaries, operational logs, backups, snapshots, swap, and storage media were not removed. This is not forensic erasure.\n\nPress Esc, Ctrl+C, or Enter to exit.")
			return
		}
		model.showDialog("Local data not deleted", "Storage path validation or preflight failed before the database was closed. Kupilot remains open and no complete deletion was reported.")
	case application.UICommandExportSession:
		if model.pendingExportID == 0 || result.RequestID != model.pendingExportID || model.sessionExport == nil {
			return
		}
		state := *model.sessionExport
		if result.Failure == "" && (result.Export == nil || result.Export.SessionID != state.SessionID ||
			result.Export.SchemaVersion != application.ExportSummarySchemaVersion) {
			return
		}
		model.clearSessionExportState()
		if result.Failure != "" {
			model.showDialog("Session not exported", "The Session summary was not published. No successful export was reported.")
			return
		}
		model.transcript.AppendNotice("The redacted Session summary was exported to the explicitly confirmed local target.")
		model.showDialog("Session summary exported", "The redacted Session summary was published as an owner-only Markdown file.")
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
		model.transcript.AppendNotice("Model data-sharing consent was revoked; any active diagnostic run was cancelled.")
	case application.UICommandCancelPrivacy:
		model.finishPrivacyAction(result.RequestID)
	case application.UICommandApproveAction, application.UICommandRejectAction, application.UICommandCancelAction, application.UICommandExpireAction:
		if model.pendingApproval == nil || model.pendingApprovalID == 0 || result.RequestID != model.pendingApprovalID ||
			result.Approval.RequestID != model.pendingApproval.RequestID ||
			result.Approval.RunID != model.pendingApproval.RunID ||
			result.Approval.ScopeGeneration != model.pendingApproval.Scope.Generation ||
			result.Approval.PolicyGeneration != model.pendingApproval.PolicyGeneration ||
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
			if model.terminalFocused && !model.dialog.Open() {
				_ = model.composer.Focus()
				model.focus = FocusComposer
			}
			model.transcript.AppendNotice("The action was approved but not executed.")
		case domain.ApprovalStateRejected:
			model.clearApproval()
			model.clearActionPresentation()
			model.transcript.AppendNotice("The action was rejected. No operation was executed.")
		case domain.ApprovalStateExpired:
			model.clearApproval()
			model.clearActionPresentation()
			model.transcript.AppendNotice("The action approval expired. No operation was executed.")
		case domain.ApprovalStateCancelled:
			model.clearApproval()
			model.clearActionPresentation()
			model.transcript.AppendNotice("The action approval was cancelled. No operation was executed.")
		case domain.ApprovalStateConsumed:
			status := approvalExecutionStatus(*result.Approval)
			if model.approvalDialog.Open() {
				model.approvalDialog.SetStatus(status, true)
			}
			model.pendingApproval = nil
			model.pendingApprovalID = 0
			model.approvalState = ""
			model.clearActionPresentation()
			model.transcript.AppendNotice(status)
			if result.Approval.ActionExecution != nil && result.Approval.ActionExecution.SafeOutput != "" {
				model.transcript.AppendNotice("Sanitized command output:\n" + result.Approval.ActionExecution.SafeOutput)
			}
		default:
			model.clearApproval()
			model.clearActionPresentation()
			model.transcript.AppendNotice("The action approval was invalidated. No operation was executed.")
		}
	case application.UICommandResumeSession:
		model.showDialog("Command unavailable", "The command is not available in the current flow.")
	}
}

func (model *Model) acceptApprovalExpiry(message ApprovalExpiryMsg) tea.Cmd {
	if model.pendingApproval == nil || model.pendingApprovalID != 0 || model.pendingPermissionID != 0 ||
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
	if model.pendingApproval == nil || model.pendingApprovalID != 0 || model.pendingPermissionID != 0 {
		return nil
	}
	request := *model.pendingApproval
	requestID := model.nextUIRequestID()
	command := application.UICommand{
		Kind: application.UICommandExpireAction, RequestID: requestID, RunID: request.RunID,
		ExpectedScopeGeneration:  request.Scope.Generation,
		ExpectedPolicyGeneration: request.PolicyGeneration,
		ApprovalID:               request.RequestID, ApprovalDigest: request.Digest,
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
	if model.terminalFocused && !model.dialog.Open() {
		_ = model.composer.Focus()
		model.focus = FocusComposer
	}
}

func (model *Model) showPrivacyReview(review application.PrivacyReview, lifecycle *application.SessionLifecycleReview) {
	if review.Validate() != nil {
		model.showDialog("Privacy unavailable", "Model data-sharing information could not be displayed safely.")
		return
	}
	if lifecycle != nil && lifecycle.Validate() != nil {
		model.showDialog("Privacy unavailable", "Local persistence information could not be displayed safely.")
		return
	}
	copy := review
	copy.Categories = append([]application.PrivacyCategoryReview(nil), review.Categories...)
	copy.NeverEligible = append([]string(nil), review.NeverEligible...)
	model.privacyReview = &copy
	model.lifecycleReview = nil
	if lifecycle != nil {
		lifecycleCopy := cloneLifecycleReview(*lifecycle)
		model.lifecycleReview = &lifecycleCopy
	}
	model.privacyPending = false
	title := "Model data sharing"
	if lifecycle != nil {
		title = "Privacy and local data"
	}
	model.showDialog(title, privacyReviewText(copy, model.lifecycleReview))
}

func (model *Model) finishPrivacyAction(requestID uint64) bool {
	if model.pendingPrivacyID == 0 || requestID != model.pendingPrivacyID {
		return false
	}
	model.pendingPrivacyID = 0
	model.privacyReview = nil
	model.lifecycleReview = nil
	model.privacyPending = false
	model.closeDialog()
	return true
}

func (model *Model) clearLocalDeletionFlow() {
	model.pendingDeleteID = 0
	model.pendingPrivacyID = 0
	model.privacyReview = nil
	model.lifecycleReview = nil
	model.localDeletion = nil
	model.privacyPending = false
	model.dialog.Close()
}

func (model *Model) resetAfterHistoryDeletion() {
	model.closePickers()
	model.composer.Reset()
	model.session = SessionView{}
	model.privacyMode = domain.PrivacyModeStandard
	model.resource = ResourceView{}
	model.pendingResumed = nil
	model.resumeOrigin = resumeOriginNone
	model.resumeScopeSelection = false
	model.resetTranscript()
}

func privacyReviewText(review application.PrivacyReview, lifecycle *application.SessionLifecycleReview) string {
	var builder strings.Builder
	if lifecycle != nil {
		mode := domain.PrivacyMode("")
		if lifecycle.CurrentSession != nil {
			mode = lifecycle.CurrentSession.PrivacyMode
		}
		builder.WriteString("Local persistence:\n")
		switch mode {
		case domain.PrivacyModeStandard:
			builder.WriteString("Session storage: history saved until explicit Session deletion.\n")
		case domain.PrivacyModeMinimal:
			builder.WriteString("Session storage: memory only and unavailable after this process exits.\n")
		default:
			builder.WriteString("Session storage: no current Session.\n")
		}
		fmt.Fprintf(&builder, "Operational details: kept for %d days (can only be shortened; 0 means memory only).\n", lifecycle.OperationalDetailRetentionDays)
		fmt.Fprintf(&builder, "Read and lifecycle audit: %d days.\nApproval and write audit: %d days.\n", lifecycle.ReadAuditRetentionDays, lifecycle.WriteAuditRetentionDays)
		builder.WriteString("Impact: memory-only Sessions cannot be resumed after exit and retain no messages, diagnoses, cluster-read or observation details, or model-request details.\n")
		builder.WriteString("Deletion is logical deletion, not forensic erasure; SQLite free pages, WAL, backups, snapshots, swap, and storage media may retain old bytes.\n\n")
	}
	fmt.Fprintf(&builder, "Destination: %s\nPolicy version: %s\nConsent: %s\n\nData eligible to be sent:\n",
		sanitizeExternalText(review.Origin, 2048), review.PolicyVersion, privacyDecisionLabel(review.Decision))
	for _, category := range review.Categories {
		state := "not included"
		if category.Enabled {
			state = "included"
		}
		fmt.Fprintf(&builder, "- [%s] %s: %s\n", state, privacyCategoryLabel(category.ID), category.Description)
	}
	builder.WriteString("\nNever eligible:\n")
	for _, value := range review.NeverEligible {
		fmt.Fprintf(&builder, "- %s\n", value)
	}
	if lifecycle != nil {
		builder.WriteString("\nA accept | L toggle container output | R reject/revoke | T shorten retention | M switch Session storage")
		if lifecycle.CurrentSession != nil && lifecycle.CurrentSession.PrivacyMode == domain.PrivacyModeStandard {
			builder.WriteString(" | E export redacted summary")
		}
		if lifecycle.CurrentSession != nil {
			builder.WriteString(" | D delete current Session")
		}
		builder.WriteString(" | H clear history | X delete all database state")
		builder.WriteString(" | Esc or Ctrl+C cancel")
	} else {
		builder.WriteString("\nA accept | L toggle container output | R reject/revoke | Esc or Ctrl+C cancel")
	}
	return builder.String()
}

func privacyDecisionLabel(decision application.PrivacyDecision) string {
	switch decision {
	case application.PrivacyDecisionAccepted:
		return "Accepted"
	case application.PrivacyDecisionRejected:
		return "Rejected"
	case application.PrivacyDecisionRevoked:
		return "Revoked"
	default:
		return "Not yet accepted"
	}
}

func privacyCategoryLabel(category application.ModelDataCategory) string {
	switch category {
	case application.DataCategoryUserQuestion:
		return "User question"
	case application.DataCategorySafeConversationContext:
		return "Conversation context"
	case application.DataCategoryResourceReferences:
		return "Resource references"
	case application.DataCategoryProjectedStatus:
		return "Kubernetes status"
	case application.DataCategoryProjectedEvents:
		return "Kubernetes events"
	case application.DataCategoryRedactedContainerOutput:
		return "Container output"
	default:
		return "Other bounded data"
	}
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
	case key.Matches(message, model.keymap.Close), key.Matches(message, model.keymap.Submit),
		key.Matches(message, model.keymap.Quit):
		command.Kind = application.UICommandCancelPrivacy
		model.dialog.Close()
		model.privacyReview = nil
		model.lifecycleReview = nil
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
	case message.Code == 't' || message.Code == 'T':
		if model.lifecycleReview == nil {
			return model, nil
		}
		days, ok := nextRetentionDays(model.lifecycleReview.OperationalDetailRetentionDays)
		if !ok {
			return model, nil
		}
		command.Kind = application.UICommandTightenRetention
		command.Lifecycle = &application.SessionLifecycleIntent{RetentionDays: &days}
		model.privacyPending = true
	case message.Code == 'm' || message.Code == 'M':
		if model.lifecycleReview == nil {
			return model, nil
		}
		mode := domain.PrivacyModeMinimal
		if model.lifecycleReview.CurrentSession != nil && model.lifecycleReview.CurrentSession.PrivacyMode == domain.PrivacyModeMinimal {
			mode = domain.PrivacyModeStandard
		}
		command.Kind = application.UICommandSetPersistenceMode
		command.Lifecycle = &application.SessionLifecycleIntent{PrivacyMode: mode}
		model.privacyPending = true
	case message.Code == 'd' || message.Code == 'D':
		if model.lifecycleReview == nil || model.lifecycleReview.CurrentSession == nil {
			return model, nil
		}
		model.beginCurrentSessionDelete()
		return model, nil
	case message.Code == 'h' || message.Code == 'H':
		if model.lifecycleReview == nil {
			return model, nil
		}
		model.beginLocalDeletion(clearLocalHistory)
		return model, nil
	case message.Code == 'x' || message.Code == 'X':
		if model.lifecycleReview == nil {
			return model, nil
		}
		model.beginLocalDeletion(deleteAllLocalState)
		return model, nil
	case message.Code == 'e' || message.Code == 'E':
		if model.lifecycleReview == nil || model.lifecycleReview.CurrentSession == nil ||
			model.lifecycleReview.CurrentSession.PrivacyMode != domain.PrivacyModeStandard {
			return model, nil
		}
		model.beginCurrentSessionExport()
		return model, nil
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
	model.privacyMode = resumed.Session.PrivacyMode
	model.startup.Ready = true
	model.startup.Failed = false
	model.resource = ResourceView{}
	model.pendingResourceID = 0
	model.pendingResource = ResourceView{}
	model.pendingResumed = nil
	model.resumeOrigin = resumeOriginNone
	model.resumeScopeSelection = false
	model.scopeSelectionRequired = false
	model.closePickers()
	model.composer.Reset()
	model.composer.ClearHistory()
	model.resetTranscript()
	for _, message := range resumed.History {
		textLimit := application.MaxQuestionBytes
		if message.Role == domain.MessageRoleAssistant {
			textLimit = application.MaxAnswerMarkdownBytes
		}
		text := sanitizeExternalText(message.Content, textLimit)
		if text == "" {
			continue
		}
		switch message.Role {
		case domain.MessageRoleUser:
			model.transcript.AppendUser(text)
			model.composer.RecordSubmission(text)
		case domain.MessageRoleAssistant:
			model.transcript.StartAgent()
			model.transcript.FinishAgent(text)
			model.appendEvidenceReferences(message.EvidenceReferences)
		default:
			model.transcript.AppendNotice(text)
		}
	}
	model.transcript.AppendNotice("Session resumed. Saved observations are display-only and cannot support new facts until a new diagnostic run collects them again.")
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
}

func (model *Model) resetTranscript() {
	model.pendingEvidence = application.UIEvidenceDetailQuery{}
	model.evidenceGeneration = 0
	model.evidenceDialog.Close()
	model.evidenceReferences = nil
	model.pendingConversation = nil
	model.pendingSubmitDraft = ""
	model.conversationRevision = 0
	model.conversationStatus = application.ConversationInputStatus{}
	model.conversationPreview = nil
	model.committedConversation = nil
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
	if changed {
		model.closeEvidenceInteraction()
	}
	if result.Failure != "" {
		if changed {
			model.clearApproval()
			model.clearActionPresentation()
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
		model.clearActionPresentation()
		model.finishRunForScopeChange()
		model.resource = ResourceView{}
		model.pendingResourceID = 0
		model.pendingResource = ResourceView{}
		model.closePickers()
	}
	model.scope = ScopeView{
		Context: sanitizeExternalText(result.Context, 253), Namespace: sanitizeExternalText(result.Namespace, 63),
		Generation: result.ScopeGeneration, ReadOnly: result.ReadOnly, Verified: true,
	}
	model.scopeSelectionRequired = false
	if changed {
		model.transcript.AppendNotice("Scope changed. The selected Resource and stale picker results were cleared.")
	}
	if result.ScopePreferenceDegraded {
		model.transcript.AppendNotice("The scope is active, but Kupilot could not save this Context for the next start.")
	}
}

func (model *Model) finishRunForScopeChange() {
	if !model.run.Active {
		return
	}
	const cancellation = "The diagnostic run was cancelled because the scope changed."
	model.run.Active = false
	model.run.Terminal = true
	model.run.Status = "cancelled"
	model.run.StreamedText = cancellation
	model.transcript.FinishAgentWithDuration(cancellation, model.currentRunElapsed())
}

func (model Model) currentRunElapsed() time.Duration {
	if model.run.StartedAt.IsZero() || model.now == nil {
		return 0
	}
	now := model.now().UTC().Truncate(time.Millisecond)
	if model.workingAt.After(now) {
		now = model.workingAt
	}
	if now.IsZero() || now.UnixMilli() < 0 || now.Before(model.run.StartedAt) {
		return 0
	}
	return now.Sub(model.run.StartedAt)
}

func statusText(status application.UIStatusResult, modelName string) string {
	contextName := status.Context
	if contextName == "" {
		contextName = "unavailable"
	}
	namespace := status.Namespace
	if namespace == "" {
		namespace = "unavailable"
	}
	access := "scope unverified"
	actions := "unavailable until scope verification"
	if status.ReadOnly {
		access = "namespace policy " + string(status.NamespaceAccess)
		actions = "typed remediation and exact local policies · permission route and fresh RBAC required"
	}
	run := "idle"
	if status.RunActive {
		run = "active (" + string(status.RunID) + ")"
	}
	session := "unavailable"
	privacy := "unavailable"
	if status.Session != nil {
		session = string(status.Session.ID)
		privacy = string(status.Session.PrivacyMode)
	}
	storage := "healthy"
	if status.PersistenceDegraded {
		storage = "degraded"
	}
	budget := status.Budget
	modelContext := status.ModelContext
	compression := "not compressed"
	if modelContext.Compressed {
		compression = "covered through " + string(modelContext.CoveredThroughMessageID)
	}
	compressedAt := "never"
	if modelContext.CompressedAtUnixMillis > 0 {
		compressedAt = time.UnixMilli(modelContext.CompressedAtUnixMillis).UTC().Format(time.RFC3339)
	}
	contextStorage := "healthy"
	if !modelContext.StorageHealthy {
		contextStorage = "degraded"
	}
	agentBinding := "unconfigured"
	if status.AgentModel.Configured {
		agentBinding = status.AgentModel.Profile + " · " + status.AgentModel.OriginHash
		if status.AgentModel.Consented {
			agentBinding += " · consented"
		} else {
			agentBinding += " · consent required"
		}
	}
	reviewerBinding := "unconfigured"
	if status.ReviewerModel.Configured {
		reviewerBinding = status.ReviewerModel.Profile + " · " + status.ReviewerModel.OriginHash
		if !status.ReviewerModel.Available {
			reviewerBinding += " · unavailable"
		} else if status.ReviewerModel.Consented {
			reviewerBinding += " · consented"
		} else {
			reviewerBinding += " · consent required"
		}
	}
	modelName = sanitizeExternalText(modelName, application.MaxModelSetupNameBytes)
	if modelName == "" {
		modelName = "unavailable"
	}
	permissionProfile := "unconfigured"
	permissionGeneration := "unavailable"
	permissionHealth := "unavailable"
	permissionBoundary := "no permission policy is active"
	permissionReviewer := "not routed"
	permissionRisk := "all actions unavailable"
	permissionRules := "0 current-process rules"
	if status.Permission.Configured {
		permissionProfile = string(status.Permission.Profile)
		permissionGeneration = fmt.Sprintf("%d", status.Permission.PolicyGeneration)
		permissionHealth = "healthy"
		if !status.Permission.Healthy {
			permissionHealth = "degraded · no permission may be inferred"
		}
		permissionBoundary, permissionReviewer, permissionRisk = permissionStatusDescriptions(status.Permission.Profile)
		permissionRules = fmt.Sprintf("%d current-process, current-Session rules", status.Permission.SessionRuleCount)
	}
	actionState := "none"
	reviewerState := "none"
	if status.Action != nil {
		actionState = fmt.Sprintf("%s · %s · route %s · state %s · scope %d · policy %d · expires %s",
			status.Action.Operation, status.Action.Risk, status.Action.Route, status.Action.State,
			status.Action.ScopeGeneration, status.Action.PolicyGeneration,
			time.UnixMilli(status.Action.ExpiresAtMillis).UTC().Format(time.RFC3339))
		if status.Action.Reviewer != nil {
			reviewerState = reviewerIdentity(*status.Action.Reviewer)
			if status.Action.Reviewer.RationaleSummary != "" {
				reviewerState += " · rationale " + sanitizeExternalText(status.Action.Reviewer.RationaleSummary, 2048)
			}
		} else if status.Action.Reviewing {
			reviewerState = "optional approval_reviewer · Reviewing"
		}
	}
	lines := []string{
		"Kupilot status",
		"",
		"Session",
		statusRow("ID", session),
		statusRow("Model", modelName),
		statusRow("Privacy", privacy),
		statusRow("Storage", storage),
		statusRow("Agent role", agentBinding),
		statusRow("Reviewer", reviewerBinding),
		"",
		"Model context",
		statusRow("Mode", string(modelContext.Mode)),
		statusRow("History", fmt.Sprintf("%d messages · %s", modelContext.EligibleMessages, statusBytes(modelContext.EligibleBytes))),
		statusRow("Summary", compression),
		statusRow("Compacted", compressedAt),
		statusRow("Recent tail", fmt.Sprintf("%d messages", modelContext.RecentTailMessages)),
		statusRow("Summary budget", fmt.Sprintf("%d/%d calls", modelContext.SummaryCallsUsed, modelContext.SummaryCallsMaximum)),
		statusRow("Storage", contextStorage),
		"",
		"Scope",
		statusRow("Context", contextName),
		statusRow("Namespace", namespace),
		statusRow("Generation", fmt.Sprintf("%d", status.ScopeGeneration)),
		statusRow("Access", access),
		statusRow("Actions", actions),
		"",
		"Permission and action supervision",
		statusRow("Profile", permissionProfile),
		statusRow("Generation", permissionGeneration),
		statusRow("Health", permissionHealth),
		statusRow("Boundary", permissionBoundary),
		statusRow("Reviewer", permissionReviewer),
		statusRow("Risk", permissionRisk),
		statusRow("Rules", permissionRules),
		statusRow("Action", actionState),
		statusRow("Review state", reviewerState),
	}
	for _, route := range status.Permission.CustomRoutes {
		lines = append(lines, statusRow("Custom route", fmt.Sprintf("%s · %s -> %s", route.Operation, route.Risk, route.Disposition)))
	}
	for _, rule := range status.Permission.SessionRules {
		lines = append(lines, statusRow("Session rule", fmt.Sprintf(
			"%s · %s · scope %s/%s generation %d access %s · policy %d · target %s · parameters %s · effect %s · risk %s · data %s · sinks %s · network %s · destination %s · %s · created %s · expires %s",
			rule.ID, rule.Operation, rule.Scope.Context, rule.Scope.Namespace, rule.Scope.Generation,
			rule.NamespaceAccess, rule.PolicyGeneration, rule.Target, rule.ParameterSummary,
			rule.Effect, rule.Risk, statusActionDataSummary(rule.DataCategories), statusActionSinkSummary(rule.AllowedSinks),
			statusActionNetworkSummary(rule.NetworkEffects), statusActionDigest(rule.NetworkDestinationHash),
			actionLimitsSummary(rule.Limits), time.UnixMilli(rule.CreatedAtMillis).UTC().Format(time.RFC3339),
			time.UnixMilli(rule.ExpiresAtMillis).UTC().Format(time.RFC3339))))
	}
	lines = append(lines,
		"",
		"Run",
		statusRow("State", run),
		statusRow("Conversation input", fmt.Sprintf(
			"%d items · %s · pending %d · committing %d · committed %d · queued %d · rejected %d · recovered %d · unknown %d · editable %d · revision %d",
			status.ConversationInput.Items, statusBytes(status.ConversationInput.Bytes),
			status.ConversationInput.Pending, status.ConversationInput.Committing,
			status.ConversationInput.Committed, status.ConversationInput.Queued,
			status.ConversationInput.Rejected, status.ConversationInput.Recovered,
			status.ConversationInput.Unknown, status.ConversationInput.Editable,
			status.ConversationInput.Revision)),
		statusRow("Input limits", fmt.Sprintf("%d items · %s aggregate · %s each",
			status.ConversationInput.MaximumItems, statusBytes(status.ConversationInput.MaximumBytes),
			statusBytes(status.ConversationInput.MaximumItemBytes))),
		statusRow("Catalog", status.CapabilityCatalogVersion),
		statusRow("Resources", fmt.Sprintf("%s · %d types", status.ResourcePolicyVersion, status.ResourceTypeCount)),
		statusRow("Observability", fmt.Sprintf("%s · Prometheus %s · Loki %s", status.ObservabilityPolicyVersion,
			statusEnabled(status.PrometheusEnabled), statusEnabled(status.LokiEnabled))),
		statusRow("Remote diagnostics", fmt.Sprintf("%s · Pod exec policies %d · container files %s · diagnostic Pod policies %d", status.RemoteDiagnosticsPolicyVersion,
			status.PodExecPolicyCount, statusEnabled(status.ContainerFileReadEnabled), status.DiagnosticPodPolicyCount)),
		statusRow("Local execution", fmt.Sprintf("%s · direct argv policies %d · shell policies %d · OS sandbox not provided", status.LocalExecutionPolicyVersion,
			status.LocalCommandPolicyCount, status.LocalShellPolicyCount)),
		"",
		"Budget",
		statusRow("Profile", string(budget.Profile)),
		statusRow("Basis", budget.ModelEvidenceBasis),
		statusRow("Time", statusDuration(budget.ElapsedMilliseconds)+" elapsed · "+statusDuration(budget.RemainingMilliseconds)+" remaining"),
		statusRow("Calls", fmt.Sprintf("%d/%d steps · %d/%d tools · %d/%d model · %d/%d summary · %d/%d reviewer", budget.StepsUsed, budget.StepsMaximum,
			budget.ToolCallsUsed, budget.ToolCallsMaximum, budget.ModelCallsUsed, budget.ModelCallsMaximum,
			budget.SummaryCallsUsed, budget.SummaryCallsMaximum, budget.ReviewerCallsUsed, budget.ReviewerCallsMaximum)),
		statusRow("Cost units", fmt.Sprintf("%d/%d model · %d/%d summary · %d/%d reviewer", budget.ModelCostUnitsUsed,
			budget.ModelCostUnitsMaximum, budget.SummaryCostUnitsUsed, budget.SummaryCostUnitsMaximum,
			budget.ReviewerCostUnitsUsed, budget.ReviewerCostUnitsMaximum)),
		statusRow("Data", fmt.Sprintf("%s/%s · %d/%d log calls", statusBytes(budget.ToolResultBytesUsed),
			statusBytes(budget.ToolResultBytesMaximum), budget.LogCallsUsed, budget.LogCallsMaximum)),
		statusRow("Remote exec", fmt.Sprintf("%d/%d calls", budget.RemoteExecCallsUsed, budget.RemoteExecCallsMaximum)),
		statusRow("Local process", fmt.Sprintf("%d/%d reserved proposals", budget.LocalProcessCallsUsed, budget.LocalProcessCallsMaximum)),
		statusRow("Events", fmt.Sprintf("%d pages · %d items/page · %s/page · %s total",
			budget.EventPagesMaximum, budget.EventPageItemsMaximum, statusBytes(budget.EventPageBytesMaximum), statusBytes(budget.EventBytesMaximum))),
		statusRow("Logs", fmt.Sprintf("%d containers · %s/read", budget.LogContainersMaximum, statusBytes(budget.LogBytesMaximum))),
		statusRow("Metrics", fmt.Sprintf("%d/%d reads · %d containers · %s/read", budget.MetricCallsUsed,
			budget.MetricCallsMaximum, budget.MetricContainersMaximum, statusBytes(budget.MetricBytesMaximum))),
		statusRow("Sources", fmt.Sprintf("%d/%d reads · %d pages · %d series · %d samples · %d lines · %s · %s window · %s step",
			budget.DataSourceCallsUsed, budget.DataSourceCallsMaximum, budget.DataSourcePagesMaximum, budget.DataSourceSeriesMaximum,
			budget.DataSourceSamplesMaximum, budget.DataSourceLinesMaximum, statusBytes(budget.DataSourceBytesMaximum),
			statusDuration(budget.DataSourceWindowMillis), statusDuration(budget.DataSourceStepMillis))),
		statusRow("Resources", fmt.Sprintf("%d pages · %d items/page · %s/page · %d scanned · %d returned · %s total",
			budget.ResourcePagesMaximum, budget.ResourcePageItemsMaximum, statusBytes(budget.ResourcePageBytesMaximum),
			budget.ResourceScannedMaximum, budget.ResourceReturnedMaximum, statusBytes(budget.ResourceBytesMaximum))),
	)
	return strings.Join(lines, "\n")
}

func permissionStatusDescriptions(profile domain.PermissionProfile) (string, string, string) {
	switch profile {
	case domain.PermissionProfileReadOnly:
		return "safe reads automatic; sensitive reads human; mutation and execution denied",
			"never routes", "approval cannot elevate a denied effect"
	case domain.PermissionProfileAsk:
		return "safe automatic; review and critical require the local user",
			"not used", "default supervised profile"
	case domain.PermissionProfileAutoReview:
		return "safe automatic; review delegated; critical remains human",
			"optional approval_reviewer for review only", "Reviewer failure or timeout authorizes nothing"
	case domain.PermissionProfileFullAccess:
		return "admitted and enabled review and critical may route automatically",
			"not used", "high risk; no scope, RBAC, consent, catalog, audit, or denial bypass"
	case domain.PermissionProfileCustom:
		return "exact routes only; missing safe/review routes deny; critical defaults human",
			"eligible exact review routes only", "empty custom policy is conservative"
	default:
		return "no permission policy is active", "not routed", "all actions unavailable"
	}
}

func statusEnabled(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func statusRow(label, value string) string {
	return fmt.Sprintf("  %-11s %s", label, value)
}

func statusDuration(milliseconds int64) string {
	return components.FormatElapsedCompact((time.Duration(milliseconds) * time.Millisecond).Truncate(time.Second))
}

func statusBytes(value int) string {
	if value >= 1024*1024 {
		return fmt.Sprintf("%.1f MiB", float64(value)/(1024*1024))
	}
	if value >= 1024 {
		return fmt.Sprintf("%.1f KiB", float64(value)/1024)
	}
	return fmt.Sprintf("%d B", value)
}

func actionLimitsSummary(limits domain.ActionLimits) string {
	return fmt.Sprintf("timeout %s · items %d · lines %d · bytes %d · output %d",
		limits.Timeout, limits.MaximumItems, limits.MaximumLines, limits.MaximumBytes, limits.MaximumOutput)
}

func statusActionDataSummary(categories domain.ActionDataCategories) string {
	values := make([]string, 0, 6)
	for _, item := range []struct {
		flag  domain.ActionDataCategories
		label string
	}{
		{domain.ActionDataResourceMetadata, "resource metadata"},
		{domain.ActionDataProjectedStatus, "projected status"},
		{domain.ActionDataProjectedEvents, "projected events"},
		{domain.ActionDataContainerOutput, "container output"},
		{domain.ActionDataFileOutput, "file output"},
		{domain.ActionDataProcessOutput, "process output"},
	} {
		if categories&item.flag != 0 {
			values = append(values, item.label)
		}
	}
	return strings.Join(values, ", ")
}

func statusActionSinkSummary(sinks domain.ActionSinks) string {
	values := make([]string, 0, 4)
	for _, item := range []struct {
		flag  domain.ActionSinks
		label string
	}{
		{domain.ActionSinkTerminal, "terminal"},
		{domain.ActionSinkModel, "model"},
		{domain.ActionSinkKubernetesAPI, "Kubernetes API"},
		{domain.ActionSinkLocalProcess, "local process"},
	} {
		if sinks&item.flag != 0 {
			values = append(values, item.label)
		}
	}
	return strings.Join(values, ", ")
}

func statusActionNetworkSummary(effects domain.ActionNetworkEffects) string {
	values := make([]string, 0, 6)
	for _, item := range []struct {
		flag  domain.ActionNetworkEffects
		label string
	}{
		{domain.ActionNetworkNone, "none"},
		{domain.ActionNetworkKubernetesAPI, "Kubernetes API"},
		{domain.ActionNetworkModelOrigin, "model origin"},
		{domain.ActionNetworkDataSource, "configured data source"},
		{domain.ActionNetworkRemotePod, "exact remote Pod/Service"},
		{domain.ActionNetworkExternalCommand, "policy-bound command destination"},
	} {
		if effects&item.flag != 0 {
			values = append(values, item.label)
		}
	}
	return strings.Join(values, ", ")
}

func statusActionDigest(digest domain.ActionDigest) string {
	if !digest.Valid() {
		return "none"
	}
	return string(digest)
}

func (model *Model) acceptApplicationEvent(event application.UIEvent) tea.Cmd {
	if event.Validate() != nil {
		return nil
	}
	if event.Kind == application.UIEventConversationInput {
		model.acceptConversationInputEvent(event)
		return nil
	}
	if model.scope.Switching || event.ScopeGeneration != model.scope.Generation ||
		event.PolicyGeneration != model.permission.PolicyGeneration {
		return nil
	}
	if event.Kind == application.UIEventRestartExecution {
		execution := *event.RestartExecution
		if model.pendingApproval != nil {
			if execution.RequestID != model.pendingApproval.RequestID || event.RunID != model.pendingApproval.RunID ||
				event.Sequence != model.pendingApproval.Sequence || !execution.Digest.Equal(model.pendingApproval.Digest) ||
				execution.EventIndex != model.approvalDialog.ExecutionIndex()+1 ||
				!model.approvalDialog.Open() || !model.approvalDialog.Submitted() {
				return nil
			}
			model.approvalDialog.SetExecutionStatus(
				execution.EventIndex, restartExecutionStatus(execution), execution.State.Terminal(),
			)
			return nil
		}
		if !model.acceptAutomaticActionEvent(execution.RequestID, execution.Digest, event.Sequence, execution.EventIndex) {
			return nil
		}
		model.transcript.AppendNotice(restartExecutionStatus(execution))
		if execution.State.Terminal() {
			model.clearActionPresentation()
		}
		return nil
	}
	if event.Kind == application.UIEventReviewerState {
		if !model.acceptReviewerEvent(*event.Reviewer) {
			return nil
		}
		model.transcript.AppendNotice(reviewerEventNotice(event.Reviewer.Status))
		return nil
	}
	if event.Kind == application.UIEventApprovalClosed {
		state := event.ApprovalResult.State
		if model.pendingApproval == nil {
			if !model.acceptClosedActionEvent(*event.ApprovalResult) {
				return nil
			}
			if state == domain.ApprovalStateConsumed {
				model.transcript.AppendNotice(approvalExecutionStatus(*event.ApprovalResult))
				if event.ApprovalResult.ActionExecution != nil && event.ApprovalResult.ActionExecution.SafeOutput != "" {
					model.transcript.AppendNotice("Sanitized command output:\n" + event.ApprovalResult.ActionExecution.SafeOutput)
				}
			} else if state == domain.ApprovalStateRejected {
				model.transcript.AppendNotice("The action was denied before execution.")
			} else {
				model.transcript.AppendNotice("The action closed without execution.")
			}
			model.clearActionPresentation()
			return nil
		}
		if event.ApprovalResult.RequestID != model.pendingApproval.RequestID || event.RunID != model.pendingApproval.RunID ||
			event.Sequence != model.pendingApproval.Sequence || event.PolicyGeneration != model.pendingApproval.PolicyGeneration ||
			!event.ApprovalResult.Digest.Equal(model.pendingApproval.Digest) {
			return nil
		}
		model.clearApproval()
		model.clearActionPresentation()
		if state == domain.ApprovalStateConsumed {
			model.transcript.AppendNotice(approvalExecutionStatus(*event.ApprovalResult))
			if event.ApprovalResult.ActionExecution != nil && event.ApprovalResult.ActionExecution.SafeOutput != "" {
				model.transcript.AppendNotice("Sanitized command output:\n" + event.ApprovalResult.ActionExecution.SafeOutput)
			}
		} else if state == domain.ApprovalStateExpired {
			model.transcript.AppendNotice("The action approval expired. No operation was executed.")
		} else {
			model.transcript.AppendNotice("The action approval was cancelled, denied, or invalidated. No operation was executed.")
		}
		return nil
	}
	if event.Kind == application.UIEventRunStarted {
		if event.Sequence != 1 || model.run.Active || !model.startup.Ready || !model.scope.ReadOnly {
			return nil
		}
		startedAt := model.now().UTC().Truncate(time.Millisecond)
		model.run = RunView{
			RunID: event.RunID, ScopeGeneration: event.ScopeGeneration,
			PolicyGeneration: event.PolicyGeneration, LastSequence: event.Sequence, StartedAt: startedAt,
			Active: true, Status: "active",
		}
		model.clearActionPresentation()
		model.workingAt = startedAt
		model.workingFrame = 0
		model.closeEvidenceInteraction()
		if event.Text != "" {
			text := sanitizeExternalText(event.Text, application.MaxQuestionBytes)
			if text != "" {
				model.transcript.AppendUser(text)
				history := editableConversationInput(text)
				if model.pendingSubmitDraft != "" {
					history = model.pendingSubmitDraft
					model.pendingSubmitDraft = ""
				}
				model.composer.RecordSubmission(history)
			}
		}
		model.transcript.StartAgent()
		return workingTick(model.run)
	}
	if !model.run.Active || model.run.Terminal || event.RunID != model.run.RunID ||
		event.ScopeGeneration != model.run.ScopeGeneration || event.PolicyGeneration != model.run.PolicyGeneration {
		return nil
	}
	if event.Kind == application.UIEventApprovalRequested {
		if model.pendingApproval != nil || model.approvalDialog.Open() {
			return nil
		}
		request := *event.Approval
		reviewerContinuation := model.actionPresentation != nil && model.reviewerEvent != nil &&
			model.reviewerEvent.Status.State == application.UIReviewerEscalated &&
			model.actionPresentation.RequestID == request.RequestID &&
			model.actionPresentation.Sequence == event.Sequence &&
			model.actionPresentation.Digest.Equal(request.Digest)
		if reviewerContinuation {
			if request.Reviewer == nil || *request.Reviewer != model.reviewerEvent.Status {
				return nil
			}
		} else if !model.acceptInitialActionSequence(event.Sequence) {
			return nil
		}
		content := components.ApprovalDialogContent{
			Operation: approvalOperationLabel(request.Operation),
			Scope: fmt.Sprintf("%s / %s · namespace access %s · scope revision %d · profile %s · policy generation %d",
				sanitizeExternalText(request.Scope.Context, 253),
				sanitizeExternalText(request.Scope.Namespace, 63), request.NamespaceAccess,
				request.Scope.Generation, request.PermissionProfile, request.PolicyGeneration),
			Resource:   approvalTargetLabel(request),
			Current:    sanitizeExternalText(request.CurrentSummary, application.MaxApprovalDisplaySummaryBytes),
			Proposed:   sanitizeExternalText(request.ProposedSummary, application.MaxApprovalDisplaySummaryBytes),
			Parameters: sanitizeExternalText(request.ParameterSummary, application.MaxApprovalDisplaySummaryBytes),
			Effects:    sanitizeExternalText(request.EffectSummary, application.MaxApprovalDisplaySummaryBytes),
			Reason:     sanitizeExternalText(request.ReasonSummary, domain.MaxApprovalReasonSummaryBytes),
			Risk:       string(request.Risk) + " · " + sanitizeExternalText(request.RiskSummary, 4096),
			Digest:     string(request.Digest), Timeout: request.Limits.Timeout,
			ExpiresAt:   request.ExpiresAt,
			SessionRule: request.Risk == domain.RiskReview,
		}
		if request.Reviewer != nil {
			content.Reviewer = reviewerIdentity(*request.Reviewer)
			content.ReviewerReason = sanitizeExternalText(request.Reviewer.RationaleSummary, 2048)
		}
		now := model.now().UTC().Truncate(time.Millisecond)
		if now.IsZero() || now.UnixMilli() < 0 {
			return nil
		}
		model.closeEvidenceInteraction()
		model.pendingApproval = &request
		model.pendingApprovalID = 0
		model.approvalState = domain.ApprovalStatePending
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
	if event.Sequence != model.run.LastSequence+1 {
		return nil
	}

	switch event.Kind {
	case application.UIEventTextDelta:
		remaining := application.MaxAnswerMarkdownBytes - len(model.run.StreamedText)
		if remaining < 0 {
			remaining = 0
		}
		text := ""
		if remaining > 0 {
			text = sanitizeExternalText(event.Text, remaining)
		}
		model.run.StreamedText += text
		model.transcript.AppendAgent(text)
	case application.UIEventValidationWarning:
		text := sanitizeExternalText(event.Text, application.MaxQuestionBytes)
		if text == "" {
			text = "Kupilot removed unsupported final-answer metadata. Review the remaining Evidence before relying on affected claims."
		}
		model.transcript.AppendNotice(text)
	case application.UIEventPersistenceDegraded:
		text := sanitizeExternalText(event.Text, application.MaxQuestionBytes)
		if text == "" {
			text = "Local persistence is degraded; this run may not be resumable."
		}
		model.run.PersistenceDegraded = true
		model.transcript.AppendNotice(text)
	case application.UIEventToolStep:
		step := event.ToolStep
		if step.Status == application.ToolStepRequested {
			model.run.StreamedText = ""
			model.transcript.ClearAgent()
		}
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
		model.clearActionPresentation()
		textLimit := application.MaxQuestionBytes
		if event.Kind == application.UIEventRunCompleted {
			textLimit = application.MaxAnswerMarkdownBytes
		}
		text := sanitizeExternalText(event.Text, textLimit)
		if text == "" {
			switch event.Kind {
			case application.UIEventRunCompleted:
				text = "The diagnostic run completed without a displayable result."
			case application.UIEventRunCancelled:
				text = "The diagnostic run was cancelled."
			default:
				text = "The diagnostic run failed safely."
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
		model.transcript.FinishAgentWithDuration(text, model.currentRunElapsed())
		if event.Kind == application.UIEventRunCompleted {
			model.appendEvidenceReferences(event.EvidenceReferences)
		}
	default:
		return nil
	}
	model.run.LastSequence = event.Sequence
	return nil
}

func (model *Model) acceptConversationInputEvent(event application.UIEvent) {
	input := event.ConversationInput
	if input == nil || input.Revision <= model.conversationRevision ||
		event.RunID != model.run.RunID || event.ScopeGeneration != model.run.ScopeGeneration ||
		event.PolicyGeneration != model.run.PolicyGeneration {
		return
	}
	model.conversationRevision = input.Revision
	model.conversationStatus = input.Status
	model.conversationPreview = make([]application.ConversationInputProjection, 0, len(input.Preview))
	for _, projection := range input.Preview {
		projection.Text = sanitizeExternalText(projection.Text, application.MaxConversationInputItemBytes)
		if projection.Text != "" {
			model.conversationPreview = append(model.conversationPreview, projection)
		}
	}
	if input.Changed == nil || input.Changed.State != application.ConversationInputCommitted {
		return
	}
	if model.committedConversation == nil {
		model.committedConversation = make(map[domain.MessageID]struct{})
	}
	if _, duplicate := model.committedConversation[input.Changed.ItemID]; duplicate {
		return
	}
	text := sanitizeExternalText(input.Changed.Text, application.MaxConversationInputItemBytes)
	if text == "" {
		return
	}
	model.committedConversation[input.Changed.ItemID] = struct{}{}
	model.transcript.InsertUserBeforeActiveAgent(text)
}

func approvalTargetLabel(request application.UIApprovalRequest) string {
	resourceName := sanitizeExternalText(request.Target.Name, 253)
	if request.Target.Namespace != "" {
		resourceName = sanitizeExternalText(request.Target.Namespace, 63) + "/" + resourceName
	}
	parts := []string{
		sanitizeExternalText(request.Target.Kind, 63) + " " + resourceName,
		"API " + sanitizeExternalText(request.Target.APIVersion, 253),
		"UID " + sanitizeExternalText(request.Target.UID, 1024),
		"resource version " + sanitizeExternalText(request.Target.ResourceVersion, 1024),
	}
	if request.TargetSubresource != "" {
		parts = append(parts, "subresource "+sanitizeExternalText(request.TargetSubresource, 64))
	}
	if request.TemplateFingerprint != "" {
		parts = append(parts, "fingerprint "+sanitizeExternalText(request.TemplateFingerprint, 1024))
	}
	if request.DeploymentGeneration > 0 {
		parts = append(parts, fmt.Sprintf("generation %d", request.DeploymentGeneration))
	}
	if request.TargetRevision > 0 {
		parts = append(parts, fmt.Sprintf("revision %d", request.TargetRevision))
	}
	if request.TargetSetDigest.Valid() {
		parts = append(parts, "target set "+string(request.TargetSetDigest), fmt.Sprintf("target count %d", request.TargetCount))
	}
	return strings.Join(parts, " · ")
}

func (model *Model) acceptReviewerEvent(event application.UIReviewerEvent) bool {
	if !model.run.Active || model.run.Terminal || event.RunID != model.run.RunID ||
		event.ScopeGeneration != model.run.ScopeGeneration || event.PolicyGeneration != model.run.PolicyGeneration {
		return false
	}
	if model.actionPresentation == nil {
		if event.EventIndex != 1 || !model.acceptInitialActionSequence(event.Sequence) {
			return false
		}
		model.actionPresentation = &actionPresentation{
			RequestID: event.RequestID, Digest: event.Digest, Sequence: event.Sequence,
		}
	} else if model.reviewerEvent == nil || model.actionPresentation.RequestID != event.RequestID ||
		model.actionPresentation.Sequence != event.Sequence || !model.actionPresentation.Digest.Equal(event.Digest) ||
		event.EventIndex != model.reviewerEvent.EventIndex+1 {
		return false
	}
	copy := event
	model.reviewerEvent = &copy
	return true
}

func (model *Model) acceptAutomaticActionEvent(
	requestID domain.ApprovalID,
	digest domain.ApprovalDigest,
	sequence int64,
	executionIndex int64,
) bool {
	if !model.run.Active || model.run.Terminal {
		return false
	}
	if model.actionPresentation == nil {
		if executionIndex != 1 || !model.acceptInitialActionSequence(sequence) {
			return false
		}
		model.actionPresentation = &actionPresentation{RequestID: requestID, Digest: digest, Sequence: sequence}
	} else if model.actionPresentation.RequestID != requestID || model.actionPresentation.Sequence != sequence ||
		!model.actionPresentation.Digest.Equal(digest) || executionIndex != model.actionPresentation.ExecutionIndex+1 {
		return false
	}
	model.actionPresentation.ExecutionIndex = executionIndex
	return true
}

func (model *Model) acceptClosedActionEvent(result application.UIApprovalResult) bool {
	if !model.run.Active || model.run.Terminal || result.RunID != model.run.RunID ||
		result.ScopeGeneration != model.run.ScopeGeneration || result.PolicyGeneration != model.run.PolicyGeneration {
		return false
	}
	if model.actionPresentation == nil {
		if !model.acceptInitialActionSequence(result.Sequence) {
			return false
		}
		return true
	}
	return model.actionPresentation.RequestID == result.RequestID &&
		model.actionPresentation.Sequence == result.Sequence && model.actionPresentation.Digest.Equal(result.Digest)
}

// Approval and Reviewer events may be inserted into the ordered Agent event
// bridge or originate while a Tool call is blocked for supervision. The
// latter have an independent action-local sequence and must not create a gap
// in the Agent stream.
func (model *Model) acceptInitialActionSequence(sequence int64) bool {
	if sequence < 1 || sequence > 4096 || sequence <= model.run.LastActionSequence {
		return false
	}
	model.run.LastActionSequence = sequence
	if sequence == model.run.LastSequence+1 {
		model.run.LastSequence = sequence
	}
	return true
}

func reviewerEventNotice(status application.UIReviewerStatus) string {
	identity := reviewerIdentity(status)
	rationale := sanitizeExternalText(status.RationaleSummary, 2048)
	suffix := ""
	if rationale != "" {
		suffix = " Rationale: " + rationale
	}
	switch status.State {
	case application.UIReviewerReviewing:
		return "Reviewer " + identity + ". No action has started."
	case application.UIReviewerApproved:
		return "Reviewer " + identity + ". This automated recommendation is not human approval." + suffix
	case application.UIReviewerDenied:
		return "Reviewer " + identity + ". No action was started." + suffix
	case application.UIReviewerEscalated:
		return "Reviewer " + identity + ". A local user decision is now required; no action has started." + suffix
	case application.UIReviewerTimedOut:
		return "Reviewer " + identity + ". The timeout authorized nothing and no action was started."
	default:
		return "Reviewer state unavailable. No action was started."
	}
}

func (model *Model) clearActionPresentation() {
	model.reviewerEvent = nil
	model.actionPresentation = nil
}

func (model *Model) acceptWorkingTick(message WorkingTickMsg) tea.Cmd {
	if !model.run.Active || model.run.Terminal || message.Terminal ||
		message.RunID != model.run.RunID || message.ScopeGeneration != model.run.ScopeGeneration ||
		message.Sequence < 1 || message.Sequence > model.run.LastSequence {
		return nil
	}
	at := message.At.UTC().Truncate(time.Millisecond)
	if at.IsZero() || at.UnixMilli() < 0 || at.Before(model.run.StartedAt) ||
		(!model.workingAt.IsZero() && !at.After(model.workingAt)) {
		return nil
	}
	model.workingAt = at
	model.workingFrame++
	return workingTick(model.run)
}

func approvalOperationLabel(operation domain.ApprovalOperation) string {
	switch operation {
	case domain.ActionOperationRestartDeployment:
		return "Restart Deployment"
	case domain.ActionOperationScaleWorkload:
		return "Scale Workload"
	case domain.ActionOperationRollbackDeployment:
		return "Rollback Deployment"
	case domain.ActionOperationDeleteOwnedPod:
		return "Delete Owned Pod"
	case domain.ActionOperationCordonNode:
		return "Cordon Node"
	case domain.ActionOperationUncordonNode:
		return "Uncordon Node"
	case domain.ActionOperationDrainNode:
		return "Drain Node"
	case domain.ActionOperationRestrictedLocalArgv:
		return "Run Restricted Command"
	case domain.ActionOperationShell:
		return "Run Restricted Shell"
	case domain.ActionOperationContainerFileRead:
		return "Read Container File"
	case domain.ActionOperationPodDiagnostic:
		return "Run Pod Diagnostic"
	case domain.ActionOperationPodExec:
		return "Run Pod Exec"
	case domain.ActionOperationDiagnosticPod:
		return "Run Diagnostic Pod"
	case domain.ActionOperationLogsCurrent:
		return "Read Current Pod Logs"
	case domain.ActionOperationLogsPrevious:
		return "Read Previous Pod Logs"
	case domain.ActionOperationLogsAllContainers:
		return "Read All-container Pod Logs"
	case domain.ActionOperationLogSearch:
		return "Search Pod Logs"
	case domain.ActionOperationPrometheusQuery:
		return "Query Prometheus"
	case domain.ActionOperationLokiQuery:
		return "Query Loki"
	default:
		return "Unavailable operation"
	}
}

func approvalExecutionStatus(result application.UIApprovalResult) string {
	if result.Execution != nil {
		return restartExecutionStatus(*result.Execution)
	}
	if result.ActionExecution == nil {
		return "The action result is unavailable."
	}
	execution := result.ActionExecution
	if execution.Authorization != nil {
		return "The exact Tool approval authority was consumed. The Tool must pass final scope, policy, expiry, and cancellation checks before one bounded attempt; completion is reported by the Tool step."
	}
	if execution.LocalProcess != nil {
		switch execution.LocalProcess.State {
		case domain.LocalProcessExited:
			if execution.LocalProcess.ExitCode == 0 {
				return "The exact local process exited successfully. This does not verify any remote state change."
			}
			return "The exact local process exited non-zero and will not be retried."
		case domain.LocalProcessOutputBlocked:
			return "The exact local process exited, but its sensitive output was blocked and the action is reported as failed."
		case domain.LocalProcessFailed:
			return "The local process failed definitively and will not be retried."
		case domain.LocalProcessUnknown:
			return "The local process outcome is unknown and will not be retried automatically."
		default:
			return "The approved local process was not started and the approval cannot be reused."
		}
	}
	resultValue := execution.Remediation
	if resultValue == nil {
		return "The action result is unavailable."
	}
	switch resultValue.Attempt.State {
	case domain.RemediationUnknown:
		return "The Kubernetes request outcome is unknown and will not be retried automatically."
	case domain.RemediationFailed:
		return "The Kubernetes request failed definitively and will not be retried."
	case domain.RemediationNotAttempted:
		return "The approved Kubernetes action was not attempted and the approval cannot be reused."
	}
	if resultValue.Verification == domain.RemediationVerified {
		return "Kubernetes accepted the exact request and bounded verification succeeded."
	}
	if resultValue.Verification == domain.RemediationVerificationUnavailable {
		return "Kubernetes accepted the request, but verification became unavailable. No retry was attempted."
	}
	return "Kubernetes accepted the request, but bounded verification failed. No retry was attempted."
}

func restartExecutionStatus(execution application.UIRestartExecution) string {
	switch execution.State {
	case application.UIRestartPatchAccepted:
		return fmt.Sprintf("Restart request accepted by Kubernetes. Verifying Deployment generation %d with %d target replicas.", execution.TargetGeneration, execution.TargetReplicas)
	case application.UIRestartPatchFailed:
		return "The restart request failed and will not be retried."
	case application.UIRestartPatchOutcomeUnknown:
		return "The restart request outcome is unknown. It will not be retried automatically."
	case application.UIRestartNotAttempted:
		return "The approved restart was not attempted. The approval cannot be reused."
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
		return "Restart request accepted; rollout verification timed out without claiming success or request failure."
	case application.UIRestartRolloutFailed:
		return "Restart request accepted; rollout failed because " + restartRolloutFailureLabel(execution.FailureCode) + "."
	case application.UIRestartRolloutUnavailable:
		return "Restart request accepted; rollout verification became unavailable. The request will not be retried."
	case application.UIRestartResultAuditFailed:
		return "High-priority error: the write result audit could not be stored after bounded retries."
	default:
		return "The restart result is unavailable."
	}
}

func restartRolloutFailureLabel(code application.RestartRolloutFailureCode) string {
	switch code {
	case application.RestartRolloutFailureProgressDeadline:
		return "the progress deadline was exceeded"
	case application.RestartRolloutFailureReplica:
		return "the Deployment reported a replica failure"
	case application.RestartRolloutFailureTargetReplaced:
		return "the Deployment was replaced"
	case application.RestartRolloutFailureTargetChanged:
		return "the Deployment changed during verification"
	default:
		return "verification reported a safe failure"
	}
}
