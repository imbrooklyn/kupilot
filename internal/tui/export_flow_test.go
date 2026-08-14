package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestPrivacyExportUsesOnlyTheRootComposerAndExplicitConfirmation(t *testing.T) {
	privacy, lifecycle := exportFlowReview(t, domain.PrivacyModeStandard)
	model, requestID := openExportPrivacy(t, privacy, lifecycle)

	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'e'})
	if cmd != nil || model.sessionExport == nil || model.sessionExport.Stage != sessionExportTargetEntry ||
		model.dialog.Open() || model.EditorCount() != 1 || model.FocusedEditorCount() != 1 {
		t.Fatalf("export target entry state = %#v command=%v dialog=%v editors=%d/%d",
			model.sessionExport, cmd != nil, model.dialog.Open(), model.EditorCount(), model.FocusedEditorCount())
	}
	if !strings.Contains(model.render(), "absolute .md target") {
		t.Fatal("the root composer did not explain the explicit export target")
	}

	target := "/private/export/session-summary.md"
	model, _ = updateModel(t, model, tea.PasteMsg{Content: target})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.sessionExport == nil || model.sessionExport.Stage != sessionExportConfirmation ||
		!model.dialog.Open() || model.EditorCount() != 1 || model.FocusedEditorCount() != 0 {
		t.Fatalf("export confirmation state = %#v command=%v dialog=%v editors=%d/%d",
			model.sessionExport, cmd != nil, model.dialog.Open(), model.EditorCount(), model.FocusedEditorCount())
	}
	frame := model.render()
	for _, want := range []string{
		target,
		application.ExportSummarySchemaVersion,
		"committed user and final assistant text",
		"four structured Diagnosis sections",
		"referenced Evidence summaries",
		"will not overwrite",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("export confirmation missing %q", want)
		}
	}
	for _, denied := range []string{"raw Tool results", "raw logs", "approval nonce"} {
		if !strings.Contains(frame, denied) {
			t.Fatalf("export confirmation did not disclose exclusion %q", denied)
		}
	}

	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
	command := applicationCommandFromCmd(t, cmd)
	if command.Kind != application.UICommandExportSession || command.RequestID != requestID ||
		command.PrivacyRevision != privacy.Revision || command.Export == nil ||
		command.Export.SessionID != testSessionID || command.Export.TargetPath != target ||
		!command.Export.ExpectedCurrent || !command.Export.Confirmed ||
		command.Export.SchemaVersion != application.ExportSummarySchemaVersion || model.pendingExportID != requestID {
		t.Fatalf("export command/state = %#v pending=%d", command, model.pendingExportID)
	}

	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandExportSession, RequestID: requestID,
		Export: &application.SessionExportResult{SessionID: testSessionID, SchemaVersion: application.ExportSummarySchemaVersion},
	}})
	if model.pendingExportID != 0 || model.sessionExport != nil || !strings.Contains(model.render(), "Session summary exported") ||
		strings.Contains(model.render(), target) {
		t.Fatalf("completed export retained authority or target: pending=%d state=%#v", model.pendingExportID, model.sessionExport)
	}
}

func TestPrivacyExportCancellationAndFailureDoNotReportSuccess(t *testing.T) {
	privacy, lifecycle := exportFlowReview(t, domain.PrivacyModeStandard)
	target := "/private/export/cancelled-summary.md"

	t.Run("target entry cancellation", func(t *testing.T) {
		model, _ := openExportPrivacy(t, privacy, lifecycle)
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e'})
		model, _ = updateModel(t, model, tea.PasteMsg{Content: target})
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
		if cmd != nil || model.sessionExport != nil || !model.dialog.Open() ||
			!strings.Contains(model.render(), "Privacy and local data") || strings.Contains(model.render(), target) {
			t.Fatalf("target cancellation state = %#v command=%v", model.sessionExport, cmd != nil)
		}
	})

	t.Run("confirmation cancellation", func(t *testing.T) {
		model, _ := openExportPrivacy(t, privacy, lifecycle)
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e'})
		model, _ = updateModel(t, model, tea.PasteMsg{Content: target})
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
		if cmd != nil || model.sessionExport != nil || !model.dialog.Open() || strings.Contains(model.render(), target) {
			t.Fatalf("confirmation cancellation state = %#v command=%v", model.sessionExport, cmd != nil)
		}
	})

	t.Run("application failure", func(t *testing.T) {
		model, requestID := pendingExportFlow(t, privacy, lifecycle, target)
		model, _ = updateModel(t, model, ApplicationFailureMsg{
			Command: application.UICommandExportSession, RequestID: requestID,
		})
		if model.pendingExportID != 0 || model.sessionExport != nil ||
			!strings.Contains(model.render(), "Session not exported") || strings.Contains(model.render(), target) ||
			strings.Contains(model.render(), "Session summary exported") {
			t.Fatalf("failed export state = pending %d state %#v", model.pendingExportID, model.sessionExport)
		}
	})

	t.Run("stale result", func(t *testing.T) {
		model, requestID := pendingExportFlow(t, privacy, lifecycle, target)
		model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
			Command: application.UICommandExportSession, RequestID: requestID + 1,
			Export: &application.SessionExportResult{SessionID: testSessionID, SchemaVersion: application.ExportSummarySchemaVersion},
		}})
		if model.pendingExportID != requestID || model.sessionExport == nil || strings.Contains(model.render(), "Session summary exported") {
			t.Fatal("stale export result changed request-bound state")
		}
	})
}

