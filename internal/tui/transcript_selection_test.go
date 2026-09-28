package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/imbrooklyn/kupilot/internal/domain"
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
		for _, shortcut := range []struct {
			goos string
			mod  tea.KeyMod
		}{
			{"darwin", 0}, {"darwin", tea.ModSuper},
			{"linux", tea.ModCtrl | tea.ModShift}, {"linux", tea.ModCtrl}, {"linux", tea.ModSuper},
		} {
			model, x, y := selectionTestModel(t)
			model.keymap.Copy = transcriptCopyBinding(shortcut.goos)
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
			if shortcut.mod != 0 {
				gesture = tea.KeyPressMsg{Code: 'c', Mod: shortcut.mod}
			}
			model, cmd := updateModel(t, model, gesture)
			if cmd == nil || copied != want || model.composer.Value() != "untouched draft" || model.selectedTranscriptText() != want {
				t.Fatalf("copy gesture changed draft, lost selection, or emitted no copy: %q", copied)
			}
			before := model.transcript.View()
			model, _ = updateModel(t, model, cmd())
			if model.composer.Value() != "untouched draft" || model.selectedTranscriptText() != want || model.transcript.View() != before {
				t.Fatal("copy completion changed composer, selection or transcript")
			}
		}
	}
}

func TestTranscriptCopyShortcutsRespectPlatformAndModifiedKeyEncoding(t *testing.T) {
	for _, test := range []struct {
		name, goos string
		message    tea.Msg
		copy       bool
		clear      bool
	}{
		{"mac-command", "darwin", tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper}, true, false},
		{"mac-caps-lock", "darwin", tea.KeyPressMsg{Code: 'C', Mod: tea.ModSuper | tea.ModCapsLock}, true, false},
		{"mac-associated-text", "darwin", tea.KeyPressMsg{Code: 'c', Text: "c", Mod: tea.ModSuper}, true, false},
		{"mac-physical-key", "darwin", tea.KeyPressMsg{Code: '\u0441', BaseCode: 'c', Mod: tea.ModSuper}, true, false},
		{"mac-control-clears", "darwin", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, false, true},
		{"mac-key-release", "darwin", tea.KeyReleaseMsg{Code: 'c', Mod: tea.ModSuper}, false, false},
		{"linux-control-shift", "linux", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift}, true, false},
		{"linux-uppercase-control-shift", "linux", tea.KeyPressMsg{Code: 'C', Mod: tea.ModCtrl | tea.ModShift}, true, false},
		{"linux-legacy-uppercase", "linux", tea.KeyPressMsg{Code: 'C', Mod: tea.ModCtrl}, true, false},
		{"linux-control", "linux", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, true, false},
		{"linux-mac-ssh", "linux", tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			model.keymap.Copy = transcriptCopyBinding(test.goos)
			model.composer.SetValue("preserved draft")
			model.run.Active = true
			copies := 0
			model.copyToClipboard = func(id uint64, text string) tea.Cmd {
				copies++
				if text != "Select" {
					t.Fatalf("copied %q", text)
				}
				return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Copied: true} }
			}
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
			model, cmd := updateModel(t, model, test.message)
			if (copies == 1) != test.copy || (cmd != nil) != test.copy ||
				(model.selectedTranscriptText() == "") != test.clear ||
				model.composer.Value() != "preserved draft" || !model.run.Active || model.quitAfterCancel {
				t.Fatalf("copy shortcut changed run/draft or used the wrong route: copies=%d selection=%q", copies, model.selectedTranscriptText())
			}
		})
	}
}

func TestTranscriptCopyWithoutSelectionDoesNotEditOrInterrupt(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		model := newTestModel()
		model.keymap.Copy = transcriptCopyBinding(goos)
		model.composer.SetValue("preserved draft")
		model.run.Active = true
		model.copyToClipboard = func(uint64, string) tea.Cmd {
			t.Fatal("an empty selection wrote to the clipboard")
			return nil
		}
		modifier := tea.ModSuper
		if goos == "linux" {
			modifier = tea.ModCtrl | tea.ModShift
		}
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Text: "c", Mod: modifier})
		if cmd != nil || model.composer.Value() != "preserved draft" || !model.run.Active || model.quitAfterCancel {
			t.Fatal("an empty copy gesture edited the draft or interrupted work")
		}
		model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if cmd != nil || model.composer.Value() != "" || !model.run.Active || model.quitAfterCancel {
			t.Fatal("Ctrl+C without selection lost its draft-clear priority")
		}
	}
}

