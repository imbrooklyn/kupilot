package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// DialogStyles keeps modal meaning explicit in text as well as color.
type DialogStyles struct {
	Frame lipgloss.Style
	Title lipgloss.Style
	Body  lipgloss.Style
	Hint  lipgloss.Style
}

// ErrorDialog is a non-editable help or safe-error modal.
type ErrorDialog struct {
	open   bool
	title  string
	body   string
	hint   string
	offset int
	styles DialogStyles
}

// NewErrorDialog creates one closed non-editable modal.
func NewErrorDialog(styles DialogStyles) ErrorDialog { return ErrorDialog{styles: styles} }

// SetStyles updates presentation without changing modal state.
func (dialog *ErrorDialog) SetStyles(styles DialogStyles) { dialog.styles = styles }

// Show opens the modal with already-safe bounded copy.
func (dialog *ErrorDialog) Show(title, body string) {
	dialog.ShowWithHint(title, body, "Esc, Ctrl+C, or Enter to close")
}

// ShowWithHint opens the same local modal with one fixed interaction hint.
func (dialog *ErrorDialog) ShowWithHint(title, body, hint string) {
	dialog.open = true
	dialog.title = title
	dialog.body = body
	dialog.hint = hint
	dialog.offset = 0
}

// Close closes and clears the modal.
func (dialog *ErrorDialog) Close() {
	dialog.open = false
	dialog.title = ""
	dialog.body = ""
	dialog.hint = ""
	dialog.offset = 0
}

// Open reports whether the modal captures keyboard input.
func (dialog ErrorDialog) Open() bool { return dialog.open }

// View renders a compact modal with an explicit close hint.
func (dialog ErrorDialog) View(width int, heights ...int) string {
	if !dialog.open {
		return ""
	}
	innerWidth := max(1, min(width-1, 78)-dialog.styles.Frame.GetHorizontalFrameSize())
	body := dialog.styles.Body.Width(innerWidth).Render(dialog.body)
	hint := dialog.hint
	if len(heights) > 0 {
		lines := strings.Split(body, "\n")
		available := dialog.bodyHeight(width, heights[0])
		start := min(dialog.offset, max(0, len(lines)-available))
		body = strings.Join(lines[start:min(len(lines), start+available)], "\n")
		if len(lines) > available {
			hint = "Up/Down, PgUp/PgDn scroll · " + hint
		}
	}
	content := dialog.styles.Title.Render(dialog.title) + "\n\n" + body + "\n\n" + dialog.styles.Hint.Render(hint)
	return dialog.styles.Frame.Width(innerWidth + dialog.styles.Frame.GetHorizontalPadding()).Render(content)
}

func (dialog ErrorDialog) bodyHeight(width, height int) int {
	innerWidth := max(1, min(width-1, 78)-dialog.styles.Frame.GetHorizontalFrameSize())
	title := lipgloss.Height(dialog.styles.Title.Width(innerWidth).Render(dialog.title))
	hint := lipgloss.Height(dialog.styles.Hint.Width(innerWidth).Render("Up/Down, PgUp/PgDn scroll · " + dialog.hint))
	return max(1, height-dialog.styles.Frame.GetVerticalFrameSize()-title-hint-4)
}

// Scroll changes only the bounded read-only dialog projection.
func (dialog *ErrorDialog) Scroll(delta, width, height int) {
	innerWidth := max(1, min(width-1, 78)-dialog.styles.Frame.GetHorizontalFrameSize())
	body := dialog.styles.Body.Width(innerWidth).Render(dialog.body)
	limit := max(0, lipgloss.Height(body)-dialog.bodyHeight(width, height))
	dialog.offset = min(limit, max(0, dialog.offset+delta))
}
