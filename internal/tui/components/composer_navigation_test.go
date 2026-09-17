package components

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The subprocess bounds termination even if a future dependency reintroduces
// an infinite loop in its synchronous word-left implementation.
func TestComposerWordMovementStopsAtBounds(t *testing.T) {
	if os.Getenv("KUPILOT_TEST_COMPOSER_BOUNDS") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestComposerWordMovementStopsAtBounds$", "-test.count=1")
		command.Env = []string{"KUPILOT_TEST_COMPOSER_BOUNDS=1"}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("bounded word movement failed: %v, context=%v\n%s", err, ctx.Err(), output)
		}
		return
	}

	for _, draft := range []string{"", " ", " \n ", "   word", "  \n  word", "\u2003\u2003word"} {
		for _, left := range []tea.KeyPressMsg{
			{Code: 'b', Mod: tea.ModAlt}, {Code: tea.KeyLeft, Mod: tea.ModAlt}, {Code: tea.KeyLeft, Mod: tea.ModCtrl},
		} {
			composer := NewComposer(ComposerStyles{}, 1024)
			composer.SetValue(draft)
			for range 4 {
				var command tea.Cmd
				var err error
				composer, command, err = composer.Update(left)
				if err != nil || command != nil || composer.Value() != draft || !composer.Focused() {
					t.Fatalf("word-left changed input or focus: key=%s err=%v", left.String(), err)
				}
			}
			if composer.input.Line() != 0 || composer.input.Column() != 0 {
				t.Fatalf("word-left did not stop at the input start: line=%d column=%d", composer.input.Line(), composer.input.Column())
			}
			composer.input.MoveToEnd()
			before := composer.Cursor().Position
			for _, right := range []tea.KeyPressMsg{
				{Code: 'f', Mod: tea.ModAlt}, {Code: tea.KeyRight, Mod: tea.ModAlt}, {Code: tea.KeyRight, Mod: tea.ModCtrl},
			} {
				var command tea.Cmd
				var err error
				composer, command, err = composer.Update(right)
				if command != nil || err != nil || composer.Value() != draft || composer.Cursor().Position != before {
					t.Fatal("word-right escaped the input end")
				}
			}
		}
	}
}

func TestComposerSelectionReplacementRespectsByteLimitAndUndo(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input tea.Msg
		want  string
		limit bool
	}{
		{"typed_exact", tea.KeyPressMsg{Code: 'x', Text: "xyz"}, "abxyz", false},
		{"pasted_exact", tea.PasteMsg{Content: "xyz"}, "abxyz", false},
		{"unicode_exact", tea.PasteMsg{Content: "\u5b57"}, "ab\u5b57", false},
		{"newline", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}, "ab\n", false},
		{"typed_one_over", tea.KeyPressMsg{Code: 'w', Text: "wxyz"}, "ab\u754c", true},
		{"pasted_one_over", tea.PasteMsg{Content: "wxyz"}, "ab\u754c", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			composer := NewComposer(ComposerStyles{}, 5)
			original := "ab\u754c"
			composer.SetValue(original)
			composer, _, _ = composer.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
			if composer.input.SelectedText() != "\u754c" {
				t.Fatal("selection did not cover exactly one Unicode character")
			}
			updated, command, err := composer.Update(test.input)
			if command != nil || errors.Is(err, ErrComposerLimit) != test.limit || err != nil && !test.limit || updated.Value() != test.want {
				t.Fatalf("selection replacement = %q command=%t err=%v, want %q limit=%t", updated.Value(), command != nil, err, test.want, test.limit)
			}
			if test.limit {
				if updated.input.SelectedText() != "\u754c" {
					t.Fatal("refused replacement changed the selection")
				}
			} else if updated.input.HasSelection() || !updated.Undo() || updated.Value() != original {
				t.Fatal("accepted selection replacement did not clear selection and preserve undo")
			}
		})
	}
}

func TestComposerSelectionCannotWriteClipboard(t *testing.T) {
	t.Parallel()

	composer := NewComposer(ComposerStyles{}, 64)
	composer.SetValue("uncommitted draft")
	composer.input.SelectAll()
	updated, command, err := composer.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})
	if command != nil || err != nil || updated.Value() != "uncommitted draft" || !updated.input.HasSelection() ||
		composer.input.KeyMap.CopySelection.Enabled() || composer.input.KeyMap.Paste.Enabled() {
		t.Fatal("native selection acquired clipboard authority")
	}
	composer.Blur()
	before := composer.input.Column()
	composer, command, err = composer.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	if command != nil || err != nil || composer.input.Column() != before || composer.Value() != "uncommitted draft" {
		t.Fatal("blurred word navigation changed editor state")
	}
}

func TestComposerNativeWordDeletion(t *testing.T) {
	t.Parallel()

	for _, event := range []tea.KeyPressMsg{
		{Code: tea.KeyBackspace, Mod: tea.ModCtrl}, {Code: tea.KeyBackspace, Mod: tea.ModAlt},
		{Code: tea.KeyDelete, Mod: tea.ModCtrl}, {Code: tea.KeyDelete, Mod: tea.ModAlt},
	} {
		t.Run(event.String(), func(t *testing.T) {
			composer := NewComposer(ComposerStyles{}, 64)
			composer.SetValue("alpha beta")
			want := "alpha "
			if event.Code == tea.KeyDelete {
				composer.input.MoveToBegin()
				want = " beta"
			}
			updated, command, err := composer.Update(event)
			if command != nil || err != nil || updated.Value() != want || !updated.Undo() || updated.Value() != "alpha beta" {
				t.Fatalf("word deletion/undo failed: key=%s err=%v", event.String(), err)
			}
		})
	}
}