func TestTranscriptSelectionInvalidatesOnDisplayChanges(t *testing.T) {
	for _, kind := range []string{"resize", "escape", "dialog", "replacement", "typing"} {
		t.Run(kind, func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
			var msg tea.Msg
			switch kind {
			case "resize":
				msg = tea.WindowSizeMsg{Width: model.width + 1, Height: model.height}
			case "escape":
				msg = tea.KeyPressMsg{Code: tea.KeyEscape}
			case "dialog":
				model.showDialog("Dialog", "New display")
				msg = tea.MouseMotionMsg{}
			case "replacement":
				model.transcript.AppendNotice("Replacement display")
				msg = tea.MouseMotionMsg{}
			case "typing":
				msg = tea.KeyPressMsg{Code: 'x', Text: "x"}
			}
			model, cmd := updateModel(t, model, msg)
			if kind != "typing" && cmd != nil || model.textSelection.content != "" {
				t.Fatal("display change retained stale coordinates or emitted work")
			}
		})
	}
}

func TestTranscriptDoubleClickSelectsWordsAndExtendsWholeGraphemes(t *testing.T) {
	for _, sample := range []struct {
		name, word string
		x          int
	}{
		{"ascii", "Select", 2},
		{"wide", "\u4e2d", 8},
		{"combining", "e\u0301", 12},
	} {
		t.Run(sample.name, func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
			model.copyToClipboard = func(uint64, string) tea.Cmd {
				t.Fatal("double-clicking a word wrote to the clipboard")
				return nil
			}
			now := time.Unix(1_700_000_000, 0)
			model.now = func() time.Time { return now }
			point := x + sample.x
			for range 2 {
				model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: point, Y: y})
				model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: point, Y: y})
				now = now.Add(200 * time.Millisecond)
			}
			if got := model.selectedTranscriptText(); got != sample.word {
				t.Fatalf("double-click selected %q, want %q", got, sample.word)
			}
		})
	}
	for _, drag := range []struct {
		start, end int
		want       string
	}{
		{2, 12, "Select \u4e2d\u6587 e\u0301"},
		{16, 2, "Select \u4e2d\u6587 e\u0301 text"},
	} {
		model, x, y := selectionTestModel(t)
		model.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
		model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + drag.start, Y: y})
		model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + drag.start, Y: y})
		model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + drag.start, Y: y})
		model, _ = updateModel(t, model, tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + drag.end, Y: y})
		model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + drag.end, Y: y})
		if got := model.selectedTranscriptText(); got != drag.want {
			t.Fatalf("word drag split a word or grapheme: %q, want %q", got, drag.want)
		}
	}
}

func TestTranscriptDoubleClickHasBoundedTimingAndExactPosition(t *testing.T) {
	for _, test := range []struct {
		interval time.Duration
		move     int
		selected bool
	}{
		{transcriptDoubleClickInterval, 0, true},
		{transcriptDoubleClickInterval + time.Millisecond, 0, false},
		{-time.Millisecond, 0, false},
		{100 * time.Millisecond, 1, false},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.interval, test.move), func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			now := time.Unix(1_700_000_000, 0)
			model.now = func() time.Time { return now }
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
			now = now.Add(test.interval)
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + test.move, Y: y})
			model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + test.move, Y: y})
			if (model.selectedTranscriptText() != "") != test.selected {
				t.Fatal("a distant click was mistaken for a double-click, or the exact limit was denied")
			}
		})
	}
}

func TestTranscriptSelectionSurvivesFocusModifiersAndNoOpScrolling(t *testing.T) {
	model, x, y := selectionTestModel(t)
	model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
	for _, message := range []tea.Msg{
		tea.MouseWheelMsg{Button: tea.MouseWheelUp}, tea.MouseWheelMsg{Button: tea.MouseWheelDown},
		tea.MouseWheelMsg{Button: tea.MouseWheelLeft}, tea.MouseWheelMsg{Button: tea.MouseWheelRight},
		tea.WindowSizeMsg{Width: model.width, Height: model.height}, tea.BlurMsg{}, tea.FocusMsg{},
		tea.KeyPressMsg{Code: tea.KeyLeftSuper}, tea.KeyPressMsg{Code: tea.KeyRightCtrl},
		tea.KeyPressMsg{Code: tea.KeyLeftShift}, tea.KeyPressMsg{Code: tea.KeyRightAlt},
	} {
		model, _ = updateModel(t, model, message)
		if model.selectedTranscriptText() != "Select" {
			t.Fatalf("%T unexpectedly cleared the selection", message)
		}
	}
}

