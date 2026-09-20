package tui

import (
	"bytes"
	"context"
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
