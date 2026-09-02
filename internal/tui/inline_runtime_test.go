package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type scriptedRuntimeMsg struct{ message tea.Msg }

type terminalRuntimeHarness struct {
	runtime TerminalRuntime
	script  []tea.Msg
	next    int
}

type rawMouseRuntimeHarness struct {
	runtime              TerminalRuntime
	wheelCount           int
	wheelReturnedCommand bool
	offsetBeforeWheel    int
}

type cursorRestoreRuntimeHarness struct {
	runtime TerminalRuntime
}

type synchronizedBuffer struct {
	mutex sync.Mutex
	data  bytes.Buffer
}

type cursorSequenceBuffer struct {
	mutex      sync.Mutex
	data       bytes.Buffer
	hidden     chan struct{}
	hiddenOnce sync.Once
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

func (buffer *cursorSequenceBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	written, err := buffer.data.Write(data)
	hidden := strings.Contains(buffer.data.String(), "\x1b[?25l")
	buffer.mutex.Unlock()
	if hidden {
		buffer.hiddenOnce.Do(func() { close(buffer.hidden) })
	}
	return written, err
}

func (buffer *cursorSequenceBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.data.String()
}

func (harness terminalRuntimeHarness) Init() tea.Cmd {
	return sequenceTerminalCommands(harness.runtime.Init(), harness.nextScriptCommand())
}

func (harness terminalRuntimeHarness) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	actual := message
	scripted, isScripted := message.(scriptedRuntimeMsg)
	if isScripted {
		actual = scripted.message
	}
	next, command := harness.runtime.Update(actual)
	updated, ok := next.(TerminalRuntime)
	if ok {
		harness.runtime = updated
	}
	if !isScripted {
		return harness, command
	}
	harness.next++
	return harness, sequenceTerminalCommands(command, harness.nextScriptCommand())
}

func (harness terminalRuntimeHarness) View() tea.View { return harness.runtime.View() }

func (harness terminalRuntimeHarness) nextScriptCommand() tea.Cmd {
	if harness.next >= len(harness.script) {
		return func() tea.Msg { return tea.Quit() }
	}
	message := harness.script[harness.next]
	return func() tea.Msg { return scriptedRuntimeMsg{message: message} }
}

func (harness rawMouseRuntimeHarness) Init() tea.Cmd { return harness.runtime.Init() }

func (harness rawMouseRuntimeHarness) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	next, command := harness.runtime.Update(message)
	if updated, ok := next.(TerminalRuntime); ok {
		harness.runtime = updated
	}
	if _, ok := message.(tea.MouseWheelMsg); ok {
		harness.wheelCount++
		harness.wheelReturnedCommand = command != nil
		return harness, tea.Quit
	}
	harness.offsetBeforeWheel = harness.runtime.Model().transcript.ScrollOffset()
	return harness, command
}

func (harness rawMouseRuntimeHarness) View() tea.View { return harness.runtime.View() }

func (harness cursorRestoreRuntimeHarness) Init() tea.Cmd { return harness.runtime.Init() }

func (harness cursorRestoreRuntimeHarness) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	next, command := harness.runtime.Update(message)
	if updated, ok := next.(TerminalRuntime); ok {
		harness.runtime = updated
	}
	if _, committed := message.(terminalHistoryCommittedMsg); committed {
		return harness, sequenceTerminalCommands(command, tea.Quit)
	}
	return harness, command
}

func (harness cursorRestoreRuntimeHarness) View() tea.View { return harness.runtime.View() }

func TestTerminalRuntimeCommitsHistoryOnceWithoutMouseOrAlternateScreen(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "How many Nodes are Ready?"})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)

	rendered, final := runTerminalRuntime(t, model,
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
	if final.PendingTerminalTranscript() != "" || final.terminalHistoryRows == 0 {
		t.Fatalf("terminal history was not fully committed: pending=%q rows=%d",
			final.PendingTerminalTranscript(), final.terminalHistoryRows)
	}
	for _, value := range []string{"How many Nodes are Ready?", "Three Nodes are Ready."} {
		if !strings.Contains(rendered, value) {
			t.Fatalf("terminal output did not receive committed history %q: %q", value, rendered)
		}
		if strings.Contains(final.View().Content, value) {
			t.Fatalf("committed history %q remained in the live renderer frame: %q", value, final.View().Content)
		}
	}
	for _, forbidden := range []string{
		"\x1b[?1049h", "\x1b[?1002h", "\x1b[?1003h", "\x1b[?1006h",
		"\x1b]12;", "\x1b[1 q", "\x1b[2 q", "\x1b[3 q", "\x1b[4 q", "\x1b[5 q", "\x1b[6 q",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("terminal runtime enabled an ownership-conflicting mode %q: %q", forbidden, rendered)
		}
	}
	if final.View().AltScreen || final.View().MouseMode != tea.MouseModeNone || final.View().OnMouse != nil {
		t.Fatalf("final live view captured terminal history or mouse input: %#v", final.View())
	}
	transcript := final.TerminalTranscript()
	if strings.Count(transcript, "How many Nodes are Ready?") != 1 ||
		strings.Count(transcript, "Three Nodes are Ready.") != 1 {
		t.Fatalf("retained safe transcript duplicated the completed turn: %q", transcript)
	}
}

