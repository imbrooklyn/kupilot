package tui

import "charm.land/bubbles/v2/key"

// KeyMap defines the fixed keyboard priority surface for the root reducer.
type KeyMap struct {
	Submit         key.Binding
	Newline        key.Binding
	Complete       key.Binding
	EditFollowUp   key.Binding
	Previous       key.Binding
	Next           key.Binding
	PreviousAlt    key.Binding
	NextAlt        key.Binding
	Reverse        key.Binding
	Evidence       key.Binding
	Find           key.Binding
	Close          key.Binding
	TranscriptUp   key.Binding
	TranscriptDown key.Binding
	Cancel         key.Binding
	Quit           key.Binding
}

// DefaultKeyMap returns fixed bindings; it cannot register commands.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Submit:         key.NewBinding(key.WithKeys("enter")),
		Newline:        key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j")),
		Complete:       key.NewBinding(key.WithKeys("tab")),
		EditFollowUp:   key.NewBinding(key.WithKeys("alt+up")),
		Previous:       key.NewBinding(key.WithKeys("up")),
		Next:           key.NewBinding(key.WithKeys("down")),
		PreviousAlt:    key.NewBinding(key.WithKeys("ctrl+p")),
		NextAlt:        key.NewBinding(key.WithKeys("ctrl+n")),
		Reverse:        key.NewBinding(key.WithKeys("shift+tab")),
		Evidence:       key.NewBinding(key.WithKeys("ctrl+e")),
		Find:           key.NewBinding(key.WithKeys("ctrl+f")),
		Close:          key.NewBinding(key.WithKeys("esc")),
		TranscriptUp:   key.NewBinding(key.WithKeys("pgup")),
		TranscriptDown: key.NewBinding(key.WithKeys("pgdown")),
		Cancel:         key.NewBinding(key.WithKeys("ctrl+x")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c")),
	}
}
