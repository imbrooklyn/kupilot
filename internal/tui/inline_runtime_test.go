package tui

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
)

type inlineRuntimeHarness struct {
	model    Model
	commands []tea.Cmd
}

type delegatingInlineRuntimeHarness struct {
	model   Model
	command tea.Cmd
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

func (harness inlineRuntimeHarness) Init() tea.Cmd {
	commands := append([]tea.Cmd(nil), harness.commands...)
	commands = append(commands, func() tea.Msg { return tea.Quit() })
	return tea.Sequence(commands...)
}

func (harness inlineRuntimeHarness) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return harness, nil
}

func (harness inlineRuntimeHarness) View() tea.View { return harness.model.View() }

func (harness delegatingInlineRuntimeHarness) Init() tea.Cmd { return harness.command }

func (harness delegatingInlineRuntimeHarness) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	next, command := harness.model.Update(message)
	updated, ok := next.(Model)
	if ok {
		harness.model = updated
	}
	return harness, command
}

func (harness delegatingInlineRuntimeHarness) View() tea.View { return harness.model.View() }

func TestInlineRuntimeWritesCommittedConversationWithoutAlternateScreen(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "test-context", Namespace: "test-namespace", Generation: 1, ReadOnly: true},
	})
	model.transcript.AppendUser("How many Nodes are Ready?")
	model.transcript.StartAgent()
	model.transcript.FinishAgent("Three Nodes are Ready.")
	block := model.transcript.CommitReady()
	if block == "" {
		t.Fatal("completed conversation did not produce a scrollback block")
	}
	model.reflow()

	rendered := runInlineRuntime(t, model, tea.Println(block))
	if !strings.Contains(rendered, "How many Nodes are Ready?") || !strings.Contains(rendered, "Three Nodes are Ready.") {
		t.Fatalf("committed conversation was absent from terminal output: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[?1049h") || strings.Contains(rendered, "\x1b[?1049l") {
		t.Fatalf("inline conversation entered the alternate screen: %q", rendered)
	}
}

func TestInlineRuntimeExcludesUnsafeExternalControlsFromScrollback(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "safe\x1b]52;c;question-canary\x07 tail"})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)
	model, userCommit := updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, agentCommit := updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCompleted, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Text: "Ready\x1b]52;c;answer-canary\x07.",
	}})
	if !commandSequencesPrintBeforeNext(userCommit) || !commandPrintsAbove(agentCommit) {
		t.Fatal("safe terminal entries did not produce scrollback commands")
	}
	rendered := runInlineRuntime(t, model, userCommit, agentCommit)
	if strings.Contains(rendered, "question-canary") || strings.Contains(rendered, "answer-canary") ||
		strings.Contains(rendered, "\x1b]52;") {
		t.Fatalf("unsafe external terminal content reached scrollback: %q", rendered)
	}
	if !strings.Contains(rendered, "safe tail") || !strings.Contains(rendered, "Ready.") {
		t.Fatalf("safe external terminal content was lost: %q", rendered)
	}
}

func TestInlineRuntimeCommitsTerminalCancellationBeforeExit(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model.quitAfterCancel = true
	rendered := runDelegatingInlineRuntime(t, model, func() tea.Msg {
		return ApplicationEventMsg{Event: application.UIEvent{
			Kind: application.UIEventRunCancelled, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2, Text: "The diagnostic run was cancelled.",
		}}
	})
	if !strings.Contains(rendered, "The diagnostic run was cancelled.") {
		t.Fatalf("terminal cancellation was absent from scrollback before exit: %q", rendered)
	}
	if strings.Contains(rendered, "\x1b[?1049h") || strings.Contains(rendered, "\x1b[?1049l") {
		t.Fatalf("terminal cancellation entered the alternate screen: %q", rendered)
	}
}

func runInlineRuntime(t *testing.T, model Model, commands ...tea.Cmd) string {
	t.Helper()
	var output synchronizedBuffer
	program := tea.NewProgram(
		inlineRuntimeHarness{model: model, commands: commands},
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
	)
	if _, err := program.Run(); err != nil {
		t.Fatalf("inline Bubble Tea runtime failed: %v", err)
	}
	return output.String()
}

func runDelegatingInlineRuntime(t *testing.T, model Model, command tea.Cmd) string {
	t.Helper()
	var output synchronizedBuffer
	program := tea.NewProgram(
		delegatingInlineRuntimeHarness{model: model, command: command},
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
	)
	if _, err := program.Run(); err != nil {
		t.Fatalf("delegating inline Bubble Tea runtime failed: %v", err)
	}
	return output.String()
}
