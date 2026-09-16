package components

import (
	"strings"
	"testing"
)

func TestSlashMenuSelectionSkipsDisabledRows(t *testing.T) {
	t.Parallel()
	menu := NewSlashMenu(SlashMenuStyles{})
	menu.SetCandidates([]SlashCandidate{
		{Name: "queue", Disabled: true, Availability: "not applicable", Reason: "No editable input."},
		{Name: "quit"},
		{Name: "cancel", Disabled: true, Availability: "not applicable"},
		{Name: "help"},
	})
	for _, step := range []struct {
		delta int
		want  string
	}{
		{0, "quit"}, {1, "help"}, {1, "quit"}, {-1, "help"}, {-1, "quit"},
	} {
		menu.Move(step.delta)
		candidate, ok := menu.SelectedCandidate()
		if !ok || candidate.Name != step.want {
			t.Fatalf("selection = %#v, want %s", candidate, step.want)
		}
	}
	if view := menu.View(); !strings.Contains(view, "/queue") || !strings.Contains(view, "No editable input.") || strings.Contains(view, "› /queue") {
		t.Fatalf("disabled row lost its explanation or gained selection: %q", view)
	}
}

func TestSlashMenuUnavailableAndEmptyCandidatesHaveNoSelection(t *testing.T) {
	t.Parallel()
	for _, candidates := range [][]SlashCandidate{
		nil,
		{{Name: "queue", Disabled: true, Availability: "not applicable"}},
	} {
		menu := NewSlashMenu(SlashMenuStyles{})
		menu.SetCandidates(candidates)
		for _, delta := range []int{0, 1, -1} {
			menu.Move(delta)
			if _, ok := menu.SelectedCandidate(); ok || menu.Selected() != -1 {
				t.Fatal("unavailable menu selected a command")
			}
		}
		if strings.Contains(menu.View(), "› /") {
			t.Fatal("unavailable menu displayed a selected row")
		}
		menu.Close()
		if _, ok := menu.SelectedCandidate(); ok || menu.View() != "" {
			t.Fatal("closed menu retained a candidate")
		}
	}
}

func TestSlashMenuPreservesValidSelectionAndRecoversFromResize(t *testing.T) {
	t.Parallel()
	menu := NewSlashMenu(SlashMenuStyles{})
	menu.SetCandidates([]SlashCandidate{{Name: "queue"}, {Name: "quit"}})
	menu.Move(1)
	menu.SetCandidates([]SlashCandidate{{Name: "quit"}, {Name: "queue", Disabled: true}})
	if candidate, ok := menu.SelectedCandidate(); !ok || candidate.Name != "quit" {
		t.Fatal("reorder lost the selected command")
	}
	menu.SetCandidates([]SlashCandidate{{Name: "queue"}, {Name: "quit"}})
	menu.SetMaxVisible(1)
	if candidate, ok := menu.SelectedCandidate(); !ok || candidate.Name != "queue" {
		t.Fatal("resize left selection outside the visible menu")
	}
	menu.SetCandidates([]SlashCandidate{{Name: "queue", Disabled: true}})
	if _, ok := menu.SelectedCandidate(); ok {
		t.Fatal("disabled command remained selected")
	}
	menu.SetCandidates([]SlashCandidate{{Name: "queue"}})
	if candidate, ok := menu.SelectedCandidate(); !ok || candidate.Name != "queue" {
		t.Fatal("newly available command was not selected")
	}
}
