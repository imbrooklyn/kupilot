package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// ScopeConflictStyles keep confirmation meaning visible without relying on color.
type ScopeConflictStyles struct {
	Frame    lipgloss.Style
	Title    lipgloss.Style
	Body     lipgloss.Style
	Selected lipgloss.Style
	Muted    lipgloss.Style
}

// ScopeConflictDialog lets the user choose whether to verify a historic scope
// candidate before separately accepting a resumed Session. It owns no editor.
type ScopeConflictDialog struct {
	open          bool
	useSavedScope bool
	current       string
	saved         string
	styles        ScopeConflictStyles
}

// NewScopeConflictDialog creates one closed default-safe confirmation.
func NewScopeConflictDialog(styles ScopeConflictStyles) ScopeConflictDialog {
	return ScopeConflictDialog{styles: styles}
}

// SetStyles updates presentation without changing the safe default selection.
func (dialog *ScopeConflictDialog) SetStyles(styles ScopeConflictStyles) { dialog.styles = styles }

// Show opens with "Keep current scope" selected by default.
func (dialog *ScopeConflictDialog) Show(current, saved string) {
	dialog.open = true
	dialog.useSavedScope = false
	dialog.current = current
	dialog.saved = saved
}

// Close clears the non-authoritative display candidates.
func (dialog *ScopeConflictDialog) Close() {
	dialog.open = false
	dialog.useSavedScope = false
	dialog.current = ""
	dialog.saved = ""
}

// Move switches between the two explicit choices.
func (dialog *ScopeConflictDialog) Move(_ int) {
	if dialog.open {
		dialog.useSavedScope = !dialog.useSavedScope
	}
}

func (dialog ScopeConflictDialog) Open() bool          { return dialog.open }
func (dialog ScopeConflictDialog) UseSavedScope() bool { return dialog.useSavedScope }

// View renders one bounded non-editable scope confirmation.
func (dialog ScopeConflictDialog) View(width int) string {
	if !dialog.open {
		return ""
	}
	keepMarker, savedMarker := "› ", "  "
	keepStyle, savedStyle := dialog.styles.Selected, dialog.styles.Body
	if dialog.useSavedScope {
		keepMarker, savedMarker = "  ", "› "
		keepStyle, savedStyle = dialog.styles.Body, dialog.styles.Selected
	}
	content := []string{
		dialog.styles.Title.Render("Confirm Session scope"),
		dialog.styles.Body.Render("The resumed Session saved a different, unverified scope candidate."),
		dialog.styles.Muted.Render("Current: " + dialog.current),
		dialog.styles.Muted.Render("Saved:   " + dialog.saved),
		keepStyle.Render(keepMarker + "Keep current scope"),
		savedStyle.Render(savedMarker + "Verify saved scope, then resume"),
		dialog.styles.Muted.Render("Enter confirms. Esc or Ctrl+C cancels resume."),
	}
	return dialog.styles.Frame.Width(max(1, min(width-6, 72))).Render(strings.Join(content, "\n"))
}
