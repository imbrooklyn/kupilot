package tui

import (
	"runtime"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// KeyMap defines the fixed keyboard priority surface for the root reducer.
type KeyMap struct {
	Submit           key.Binding
	Newline          key.Binding
	Complete         key.Binding
	EditFollowUp     key.Binding
	Previous         key.Binding
	Next             key.Binding
	PreviousAlt      key.Binding
	NextAlt          key.Binding
	Reverse          key.Binding
	Evidence         key.Binding
	Find             key.Binding
	HistorySearch    key.Binding
	Undo             key.Binding
	Redo             key.Binding
	PreviousUser     key.Binding
	NextUser         key.Binding
	PreviousAgent    key.Binding
	NextAgent        key.Binding
	PreviousIssue    key.Binding
	NextIssue        key.Binding
	PreviousApproval key.Binding
	NextApproval     key.Binding
	Close            key.Binding
	Copy             key.Binding
	TranscriptUp     key.Binding
	TranscriptDown   key.Binding
	Cancel           key.Binding
	Quit             key.Binding
}

// DefaultKeyMap returns fixed bindings; it cannot register commands.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Submit:           key.NewBinding(key.WithKeys("enter")),
		Newline:          key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j")),
		Complete:         key.NewBinding(key.WithKeys("tab")),
		EditFollowUp:     key.NewBinding(key.WithKeys("alt+up")),
		Previous:         key.NewBinding(key.WithKeys("up")),
		Next:             key.NewBinding(key.WithKeys("down")),
		PreviousAlt:      key.NewBinding(key.WithKeys("ctrl+p")),
		NextAlt:          key.NewBinding(key.WithKeys("ctrl+n")),
		Reverse:          key.NewBinding(key.WithKeys("shift+tab")),
		Evidence:         key.NewBinding(key.WithKeys("alt+e")),
		Find:             key.NewBinding(key.WithKeys("alt+s")),
		HistorySearch:    key.NewBinding(key.WithKeys("ctrl+r")),
		Undo:             key.NewBinding(key.WithKeys("alt+z")),
		Redo:             key.NewBinding(key.WithKeys("alt+y")),
		PreviousUser:     key.NewBinding(key.WithKeys("alt+u")),
		NextUser:         key.NewBinding(key.WithKeys("alt+shift+u")),
		PreviousAgent:    key.NewBinding(key.WithKeys("alt+a")),
		NextAgent:        key.NewBinding(key.WithKeys("alt+shift+a")),
		PreviousIssue:    key.NewBinding(key.WithKeys("alt+i")),
		NextIssue:        key.NewBinding(key.WithKeys("alt+shift+i")),
		PreviousApproval: key.NewBinding(key.WithKeys("alt+p")),
		NextApproval:     key.NewBinding(key.WithKeys("alt+shift+p")),
		Close:            key.NewBinding(key.WithKeys("esc")),
		Copy:             transcriptCopyBinding(runtime.GOOS),
		TranscriptUp:     key.NewBinding(key.WithKeys("pgup")),
		TranscriptDown:   key.NewBinding(key.WithKeys("pgdown")),
		Cancel:           key.NewBinding(key.WithKeys("ctrl+x")),
		Quit:             key.NewBinding(key.WithKeys("ctrl+c")),
	}
}

func transcriptCopyBinding(goos string) key.Binding {
	if goos == "darwin" {
		return key.NewBinding(key.WithKeys("super+c"), key.WithHelp("Command+C", "copy selection"))
	}
	// Accept Command+C from a Mac terminal connected over SSH as well.
	return key.NewBinding(key.WithKeys("ctrl+shift+c", "ctrl+c", "super+c"), key.WithHelp("Ctrl+Shift+C", "copy selection"))
}

func copyKeystroke(message tea.KeyPressMsg) string {
	// Associated text and Caps Lock must not hide a modified copy key. Some
	// terminals encode Ctrl+Shift+C as an uppercase C with only Ctrl set.
	event := tea.Key(message)
	if event.Code == 'C' {
		event.Code = 'c'
	}
	if event.BaseCode == 'C' {
		event.BaseCode = 'c'
	}
	return event.Keystroke()
}
