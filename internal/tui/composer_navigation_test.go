package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

func TestComposerLineNavigationRetainsFocusAfterEvidence(t *testing.T) {
	t.Parallel()

	for _, keys := range []struct {
		name       string
		start, end tea.KeyPressMsg
	}{
		{"control", tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}},
		{"home_end", tea.KeyPressMsg{Code: tea.KeyHome}, tea.KeyPressMsg{Code: tea.KeyEnd}},
		{"command_arrows", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModSuper}, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModSuper}},
	} {
		for _, state := range []string{"empty_history", "committed_evidence", "active_run_with_evidence"} {
			for _, draft := range []string{"", "draft", "first line\n\u4f60\u597d"} {
				t.Run(keys.name+"/"+state+"/"+draft, func(t *testing.T) {
					model := newTestModel()
					if state != "empty_history" {
						model, _ = modelWithEvidenceReference(t)
					}
					model.run.Active = state == "active_run_with_evidence"
					model.composer.SetValue(draft)
					entries := model.transcript.Entries()
					for _, event := range []tea.KeyPressMsg{keys.start, keyText("X"), keys.end, keyText("\u754c")} {
						var cmd tea.Cmd
						model, cmd = updateModel(t, model, event)
						if cmd != nil || model.FocusedEditorCount() != 1 || model.focus != FocusComposer ||
							model.transcript.EvidenceSelecting() || model.evidenceDialog.Open() || model.pendingEvidence.RequestID != 0 {
							t.Fatalf("key %s stole focus or emitted work: focused=%d selecting=%v command=%v",
								event.String(), model.FocusedEditorCount(), model.transcript.EvidenceSelecting(), cmd != nil)
						}
					}
					lastLine := strings.LastIndexByte(draft, '\n') + 1
					want := draft[:lastLine] + "X" + draft[lastLine:] + "\u754c"
					if model.composer.Value() != want || !reflect.DeepEqual(entries, model.transcript.Entries()) {
						t.Fatalf("line navigation changed the draft incorrectly or changed history: got %q, want %q", model.composer.Value(), want)
					}
				})
			}
		}
	}
}

func TestComposerWordAndCharacterBindingsStayInEditor(t *testing.T) {
	t.Parallel()

	for _, binding := range []struct {
		name        string
		left, right tea.KeyPressMsg
		leftResult  string
		rightResult string
	}{
		{"control_characters", tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}, "alpha betX a", "aX lpha beta"},
		{"meta_words", tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt}, tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt}, "alpha X beta", "alphaX  beta"},
		{"option_arrows", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt}, "alpha X beta", "alphaX  beta"},
		{"control_arrows", tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}, "alpha X beta", "alphaX  beta"},
	} {
		for _, direction := range []string{"left", "right"} {
			t.Run(binding.name+"/"+direction, func(t *testing.T) {
				model, _ := modelWithEvidenceReference(t)
				model.transcript.AppendLandmarkNotice("A previous run failed safely.", components.TranscriptLandmarkFailureUnknown)
				model.composer.SetValue("alpha beta")
				before := model.transcript.View()
				event, want := binding.left, binding.leftResult
				if direction == "right" {
					model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyHome})
					event, want = binding.right, binding.rightResult
				}
				model, cmd := updateModel(t, model, event)
				if cmd != nil || model.searchMode || model.historySearchMode || model.transcript.EvidenceSelecting() ||
					model.FocusedEditorCount() != 1 || model.transcript.View() != before {
					t.Fatalf("%s was intercepted outside the editor", event.String())
				}
				model, cmd = updateModel(t, model, keyText("X "))
				if cmd != nil || model.composer.Value() != want {
					t.Fatalf("%s insertion = %q, want %q", event.String(), model.composer.Value(), want)
				}
			})
		}
	}
}

func TestEvidenceShortcutExplicitlyOwnsAndRestoresComposer(t *testing.T) {
	t.Parallel()

	for _, closeKey := range []tea.KeyPressMsg{
		{Code: 'e', Mod: tea.ModAlt}, {Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl},
	} {
		t.Run(closeKey.String(), func(t *testing.T) {
			model, _ := modelWithEvidenceReference(t)
			model.composer.SetValue("draft")
			model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyLeft})
			model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModAlt})
			if cmd != nil || !model.transcript.EvidenceSelecting() || model.FocusedEditorCount() != 0 {
				t.Fatal("explicit observation inspection did not take local keyboard ownership")
			}
			model, cmd = updateModel(t, model, keyText("ignored"))
			if cmd != nil || model.composer.Value() != "draft" {
				t.Fatal("inspection allowed input into the hidden composer")
			}
			model, cmd = updateModel(t, model, closeKey)
			if cmd != nil || model.transcript.EvidenceSelecting() || model.FocusedEditorCount() != 1 {
				t.Fatal("closing inspection did not restore the composer")
			}
			model, cmd = updateModel(t, model, keyText("X"))
			if cmd != nil || model.composer.Value() != "drafXt" {
				t.Fatal("inspection failed to preserve the draft and insertion point")
			}
		})
	}

	model := newTestModel()
	model.composer.SetValue("draft")
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModAlt})
	if cmd != nil || model.FocusedEditorCount() != 1 || model.composer.Value() != "draft" || model.transcript.EvidenceSelecting() {
		t.Fatal("inspection without supporting observations changed focus or the draft")
	}
}

