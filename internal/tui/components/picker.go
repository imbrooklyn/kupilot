package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	// MaxPickerCandidates is the fixed number of candidate rows visible at once.
	MaxPickerCandidates = 8
	maxPickerSource     = 50
)

// PickerStyles supplies semantic styles shared by concrete non-editable Pickers.
type PickerStyles struct {
	Normal   lipgloss.Style
	Selected lipgloss.Style
	Muted    lipgloss.Style
	Danger   lipgloss.Style
}

type pickerList[T any] struct {
	open        bool
	loading     bool
	failed      bool
	items       []T
	selected    int
	offset      int
	maxVisible  int
	emptyText   string
	render      func(T) string
	styles      PickerStyles
	sourceLimit int
	width       int
}

func newPickerList[T any](emptyText string, render func(T) string, styles PickerStyles) pickerList[T] {
	return pickerList[T]{
		maxVisible: MaxPickerCandidates, emptyText: emptyText, render: render,
		styles: styles, sourceLimit: maxPickerSource, width: 80,
	}
}

func (picker *pickerList[T]) setStyles(styles PickerStyles) { picker.styles = styles }

func (picker *pickerList[T]) setLoading() {
	picker.open = true
	picker.loading = true
	picker.failed = false
	picker.items = nil
	picker.selected = 0
	picker.offset = 0
}

func (picker *pickerList[T]) setFailed() {
	picker.open = true
	picker.loading = false
	picker.failed = true
	picker.items = nil
	picker.selected = 0
	picker.offset = 0
}

func (picker *pickerList[T]) setItems(items []T) {
	limit := min(len(items), picker.sourceLimit)
	picker.items = append(picker.items[:0], items[:limit]...)
	picker.open = true
	picker.loading = false
	picker.failed = false
	if len(picker.items) == 0 {
		picker.selected = 0
		picker.offset = 0
		return
	}
	if picker.selected >= len(picker.items) {
		picker.selected = len(picker.items) - 1
	}
	picker.ensureVisible()
}

func (picker *pickerList[T]) setMaxVisible(limit int) {
	picker.maxVisible = max(1, min(limit, MaxPickerCandidates))
	picker.ensureVisible()
}

func (picker *pickerList[T]) setWidth(width int) { picker.width = max(1, width) }

func (picker *pickerList[T]) close() {
	picker.open = false
	picker.loading = false
	picker.failed = false
	picker.items = nil
	picker.selected = 0
	picker.offset = 0
}

func (picker pickerList[T]) isOpen() bool { return picker.open }

func (picker *pickerList[T]) move(delta int) {
	if len(picker.items) == 0 {
		return
	}
	picker.selected = (picker.selected + delta) % len(picker.items)
	if picker.selected < 0 {
		picker.selected += len(picker.items)
	}
	picker.ensureVisible()
}

func (picker *pickerList[T]) ensureVisible() {
	if len(picker.items) == 0 {
		picker.offset = 0
		return
	}
	if picker.selected < picker.offset {
		picker.offset = picker.selected
	}
	if picker.selected >= picker.offset+picker.maxVisible {
		picker.offset = picker.selected - picker.maxVisible + 1
	}
	maximum := max(0, len(picker.items)-picker.maxVisible)
	picker.offset = min(maximum, max(0, picker.offset))
}

func (picker pickerList[T]) selectedItem() (T, bool) {
	var zero T
	if !picker.open || picker.loading || picker.failed || len(picker.items) == 0 ||
		picker.selected < 0 || picker.selected >= len(picker.items) {
		return zero, false
	}
	return picker.items[picker.selected], true
}

func (picker pickerList[T]) sourceCount() int { return len(picker.items) }

func (picker pickerList[T]) visibleCount() int {
	if !picker.open || picker.loading || picker.failed {
		return 0
	}
	return min(len(picker.items), picker.maxVisible)
}

func (picker pickerList[T]) height() int {
	if !picker.open {
		return 0
	}
	if picker.loading || picker.failed || len(picker.items) == 0 {
		return 1
	}
	return picker.visibleCount()
}

func (picker pickerList[T]) view() string {
	if !picker.open {
		return ""
	}
	if picker.loading {
		return picker.styles.Muted.Render("  Loading…")
	}
	if picker.failed {
		return picker.styles.Danger.Render("  Candidates unavailable")
	}
	if len(picker.items) == 0 {
		return picker.styles.Muted.Render("  " + picker.emptyText)
	}
	end := min(len(picker.items), picker.offset+picker.maxVisible)
	lines := make([]string, 0, end-picker.offset)
	for index := picker.offset; index < end; index++ {
		marker := "  "
		style := picker.styles.Normal
		if index == picker.selected {
			marker = "› "
			style = picker.styles.Selected
		}
		content := middleElideColumns(picker.render(picker.items[index]), max(1, picker.width-lipgloss.Width(marker)))
		lines = append(lines, style.Render(marker+content))
	}
	return strings.Join(lines, "\n")
}
