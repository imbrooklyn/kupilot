package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

type composerKeyHarness struct {
	model           Model
	keys            []tea.KeyPressMsg
	returnedCommand bool
}

func (harness composerKeyHarness) Init() tea.Cmd { return nil }

func (harness composerKeyHarness) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	event, ok := message.(tea.KeyPressMsg)
	if !ok {
		return harness, nil
	}
	if event.Code == tea.KeyF12 {
		return harness, tea.Quit
	}
	harness.keys = append(harness.keys, event)
	next, command := harness.model.Update(event)
	harness.model = next.(Model)
	harness.returnedCommand = harness.returnedCommand || command != nil
	return harness, nil
}

func (harness composerKeyHarness) View() tea.View { return harness.model.View() }

func TestComposerAcceptsNativeTerminalCursorEncodings(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, input, want, key string
		keyCount               int
	}{
		{"meta_left", "\x1bbX", "alpha Xbeta", "alt+b", 2},
		{"meta_right", "\x01\x1bfX", "alphaX beta", "alt+f", 3},
		{"option_left", "\x1b[1;3DX", "alpha Xbeta", "alt+left", 2},
		{"option_right", "\x01\x1b[1;3CX", "alphaX beta", "alt+right", 3},
		{"control_left", "\x1b[1;5DX", "alpha Xbeta", "ctrl+left", 2},
		{"control_right", "\x01\x1b[1;5CX", "alphaX beta", "ctrl+right", 3},
		{"control_character_left", "\x02X", "alpha betXa", "ctrl+b", 2},
		{"control_character_right", "\x01\x06X", "aXlpha beta", "ctrl+f", 3},
		{"control_line_end", "\x01\x05X", "alpha betaX", "ctrl+e", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			model, _ := modelWithEvidenceReference(t)
			model.transcript.AppendLandmarkNotice("A previous run failed safely.", components.TranscriptLandmarkFailureUnknown)
			model.composer.SetValue("alpha beta")
			transcript := model.transcript.View()
			var output synchronizedBuffer
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			program := tea.NewProgram(composerKeyHarness{model: model},
				tea.WithContext(ctx),
				tea.WithInput(strings.NewReader(test.input+"\x1b[24~")),
				tea.WithOutput(&output),
				tea.WithEnvironment([]string{"TERM=xterm-256color", "NO_COLOR=1"}),
				tea.WithWindowSize(80, 24),
				tea.WithoutSignalHandler(),
			)
			result, err := program.Run()
			if err != nil {
				t.Fatalf("native terminal cursor fixture failed: %v", err)
			}
			final := result.(composerKeyHarness)
			if len(final.keys) != test.keyCount || final.keys[len(final.keys)-2].String() != test.key || final.returnedCommand ||
				final.model.composer.Value() != test.want || final.model.FocusedEditorCount() != 1 || final.model.searchMode ||
				final.model.transcript.EvidenceSelecting() || final.model.transcript.View() != transcript {
				t.Fatalf("native cursor result: keys=%v draft=%q command=%v focused=%d, want %q",
					final.keys, final.model.composer.Value(), final.returnedCommand, final.model.FocusedEditorCount(), test.want)
			}
		})
	}
}
