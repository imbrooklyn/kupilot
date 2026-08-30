package components

import (
	"errors"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	MinComposerRows    = 1
	MaxComposerRows    = 8
	maxContentRows     = 65_536
	defaultPlaceholder = "Ask a question, or type / for commands"
)

// ErrComposerLimit reports that an edit would exceed the bounded draft size.
var ErrComposerLimit = errors.New("composer draft limit exceeded")

// ComposerStyles keeps the editable and historic user surfaces visually related.
type ComposerStyles struct {
	FocusedSurface lipgloss.Style
	BlurredSurface lipgloss.Style
	Textarea       textarea.Styles
}

// Composer owns the TUI's sole editable textarea and its local draft history.
type Composer struct {
	input       textarea.Model
	styles      ComposerStyles
	width       int
	maxBytes    int
	history     []string
	historyAt   int
	historyOpen bool
	secretMode  bool
}

// NewComposer creates a focused one-to-eight-row multiline editor.
func NewComposer(styles ComposerStyles, maxBytes int) Composer {
	input := textarea.New()
	input.Prompt = "› "
	input.Placeholder = defaultPlaceholder
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.CharLimit = 0
	input.DynamicHeight = true
	input.MinHeight = MinComposerRows
	input.MaxHeight = MaxComposerRows
	input.MaxContentHeight = maxContentRows
	input.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter", "ctrl+j"),
		key.WithHelp("shift+enter", "newline"),
	)
	input.KeyMap.Paste = key.Binding{}
	input.SetStyles(styles.Textarea)
	// A real terminal cursor gives the operating system input method the exact
	// insertion point. A virtual cursor is only painted into text and causes IME
	// candidate windows and placeholder composition to use stale coordinates.
	input.SetVirtualCursor(false)
	input.SetHeight(MinComposerRows)
	input.SetWidth(40)
	_ = input.Focus()
	return Composer{
		input:     input,
		styles:    styles,
		width:     40,
		maxBytes:  maxBytes,
		historyAt: 0,
	}
}

// SetStyles updates presentation without replacing the sole editor or any
// draft, cursor, history, focus, or secret-mode state.
func (composer *Composer) SetStyles(styles ComposerStyles) {
	composer.styles = styles
	composer.input.SetStyles(styles.Textarea)
}

// Update applies one already-sanitized input message without exceeding maxBytes.
func (composer Composer) Update(msg tea.Msg) (Composer, tea.Cmd, error) {
	original := composer.input.Value()
	additional := 0
	switch value := msg.(type) {
	case tea.PasteMsg:
		additional = len(value.Content)
	case tea.KeyPressMsg:
		if key.Matches(value, composer.input.KeyMap.InsertNewline) {
			additional = 1
		} else {
			additional = len(value.Text)
		}
	}
	if additional > 0 && composer.maxBytes > 0 && len(composer.input.Value())+additional > composer.maxBytes {
		return composer, nil, ErrComposerLimit
	}

	if _, historyKey := msg.(tea.KeyPressMsg); historyKey {
		composer.closeHistory()
	}
	var cmd tea.Cmd
	composer.input, cmd = composer.input.Update(msg)
	if additional > 0 && len(composer.input.Value()) != len(original)+additional {
		composer.input.SetValue(original)
		composer.input.MoveToEnd()
		return composer, nil, ErrComposerLimit
	}
	return composer, cmd, nil
}

// SetWidth updates the editable content width inside the quiet surface padding.
func (composer *Composer) SetWidth(width int) {
	if width < 8 {
		width = 8
	}
	composer.width = width
	composer.input.SetWidth(max(1, width-2))
}

// SetMaxRows tightens the editor for a small terminal while preserving 1-8 rows.
func (composer *Composer) SetMaxRows(rows int) {
	rows = max(MinComposerRows, min(rows, MaxComposerRows))
	composer.input.MaxHeight = rows
	if composer.input.Height() > rows {
		composer.input.SetHeight(rows)
	}
}

// Value returns the current draft.
func (composer Composer) Value() string { return composer.input.Value() }

// SetValue replaces the current draft and moves the cursor to the end.
func (composer *Composer) SetValue(value string) {
	composer.input.SetValue(value)
	composer.input.MoveToEnd()
}

// Reset clears the draft without changing the retained history.
func (composer *Composer) Reset() {
	composer.input.Reset()
	composer.closeHistory()
}