func TestPrivacyExportInvalidTargetReturnsToTheSameComposerWithoutACommand(t *testing.T) {
	privacy, lifecycle := exportFlowReview(t, domain.PrivacyModeStandard)
	model, _ := openExportPrivacy(t, privacy, lifecycle)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e'})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: " relative-summary.md"})

	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || model.sessionExport == nil || model.sessionExport.Stage != sessionExportTargetError ||
		!model.dialog.Open() || !strings.Contains(model.render(), "Export target unavailable") {
		t.Fatalf("invalid target state = %#v command=%v dialog=%v", model.sessionExport, cmd != nil, model.dialog.Open())
	}

	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil || model.sessionExport == nil || model.sessionExport.Stage != sessionExportTargetEntry ||
		model.dialog.Open() || model.EditorCount() != 1 || model.FocusedEditorCount() != 1 {
		t.Fatalf("target correction state = %#v command=%v dialog=%v editors=%d/%d",
			model.sessionExport, cmd != nil, model.dialog.Open(), model.EditorCount(), model.FocusedEditorCount())
	}
}

func TestPrivacyDoesNotOfferExportForMinimalPersistence(t *testing.T) {
	privacy, lifecycle := exportFlowReview(t, domain.PrivacyModeMinimal)
	model, _ := openExportPrivacy(t, privacy, lifecycle)
	if strings.Contains(model.render(), "E export") {
		t.Fatal("minimal persistence offered an unavailable cross-process export")
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'e'})
	if cmd != nil || model.sessionExport != nil || !model.dialog.Open() {
		t.Fatalf("minimal export key changed state: command=%v state=%#v", cmd != nil, model.sessionExport)
	}
}

func exportFlowReview(t *testing.T, mode domain.PrivacyMode) (application.PrivacyReview, application.SessionLifecycleReview) {
	t.Helper()
	manager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: new(tuiPrivacyStore), Origin: "https://model.example",
		Now: func() time.Time { return time.UnixMilli(32_000).UTC() },
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	privacy, err := manager.Review(context.Background())
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	return privacy, application.SessionLifecycleReview{
		CurrentSession:                 &application.UISessionState{ID: testSessionID, Title: "Current Session", PrivacyMode: mode},
		OperationalDetailRetentionDays: application.DefaultOperationalDetailRetentionDays,
		ReadAuditRetentionDays:         application.ReadAuditRetentionDays,
		WriteAuditRetentionDays:        application.WriteAuditRetentionDays,
		StandardContentUntilDeletion:   true,
	}
}

func openExportPrivacy(
	t *testing.T,
	privacy application.PrivacyReview,
	lifecycle application.SessionLifecycleReview,
) (Model, uint64) {
	t.Helper()
	model := newTestModel()
	model.session = SessionView{ID: testSessionID, Title: "Current Session"}
	model.privacyMode = lifecycle.CurrentSession.PrivacyMode
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/privacy"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	show := applicationCommandFromCmd(t, cmd)
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandShowPrivacy, RequestID: show.RequestID,
		Privacy: &privacy, Lifecycle: &lifecycle,
	}})
	return model, show.RequestID
}

func pendingExportFlow(
	t *testing.T,
	privacy application.PrivacyReview,
	lifecycle application.SessionLifecycleReview,
	target string,
) (Model, uint64) {
	t.Helper()
	model, requestID := openExportPrivacy(t, privacy, lifecycle)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e'})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: target})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
	_ = applicationCommandFromCmd(t, cmd)
	return model, requestID
}