func TestNativeSelectionRequiresExplicitCopyAndSerializesLatestIntent(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			if native {
				model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
			}
			var copies []string
			model.copyToClipboard = func(id uint64, text string) tea.Cmd {
				copies = append(copies, text)
				return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Copied: true} }
			}
			model, _ = updateModel(t, model, tea.MouseMotionMsg{X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
			if len(copies) != 0 {
				t.Fatal("hover or an unfinished drag wrote to the clipboard")
			}
			// X10 releases do not identify the button; only a live left drag qualifies.
			model, release := updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseNone, X: x + 6, Y: y})
			if release != nil || len(copies) != 0 || model.selectedTranscriptText() != "Select" {
				t.Fatal("mouse release copied or lost the selection")
			}
			model, first := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
			if first == nil || len(copies) != 1 || copies[0] != "Select" {
				t.Fatal("explicit copy did not use the exact selection")
			}
			model, duplicate := updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseNone, X: x + 6, Y: y})
			model, repeat := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
			if duplicate != nil || repeat != nil || len(copies) != 1 || model.textSelection.copyQueued {
				t.Fatal("releasing twice or pressing copy during the same write duplicated it")
			}
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 12, Y: y})
			model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 13, Y: y})
			if len(copies) != 1 || model.textSelection.copyQueued {
				t.Fatal("selecting new text implicitly requested a copy")
			}
			model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
			if len(copies) != 1 || !model.textSelection.copyQueued {
				t.Fatal("overlapping writes were started or the latest selection was lost")
			}
			model, second := updateModel(t, model, first())
			if second == nil || len(copies) != 2 || copies[1] != "e\u0301" || model.selectedTranscriptText() != "e\u0301" {
				t.Fatal("the prior completion did not serialize the latest valid selection")
			}
			model, _ = updateModel(t, model, first())
			if model.selectionCopyHint() != "Copying selection" {
				t.Fatal("a stale acknowledgement marked a newer selection as copied")
			}
			model, _ = updateModel(t, model, second())
			if model.selectionCopyHint() != "Selection copied" || model.selectedTranscriptText() != "e\u0301" || len(copies) != 2 {
				t.Fatal("copy completion lost selection, repeated a write or failed to report success")
			}
		})
	}
}

func TestSelectionAfterCopyNeedsNewExplicitIntent(t *testing.T) {
	for _, newSelection := range []bool{false, true} {
		model, x, y := selectionTestModel(t)
		model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
		copies := 0
		model.copyToClipboard = func(id uint64, _ string) tea.Cmd {
			copies++
			return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Copied: true} }
		}
		model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		model, _ = updateModel(t, model, tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
		model, first := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
		if newSelection {
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 12, Y: y})
		}
		model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 13, Y: y})
		model, next := updateModel(t, model, first())
		if next != nil || copies != 1 || model.textSelection.copyQueued || model.textSelection.copyResult.RequestID != 0 ||
			model.selectedTranscriptText() == "" {
			t.Fatal("a changed selection reused an old copy request or acknowledgement")
		}
	}
}

func TestSelectionCopyCompletionCannotRetryInvalidatedIntent(t *testing.T) {
	for _, invalidate := range []string{"escape", "paste", "range", "replacement", "session"} {
		t.Run(invalidate, func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
			calls := 0
			model.copyToClipboard = func(id uint64, _ string) tea.Cmd {
				calls++
				return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Copied: true} }
			}
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
			model, first := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 12, Y: y})
			model, _ = updateModel(t, model, tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + 13, Y: y})
			model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
			if !model.textSelection.copyQueued {
				t.Fatal("the second explicit request was not queued")
			}
			switch invalidate {
			case "escape":
				model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
			case "paste":
				model, _ = updateModel(t, model, tea.PasteMsg{Content: "pasted draft"})
			case "range":
				model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 17, Y: y})
			case "replacement":
				model.transcript.AppendNotice("The selected display changed.")
			case "session":
				model.resetTranscript()
				model.session.ID = domain.SessionID("01900000-0000-7000-8000-000000000002")
				// Even identical visible text belongs to a new selection gesture.
				model.transcript.StartAgent()
				model.transcript.FinishCommittedAgent("Select \u4e2d\u6587 e\u0301 text.")
				model.reflow()
			}
			model, next := updateModel(t, model, first())
			if next != nil || calls != 1 || model.textSelection.copyQueued ||
				invalidate != "range" && model.selectedTranscriptText() != "" {
				t.Fatal("completion copied an invalidated selection")
			}
		})
	}
}