// SetPlaceholder changes only the code-authored purpose hint for the one editor.
func (composer *Composer) SetPlaceholder(value string) {
	composer.input.Placeholder = value
}

// ResetPlaceholder restores the normal question and Slash-command hint.
func (composer *Composer) ResetPlaceholder() {
	composer.input.Placeholder = defaultPlaceholder
}

// SetMaxBytes changes the bounded input ceiling for one fixed composer mode.
func (composer *Composer) SetMaxBytes(limit int) {
	if limit > 0 {
		composer.maxBytes = limit
	}
}

// SetSecretMode masks rendering and disables history eligibility. The actual
// value remains available only until the setup request is submitted.
func (composer *Composer) SetSecretMode(enabled bool) {
	composer.secretMode = enabled
	composer.closeHistory()
}

// RecordSubmission adds one submitted message to local prompt history.
func (composer *Composer) RecordSubmission(value string) {
	if composer.secretMode || strings.TrimSpace(value) == "" {
		return
	}
	if len(composer.history) == 0 || composer.history[len(composer.history)-1] != value {
		composer.history = append(composer.history, value)
	}
	composer.closeHistory()
}

// PreviousHistory recalls the next older submitted draft.
func (composer *Composer) PreviousHistory() bool {
	if len(composer.history) == 0 || !composer.HistoryEligible() {
		return false
	}
	if !composer.historyOpen {
		composer.historyOpen = true
		composer.historyAt = len(composer.history)
	}
	if composer.historyAt > 0 {
		composer.historyAt--
		composer.SetValue(composer.history[composer.historyAt])
		composer.historyOpen = true
		return true
	}
	return false
}

// NextHistory recalls a newer submitted draft, ending at an empty composer.
func (composer *Composer) NextHistory() bool {
	if !composer.historyOpen {
		return false
	}
	if composer.historyAt < len(composer.history)-1 {
		composer.historyAt++
		composer.SetValue(composer.history[composer.historyAt])
		composer.historyOpen = true
		return true
	}
	composer.input.Reset()
	composer.closeHistory()
	return true
}

// HistoryEligible reports whether an explicit history shortcut may recall a
// submitted draft without exposing secret-mode input.
func (composer Composer) HistoryEligible() bool {
	return !composer.secretMode && (composer.historyOpen || composer.input.Value() == "" && composer.input.LineCount() == 1)
}

func (composer *Composer) closeHistory() {
	composer.historyOpen = false
	composer.historyAt = len(composer.history)
}

// Height returns the current visible editable-row count.
func (composer Composer) Height() int { return composer.input.Height() }

// FrameHeight includes the quiet blank row above and below the editable rows.
func (composer Composer) FrameHeight() int { return composer.input.Height() + 2 }

// ScrollOffset reports the textarea's internal vertical scroll position.
func (composer Composer) ScrollOffset() int { return composer.input.ScrollYOffset() }

// Focused reports whether the only editor owns keyboard focus.
func (composer Composer) Focused() bool { return composer.input.Focused() }

// Focus gives keyboard focus to the sole textarea.
func (composer *Composer) Focus() tea.Cmd { return composer.input.Focus() }

// Blur removes keyboard focus from the textarea.
func (composer *Composer) Blur() { composer.input.Blur() }

// Cursor returns the real cursor position relative to the rendered composer.
// The root view adds the composer's vertical layout offset.
func (composer Composer) Cursor() *tea.Cursor {
	composer.prepareRenderInput()
	cursor := composer.input.Cursor()
	if cursor == nil {
		return nil
	}
	style := composer.styles.BlurredSurface
	if composer.input.Focused() {
		style = composer.styles.FocusedSurface
	}
	cursor.Position.X += style.GetMarginLeft() + style.GetBorderLeftSize() + style.GetPaddingLeft()
	cursor.Position.Y += style.GetMarginTop() + style.GetBorderTopSize() + style.GetPaddingTop()
	return cursor
}

// View renders the active user-input surface.
func (composer Composer) View() string {
	style := composer.styles.BlurredSurface
	if composer.input.Focused() {
		style = composer.styles.FocusedSurface
	}
	composer.prepareRenderInput()
	return style.Width(max(1, composer.width-2)).Render(composer.input.View())
}

func (composer *Composer) prepareRenderInput() {
	if composer.secretMode && composer.input.Value() != "" {
		composer.input.SetValue(strings.Repeat("•", utf8.RuneCountInString(composer.input.Value())))
		composer.input.MoveToEnd()
	}
}
