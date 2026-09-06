package components

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// PermissionCandidate keeps the three distinct concepts visible together:
// the technical boundary, Reviewer route, and residual risk.
type PermissionCandidate struct {
	Profile  domain.PermissionProfile
	Current  bool
	Boundary string
	Reviewer string
	Risk     string
}

// PermissionPicker is a fixed, borderless, non-editable list. The root model's
// one composer remains the only editor and supplies optional local filtering.
type PermissionPicker struct {
	open     bool
	items    []PermissionCandidate
	selected int
	width    int
	styles   PickerStyles
}

func NewPermissionPicker(styles PickerStyles) PermissionPicker {
	return PermissionPicker{width: 80, styles: styles}
}

func (picker *PermissionPicker) SetStyles(styles PickerStyles) { picker.styles = styles }
func (picker *PermissionPicker) SetWidth(width int)            { picker.width = max(8, width) }

func (picker *PermissionPicker) SetCandidates(values []PermissionCandidate) {
	picker.items = append(picker.items[:0], values...)
	picker.open = true
	picker.selected = 0
	for index, candidate := range picker.items {
		if candidate.Current {
			picker.selected = index
			break
		}
	}
}

func (picker *PermissionPicker) Close() {
	picker.open = false
	picker.items = nil
	picker.selected = 0
}

func (picker *PermissionPicker) Move(delta int) {
	if !picker.open || len(picker.items) == 0 {
		return
	}
	picker.selected = (picker.selected + delta) % len(picker.items)
	if picker.selected < 0 {
		picker.selected += len(picker.items)
	}
}

func (picker PermissionPicker) Open() bool { return picker.open }

func (picker PermissionPicker) Selected() (PermissionCandidate, bool) {
	if !picker.open || picker.selected < 0 || picker.selected >= len(picker.items) {
		return PermissionCandidate{}, false
	}
	return picker.items[picker.selected], true
}

func (picker PermissionPicker) Height() int {
	if !picker.open {
		return 0
	}
	return lipgloss.Height(picker.View())
}

func (picker PermissionPicker) View() string {
	if !picker.open {
		return ""
	}
	if len(picker.items) == 0 {
		return picker.styles.Muted.Render("  No matching permission profiles")
	}
	lines := make([]string, 0, len(picker.items)*2+1)
	lines = append(lines, picker.styles.Muted.Render(middleElideColumns("B = boundary · V = Reviewer · R = risk", picker.width)))
	for index, candidate := range picker.items {
		marker := "  "
		style := picker.styles.Normal
		if index == picker.selected {
			marker = "› "
			style = picker.styles.Selected
		}
		name := string(candidate.Profile)
		if candidate.Current {
			name += " · current"
		}
		lines = append(lines, style.Render(marker+middleElideColumns(name, max(1, picker.width-2))))
		budget := max(3, picker.width-12)
		boundaryWidth := max(1, budget/3)
		reviewerWidth := max(1, (budget-boundaryWidth)/2)
		riskWidth := max(1, budget-boundaryWidth-reviewerWidth)
		detail := "B:" + middleElideColumns(candidate.Boundary, boundaryWidth) +
			" V:" + middleElideColumns(candidate.Reviewer, reviewerWidth) +
			" R:" + middleElideColumns(candidate.Risk, riskWidth)
		lines = append(lines, picker.styles.Muted.Render("    "+clipFooterColumns(detail, max(1, picker.width-4))))
	}
	return strings.Join(lines, "\n")
}
