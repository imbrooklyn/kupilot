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
		FocusedSurface: lipgloss.NewStyle().Padding(1, 0),
	}, 1024)
	composer.SetWidth(80)
	if composer.input.VirtualCursor() {
		t.Fatal("composer enabled the virtual cursor used inside rendered placeholder text")
	}
	if view := composer.View(); !strings.Contains(view, defaultPlaceholder) {
		t.Fatalf("empty composer truncated its placeholder: %q", view)
	}
	cursor := composer.Cursor()
	if cursor == nil || cursor.Position.X != 2 || cursor.Position.Y != 1 {
		t.Fatalf("empty composer cursor = %#v, want position (2,1)", cursor)
	}
}

func TestComposerPreservesOneCommittedUnicodeInputWithoutSpaces(t *testing.T) {
	t.Parallel()

	composer := NewComposer(ComposerStyles{
		FocusedSurface: lipgloss.NewStyle().Padding(1, 0),
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
	if cursor == nil || cursor.Position.X != 6 || cursor.Position.Y != 1 {
		t.Fatalf("Unicode composer cursor = %#v, want position (6,1)", cursor)
	}
}

func TestComposerShowsPromptOnlyOnceAndKeepsContinuationIndent(t *testing.T) {
	t.Parallel()

	composer := NewComposer(ComposerStyles{
		FocusedSurface: lipgloss.NewStyle().Padding(1, 0),
	}, 1024)
	composer.SetWidth(24)
	composer.SetValue("first line\nthird line\nfourth line")
	view := composer.View()
	if count := strings.Count(view, "›"); count != 1 {
		t.Fatalf("composer prompt count = %d, want 1:\n%s", count, view)
	}
	lines := strings.Split(view, "\n")
	firstColumn := visualTextColumn(lines[1], "first line")
	thirdColumn := visualTextColumn(lines[2], "third line")
	fourthColumn := visualTextColumn(lines[3], "fourth line")
	if firstColumn < 0 || thirdColumn != firstColumn || fourthColumn != firstColumn {
		t.Fatalf("continuation columns = %d, %d, %d:\n%s", firstColumn, thirdColumn, fourthColumn, view)
	}

	composer.SetWidth(14)
	composer.SetValue(strings.Repeat("x", 40))
	wrapped := composer.View()
	if count := strings.Count(wrapped, "›"); count != 1 {
		t.Fatalf("soft-wrapped composer prompt count = %d, want 1:\n%s", count, wrapped)
	}
	wantColumn := -1
	wrappedRows := 0
	for _, line := range strings.Split(wrapped, "\n") {
		column := visualTextColumn(line, "x")
		if column < 0 {
			continue
		}
		wrappedRows++
		if wantColumn < 0 {
			wantColumn = column
		} else if column != wantColumn {
			t.Fatalf("soft-wrapped continuation column = %d, want %d:\n%s", column, wantColumn, wrapped)
		}
	}
	if wrappedRows < 2 {
		t.Fatalf("composer did not soft-wrap the bounded input:\n%s", wrapped)
	}
}

func TestComposerArrowHistoryRequiresAnUnchangedWholeInputBoundary(t *testing.T) {
	t.Parallel()

	composer := NewComposer(ComposerStyles{}, 1024)
	composer.RecordSubmission("older question")
	composer.RecordSubmission("newer line\n\u4f60\u597d")
	if !composer.ArrowHistoryEligible() || !composer.PreviousHistory() {
		t.Fatal("empty composer could not enter submitted-input history")
	}
	if got := composer.Value(); got != "newer line\n\u4f60\u597d" || !composer.ArrowHistoryEligible() {
		t.Fatalf("recalled Unicode multiline entry = %q, eligible=%v", got, composer.ArrowHistoryEligible())
	}

	updated, _, err := composer.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if err != nil {
		t.Fatalf("move inside recalled entry: %v", err)
	}
	composer = updated
	if composer.ArrowHistoryEligible() {
		t.Fatal("interior cursor position stole an arrow from multiline editing")
	}
	updated, _, err = composer.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if err != nil {
		t.Fatalf("return to recalled-entry boundary: %v", err)
	}
	composer = updated
	if !composer.ArrowHistoryEligible() {
		t.Fatal("cursor-only movement discarded unchanged history navigation state")
	}

	updated, _, err = composer.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if err != nil {
		t.Fatalf("edit recalled entry: %v", err)
	}
	composer = updated
	if composer.ArrowHistoryEligible() {
		t.Fatal("edited recalled entry remained eligible for arrow history")
	}
}

func TestComposerClearHistoryRemovesPriorSessionInputs(t *testing.T) {
	t.Parallel()

	composer := NewComposer(ComposerStyles{}, 1024)
	composer.RecordSubmission("Prior Session question.")
	composer.SetValue("Preserved draft.")
	composer.ClearHistory()
	if composer.Value() != "Preserved draft." {
		t.Fatalf("ClearHistory changed the current draft: %q", composer.Value())
	}
	composer.Reset()
	if composer.PreviousHistory() || composer.Value() != "" {
		t.Fatalf("cleared prior Session history remained recallable: %q", composer.Value())
	}
	composer.RecordSubmission("Current Session question.")
	if !composer.PreviousHistory() || composer.Value() != "Current Session question." {
		t.Fatalf("new Session history could not be recalled after clearing: %q", composer.Value())
	}
}

func visualTextColumn(line, value string) int {
	index := strings.Index(line, value)
	if index < 0 {
		return -1
	}
	return lipgloss.Width(line[:index])
}
