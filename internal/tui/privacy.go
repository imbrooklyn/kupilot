package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type sessionDeleteOrigin uint8

const (
	deleteFromPrivacy sessionDeleteOrigin = iota + 1
	deleteFromResumePicker
)

type sessionDeleteState struct {
	SessionID       domain.SessionID
	Title           string
	ExpectedCurrent bool
	Origin          sessionDeleteOrigin
}

type localDeletionKind uint8

const (
	clearLocalHistory localDeletionKind = iota + 1
	deleteAllLocalState
)

type localDeletionState struct {
	Kind localDeletionKind
}

type sessionExportStage uint8

const (
	sessionExportTargetEntry sessionExportStage = iota + 1
	sessionExportTargetError
	sessionExportConfirmation
)

type sessionExportState struct {
	SessionID       domain.SessionID
	PrivacyRequest  uint64
	PrivacyRevision string
	TargetPath      string
	Stage           sessionExportStage
}

func (model *Model) beginCurrentSessionExport() {
	if model.lifecycleReview == nil || model.lifecycleReview.CurrentSession == nil || model.privacyReview == nil ||
		model.pendingPrivacyID == 0 || model.lifecycleReview.CurrentSession.PrivacyMode != domain.PrivacyModeStandard {
		return
	}
	current := model.lifecycleReview.CurrentSession
	model.sessionExport = &sessionExportState{
		SessionID: current.ID, PrivacyRequest: model.pendingPrivacyID,
		PrivacyRevision: model.privacyReview.Revision, Stage: sessionExportTargetEntry,
	}
	model.dialog.Close()
	model.closePickers()
	model.slashMenu.Close()
	model.composer.Reset()
	model.composer.SetPlaceholder("Enter an explicit absolute .md target, then press Enter")
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
}

func (model *Model) cancelSessionExport() {
	model.sessionExport = nil
	model.pendingExportID = 0
	model.composer.Reset()
	model.composer.ResetPlaceholder()
	if model.privacyReview != nil && model.lifecycleReview != nil {
		model.showDialog("Privacy and local data", privacyReviewText(*model.privacyReview, model.lifecycleReview))
	} else {
		model.closeDialog()
	}
}

func sessionExportConfirmationText(state sessionExportState) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Target: %s\nSchema: %s\n\n", state.TargetPath, application.ExportSummarySchemaVersion)
	builder.WriteString("Included categories:\n")
	builder.WriteString("- safe Session display metadata and timestamps\n")
	builder.WriteString("- committed user and final assistant text after redaction and caps\n")
	builder.WriteString("- the four structured Diagnosis sections after redaction and caps\n")
	builder.WriteString("- referenced Evidence summaries or expired markers\n\n")
	builder.WriteString("Excluded categories include raw Tool inputs and raw Tool results, raw logs, complete prompts and model payloads, credentials, Secrets, and approval nonce or digest authority.\n\n")
	builder.WriteString("The export uses an owner-only file, will not overwrite an existing target, and is not encrypted. Deleting it does not guarantee forensic erasure.\n\nY confirms export. Esc or Enter cancels.")
	return builder.String()
}

func (model *Model) showSessionExportTargetError(message string) {
	if model.sessionExport == nil {
		return
	}
	state := *model.sessionExport
	state.Stage = sessionExportTargetError
	model.sessionExport = &state
	model.showDialog("Export target unavailable", message)
}

func (model Model) updateSessionExportTargetErrorKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.sessionExport == nil || model.sessionExport.Stage != sessionExportTargetError {
		return model, nil
	}
	if !key.Matches(message, model.keymap.Close) && !key.Matches(message, model.keymap.Submit) {
		return model, nil
	}
	state := *model.sessionExport
	state.Stage = sessionExportTargetEntry
	model.sessionExport = &state
	model.dialog.Close()
	model.composer.SetPlaceholder("Enter an explicit absolute .md target, then press Enter")
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
	return model, nil
}