func TestTerminalRuntimeWaitsForInitialClearBeforeHistoryCommit(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.AppendNotice("Startup notice.")
	runtime := newTerminalRuntime(model, immediateTerminalFrameBarrier)
	next, command := runtime.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	before, ok := next.(TerminalRuntime)
	if !ok {
		t.Fatalf("pre-clear runtime state = %T", next)
	}
	beforeModel := before.Model()
	if command != nil || beforeModel.PendingTerminalTranscript() == "" ||
		beforeModel.terminalHistoryRows != 0 {
		t.Fatalf("history committed before the clear gate: command=%v pending=%q rows=%d",
			command != nil, beforeModel.PendingTerminalTranscript(), beforeModel.terminalHistoryRows)
	}
	next, command = before.Update(terminalReadyMsg{})
	after, ok := next.(TerminalRuntime)
	afterModel := after.Model()
	if !ok || command == nil || afterModel.PendingTerminalTranscript() == "" ||
		afterModel.terminalHistoryRows == 0 || !after.commitPending ||
		strings.Contains(afterModel.View().Content, "Startup notice.") {
		t.Fatalf("history was not prepared after the clear gate: state=%T command=%v pending=%q rows=%d commit_pending=%t view=%q",
			next, command != nil, afterModel.PendingTerminalTranscript(), afterModel.terminalHistoryRows,
			after.commitPending, afterModel.View().Content)
	}
	next, _ = after.Update(terminalHistoryCommittedMsg{end: after.pendingEnd, rows: after.pendingRows})
	committed, ok := next.(TerminalRuntime)
	committedModel := committed.Model()
	if !ok || committed.commitPending || committedModel.PendingTerminalTranscript() != "" {
		t.Fatalf("prepared history was not acknowledged: state=%T pending=%t transcript=%q",
			next, committed.commitPending, committedModel.PendingTerminalTranscript())
	}
}

func TestTerminalRuntimeRestoresComposerCursorAfterHistoryInsertion(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.AppendNotice("Startup notice.")
	runtime := newTerminalRuntime(model, immediateTerminalFrameBarrier)
	next, _ := runtime.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	sized, ok := next.(TerminalRuntime)
	if !ok {
		t.Fatalf("sized runtime state = %T", next)
	}
	next, command := sized.Update(terminalReadyMsg{})
	prepared, ok := next.(TerminalRuntime)
	if !ok || command == nil || !prepared.commitPending || prepared.View().Cursor == nil {
		t.Fatalf("prepared cursor state = runtime %T command=%t pending=%t cursor=%#v",
			next, command != nil, prepared.commitPending, prepared.View().Cursor)
	}
	preparedPosition := prepared.View().Cursor.Position

	next, _ = prepared.Update(terminalHistoryInsertionStartedMsg{end: prepared.pendingEnd})
	inserting, ok := next.(TerminalRuntime)
	if !ok || !inserting.insertionStarted || inserting.View().Cursor != nil {
		t.Fatalf("insertion cursor state = runtime %T started=%t cursor=%#v",
			next, inserting.insertionStarted, inserting.View().Cursor)
	}

	next, _ = inserting.Update(terminalHistoryCommittedMsg{
		end: inserting.pendingEnd, rows: inserting.pendingRows,
	})
	committed, ok := next.(TerminalRuntime)
	if !ok {
		t.Fatalf("committed runtime state = %T", next)
	}
	cursor := committed.View().Cursor
	if committed.insertionStarted || committed.commitPending || cursor == nil ||
		cursor.Position != preparedPosition {
		t.Fatalf("committed cursor state = runtime %T started=%t pending=%t cursor=%#v",
			next, committed.insertionStarted, committed.commitPending, cursor)
	}
}