func TestSelectionCopySerializesNewSessionGestureAfterOldWrite(t *testing.T) {
	model, x, y := selectionTestModel(t)
	model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
	var copies []string
	model.copyToClipboard = func(id uint64, text string) tea.Cmd {
		copies = append(copies, text)
		return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Copied: true} }
	}
	model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
	model, first := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
	model.resetTranscript()
	model.session.ID = domain.SessionID("01900000-0000-7000-8000-000000000002")
	model.transcript.StartAgent()
	model.transcript.FinishCommittedAgent("Select \u4e2d\u6587 e\u0301 text.")
	model.reflow()
	model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 12, Y: y})
	model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 13, Y: y})
	model, concurrent := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
	if concurrent != nil || len(copies) != 1 {
		t.Fatal("a Session switch allowed concurrent clipboard writes")
	}
	model, second := updateModel(t, model, first())
	if second == nil || len(copies) != 2 || copies[1] != "e\u0301" || model.pendingClipboardSession != model.session.ID {
		t.Fatal("the old Session completion swallowed a new explicit selection")
	}
	model, _ = updateModel(t, model, second())
	if model.selectionCopyHint() != "Selection copied" || model.selectedTranscriptText() != "e\u0301" {
		t.Fatal("the new Session selection was not acknowledged independently")
	}
}

func TestNativeSelectionIgnoresEmptyClicksAndUnfinishedFocusLoss(t *testing.T) {
	model, x, y := selectionTestModel(t)
	model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
	writes := 0
	model.copyToClipboard = func(uint64, string) tea.Cmd { writes++; return nil }
	for _, event := range []tea.Msg{
		tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y},
		tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y},
		tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper},
		tea.MouseClickMsg{Button: tea.MouseLeft, X: x + 12, Y: y},
		tea.MouseMotionMsg{Button: tea.MouseLeft, X: x + 13, Y: y},
		tea.BlurMsg{}, tea.FocusMsg{},
		tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 13, Y: y},
	} {
		model, _ = updateModel(t, model, event)
	}
	if writes != 0 || model.pendingClipboardID != 0 {
		t.Fatal("an empty or interrupted gesture wrote to the clipboard")
	}
}

func TestSelectionCopyReportsFailureAndUnconfirmedDelivery(t *testing.T) {
	for _, requested := range []bool{false, true} {
		t.Run(fmt.Sprint(requested), func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			model.copyToClipboard = func(id uint64, _ string) tea.Cmd {
				return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Requested: requested} }
			}
			model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 6, Y: y})
			model, command := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModSuper})
			before := model.TerminalTranscript()
			model, next := updateModel(t, model, command())
			if next != nil || model.pendingClipboardID != 0 || model.TerminalTranscript() != before {
				t.Fatal("a failed or unconfirmed copy retried or changed the committed transcript")
			}
			if requested {
				if model.selectionCopyHint() != "Selection copy unconfirmed" || model.selectedTranscriptText() != "Select" {
					t.Fatal("unconfirmed delivery was reported as success or lost the selection")
				}
			} else if !model.dialog.Open() || !strings.Contains(model.dialog.View(80), "clipboard write failed") {
				t.Fatal("copy failure was not visible")
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
		for row, line := range strings.Split(model.selectableTranscript(), "\n") {
			if prefix, _, ok := strings.Cut(ansi.Strip(line), "Scrollable"); ok {
				x := ansi.StringWidth(prefix)
				model, _ = updateModel(t, model, tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: row})
				model, _ = updateModel(t, model, tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x + 6, Y: row})
				break
			}
		}
		if model.selectedTranscriptText() == "" {
			t.Fatal("test requires a visible selection before scrolling")
		}
		for range 4 {
			var cmd tea.Cmd
			model, cmd = updateModel(t, model, tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 4, Y: composerY})
			if cmd != nil || model.composer.Value() != draft || model.textSelection.content != "" {
				t.Fatal("wheel over composer changed draft/history, emitted work or kept a stale selection")
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
