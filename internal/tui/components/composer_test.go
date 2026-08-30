package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestComposerUsesRealCursorWithoutOverwritingPlaceholder(t *testing.T) {
	t.Parallel()

	composer := NewComposer(ComposerStyles{
		FocusedSurface: lipgloss.NewStyle().Padding(1, 1),
	}, 1024)
	composer.SetWidth(80)
	if composer.input.VirtualCursor() {
		t.Fatal("composer enabled the virtual cursor used inside rendered placeholder text")
	}
	if view := composer.View(); !strings.Contains(view, defaultPlaceholder) {
		t.Fatalf("empty composer truncated its placeholder: %q", view)
	}
	cursor := composer.Cursor()
	if cursor == nil || cursor.Position.X != 3 || cursor.Position.Y != 1 {
		t.Fatalf("empty composer cursor = %#v, want position (3,1)", cursor)
	}
}

func TestComposerPreservesOneCommittedUnicodeInputWithoutSpaces(t *testing.T) {
	t.Parallel()

	composer := NewComposer(ComposerStyles{
		FocusedSurface: lipgloss.NewStyle().Padding(1, 1),
	}, 1024)
	committed := "\u4f60\u597d"
	runes := []rune(committed)
	updated, cmd, err := composer.Update(tea.KeyPressMsg{Code: runes[0], Text: committed})
	if err != nil || cmd != nil {
		t.Fatalf("committed Unicode input returned cmd=%T err=%v", cmd, err)
	}
	if got := updated.Value(); got != committed || strings.ContainsRune(got, ' ') {
		t.Fatalf("committed Unicode input = %q, want exact text without inserted spaces", got)
	}
	cursor := updated.Cursor()
	if cursor == nil || cursor.Position.X != 7 || cursor.Position.Y != 1 {
		t.Fatalf("Unicode composer cursor = %#v, want position (7,1)", cursor)
	}
}