func TestTerminalRuntimeFlushesHiddenCursorBeforePrintingHistory(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.AppendNotice("Startup cursor fixture.")
	barrierReached := make(chan struct{})
	releaseBarrier := make(chan struct{})
	var barrierOnce sync.Once
	barrier := func() tea.Cmd {
		return func() tea.Msg {
			barrierOnce.Do(func() { close(barrierReached) })
			<-releaseBarrier
			return terminalFrameSettledMsg{}
		}
	}
	defer func() {
		select {
		case <-releaseBarrier:
		default:
			close(releaseBarrier)
		}
	}()

	output := &cursorSequenceBuffer{hidden: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	program := tea.NewProgram(
		cursorRestoreRuntimeHarness{runtime: newTerminalRuntime(model, barrier)},
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(output),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
	)
	type programResult struct {
		model tea.Model
		err   error
	}
	finished := make(chan programResult, 1)
	go func() {
		final, err := program.Run()
		finished <- programResult{model: final, err: err}
	}()

	select {
	case <-barrierReached:
	case <-ctx.Done():
		t.Fatal("terminal insertion did not reach the renderer-settle barrier")
	}
	select {
	case <-output.hidden:
	case <-ctx.Done():
		t.Fatal("terminal history insertion reached its barrier before hiding the physical cursor")
	}
	beforePrint := len(output.String())
	close(releaseBarrier)

	var result programResult
	select {
	case result = <-finished:
	case <-ctx.Done():
		t.Fatal("terminal insertion did not finish")
	}
	if result.err != nil {
		t.Fatalf("terminal runtime failed: %v", result.err)
	}
	final, ok := result.model.(cursorRestoreRuntimeHarness)
	if !ok || final.runtime.View().Cursor == nil {
		t.Fatalf("final runtime/cursor = %T/%#v", result.model, final.runtime.View().Cursor)
	}
	rendered := output.String()
	if beforePrint > len(rendered) {
		t.Fatalf("terminal output shrank from %d to %d bytes", beforePrint, len(rendered))
	}
	afterBarrier := rendered[beforePrint:]
	historyAt := strings.Index(afterBarrier, "Startup cursor fixture.")
	if historyAt < 0 || !strings.Contains(afterBarrier[historyAt:], "\x1b[?25h") {
		t.Fatalf("terminal output did not restore the cursor after inserted history: %q", afterBarrier)
	}
}

func TestTerminalRuntimeSettlesCompactFrameBeforePrintingCompletedTurn(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "List the cluster inventory."})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)
	userBlock, userRows := model.transcript.CommitReady()
	if userBlock == "" || userRows == 0 || !strings.HasSuffix(userBlock, "\n") {
		t.Fatal("submitted user history was not available")
	}
	model.terminalHistoryRows += userRows
	model.reflow()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID,
			Name:         domain.ToolNameGetClusterOverview,
			Purpose:      "List cluster inventory.",
			Status:       application.ToolStepSucceeded,
		},
	}})
	if active := model.View().Content; !strings.Contains(active, "List cluster inventory.") ||
		!strings.Contains(active, "Working (") {
		t.Fatalf("active frame did not contain live progress: %q", active)
	}

	runtime := newTerminalRuntime(model, immediateTerminalFrameBarrier)
	runtime.cleared = true
	runtime.sized = true
	next, command := runtime.Update(ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventRunCompleted, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 3,
		Text: "| Resource | State |\n|---|---|\n| node-a | Ready |",
	}})
	prepared, ok := next.(TerminalRuntime)
	if !ok || command == nil || !prepared.commitPending {
		t.Fatalf("completed turn was not staged: state=%T command=%t pending=%t",
			next, command != nil, prepared.commitPending)
	}
	if prepared.PendingTerminalTranscript() == "" {
		t.Fatal("prepared history lost its pre-insertion shutdown fallback")
	}
	frame := prepared.Model().View().Content
	if strings.Contains(frame, "List cluster inventory.") || strings.Contains(frame, "Working (") ||
		strings.Contains(frame, "Resource | State") {
		t.Fatalf("immutable or transient run content remained in the insertion frame: %q", frame)
	}
	frameHeight := prepared.Model().TerminalFrameHeight()
	if frameHeight >= prepared.Model().height {
		t.Fatalf("history insertion had no protected terminal row: frame=%d terminal=%d",
			frameHeight, prepared.Model().height)
	}
	if terminalFrameSettleDelay < 2*(time.Second/60) {
		t.Fatalf("terminal frame barrier = %s, want at least two 60 Hz frames", terminalFrameSettleDelay)
	}
	pending := prepared.Model()
	if history := pending.PendingTerminalTranscript(); !strings.HasSuffix(history, "\n") ||
		strings.HasSuffix(history, "\n\n") {
		t.Fatalf("completed history did not end in exactly one separator row: %q", history)
	}
	availableRows := pending.height - frameHeight
	batches := terminalHistoryBatches(pending.PendingTerminalTranscript(), availableRows)
	insertedRows := 0
	for _, batch := range batches {
		batchRows := 1 + strings.Count(batch, "\n")
		insertedRows += batchRows
		if batchRows > availableRows {
			t.Fatalf("history batch rows = %d, safe capacity = %d: %q", batchRows, availableRows, batch)
		}
		for _, line := range strings.Split(batch, "\n") {
			if width := lipgloss.Width(line); width >= pending.width {
				t.Fatalf("history row width = %d, terminal width = %d: %q", width, pending.width, line)
			}
		}
	}
	if insertedRows != prepared.pendingRows {
		t.Fatalf("terminal insertion rows = %d, want %d", insertedRows, prepared.pendingRows)
	}

	next, _ = prepared.Update(terminalHistoryInsertionStartedMsg{end: prepared.pendingEnd})
	inserting, ok := next.(TerminalRuntime)
	if !ok || !inserting.insertionStarted || inserting.PendingTerminalTranscript() != "" {
		t.Fatalf("ambiguous in-flight insertion retained a duplicate-prone fallback: state=%T started=%t fallback=%q",
			next, inserting.insertionStarted, inserting.PendingTerminalTranscript())
	}
	insertingModel := inserting.Model()
	if insertingModel.PendingTerminalTranscript() == "" {
		t.Fatal("in-flight insertion removed the recoverable source transcript")
	}

	next, _ = inserting.Update(terminalHistoryCommittedMsg{
		end: inserting.pendingEnd, rows: inserting.pendingRows,
	})
	committed, ok := next.(TerminalRuntime)
	committedModel := committed.Model()
	if !ok || committed.commitPending || committedModel.PendingTerminalTranscript() != "" {
		t.Fatalf("completed turn was not committed once: state=%T pending=%t transcript=%q",
			next, committed.commitPending, committedModel.PendingTerminalTranscript())
	}
	terminal := committedModel.TerminalTranscript()
	if strings.Count(terminal, "List cluster inventory.") != 1 ||
		strings.Count(terminal, "node-a") != 1 {
		t.Fatalf("completed terminal source was duplicated: %q", terminal)
	}
}

