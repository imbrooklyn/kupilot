package tui

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	enterAlternateScreen = "\x1b[?1049h"
	leaveAlternateScreen = "\x1b[?1049l"
)

type scriptedRuntimeMsg struct{ message tea.Msg }

type fullscreenRuntimeHarness struct {
	model  Model
	script []tea.Msg
	next   int
}

type synchronizedBuffer struct {
	mutex sync.Mutex
	data  bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.data.Write(data)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.data.String()
}

func (harness fullscreenRuntimeHarness) Init() tea.Cmd {
	return harness.nextScriptCommand()
}

func (harness fullscreenRuntimeHarness) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	scripted, ok := message.(scriptedRuntimeMsg)
	if !ok {
		if _, resize := message.(tea.WindowSizeMsg); resize {
			next, _ := harness.model.Update(message)
			if updated, valid := next.(Model); valid {
				harness.model = updated
			}
		}
		return harness, nil
	}

	next, _ := harness.model.Update(scripted.message)
	if updated, valid := next.(Model); valid {
		harness.model = updated
	}
	harness.next++
	return harness, harness.nextScriptCommand()
}

func (harness fullscreenRuntimeHarness) View() tea.View { return harness.model.View() }

func (harness fullscreenRuntimeHarness) nextScriptCommand() tea.Cmd {
	if harness.next >= len(harness.script) {
		return func() tea.Msg { return tea.Quit() }
	}
	message := harness.script[harness.next]
	return func() tea.Msg { return scriptedRuntimeMsg{message: message} }
}

func TestFullscreenRuntimeRestoresPrimaryScreenBeforeOneCompletedTranscript(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "How many Nodes are Ready?"})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)

	rendered, final, transcript := runFullscreenRuntime(t, model,
		runStartedEvent(1),
		application.UIEvent{
			Kind: application.UIEventToolStep, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2,
			ToolStep: &application.ToolStep{
				InvocationID: testInvocationID,
				Name:         domain.ToolNameGetClusterOverview,
				Purpose:      "Count the current cluster nodes.",
				Status:       application.ToolStepSucceeded,
			},
		},
		application.UIEvent{
			Kind: application.UIEventRunCompleted, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 3, Text: "Three Nodes are Ready.",
		},
	)
	if final.run.Status != "completed" || !final.run.Terminal {
		t.Fatalf("final runtime state = %#v", final.run)
	}
	liveFrame := final.View().Content
	for _, value := range []string{
		"How many Nodes are Ready?",
		"Three Nodes are Ready.",
		"Ask a question, or type / for commands",
		"Context test-context",
	} {
		if strings.Count(liveFrame, value) != 1 {
			t.Fatalf("live fullscreen count for %q was not one: %q", value, liveFrame)
		}
	}
	if got := lipgloss.Height(liveFrame); got != final.height {
		t.Fatalf("live fullscreen height = %d, want %d", got, final.height)
	}
	assertOneRestoredTranscript(t, rendered, transcript, final.width,
		"How many Nodes are Ready?",
		"Inspect cluster overview · done",
		"Three Nodes are Ready.",
		"Worked for",
	)
}

func TestFullscreenRuntimeExcludesUnsafeExternalControlsFromPrimaryTranscript(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "safe\x1b]52;c;question-canary\x07 tail"})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)

	rendered, _, transcript := runFullscreenRuntime(t, model,
		runStartedEvent(1),
		application.UIEvent{
			Kind: application.UIEventRunCompleted, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2, Text: "Ready\x1b]52;c;answer-canary\x07.",
		},
	)
	if strings.Contains(rendered, "question-canary") || strings.Contains(rendered, "answer-canary") ||
		strings.Contains(rendered, "\x1b]52;") || strings.Contains(transcript, "question-canary") ||
		strings.Contains(transcript, "answer-canary") {
		t.Fatalf("unsafe external terminal content reached runtime or post-restore output: %q", rendered)
	}
	if !strings.Contains(transcript, "safe tail") || !strings.Contains(transcript, "Ready.") {
		t.Fatalf("safe external terminal content was lost: %q", transcript)
	}
}

func TestFullscreenRuntimeKeepsTerminalCancellationForPostRestoreOutput(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Inspect the active run."})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model.quitAfterCancel = true

	_, _, transcript := runFullscreenRuntime(t, model, application.UIEvent{
		Kind: application.UIEventRunCancelled, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Text: "The diagnostic run was cancelled.",
	})
	if !strings.Contains(transcript, "The diagnostic run was cancelled.") ||
		strings.Contains(transcript, "Working (") || strings.Contains(transcript, "supervised") {
		t.Fatalf("terminal cancellation projection = %q", transcript)
	}
}

func runFullscreenRuntime(t *testing.T, model Model, events ...application.UIEvent) (string, Model, string) {
	t.Helper()
	script := make([]tea.Msg, 0, len(events))
	for _, event := range events {
		script = append(script, ApplicationEventMsg{Event: event})
	}
	var output synchronizedBuffer
	program := tea.NewProgram(
		fullscreenRuntimeHarness{model: model, script: script},
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
	)
	finalState, err := program.Run()
	if err != nil {
		t.Fatalf("fullscreen Bubble Tea runtime failed: %v", err)
	}
	finalHarness, ok := finalState.(fullscreenRuntimeHarness)
	if !ok {
		t.Fatalf("final runtime state = %T, want fullscreenRuntimeHarness", finalState)
	}
	transcript := finalHarness.model.TerminalTranscript()
	if transcript != "" {
		if _, err := fmt.Fprintln(&output, transcript); err != nil {
			t.Fatalf("post-restore transcript write failed: %v", err)
		}
	}
	return output.String(), finalHarness.model, transcript
}

func assertOneRestoredTranscript(t *testing.T, rendered, transcript string, width int, required ...string) {
	t.Helper()
	if strings.Count(rendered, enterAlternateScreen) != 1 || strings.Count(rendered, leaveAlternateScreen) != 1 {
		t.Fatalf("alternate-screen lifecycle was not exactly one enter/leave pair: %q", rendered)
	}
	leaveAt := strings.LastIndex(rendered, leaveAlternateScreen)
	if enterAt := strings.Index(rendered, enterAlternateScreen); enterAt < 0 || enterAt >= leaveAt {
		t.Fatalf("alternate-screen controls were out of order: enter=%d leave=%d", enterAt, leaveAt)
	}
	if transcript == "" || !strings.HasSuffix(rendered, transcript+"\n") {
		t.Fatalf("completed transcript was not written after primary-screen restoration: %q", rendered)
	}
	postRestore := rendered[leaveAt+len(leaveAlternateScreen):]
	for _, value := range required {
		if strings.Count(postRestore, value) != 1 {
			t.Fatalf("post-restore count for %q was not one: %q", value, postRestore)
		}
	}
	for _, forbidden := range []string{"Ask a question, or type / for commands", "Context test-context", "supervised", "Working ("} {
		if strings.Contains(postRestore, forbidden) {
			t.Fatalf("post-restore transcript contained live UI state %q: %q", forbidden, postRestore)
		}
	}
	if lines := strings.Split(transcript, "\n"); len(lines) > 20 {
		t.Fatalf("post-restore transcript replayed a full-height frame (%d lines): %q", len(lines), transcript)
	} else {
		for _, line := range lines {
			if got := lipgloss.Width(line); got >= width {
				t.Fatalf("post-restore line width = %d, want less than terminal width %d: %q", got, width, line)
			}
		}
	}
}
