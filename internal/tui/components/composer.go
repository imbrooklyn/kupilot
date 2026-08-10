package components

import (
	"errors"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	MinComposerRows = 3
	MaxComposerRows = 8
	maxContentRows  = 65_536
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
}

// NewComposer creates a focused three-to-eight-row multiline editor.
func NewComposer(styles ComposerStyles, maxBytes int) Composer {
	input := textarea.New()
	input.Prompt = ""
	input.Placeholder = "Ask a question, or type / for commands"
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.CharLimit = 0
	input.DynamicHeight = true
	input.MinHeight = MinComposerRows
	input.MaxHeight = MaxComposerRows
	input.MaxContentHeight = maxContentRows
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline"))
	input.KeyMap.Paste = key.Binding{}
	input.SetStyles(styles.Textarea)
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

// Update applies one already-sanitized input message without exceeding maxBytes.
func (composer Composer) Update(msg tea.Msg) (Composer, tea.Cmd, error) {
	original := composer.input.Value()
	additional := 0
	switch value := msg.(type) {
	case tea.PasteMsg:
		additional = len(value.Content)
	case tea.KeyPressMsg:
		additional = len(value.Text)
		if value.Keystroke() == "ctrl+j" {
			additional = 1
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

// SetWidth updates the editable content width while preserving the border frame.
func (composer *Composer) SetWidth(width int) {
	if width < 8 {
		width = 8
	}
	composer.width = width
	composer.input.SetWidth(max(1, width-4))
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

// RecordSubmission adds one submitted message to local prompt history.
func (composer *Composer) RecordSubmission(value string) {
	if strings.TrimSpace(value) == "" {
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

// HistoryEligible reports whether Up/Down should recall history instead of moving a cursor.
func (composer Composer) HistoryEligible() bool {
	return composer.historyOpen || composer.input.Value() == "" && composer.input.LineCount() == 1
}

func (composer *Composer) closeHistory() {
	composer.historyOpen = false
	composer.historyAt = len(composer.history)
}

// Height returns the current visible editable-row count.
func (composer Composer) Height() int { return composer.input.Height() }

// FrameHeight includes the surface border around the editable rows.
func (composer Composer) FrameHeight() int { return composer.input.Height() + 2 }

// ScrollOffset reports the textarea's internal vertical scroll position.
func (composer Composer) ScrollOffset() int { return composer.input.ScrollYOffset() }

// Focused reports whether the only editor owns keyboard focus.
func (composer Composer) Focused() bool { return composer.input.Focused() }

// Focus gives keyboard focus to the sole textarea.
func (composer *Composer) Focus() tea.Cmd { return composer.input.Focus() }

// Blur removes keyboard focus from the textarea.
func (composer *Composer) Blur() { composer.input.Blur() }

// View renders the active user-input surface.
func (composer Composer) View() string {
	style := composer.styles.BlurredSurface
	if composer.input.Focused() {
		style = composer.styles.FocusedSurface
	}
	return style.Width(max(1, composer.width-4)).Render(composer.input.View())
}
