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
	hint   string
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
}

// Close closes and clears the modal.
func (dialog *ErrorDialog) Close() {
	dialog.open = false
	dialog.title = ""
	dialog.body = ""
	dialog.hint = ""
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
		dialog.styles.Hint.Render(dialog.hint)
	return dialog.styles.Frame.Width(max(1, min(width-6, 72))).Render(content)
}
