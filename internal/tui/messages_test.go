package tui

import (
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func TestSanitizeExternalTextRemovesTerminalAndBidiControls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "CSI", input: "a\x1b[31mb\x1b[0m", want: "ab"},
		{name: "OSC", input: "a\x1b]8;;ignored\x07b", want: "ab"},
		{name: "DCS", input: "a\x1bPignored\x1b\\b", want: "ab"},
		{name: "C0 and C1", input: "a\x00\t\u0085b", want: "ab"},
		{name: "C1 CSI", input: "a\u009b31mb", want: "ab"},
		{name: "bidirectional override", input: "a\u202eb", want: "ab"},
		{name: "line endings", input: "a\r\nb\rc", want: "a\nb\nc"},
		{name: "unterminated OSC", input: "a\x1b]ignored", want: "a"},
		{name: "invalid UTF-8", input: "a" + string([]byte{0xff}) + "b", want: "a�b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeExternalText(tt.input, 0)
			if got != tt.want || containsUnsafeTerminalText(got) {
				t.Fatalf("sanitizeExternalText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeExternalTextAppliesUTF8SafeBound(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("a", 8) + "\u754c"
	got := sanitizeExternalText(input, 8)
	if len(got) > 8 || !strings.HasPrefix(input, got) || containsUnsafeTerminalText(got) {
		t.Fatalf("bounded text = %q (%d bytes)", got, len(got))
	}
}

func TestApplicationTextAndScopeAreSanitizedBeforeRenderState(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope: ScopeView{
			Context: "test\x1b]52;c;ignored\x07-context", Namespace: "test\u202e-namespace",
			Generation: 7, ReadOnly: true, Verified: true,
		},
	})
	if model.scope.Context != "test-context" || model.scope.Namespace != "test-namespace" {
		t.Fatalf("sanitized scope = %#v", model.scope)
	}
	model.acceptApplicationEvent(runStartedEvent(1))
	model.acceptApplicationEvent(application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2,
		Text: "safe\x1b[31m text\x1b[0m\x1b]8;;ignored\x07",
	})
	if model.run.StreamedText != "safe text" || containsUnsafeTerminalText(model.run.StreamedText) {
		t.Fatalf("sanitized stream = %q", model.run.StreamedText)
	}
}