func TestTerminalRuntimeKeepsHistoryLiveWhenNoInsertionRowIsSafe(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.AppendNotice("Visible on a tiny terminal.")
	runtime := newTerminalRuntime(model, immediateTerminalFrameBarrier)
	next, _ := runtime.Update(tea.WindowSizeMsg{Width: 24, Height: 3})
	sized, ok := next.(TerminalRuntime)
	if !ok {
		t.Fatalf("sized runtime state = %T", next)
	}
	next, command := sized.Update(terminalReadyMsg{})
	blocked, ok := next.(TerminalRuntime)
	blockedModel := blocked.Model()
	if !ok || command != nil || blocked.commitPending || blockedModel.terminalHistoryRows != 0 ||
		blockedModel.View().Content == "" ||
		!strings.Contains(blockedModel.PendingTerminalTranscript(), "Visible on a tiny terminal.") {
		t.Fatalf("unsafe tiny-terminal insertion was not blocked: state=%T command=%t pending=%t rows=%d view=%q transcript=%q",
			next, command != nil, blocked.commitPending, blockedModel.terminalHistoryRows,
			blockedModel.View().Content, blockedModel.PendingTerminalTranscript())
	}
}

func TestTerminalHistoryBatchesPreserveRowsWithinProtectedCapacity(t *testing.T) {
	t.Parallel()

	batches := terminalHistoryBatches("one\n\nthree\nfour\nfive\nsix\nseven", 3)
	want := []string{"one\n \nthree", "four\nfive\nsix", "seven"}
	if len(batches) != len(want) {
		t.Fatalf("history batches = %#v, want %#v", batches, want)
	}
	for index := range want {
		if batches[index] != want[index] || 1+strings.Count(batches[index], "\n") > 3 {
			t.Fatalf("history batch %d = %q, want %q", index, batches[index], want[index])
		}
	}
}

