package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

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

// Advance only after the actual Bubble Tea writer emits the expected frame.
// No sleeps or assumed frame rate are used to drive the rendering regression.
type probeOutput struct {
	sync.Mutex
	all, tail, marker string
	ready             chan struct{}
}

func (o *probeOutput) Write(p []byte) (int, error) {
	o.Lock()
	defer o.Unlock()
	o.all += string(p)
	o.tail += string(p)
	if o.marker != "" && strings.Contains(o.tail, o.marker) {
		o.marker = ""
		o.ready <- struct{}{}
	}
	return len(p), nil
}
func (o *probeOutput) expect(marker string) {
	o.Lock()
	defer o.Unlock()
	o.marker = marker
	o.tail = ""
}

type probeStep int

type mouseInputHarness struct {
	Model
	wheels       int
	scrolled     bool
	changedDraft bool
}

func (h mouseInputHarness) Init() tea.Cmd { return nil }

func (h mouseInputHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := h.Model.transcript.ScrollOffset()
	next, cmd := h.Model.Update(msg)
	h.Model = next.(Model)
	switch event := msg.(type) {
	case tea.MouseWheelMsg:
		h.wheels++
		h.scrolled = h.scrolled || h.Model.transcript.ScrollOffset() != before
		h.changedDraft = h.changedDraft || h.Model.composer.Value() != "" || cmd != nil
	case tea.KeyPressMsg:
		if event.Code == tea.KeyUp {
			return h, tea.Quit
		}
	}
	return h, cmd
}

func TestTerminalMouseProtocolSeparatesWheelFromKeyboardHistory(t *testing.T) {
	model := newTestModel()
	model.composer.RecordSubmission("keyboard history")
	for range 50 {
		model.transcript.AppendNotice("Scrollable row")
	}
	model.reflow()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// Real SGR wheel reports over the composer, followed by a physical Up key.
	input := strings.NewReader("\x1b[<64;5;23M\x1b[<65;5;23M\x1b[A")
	program := tea.NewProgram(mouseInputHarness{Model: model}, tea.WithContext(ctx),
		tea.WithInput(input), tea.WithOutput(io.Discard), tea.WithWindowSize(80, 24),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		t.Fatal(err)
	}
	result := final.(mouseInputHarness)
	if result.wheels != 2 || !result.scrolled || result.changedDraft || result.composer.Value() != "keyboard history" {
		t.Fatalf("mouse/keyboard separation failed: wheels=%d scrolled=%t changed draft=%t final=%q",
			result.wheels, result.scrolled, result.changedDraft, result.composer.Value())
	}
}

type selectionInputHarness struct {
	Model
	copiedOnRelease bool
}

func (h selectionInputHarness) Init() tea.Cmd { return nil }

func (h selectionInputHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if event, ok := msg.(tea.KeyPressMsg); ok && event.Code == tea.KeyF12 {
		return h, tea.Quit
	}
	next, cmd := h.Model.Update(msg)
	h.Model = next.(Model)
	switch msg.(type) {
	case tea.MouseReleaseMsg:
		h.copiedOnRelease = h.copiedOnRelease || cmd != nil
	case ClipboardResultMsg, tea.PasteMsg:
		return h, tea.Quit
	}
	return h, cmd
}

func TestTerminalProtocolDoubleClickRequiresExplicitPlatformCopyKey(t *testing.T) {
	for _, test := range []struct {
		name, goos, input string
		native, copy      bool
	}{
		{"mac-native-command", "darwin", "\x1b[99;9u", true, true},
		{"mac-terminal-command", "darwin", "\x1b[99;9u", false, true},
		{"linux-control-shift", "linux", "\x1b[99;6u", false, true},
		{"linux-control", "linux", "\x03", false, true},
		{"mac-command-consumed-by-terminal", "darwin", "\x1b[24~", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, x, y := selectionTestModel(t)
			model.keymap.Copy = transcriptCopyBinding(test.goos)
			model.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
			model.composer.SetValue("preserved draft")
			if test.native {
				model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
			}
			var copies []string
			model.copyToClipboard = func(id uint64, text string) tea.Cmd {
				copies = append(copies, text)
				return func() tea.Msg { return ClipboardResultMsg{RequestID: id, Copied: true} }
			}
			// Actual SGR mouse reports use one-based cells. If the terminal eats
			// Command+C, only the harness's final F12 arrives: zero copy writes.
			click := fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", x+3, y+1, x+3, y+1)
			input := click + click + test.input
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			program := tea.NewProgram(selectionInputHarness{Model: model}, tea.WithContext(ctx),
				tea.WithInput(strings.NewReader(input)), tea.WithOutput(io.Discard),
				tea.WithWindowSize(80, 24), tea.WithEnvironment([]string{"TERM=xterm-256color"}),
				tea.WithoutSignalHandler())
			final, err := program.Run()
			if err != nil {
				t.Fatal(err)
			}
			result := final.(selectionInputHarness)
			if result.copiedOnRelease || result.selectedTranscriptText() != "Select" || result.composer.Value() != "preserved draft" {
				t.Fatalf("terminal selection/copy failed: writes=%q selection=%q status=%q",
					copies, result.selectedTranscriptText(), result.selectionCopyHint())
			}
			if test.copy {
				if len(copies) != 1 || copies[0] != "Select" || result.selectionCopyHint() != "Selection copied" {
					t.Fatal("the explicit platform shortcut did not copy exactly once")
				}
			} else if len(copies) != 0 || result.selectionCopyHint() != "Command+C copy" {
				t.Fatal("selection copied without a forwarded key")
			}
		})
	}
}