func (model Model) updateSessionExportTargetKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.sessionExport == nil || model.sessionExport.Stage != sessionExportTargetEntry || model.pendingExportID != 0 {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Quit) {
		model.cancelSessionExport()
		return model, nil
	}
	if key.Matches(message, model.keymap.Newline) {
		model.showSessionExportTargetError("The export target must be one explicit single-line absolute .md path.")
		return model, nil
	}
	if key.Matches(message, model.keymap.Submit) {
		state := *model.sessionExport
		state.TargetPath = model.composer.Value()
		intent := application.ExportSummaryIntent{
			SessionID: state.SessionID, TargetPath: state.TargetPath,
			ExpectedCurrent: true, Confirmed: true, SchemaVersion: application.ExportSummarySchemaVersion,
		}
		if intent.Validate() != nil {
			model.showSessionExportTargetError("The export target must be one explicit single-line absolute .md path.")
			return model, nil
		}
		state.Stage = sessionExportConfirmation
		model.sessionExport = &state
		model.composer.Reset()
		model.composer.ResetPlaceholder()
		model.showDialog("Export redacted Session summary?", sessionExportConfirmationText(state))
		return model, nil
	}
	updated, cmd, err := model.composer.Update(message)
	if err != nil || len(updated.Value()) > 4096 {
		model.showSessionExportTargetError("The export target exceeds the fixed path limit.")
		return model, nil
	}
	model.composer = updated
	return model, cmd
}

func (model Model) updateSessionExportConfirmationKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.sessionExport == nil || model.sessionExport.Stage != sessionExportConfirmation || model.pendingExportID != 0 {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
		model.cancelSessionExport()
		return model, nil
	}
	if message.Code != 'y' && message.Code != 'Y' {
		return model, nil
	}
	state := *model.sessionExport
	command := application.UICommand{
		Kind: application.UICommandExportSession, RequestID: state.PrivacyRequest,
		PrivacyRevision: state.PrivacyRevision,
		Export: &application.ExportSummaryIntent{
			SessionID: state.SessionID, TargetPath: state.TargetPath,
			ExpectedCurrent: true, Confirmed: true, SchemaVersion: application.ExportSummarySchemaVersion,
		},
	}
	if command.Validate() != nil {
		model.cancelSessionExport()
		model.showDialog("Session not exported", "The export request could not be constructed safely.")
		return model, nil
	}
	model.pendingExportID = command.RequestID
	model.privacyPending = true
	model.dialog.Show("Exporting Session summary", "Waiting for the content-free audit record and atomic owner-only file publication.")
	return model, applicationCommand(command)
}

func (model *Model) clearSessionExportState() {
	model.pendingExportID = 0
	model.pendingPrivacyID = 0
	model.sessionExport = nil
	model.privacyReview = nil
	model.lifecycleReview = nil
	model.privacyPending = false
	model.composer.Reset()
	model.composer.ResetPlaceholder()
	model.dialog.Close()
}

func (model *Model) beginCurrentSessionDelete() {
	if model.lifecycleReview == nil || model.lifecycleReview.CurrentSession == nil {
		return
	}
	current := model.lifecycleReview.CurrentSession
	model.beginSessionDelete(sessionDeleteState{
		SessionID: current.ID, Title: current.Title, ExpectedCurrent: true, Origin: deleteFromPrivacy,
	})
}

func (model *Model) beginSelectedSessionDelete() {
	if model.activePicker != application.UICompletionSession {
		return
	}
	candidate, ok := model.sessionPicker.Selected()
	if !ok {
		return
	}
	model.beginSessionDelete(sessionDeleteState{
		SessionID: domain.SessionID(candidate.ID), Title: candidate.Title,
		ExpectedCurrent: model.session.ID != "" && model.session.ID == domain.SessionID(candidate.ID),
		Origin:          deleteFromResumePicker,
	})
}

func isSessionPickerDeleteKey(message tea.KeyPressMsg) bool {
	return message.Text == "D" || message.Code == 'D' ||
		message.ShiftedCode == 'D' && message.Mod&tea.ModShift != 0
}

func (model *Model) beginSessionDelete(state sessionDeleteState) {
	if !state.SessionID.Valid() || state.Origin != deleteFromPrivacy && state.Origin != deleteFromResumePicker {
		return
	}
	copy := state
	copy.Title = sanitizeExternalText(copy.Title, 512)
	model.sessionDelete = &copy
	title := "Delete selected Session?"
	if copy.ExpectedCurrent {
		title = "Delete current Session?"
	}
	model.showDialog(title, sessionDeleteConfirmationText(copy))
}

func sessionDeleteConfirmationText(state sessionDeleteState) string {
	var builder strings.Builder
	if state.Title != "" {
		fmt.Fprintf(&builder, "Session: %s\n", state.Title)
	}
	fmt.Fprintf(&builder, "Session ID: %s\n\n", state.SessionID)
	builder.WriteString("This removes the Session-owned Messages, runs, model metadata, Tool details, Evidence, Diagnoses, approvals, decisions, and linked read/write audit in one transaction.\n\n")
	if state.ExpectedCurrent {
		builder.WriteString("An active AgentRun and any pending or approved-not-executed approval must be cancelled first.\n\n")
	}
	builder.WriteString("Logical deletion does not guarantee forensic erasure from SQLite free pages, WAL, backups, snapshots, swap, or storage media.\n\nY confirms deletion. Esc or Enter cancels.")
	return builder.String()
}

