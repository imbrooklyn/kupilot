package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func TestDialogCloseAndRepeatedResizePreserveConversation(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {32, 10}, {8, 3}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			model := newTestModel()
			model.transcript.AppendUser("Question marker")
			model.transcript.StartAgent()
			model.transcript.FinishCommittedAgent("Answer marker")
			history := model.TerminalTranscript()
			for range 3 {
				model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				model.showDialog("Temporary dialog", strings.Repeat("Temporary body\n", 100))
				view := model.View()
				if !view.AltScreen || lipgloss.Height(view.Content) > size[1] || lipgloss.Width(view.Content) >= size[0] {
					t.Fatalf("unbounded modal %dx%d", lipgloss.Width(view.Content), lipgloss.Height(view.Content))
				}
				model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
				if strings.Contains(model.View().Content, "Temporary") || strings.Contains(model.TerminalTranscript(), "Temporary") {
					t.Fatal("closed modal leaked")
				}
			}
			model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 80, Height: 24})
			if model.TerminalTranscript() != history {
				t.Fatal("resize or modal mutated history")
			}
		})
	}
}

func TestLongDialogScrollReachesLastLineWithoutBusinessCommand(t *testing.T) {
	model := newTestModel()
	var lines []string
	for index := range 60 {
		lines = append(lines, fmt.Sprintf("Line %02d", index))
	}
	model.showDialog("Details", strings.Join(lines, "\n"))
	if !strings.Contains(model.View().Content, "Line 00") {
		t.Fatal("first line unavailable")
	}
	for range 20 {
		var cmd tea.Cmd
		model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyPgDown})
		if cmd != nil {
			t.Fatal("dialog scrolling performed business I/O")
		}
	}
	if !strings.Contains(model.View().Content, "Line 59") || !strings.Contains(model.View().Content, "close") {
		t.Fatal("last line or close hint unavailable")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	model.showDialog("Details", strings.Join(lines, "\n"))
	if !strings.Contains(model.View().Content, "Line 00") {
		t.Fatal("new dialog retained old scroll offset")
	}
}

func TestShutdownProjectionExcludesProvisionalAndCancelledContent(t *testing.T) {
	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Inspect checkout"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1, "Inspect checkout")})
	model.transcript.AppendAgent("PROVISIONAL_CANARY")
	if strings.Contains(model.TerminalTranscript(), "PROVISIONAL_CANARY") {
		t.Fatal("provisional answer entered shutdown output")
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runTerminalEvent(application.UIEventRunCancelled, 2, "The run was cancelled.")})
	if strings.Contains(model.TerminalTranscript(), "PROVISIONAL_CANARY") || !strings.Contains(model.TerminalTranscript(), "cancelled") {
		t.Fatal("cancelled projection was unsafe")
	}
	model.showDialog("Dialog canary", "Never persist this body")
	if strings.Contains(model.TerminalTranscript(), "canary") {
		t.Fatal("dialog entered history")
	}
}
