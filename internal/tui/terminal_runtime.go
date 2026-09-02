package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const terminalFrameSettleDelay = 40 * time.Millisecond

// TerminalRuntime owns the renderer-only transition from retained safe
// transcript state to terminal scrollback. The core Model remains directly
// testable without turning presentation commits into Application commands.
type TerminalRuntime struct {
	model            Model
	cleared          bool
	sized            bool
	commitPending    bool
	insertionStarted bool
	pendingEnd       int
	pendingRows      int
	frameBarrier     func() tea.Cmd
}

type terminalReadyMsg struct{}
type terminalFrameSettledMsg struct{}
type terminalHistoryInsertionStartedMsg struct {
	end int
}
type terminalHistoryCommittedMsg struct {
	end  int
	rows int
}

// NewTerminalRuntime wraps one TUI Model for execution by Bubble Tea.
func NewTerminalRuntime(model Model) TerminalRuntime {
	return newTerminalRuntime(model, waitForTerminalFrame)
}

func newTerminalRuntime(model Model, frameBarrier func() tea.Cmd) TerminalRuntime {
	return TerminalRuntime{model: model, frameBarrier: frameBarrier}
}

func (runtime TerminalRuntime) Init() tea.Cmd {
	ready := func() tea.Msg { return terminalReadyMsg{} }
	return sequenceTerminalCommands(tea.ClearScreen, ready, runtime.model.Init())
}

func (runtime TerminalRuntime) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case terminalReadyMsg:
		runtime.cleared = true
		return runtime.commitHistory(nil)
	case terminalFrameSettledMsg:
		return runtime, nil
	case terminalHistoryInsertionStartedMsg:
		if runtime.commitPending && message.end == runtime.pendingEnd {
			runtime.insertionStarted = true
		}
		return runtime, nil
	case terminalHistoryCommittedMsg:
		if !runtime.commitPending || message.end != runtime.pendingEnd || message.rows != runtime.pendingRows ||
			!runtime.model.transcript.CompleteCommit(message.end) {
			return runtime, nil
		}
		runtime.commitPending = false
		runtime.insertionStarted = false
		runtime.pendingEnd = 0
		runtime.pendingRows = 0
		return runtime.commitHistory(nil)
	}
	previousHeight := runtime.model.height
	next, command := runtime.model.Update(message)
	updated, ok := next.(Model)
	if !ok {
		return runtime, command
	}
	runtime.model = updated
	if _, ok := message.(tea.WindowSizeMsg); ok {
		runtime.sized = true
		if previousHeight != runtime.model.height {
			runtime.model.reflow()
		}
	}
	if !runtime.cleared || !runtime.sized || runtime.commitPending {
		return runtime, command
	}
	return runtime.commitHistory(command)
}

func (runtime TerminalRuntime) commitHistory(command tea.Cmd) (tea.Model, tea.Cmd) {
	if !runtime.cleared || !runtime.sized || runtime.commitPending {
		return runtime, command
	}
	history, rows, end := runtime.model.transcript.PrepareCommit()
	if history == "" {
		return runtime, command
	}
	runtime.model.terminalHistoryRows += rows
	runtime.model.reflow()
	frameHeight := runtime.model.TerminalFrameHeight()
	if runtime.model.height <= 1 || frameHeight >= runtime.model.height {
		runtime.model.terminalHistoryRows = max(0, runtime.model.terminalHistoryRows-rows)
		_ = runtime.model.transcript.AbortCommit(end)
		runtime.model.reflow()
		return runtime, command
	}
	runtime.commitPending = true
	runtime.insertionStarted = false
	runtime.pendingEnd = end
	runtime.pendingRows = rows
	batches := terminalHistoryBatches(history, runtime.model.height-frameHeight)
	commands := make([]tea.Cmd, 0, 3+len(batches))
	started := terminalHistoryInsertionStartedMsg{end: end}
	commands = append(commands, func() tea.Msg { return started })
	commands = append(commands, runtime.frameBarrier())
	for _, batch := range batches {
		commands = append(commands, tea.Println(batch))
	}
	committed := terminalHistoryCommittedMsg{end: end, rows: rows}
	commands = append(commands, func() tea.Msg { return committed }, command)
	return runtime, sequenceTerminalCommands(commands...)
}

func (runtime TerminalRuntime) View() tea.View {
	view := runtime.model.View()
	if runtime.insertionStarted {
		// Bubble Tea's primary-screen history insertion resets the renderer's
		// physical cursor to the frame origin. Hiding it for the insertion and
		// restoring it after acknowledgement makes the next view observably
		// different, so the renderer moves the real cursor back to the composer.
		view.Cursor = nil
	}
	return view
}

// Model returns a copy of the final pure TUI state after Bubble Tea exits.
func (runtime TerminalRuntime) Model() Model {
	return runtime.model
}

// PendingTerminalTranscript returns a shutdown fallback only while terminal
// insertion has not begun. Once a batch may have reached scrollback, replaying
// the whole block would risk a permanent duplicate.
func (runtime TerminalRuntime) PendingTerminalTranscript() string {
	if runtime.commitPending && runtime.insertionStarted {
		return ""
	}
	return runtime.model.PendingTerminalTranscript()
}

func waitForTerminalFrame() tea.Cmd {
	return tea.Tick(terminalFrameSettleDelay, func(time.Time) tea.Msg {
		return terminalFrameSettledMsg{}
	})
}

func terminalHistoryBatches(history string, maxRows int) []string {
	maxRows = max(1, maxRows)
	lines := strings.Split(history, "\n")
	for index, line := range lines {
		// Bubble Tea ignores an entirely empty Println payload. Ordinary spaces
		// preserve intentional separator rows while remaining inert terminal text.
		if line == "" {
			lines[index] = " "
		}
	}
	batches := make([]string, 0, (len(lines)+maxRows-1)/maxRows)
	for start := 0; start < len(lines); start += maxRows {
		end := min(len(lines), start+maxRows)
		batches = append(batches, strings.Join(lines[start:end], "\n"))
	}
	return batches
}

func sequenceTerminalCommands(commands ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(commands))
	for _, command := range commands {
		if command != nil {
			filtered = append(filtered, command)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Sequence(filtered...)
	}
}