func TestTerminalRuntimeBlocksUnsafeControlsBeforeHistoryInsertion(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "safe\x1b]52;c;question-canary\x07 tail"})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)

	rendered, final := runTerminalRuntime(t, model,
		runStartedEvent(1),
		application.UIEvent{
			Kind: application.UIEventRunCompleted, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2, Text: "Ready\x1b]52;c;answer-canary\x07.",
		},
	)
	if strings.Contains(rendered, "question-canary") || strings.Contains(rendered, "answer-canary") ||
		strings.Contains(rendered, "\x1b]52;") || strings.Contains(final.TerminalTranscript(), "canary") {
		t.Fatalf("unsafe external terminal content reached renderer output: %q", rendered)
	}
	if transcript := final.TerminalTranscript(); !strings.Contains(transcript, "safe tail") ||
		!strings.Contains(transcript, "Ready.") {
		t.Fatalf("safe external terminal content was lost: %q", transcript)
	}
}

func TestTerminalRuntimeCommitsTerminalCancellationWithoutLiveState(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Inspect the active run."})
	model, submit := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, submit)

	_, final := runTerminalRuntime(t, model,
		runStartedEvent(1),
		application.UIEvent{
			Kind: application.UIEventRunCancelled, RunID: testRunID,
			ScopeGeneration: 7, Sequence: 2, Text: "The diagnostic run was cancelled.",
		},
	)
	transcript := final.TerminalTranscript()
	if !strings.Contains(transcript, "The diagnostic run was cancelled.") ||
		strings.Contains(transcript, "Working (") || strings.Contains(transcript, "supervised") ||
		final.PendingTerminalTranscript() != "" {
		t.Fatalf("terminal cancellation projection = %q pending=%q", transcript, final.PendingTerminalTranscript())
	}
}

func TestTerminalRuntimeRawMouseInputIsInertAndReportingStaysDisabled(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.SetSize(40, 4)
	model.transcript.StartAgent()
	for index := range 24 {
		model.transcript.AppendNotice(strings.Repeat("x", index+1))
	}
	input := bytes.NewBufferString("\x1b[<64;1;1M")
	var output synchronizedBuffer
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	program := tea.NewProgram(
		rawMouseRuntimeHarness{
			runtime:           newTerminalRuntime(model, immediateTerminalFrameBarrier),
			offsetBeforeWheel: model.transcript.ScrollOffset(),
		},
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(&output),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
	)
	finalState, err := program.Run()
	if err != nil {
		t.Fatalf("raw mouse runtime failed: %v", err)
	}
	final, ok := finalState.(rawMouseRuntimeHarness)
	if !ok {
		t.Fatalf("final runtime state = %T, want rawMouseRuntimeHarness", finalState)
	}
	if final.wheelCount != 1 || final.wheelReturnedCommand ||
		final.runtime.Model().transcript.ScrollOffset() != final.offsetBeforeWheel {
		t.Fatalf("raw mouse input changed retained UI state: count=%d command=%v offset=%d want=%d",
			final.wheelCount, final.wheelReturnedCommand,
			final.runtime.Model().transcript.ScrollOffset(), final.offsetBeforeWheel)
	}
	for _, forbidden := range []string{"\x1b[?1002h", "\x1b[?1003h", "\x1b[?1006h"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("runtime enabled mouse reporting %q: %q", forbidden, output.String())
		}
	}
}

func runTerminalRuntime(t *testing.T, model Model, events ...application.UIEvent) (string, Model) {
	t.Helper()
	script := make([]tea.Msg, 0, len(events))
	for _, event := range events {
		script = append(script, ApplicationEventMsg{Event: event})
	}
	var output synchronizedBuffer
	program := tea.NewProgram(
		terminalRuntimeHarness{
			runtime: newTerminalRuntime(model, immediateTerminalFrameBarrier), script: script,
		},
		tea.WithInput(nil),
		tea.WithOutput(&output),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}),
		tea.WithWindowSize(80, 24),
		tea.WithoutSignalHandler(),
	)
	finalState, err := program.Run()
	if err != nil {
		t.Fatalf("terminal Bubble Tea runtime failed: %v", err)
	}
	finalHarness, ok := finalState.(terminalRuntimeHarness)
	if !ok {
		t.Fatalf("final runtime state = %T, want terminalRuntimeHarness", finalState)
	}
	return output.String(), finalHarness.runtime.Model()
}

func immediateTerminalFrameBarrier() tea.Cmd {
	return func() tea.Msg { return terminalFrameSettledMsg{} }
}
