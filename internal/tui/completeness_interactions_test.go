package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestSubmittedInputReverseSearchAcceptsAndCancelRestoresDraft(t *testing.T) {
	model := newTestModel()
	model.composer.RecordSubmission("first committed question")
	model.composer.RecordSubmission("second committed question")
	model.composer.SetValue("draft to restore")

	model, command := model.beginSubmittedHistorySearch()
	if command != nil || !model.historySearchMode || model.composer.Value() != "second committed question" {
		t.Fatalf("opened history search = mode %v value %q", model.historySearchMode, model.composer.Value())
	}
	model, _ = model.updateSubmittedHistorySearchKey(keyText("first"))
	if model.composer.Value() != "first committed question" || len(model.historySearchMatches) != 1 {
		t.Fatalf("filtered history search = %q/%v", model.composer.Value(), model.historySearchMatches)
	}
	model, _ = model.updateSubmittedHistorySearchKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.historySearchMode || model.composer.Value() != "draft to restore" {
		t.Fatalf("cancelled history search = mode %v value %q", model.historySearchMode, model.composer.Value())
	}

	model, _ = model.beginSubmittedHistorySearch()
	model, _ = model.updateSubmittedHistorySearchKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.historySearchMode || model.composer.Value() != "second committed question" {
		t.Fatalf("accepted history search = mode %v value %q", model.historySearchMode, model.composer.Value())
	}
}

func TestSubmittedInputReverseSearchEnforcesQueryBoundAndRejectsMultilinePaste(t *testing.T) {
	model := newTestModel()
	model.composer.RecordSubmission("committed input")
	model, _ = model.beginSubmittedHistorySearch()
	model, _ = model.updateSubmittedHistorySearchPaste(tea.PasteMsg{Content: strings.Repeat("x", MaxSubmittedHistorySearchQueryBytes)})
	if len(model.historySearchQuery) != MaxSubmittedHistorySearchQueryBytes {
		t.Fatalf("exact query bytes = %d", len(model.historySearchQuery))
	}
	model, _ = model.updateSubmittedHistorySearchKey(keyText("x"))
	if len(model.historySearchQuery) != MaxSubmittedHistorySearchQueryBytes {
		t.Fatal("one-over query changed bounded state")
	}
	model, _ = model.updateSubmittedHistorySearchPaste(tea.PasteMsg{Content: "line one\nline two"})
	if len(model.historySearchQuery) != MaxSubmittedHistorySearchQueryBytes {
		t.Fatal("multiline paste changed reverse-search state")
	}
}

func TestSlashAvailabilityIsContentFreeAndTerminalCapabilityBound(t *testing.T) {
	model := newTestModel()
	copyCommand, _ := findSlashCommand("copy")
	availability := model.slashAvailability(copyCommand)
	if availability.State != SlashUnsupportedTerminal || strings.Contains(availability.Reason, string(testSessionID)) {
		t.Fatalf("unsupported copy availability = %#v", availability)
	}
	model.terminalCapabilities.OSC52 = TerminalCapabilityAvailable
	model.terminalClipboard = true
	availability = model.slashAvailability(copyCommand)
	if availability.State != SlashNotApplicable {
		t.Fatalf("copy without committed answer = %#v", availability)
	}
	model.transcript.StartAgent()
	model.transcript.FinishCommittedAgent("safe final")
	availability = model.slashAvailability(copyCommand)
	if availability.State != SlashAvailable {
		t.Fatalf("copy with verified sink and answer = %#v", availability)
	}

	deleteCommand, _ := findSlashCommand("delete")
	model.run.Active = true
	availability = model.slashAvailability(deleteCommand)
	if availability.State != SlashBusy || availability.Reason == "" {
		t.Fatalf("active deletion availability = %#v", availability)
	}
	model.run.Active = false
	model.pendingSubmitID = 41
	availability = model.slashAvailability(deleteCommand)
	if availability.State != SlashBusy || availability.Reason == "" {
		t.Fatalf("starting deletion availability = %#v", availability)
	}
	model.pendingSubmitID = 0
	model.pendingConversation = &pendingConversationInput{}
	availability = model.slashAvailability(deleteCommand)
	if availability.State != SlashBusy || availability.Reason == "" {
		t.Fatalf("commit-barrier deletion availability = %#v", availability)
	}

	model.pendingConversation = nil
	model.pendingSubmitID = 0
	model.run.Active = true
	modelCommand, _ := findSlashCommand("model")
	availability = model.slashAvailability(modelCommand)
	if availability.State != SlashBusy || availability.Reason == "" {
		t.Fatalf("active model-configuration availability = %#v", availability)
	}
}

func TestDoctorRenderingUsesOnlyTypedRedactedProjection(t *testing.T) {
	model := newTestModel()
	canary := "forbidden-session-title-canary"
	result, err := application.NewDoctorResult("dev", "v3", domain.SHA256Hex("origin"), true, false,
		application.SessionStorageHealth{SchemaRevision: 16, SessionCount: 2})
	if err != nil {
		t.Fatalf("NewDoctorResult() error = %v", err)
	}
	model.session.Title = canary
	model.showDoctor(result)
	view := model.View().Content
	if strings.Contains(view, canary) || !strings.Contains(view, "No model, Kubernetes, Tool") || !strings.Contains(view, "Reviewer, approval, process") ||
		!strings.Contains(view, "protocol_continuation_unavailable") || !strings.Contains(view, "eino_adk v0.9.19") ||
		!strings.Contains(view, "eino_openai v0.1.13") || !strings.Contains(view, "live conformance not_run") {
		t.Fatalf("doctor view was not bounded/redacted: %q", view)
	}
}