func TestComposerWordSelectionReplacesTextWithoutTranscriptNavigation(t *testing.T) {
	t.Parallel()

	for _, event := range []tea.KeyPressMsg{
		{Code: 'f', Mod: tea.ModAlt | tea.ModShift},
		{Code: tea.KeyRight, Mod: tea.ModAlt | tea.ModShift},
		{Code: tea.KeyRight, Mod: tea.ModCtrl | tea.ModShift},
		{Code: 'b', Mod: tea.ModAlt | tea.ModShift},
		{Code: tea.KeyLeft, Mod: tea.ModAlt | tea.ModShift},
		{Code: tea.KeyLeft, Mod: tea.ModCtrl | tea.ModShift},
	} {
		t.Run(event.String(), func(t *testing.T) {
			model, _ := modelWithEvidenceReference(t)
			model.transcript.AppendLandmarkNotice("Previous safe failure.", components.TranscriptLandmarkFailureUnknown)
			model.composer.SetValue("alpha beta")
			want := "alpha X"
			if event.Code == 'f' || event.Code == tea.KeyRight {
				model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyHome})
				want = "X beta"
			}
			transcript := model.transcript.View()
			model, cmd := updateModel(t, model, event)
			if cmd != nil || model.transcript.View() != transcript {
				t.Fatal("word selection navigated the transcript or emitted work")
			}
			model, cmd = updateModel(t, model, keyText("X"))
			if cmd != nil || model.composer.Value() != want || model.FocusedEditorCount() != 1 || model.dialog.Open() {
				t.Fatalf("word selection replacement = %q command=%t, want %q", model.composer.Value(), cmd != nil, want)
			}
		})
	}
}

func TestFailureNavigationUsesExplicitIssueKeys(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.AppendLandmarkNotice("First safe failure.", components.TranscriptLandmarkFailureUnknown)
	model.transcript.AppendLandmarkNotice("Second safe failure.", components.TranscriptLandmarkFailureUnknown)
	model.composer.SetValue("draft")
	for _, event := range []tea.KeyPressMsg{{Code: 'i', Mod: tea.ModAlt}, {Code: 'i', Mod: tea.ModAlt | tea.ModShift}} {
		var cmd tea.Cmd
		model, cmd = updateModel(t, model, event)
		view := model.transcript.View()
		marker := strings.Index(view, "Jump target")
		first := strings.Index(view, "First safe failure.")
		if cmd != nil || marker < 0 || model.composer.Value() != "draft" || model.FocusedEditorCount() != 1 {
			t.Fatal("explicit failure navigation lost the draft or emitted work")
		}
		if event.Mod == tea.ModAlt && marker < first || event.Mod&tea.ModShift != 0 && marker > first {
			t.Fatal("failure navigation selected the wrong direction")
		}
	}
}

func TestComposerNavigationCannotBypassFocusOwners(t *testing.T) {
	t.Parallel()

	for _, owner := range []string{"terminal_blur", "modal", "approval", "pending_submit"} {
		t.Run(owner, func(t *testing.T) {
			model, _ := modelWithEvidenceReference(t)
			model.composer.SetValue("draft")
			switch owner {
			case "terminal_blur":
				model, _ = updateModel(t, model, tea.BlurMsg{})
			case "modal":
				model.showDialog("Help", "Local help.")
			case "approval":
				now := time.UnixMilli(1_700_000_600_000).UTC()
				model = newTestModel()
				model.now = func() time.Time { return now }
				model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
				model.composer.SetValue("draft")
				request := testUIApprovalRequest(t, now, 2)
				model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
					Kind: application.UIEventApprovalRequested, RunID: testRunID,
					ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Approval: &request,
				}})
			case "pending_submit":
				model.pendingSubmitID = 1
			}
			focused := model.FocusedEditorCount()
			for _, event := range []tea.KeyPressMsg{
				{Code: 'a', Mod: tea.ModCtrl}, {Code: 'e', Mod: tea.ModCtrl},
				{Code: 'b', Mod: tea.ModCtrl}, {Code: 'f', Mod: tea.ModCtrl},
				{Code: 'b', Mod: tea.ModAlt}, {Code: 'f', Mod: tea.ModAlt},
				{Code: tea.KeyLeft, Mod: tea.ModCtrl}, {Code: tea.KeyRight, Mod: tea.ModCtrl},
				{Code: tea.KeyLeft, Mod: tea.ModAlt | tea.ModShift}, {Code: tea.KeyRight, Mod: tea.ModAlt | tea.ModShift},
				{Code: 's', Mod: tea.ModAlt}, {Code: 'i', Mod: tea.ModAlt},
				{Code: tea.KeyLeft, Mod: tea.ModSuper}, {Code: tea.KeyRight, Mod: tea.ModSuper},
				{Code: 'e', Mod: tea.ModAlt}, keyText("ignored"),
			} {
				var cmd tea.Cmd
				model, cmd = updateModel(t, model, event)
				if cmd != nil || model.FocusedEditorCount() != focused || model.composer.Value() != "draft" ||
					model.transcript.EvidenceSelecting() || model.approvalDialog.Submitted() || model.approvalDialog.ApproveSelected() {
					t.Fatalf("key %s bypassed %s", event.String(), owner)
				}
			}
		})
	}
}
