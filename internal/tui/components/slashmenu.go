package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const MaxSlashCandidates = 8

// SlashCandidate is one code-defined completion row and owns no editor state.
type SlashCandidate struct {
	Name         string
	Usage        string
	Summary      string
	Availability string
	Reason       string
	Disabled     bool
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
	width      int
	styles     SlashMenuStyles
}

// NewSlashMenu creates a closed menu with no input control of its own.
func NewSlashMenu(styles SlashMenuStyles) SlashMenu {
	return SlashMenu{selected: -1, maxVisible: MaxSlashCandidates, width: 80, styles: styles}
}

// SetStyles updates presentation without changing candidates or selection.
func (menu *SlashMenu) SetStyles(styles SlashMenuStyles) { menu.styles = styles }

// SetCandidates opens the menu and defensively copies at most eight rows.
func (menu *SlashMenu) SetCandidates(candidates []SlashCandidate) {
	previous, _ := menu.SelectedCandidate()
	limit := min(len(candidates), MaxSlashCandidates, menu.maxVisible)
	menu.candidates = append(menu.candidates[:0], candidates[:limit]...)
	menu.open = true
	menu.selected = -1
	for index, candidate := range menu.candidates {
		if candidate.Disabled {
			continue
		}
		if menu.selected < 0 {
			menu.selected = index
		}
		if candidate.Name == previous.Name {
			menu.selected = index
			break
		}
	}
}

// SetMaxVisible tightens the visible list for small terminals.
func (menu *SlashMenu) SetMaxVisible(limit int) {
	menu.maxVisible = max(1, min(limit, MaxSlashCandidates))
	if len(menu.candidates) > menu.maxVisible {
		menu.SetCandidates(menu.candidates)
	}
}

// SetWidth bounds each candidate row without creating horizontal scrolling.
func (menu *SlashMenu) SetWidth(width int) { menu.width = max(1, width) }

// Close removes the candidate region without changing composer text.
func (menu *SlashMenu) Close() {
	menu.open = false
	menu.candidates = nil
	menu.selected = -1
}

// Open reports whether the candidate region is present.
func (menu SlashMenu) Open() bool { return menu.open }

// Candidates returns a defensive copy of visible rows.
func (menu SlashMenu) Candidates() []SlashCandidate {
	return append([]SlashCandidate(nil), menu.candidates...)
}

// Selected returns the current row index, or -1 when no row is available.
func (menu SlashMenu) Selected() int { return menu.selected }

// SelectedCandidate returns the current row when one exists.
func (menu SlashMenu) SelectedCandidate() (SlashCandidate, bool) {
	if !menu.open || len(menu.candidates) == 0 || menu.selected < 0 || menu.selected >= len(menu.candidates) {
		return SlashCandidate{}, false
	}
	candidate := menu.candidates[menu.selected]
	return candidate, !candidate.Disabled
}

// Move changes selection with wraparound, skipping disabled rows.
func (menu *SlashMenu) Move(delta int) {
	available := make([]int, 0, len(menu.candidates))
	position := 0
	for index, candidate := range menu.candidates {
		if candidate.Disabled {
			continue
		}
		if index == menu.selected {
			position = len(available)
		}
		available = append(available, index)
	}
	if len(available) == 0 {
		return
	}
	position = (position + delta) % len(available)
	if position < 0 {
		position += len(available)
	}
	menu.selected = available[position]
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
		availability := candidate.Availability
		if availability == "" {
			availability = "available"
		}
		line += " · " + availability
		if availability != "available" {
			style = menu.styles.Disabled
			if candidate.Reason != "" {
				line += " — " + candidate.Reason
			}
		}
		lines = append(lines, style.Render(middleElideColumns(line, menu.width)))
	}
	return strings.Join(lines, "\n")
}
