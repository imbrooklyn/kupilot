package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func selectionTestModel(t *testing.T) (Model, int, int) {
	t.Helper()
	model := newTestModel()
	model.terminalClipboard = true
	model.transcript.StartAgent()
	model.transcript.FinishCommittedAgent("Select \u4e2d\u6587 e\u0301 text.")
	model.reflow()
	content := model.selectableTranscript()
	for row, line := range strings.Split(content, "\n") {
		if prefix, _, found := strings.Cut(ansi.Strip(line), "Select"); found {
			return model, ansi.StringWidth(prefix), row
		}
	}
	t.Fatalf("no selectable answer in %q", content)
	return Model{}, 0, 0
}

func TestTranscriptDragCopiesSafeUnicodeWithoutEditingComposer(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, copyKey := range []bool{false, true} {
			model, x, y := selectionTestModel(t)
			model.composer.SetValue("untouched draft")
			left, right := x, x+ansi.StringWidth("Select \u4e2d\u6587 e\u0301")
			if reverse {
				left, right = right, left
			}
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: left, Y: y})
			model, _ = updateModel(t, model, tea.MouseMotionMsg{Button: tea.MouseLeft, X: right, Y: y})
			model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: right, Y: y})
			want := "Select \u4e2d\u6587 e\u0301"
			if got := model.selectedTranscriptText(); got != want {
				t.Fatalf("selection = %q, want %q", got, want)
			}
			if got := ansi.Strip(model.View().Content); got != ansi.Strip(model.render()) {
				t.Fatal("selection highlighting changed layout or visible text")
			}
			var copied string
			model.copyToClipboard = func(id uint64, text string) tea.Cmd {
				copied = text
				return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Copied: true} }
			}
			var gesture tea.Msg = tea.MouseClickMsg{Button: tea.MouseRight, X: x, Y: y}
			if copyKey {
				gesture = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
			}
			model, cmd := updateModel(t, model, gesture)
			if cmd == nil || copied != want || model.composer.Value() != "untouched draft" || model.textSelection.content != "" {
				t.Fatalf("copy gesture changed draft, lost selection, or emitted no copy: %q", copied)
			}
			model, _ = updateModel(t, model, cmd())
			if model.composer.Value() != "untouched draft" {
				t.Fatal("copy completion changed composer")
			}
		}
	}
}

func TestTranscriptSelectionInvalidatesOnDisplayChanges(t *testing.T) {
	for _, kind := range []string{"wheel", "resize", "blur", "escape", "dialog", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
			var msg tea.Msg
			switch kind {
			case "wheel":
				msg = tea.MouseWheelMsg{Button: tea.MouseWheelUp}
			case "resize":
				msg = tea.WindowSizeMsg{Width: model.width, Height: model.height}
			case "blur":
				msg = tea.BlurMsg{}
			case "escape":
				msg = tea.KeyPressMsg{Code: tea.KeyEscape}
			case "dialog":
				model.showDialog("Dialog", "New display")
				msg = tea.MouseMotionMsg{}
			case "replacement":
				model.transcript.AppendNotice("Replacement display")
				msg = tea.MouseMotionMsg{}
			}
			model, cmd := updateModel(t, model, msg)
			if cmd != nil || model.textSelection.content != "" {
				t.Fatal("display change retained stale coordinates or emitted work")
			}
		})
	}
}

func TestWheelOverComposerNeverEditsDraftOrHistory(t *testing.T) {
	for _, draft := range []string{"", "draft", "first\nsecond\nthird"} {
		model := newTestModel()
		model.composer.RecordSubmission("must not be recalled")
		model.composer.SetValue(draft)
		for range 40 {
			model.transcript.AppendNotice("Scrollable conversation")
		}
		model.reflow()
		_, composerY, _ := model.renderLayout()
		for _, event := range []tea.Msg{
			tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: composerY},
			tea.MouseMotionMsg{Button: tea.MouseLeft, X: 8, Y: composerY},
			tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 8, Y: composerY},
		} {
			var cmd tea.Cmd
			model, cmd = updateModel(t, model, event)
			if cmd != nil || model.composer.Value() != draft || model.textSelection.content != "" {
				t.Fatal("mouse gesture in composer changed input or selected hidden text")
			}
		}
		bottom := model.transcript.ScrollOffset()
		for range 4 {
			var cmd tea.Cmd
			model, cmd = updateModel(t, model, tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 4, Y: composerY})
			if cmd != nil || model.composer.Value() != draft {
				t.Fatal("wheel over composer emitted work or changed draft/history")
			}
		}
		if model.transcript.ScrollOffset() >= bottom {
			t.Fatal("wheel over composer did not scroll transcript")
		}
	}
}

func TestTranscriptSelectionPreservesIndentationAndWholeGraphemes(t *testing.T) {
	model := newTestModel()
	model.textSelection = transcriptTextSelection{
		content: "  \u4e2d\u6587\n    e\u0301 \U0001f469\u200d\U0001f4bb", startX: 0, startY: 0, endX: 7, endY: 1,
	}
	if got, want := model.selectedTranscriptText(), "  \u4e2d\u6587\n    e\u0301 \U0001f469\u200d\U0001f4bb"; got != want {
		t.Fatalf("selection = %q, want %q", got, want)
	}
	left, right := selectionColumns("\u4e2d\u6587", 1, 3)
	if left != 0 || right != 4 {
		t.Fatalf("wide glyph endpoints = %d/%d, want 0/4", left, right)
	}
}
