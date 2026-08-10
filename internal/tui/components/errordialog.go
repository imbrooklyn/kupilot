package components

import "charm.land/lipgloss/v2"

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
	styles DialogStyles
}

// NewErrorDialog creates one closed non-editable modal.
func NewErrorDialog(styles DialogStyles) ErrorDialog { return ErrorDialog{styles: styles} }

// Show opens the modal with already-safe bounded copy.
func (dialog *ErrorDialog) Show(title, body string) {
	dialog.open = true
	dialog.title = title
	dialog.body = body
}

// Close closes and clears the modal.
func (dialog *ErrorDialog) Close() {
	dialog.open = false
	dialog.title = ""
	dialog.body = ""
}

// Open reports whether the modal captures keyboard input.
func (dialog ErrorDialog) Open() bool { return dialog.open }

// View renders a compact modal with an explicit close hint.
func (dialog ErrorDialog) View(width int) string {
	if !dialog.open {
		return ""
	}
	content := dialog.styles.Title.Render(dialog.title) + "\n\n" +
		dialog.styles.Body.Render(dialog.body) + "\n\n" +
		dialog.styles.Hint.Render("Esc or Enter to close")
	return dialog.styles.Frame.Width(max(20, min(width-8, 72))).Render(content)
}