func TestTerminalProtocolBracketedPasteEditsDraftWithoutSubmittingOrCopying(t *testing.T) {
	model, x, y := selectionTestModel(t)
	model.terminalCapabilities.NativeClipboard = TerminalCapabilityAvailable
	model.composer.SetValue("draft ")
	model.copyToClipboard = func(uint64, string) tea.Cmd {
		t.Error("selection or paste wrote to the clipboard")
		return nil
	}
	// Paste content is delivered by the terminal, not a clipboard read request.
	input := fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm\x1b[200~first\nsecond\x1b[201~", x+1, y+1, x+7, y+1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	program := tea.NewProgram(selectionInputHarness{Model: model}, tea.WithContext(ctx),
		tea.WithInput(strings.NewReader(input)), tea.WithOutput(io.Discard),
		tea.WithWindowSize(80, 24), tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithoutSignalHandler())
	final, err := program.Run()
	if err != nil {
		t.Fatal(err)
	}
	result := final.(selectionInputHarness)
	if result.copiedOnRelease || result.pendingClipboardID != 0 || result.selectedTranscriptText() != "" ||
		result.composer.Value() != "draft first\nsecond" || result.pendingSubmitID != 0 || result.run.Active {
		t.Fatal("bracketed paste copied, submitted, or failed to edit the draft and clear selection")
	}
}

type probeHarness struct{ Model }

func (h probeHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if step, ok := msg.(probeStep); ok {
		switch step {
		case 1:
			h.Model.transcript.AppendUser("QUESTION_MARKER")
			h.Model.transcript.StartAgent()
			h.Model.run.Active = true
			h.Model.transcript.UpsertToolStep(components.ToolStep{InvocationID: "a", Name: "Inspect", Purpose: "TOOL_ALPHA", Status: "succeeded"})
		case 2:
			h.Model.showDialog("DIALOG_MARKER", strings.Repeat("Ephemeral details\n", 50))
		case 3:
			h.Model.closeDialog()
			h.Model.composer.SetValue("CCCCCCCC")
		case 4:
			h.Model.transcript.UpsertToolStep(components.ToolStep{InvocationID: "b", Name: "Inspect", Purpose: "TOOL_BETA", Status: "succeeded"})
		case 5:
			h.Model.run.Active = false
			h.Model.run.Terminal = true
			h.Model.transcript.FinishCommittedAgent("FINAL_MARKER")
			h.Model.composer.SetValue("DDDDDDDD")
		case 6:
			return h, tea.Quit
		}
		h.Model.reflow()
		return h, nil
	}
	next, cmd := h.Model.Update(msg)
	h.Model = next.(Model)
	return h, cmd
}
func TestManagedScreenSurvivesDialogsAndToolProgress(t *testing.T) {
	model := newTestModel()
	model.composer.SetValue("BASE_MARKER")
	out := &probeOutput{ready: make(chan struct{}, 1), marker: "BASE_MARKER"}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	p := tea.NewProgram(probeHarness{model}, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(out), tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}), tea.WithWindowSize(80, 24), tea.WithoutSignalHandler())
	done := make(chan error, 1)
	var final tea.Model
	go func() { var err error; final, err = p.Run(); done <- err }()
	wait := func() {
		select {
		case <-out.ready:
		case <-ctx.Done():
			out.Lock()
			detail := out.marker + " " + out.tail
			out.Unlock()
			t.Fatal(ctx.Err(), detail)
		}
	}
	wait()
	for i, marker := range []string{"TOOL_ALPHA", "DIALOG_MARKER", "CCCCCCCC", "TOOL_BETA", "DDDDDDDD"} {
		out.expect(marker)
		p.Send(probeStep(i + 1))
		wait()
	}
	p.Send(probeStep(6))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f := final.(probeHarness).Model
	if !strings.Contains(out.all, "\x1b[?1002h") || !strings.Contains(out.all, "\x1b[?1006h") {
		t.Fatal("renderer did not enable cell-motion and SGR mouse reporting")
	}
	if !strings.Contains(out.all, "\x1b[?1002l") || !strings.Contains(out.all, "\x1b[?1006l") {
		t.Fatal("renderer did not restore mouse reporting on exit")
	}
	if strings.Count(out.all, "\x1b[?1049h") != 1 || strings.Count(out.all, "\x1b[?1049l") != 1 {
		t.Fatal("dialog or progress changed renderer ownership")
	}
	before, _, _ := strings.Cut(out.all, "\x1b[?1049h")
	_, after, _ := strings.Cut(out.all, "\x1b[?1049l")
	for _, text := range []string{"QUESTION_MARKER", "TOOL_ALPHA", "TOOL_BETA", "FINAL_MARKER"} {
		if strings.Contains(before+after, text) || strings.Count(f.TerminalTranscript(), text) != 1 {
			t.Fatalf("duplicate or unmanaged output: %s", text)
		}
	}
	for _, text := range []string{"DIALOG_MARKER", "Ephemeral details", "Working (", "CCCCCCCC"} {
		if strings.Contains(f.View().Content, text) || strings.Contains(f.TerminalTranscript(), text) {
			t.Fatalf("transient content survived: %s", text)
		}
	}
}