func (model Model) updateSessionDeleteKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.sessionDelete == nil || model.pendingDeleteID != 0 {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
		origin := model.sessionDelete.Origin
		model.sessionDelete = nil
		if origin == deleteFromPrivacy && model.privacyReview != nil && model.lifecycleReview != nil {
			model.showDialog("Privacy and local data", privacyReviewText(*model.privacyReview, model.lifecycleReview))
		} else {
			model.closeDialog()
		}
		return model, nil
	}
	if message.Code != 'y' && message.Code != 'Y' {
		return model, nil
	}
	requestID := model.nextUIRequestID()
	state := *model.sessionDelete
	command := application.UICommand{
		Kind: application.UICommandDeleteSession, RequestID: requestID,
		Lifecycle: &application.SessionLifecycleIntent{
			SessionID: state.SessionID, ExpectedCurrent: state.ExpectedCurrent, Confirmed: true,
		},
	}
	if command.Validate() != nil {
		model.sessionDelete = nil
		model.showDialog("Session not deleted", "The deletion request could not be constructed safely.")
		return model, nil
	}
	model.pendingDeleteID = requestID
	model.dialog.Show("Deleting Session", "Waiting for Application to cancel unsafe state and commit the deletion transaction.")
	return model, applicationCommand(command)
}

func (model *Model) beginLocalDeletion(kind localDeletionKind) {
	if kind != clearLocalHistory && kind != deleteAllLocalState {
		return
	}
	model.localDeletion = &localDeletionState{Kind: kind}
	if kind == clearLocalHistory {
		model.showDialog("Clear all Session history?", "This removes every Session graph and all associated read/write audit in bounded transactions. Settings and valid model data-sharing consent remain. An active AgentRun and every pending or approved-not-executed approval must be cancelled first.\n\nLogical deletion does not guarantee forensic erasure from SQLite free pages, WAL, backups, snapshots, swap, or storage media.\n\nY confirms deletion. Esc or Enter cancels.")
		return
	}
	model.showDialog("Delete all local database state?", "This closes KuPilot local storage and removes only the validated database plus known SQLite journal, WAL, and shared-memory sidecars. Session history, settings, and model data-sharing consent are removed. Exported summaries, operational log files, backups, snapshots, swap, and storage media are outside this operation.\n\nKuPilot exits after storage closes, including after a partial file-removal failure. This does not guarantee forensic erasure.\n\nY confirms deletion and exit. Esc or Enter cancels.")
}

func (model Model) updateLocalDeletionKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.localDeletion == nil || model.pendingDeleteID != 0 {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
		model.localDeletion = nil
		if model.privacyReview != nil && model.lifecycleReview != nil {
			model.showDialog("Privacy and local data", privacyReviewText(*model.privacyReview, model.lifecycleReview))
		} else {
			model.closeDialog()
		}
		return model, nil
	}
	if message.Code != 'y' && message.Code != 'Y' {
		return model, nil
	}
	requestID := model.nextUIRequestID()
	kind := application.UICommandClearHistory
	title := "Clearing Session history"
	body := "Waiting for Application to cancel unsafe state and commit the bounded deletion transaction."
	if model.localDeletion.Kind == deleteAllLocalState {
		kind = application.UICommandDeleteAllLocalState
		title = "Deleting local database state"
		body = "Waiting for Application to cancel unsafe state, close storage, and remove the validated database files."
	}
	command := application.UICommand{
		Kind: kind, RequestID: requestID,
		Lifecycle: &application.SessionLifecycleIntent{Confirmed: true},
	}
	if command.Validate() != nil {
		model.localDeletion = nil
		model.showDialog("Local data not deleted", "The deletion request could not be constructed safely.")
		return model, nil
	}
	model.pendingDeleteID = requestID
	model.dialog.Show(title, body)
	return model, applicationCommand(command)
}

func nextRetentionDays(current int) (int, bool) {
	for _, candidate := range []int{30, 14, 7, 0} {
		if candidate < current {
			return candidate, true
		}
	}
	return 0, false
}

func cloneLifecycleReview(review application.SessionLifecycleReview) application.SessionLifecycleReview {
	copy := review
	if review.CurrentSession != nil {
		current := *review.CurrentSession
		copy.CurrentSession = &current
	}
	return copy
}
