package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type sessionDeleteOrigin uint8

const (
	deleteFromPrivacy sessionDeleteOrigin = iota + 1
	deleteFromResumePicker
	deleteFromSessionsPicker
	deleteFromCommand
)

type sessionDeleteState struct {
	SessionID       domain.SessionID
	Title           string
	ExpectedCurrent bool
	Origin          sessionDeleteOrigin
	Review          *application.SessionDeletionReview
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
	builder.WriteString("- the final Markdown answer and its Evidence and proposed-action metadata after redaction and caps\n")
	builder.WriteString("- referenced observation summaries or expired markers\n\n")
	builder.WriteString("Excluded categories include raw cluster-read requests and results, raw logs, complete prompts and model payloads, credentials, Secrets, and approval nonce or digest authority.\n\n")
	builder.WriteString("The export uses an owner-only file, will not overwrite an existing target, and is not encrypted. Deleting it does not guarantee forensic erasure.\n\nY confirms export. Esc, Ctrl+C, or Enter cancels.")
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
	if key.Matches(message, model.keymap.Quit) {
		model.cancelSessionExport()
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
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) ||
		key.Matches(message, model.keymap.Quit) {
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

func (model Model) beginCurrentSessionDeletion(origin sessionDeleteOrigin) (Model, tea.Cmd) {
	if model.session.ID == "" || origin != deleteFromPrivacy && origin != deleteFromCommand && origin != deleteFromSessionsPicker {
		return model, nil
	}
	return model.beginSessionDelete(sessionDeleteState{
		SessionID: model.session.ID, Title: model.session.Title, ExpectedCurrent: true, Origin: origin,
	})
}

func (model Model) beginSelectedSessionDelete() (Model, tea.Cmd) {
	if model.activePicker != application.UICompletionSession && model.activePicker != application.UICompletionSessionManagement {
		return model, nil
	}
	candidate, ok := model.sessionPicker.Selected()
	if !ok {
		return model, nil
	}
	if model.activePicker == application.UICompletionSessionManagement && !candidate.Deletable && !candidate.Current {
		model.showDialog("Deletion unavailable", "The selected Session is protected and was not added to a deletion plan.")
		return model, nil
	}
	origin := deleteFromResumePicker
	if model.activePicker == application.UICompletionSessionManagement {
		origin = deleteFromSessionsPicker
	}
	return model.beginSessionDelete(sessionDeleteState{
		SessionID: domain.SessionID(candidate.ID), Title: candidate.Title,
		ExpectedCurrent: model.session.ID != "" && model.session.ID == domain.SessionID(candidate.ID),
		Origin:          origin,
	})
}

func isSessionPickerDeleteKey(message tea.KeyPressMsg) bool {
	return message.Text == "D" || message.Code == 'D' ||
		message.ShiftedCode == 'D' && message.Mod&tea.ModShift != 0
}

func isSessionBatchDeleteKey(message tea.KeyPressMsg) bool {
	return message.Text == "B" || message.Code == 'B' ||
		message.ShiftedCode == 'B' && message.Mod&tea.ModShift != 0
}

func (model Model) beginBatchSessionDelete() (Model, tea.Cmd) {
	if model.activePicker != application.UICompletionSessionManagement {
		return model, nil
	}
	model.closePickers()
	model.slashMenu.Close()
	model.sessionDelete = &sessionDeleteState{Origin: deleteFromSessionsPicker}
	model.composer.Reset()
	model.composer.SetPlaceholder("Enter 1d, 2w, or a timezone-qualified RFC3339 cutoff")
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
	return model, nil
}

func (model Model) updateBatchSessionDeleteKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.sessionDelete == nil || model.sessionDelete.SessionID != "" || model.sessionDelete.Review != nil ||
		model.pendingDeleteID != 0 || model.dialog.Open() {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Quit) {
		model.sessionDelete = nil
		model.composer.Reset()
		model.composer.ResetPlaceholder()
		return model, model.openCompletion(application.UICompletionSessionManagement, "", resumeOriginNone)
	}
	if key.Matches(message, model.keymap.Newline) {
		model.showDialog("Invalid cutoff", "The inactive-Session cutoff must be one exact single-line relative duration or timezone-qualified RFC3339 value.")
		return model, nil
	}
	if key.Matches(message, model.keymap.Submit) {
		requestID := model.nextUIRequestID()
		command := application.UICommand{
			Kind: application.UICommandPreviewSessionDeletion, RequestID: requestID,
			Lifecycle: &application.SessionLifecycleIntent{
				DeletionKind: application.SessionDeletionBefore, Cutoff: model.composer.Value(),
				Limit: application.SessionDeleteDefaultLimit,
			},
		}
		if command.Validate() != nil {
			model.showDialog("Invalid cutoff", "Use a positive whole-number d/w duration or an RFC3339 timestamp with a timezone.")
			return model, nil
		}
		model.pendingDeleteID = requestID
		model.composer.Reset()
		model.composer.ResetPlaceholder()
		model.showDialog("Preparing batch preview", "Resolving one frozen UTC cutoff and bounded content-free selection. No data has been deleted.")
		return model, applicationCommand(command)
	}
	updated, command, err := model.composer.Update(message)
	if err == nil && len(updated.Value()) <= 64 {
		model.composer = updated
	}
	return model, command
}

func (model Model) beginSessionDelete(state sessionDeleteState) (Model, tea.Cmd) {
	if !state.SessionID.Valid() || state.Origin < deleteFromPrivacy || state.Origin > deleteFromCommand {
		return model, nil
	}
	copy := state
	copy.Title = sanitizeExternalText(copy.Title, 512)
	model.sessionDelete = &copy
	requestID := model.nextUIRequestID()
	command := application.UICommand{
		Kind: application.UICommandPreviewSessionDeletion, RequestID: requestID,
		Lifecycle: &application.SessionLifecycleIntent{
			SessionID: copy.SessionID, DeletionKind: application.SessionDeletionExact,
			Limit: 1,
		},
	}
	if command.Validate() != nil {
		model.sessionDelete = nil
		model.showDialog("Deletion unavailable", "The deletion preview could not be constructed safely.")
		return model, nil
	}
	model.pendingDeleteID = requestID
	model.showDialog("Preparing deletion preview", "Waiting for the bounded Application-owned Session snapshot. No data has been deleted.")
	return model, applicationCommand(command)
}

func sessionDeleteConfirmationText(state sessionDeleteState) string {
	if state.Review == nil {
		return "The deletion preview is unavailable."
	}
	if state.Review.Plan.Snapshot.Request.Kind == application.SessionDeletionBefore {
		return batchSessionDeleteConfirmationText(*state.Review)
	}
	if len(state.Review.Plan.Sessions) != 1 {
		return "The deletion preview is unavailable."
	}
	selected := state.Review.Plan.Sessions[0]
	var builder strings.Builder
	if selected.Title != "" {
		fmt.Fprintf(&builder, "Session: %s\n", sanitizeExternalText(selected.Title, 512))
	}
	fmt.Fprintf(&builder, "Session ID: %s\n", selected.ID)
	fmt.Fprintf(&builder, "Last active: %s · %s\n", relativeSessionActivity(selected.LastActivityAt, state.Review.Plan.Snapshot.Request.FrozenNow),
		selected.LastActivityAt.Local().Format(time.RFC3339Nano+" MST"))
	fmt.Fprintf(&builder, "Last active UTC: %s\n", selected.LastActivityAt.UTC().Format(time.RFC3339Nano))
	if state.Review.Current {
		fmt.Fprintf(&builder, "Editable in-memory input: %d item(s), %d bytes\n", state.Review.QueueItems, state.Review.QueueBytes)
	}
	builder.WriteString("\n")
	builder.WriteString("This removes the Session's messages, runs, model metadata, cluster-read details, observations, diagnoses, approvals, decisions, and linked read/write audit in one transaction.\n\n")
	builder.WriteString("It does not remove exported files, terminal scrollback, logs, backups, configuration, credentials, cache, or SQLite free pages. Logical deletion is not forensic erasure.\n\nY confirms deletion. Esc, Ctrl+C, or Enter cancels.")
	return builder.String()
}

func batchSessionDeleteConfirmationText(review application.SessionDeletionReview) string {
	plan := review.Plan
	request := plan.Snapshot.Request
	var builder strings.Builder
	fmt.Fprintf(&builder, "Frozen cutoff local: %s\n", request.Cutoff.Local().Format(time.RFC3339Nano+" MST"))
	fmt.Fprintf(&builder, "Frozen cutoff UTC: %s\n", request.Cutoff.UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(&builder, "Matched: %d · eligible: %d · protected: %d · selected: %d\n",
		plan.Snapshot.Matched, plan.Snapshot.Eligible, plan.Snapshot.Protected, len(plan.Sessions))
	fmt.Fprintf(&builder, "Batch limit: %d · selection digest: %s\n", request.Limit, plan.Digest)
	if plan.Oldest != nil && plan.Newest != nil {
		fmt.Fprintf(&builder, "Selected activity UTC: %s through %s\n", plan.Oldest.UTC().Format(time.RFC3339Nano), plan.Newest.UTC().Format(time.RFC3339Nano))
	}
	builder.WriteString("\nThe current Session, active runs, pending/approved actions, corrupt or future activity, exported files, terminal scrollback, logs, backups, configuration, credentials, cache, and SQLite free pages are excluded. Deletion is logical, not forensic.\n\n")
	if plan.Snapshot.OverLimit {
		builder.WriteString("The eligible set exceeds the bounded batch limit. No data can be deleted; use a narrower cutoff. Esc closes this preview.")
	} else if len(plan.Sessions) == 0 {
		builder.WriteString("No eligible Session was selected. No data was deleted. Esc closes this preview.")
	} else {
		builder.WriteString("Y confirms this exact snapshot. Esc, Ctrl+C, or Enter cancels.")
	}
	return builder.String()
}

func (model Model) updateSessionDeleteKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.sessionDelete == nil || model.pendingDeleteID != 0 || model.sessionDelete.Review == nil {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) ||
		key.Matches(message, model.keymap.Quit) {
		origin := model.sessionDelete.Origin
		wasBatch := model.sessionDelete.SessionID == ""
		model.sessionDelete = nil
		if origin == deleteFromPrivacy && model.privacyReview != nil && model.lifecycleReview != nil {
			model.showDialog("Privacy and local data", privacyReviewText(*model.privacyReview, model.lifecycleReview))
		} else {
			model.closeDialog()
		}
		if origin == deleteFromSessionsPicker && wasBatch {
			return model, model.openCompletion(application.UICompletionSessionManagement, "", resumeOriginNone)
		}
		return model, nil
	}
	if message.Code != 'y' && message.Code != 'Y' {
		return model, nil
	}
	requestID := model.nextUIRequestID()
	state := *model.sessionDelete
	if state.Review.Plan.Snapshot.OverLimit || len(state.Review.Plan.Sessions) == 0 {
		return model, nil
	}
	command := application.UICommand{
		Kind: application.UICommandDeleteSession, RequestID: requestID,
		Lifecycle: &application.SessionLifecycleIntent{
			Confirmed: true, DeletionPlan: &state.Review.Plan, Confirmation: state.Review.Plan.Digest,
		},
	}
	if command.Validate() != nil {
		model.sessionDelete = nil
		model.showDialog("Session not deleted", "The deletion request could not be constructed safely.")
		return model, nil
	}
	model.pendingDeleteID = requestID
	model.dialog.Show("Deleting Session", "Waiting for the exact snapshot recheck and all-or-nothing deletion transaction.")
	return model, applicationCommand(command)
}

func (model *Model) beginLocalDeletion(kind localDeletionKind) {
	if kind != clearLocalHistory && kind != deleteAllLocalState {
		return
	}
	model.localDeletion = &localDeletionState{Kind: kind}
	if kind == clearLocalHistory {
		model.showDialog("Clear all Session history?", "This removes every Session graph and all associated read/write audit in bounded transactions. Settings and valid model data-sharing consent remain. An active diagnostic run and every pending or approved-but-not-executed approval must be cancelled first.\n\nLogical deletion does not guarantee forensic erasure from SQLite free pages, WAL, backups, snapshots, swap, or storage media.\n\nY confirms deletion. Esc, Ctrl+C, or Enter cancels.")
		return
	}
	model.showDialog("Delete all local database state?", "This closes Kupilot local storage and removes only the validated database plus known SQLite journal, WAL, and shared-memory sidecars. Session history, settings, and model data-sharing consent are removed. Exported summaries, operational log files, backups, snapshots, swap, and storage media are outside this operation.\n\nKupilot exits after storage closes, including after a partial file-removal failure. This does not guarantee forensic erasure.\n\nY confirms deletion and exit. Esc, Ctrl+C, or Enter cancels.")
}

func (model Model) updateLocalDeletionKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.localDeletion == nil || model.pendingDeleteID != 0 {
		return model, nil
	}
	if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) ||
		key.Matches(message, model.keymap.Quit) {
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
