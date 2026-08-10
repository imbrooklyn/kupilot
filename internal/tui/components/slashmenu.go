package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const MaxSlashCandidates = 8

// SlashCandidate is one code-defined completion row and owns no editor state.
type SlashCandidate struct {
	Name           string
	Usage          string
	Summary        string
	Enabled        bool
	DisabledReason string
}

// SlashMenuStyles provides semantic styles without embedding command behavior.
type SlashMenuStyles struct {
	Normal   lipgloss.Style
	Selected lipgloss.Style
	Muted    lipgloss.Style
	Disabled lipgloss.Style
}

// SlashMenu is an untitled, unframed selection list below the composer.
type SlashMenu struct {
	open       bool
	candidates []SlashCandidate
	selected   int
	maxVisible int
	styles     SlashMenuStyles
}

// NewSlashMenu creates a closed menu with no input control of its own.
func NewSlashMenu(styles SlashMenuStyles) SlashMenu {
	return SlashMenu{maxVisible: MaxSlashCandidates, styles: styles}
}

// SetCandidates opens the menu and defensively copies at most eight rows.
func (menu *SlashMenu) SetCandidates(candidates []SlashCandidate) {
	limit := min(len(candidates), MaxSlashCandidates, menu.maxVisible)
	menu.candidates = append(menu.candidates[:0], candidates[:limit]...)
	menu.open = true
	if len(menu.candidates) == 0 {
		menu.selected = 0
	} else if menu.selected >= len(menu.candidates) {
		menu.selected = len(menu.candidates) - 1
	}
}

// SetMaxVisible tightens the visible list for small terminals.
func (menu *SlashMenu) SetMaxVisible(limit int) {
	menu.maxVisible = max(1, min(limit, MaxSlashCandidates))
	if len(menu.candidates) > menu.maxVisible {
		menu.candidates = menu.candidates[:menu.maxVisible]
	}
}

// Close removes the candidate region without changing composer text.
func (menu *SlashMenu) Close() {
	menu.open = false
	menu.candidates = nil
	menu.selected = 0
}

// Open reports whether the candidate region is present.
func (menu SlashMenu) Open() bool { return menu.open }

// Candidates returns a defensive copy of visible rows.
func (menu SlashMenu) Candidates() []SlashCandidate {
	return append([]SlashCandidate(nil), menu.candidates...)
}

// Selected returns the current row index.
func (menu SlashMenu) Selected() int { return menu.selected }

// SelectedCandidate returns the current row when one exists.
func (menu SlashMenu) SelectedCandidate() (SlashCandidate, bool) {
	if !menu.open || len(menu.candidates) == 0 || menu.selected < 0 || menu.selected >= len(menu.candidates) {
		return SlashCandidate{}, false
	}
	return menu.candidates[menu.selected], true
}

// Move changes selection with wraparound.
func (menu *SlashMenu) Move(delta int) {
	if len(menu.candidates) == 0 {
		return
	}
	menu.selected = (menu.selected + delta) % len(menu.candidates)
	if menu.selected < 0 {
		menu.selected += len(menu.candidates)
	}
}

// Height returns the number of rows currently rendered.
func (menu SlashMenu) Height() int {
	if !menu.open {
		return 0
	}
	if len(menu.candidates) == 0 {
		return 1
	}
	return len(menu.candidates)
}

// View renders rows only: no title, border, prompt, or mode label.
func (menu SlashMenu) View() string {
	if !menu.open {
		return ""
	}
	if len(menu.candidates) == 0 {
		return menu.styles.Muted.Render("  No matching commands")
	}
	lines := make([]string, 0, len(menu.candidates))
	for index, candidate := range menu.candidates {
		marker := "  "
		style := menu.styles.Normal
		if index == menu.selected {
			marker = "› "
			style = menu.styles.Selected
		}
		line := marker + "/" + candidate.Name
		if candidate.Usage != "" {
			line += " " + candidate.Usage
		}
		if candidate.Summary != "" {
			line += "  " + candidate.Summary
		}
		if !candidate.Enabled {
			style = menu.styles.Disabled
			if candidate.DisabledReason != "" {
				line += " — " + candidate.DisabledReason
			}
		}
		lines = append(lines, style.Render(line))
	}
	return strings.Join(lines, "\n")
}
