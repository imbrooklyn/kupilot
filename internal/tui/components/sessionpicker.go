package components

import "strings"

// SessionCandidate contains safe resume metadata without a Message preview.
type SessionCandidate struct {
	ID        string
	Title     string
	UpdatedAt string
	Context   string
	Namespace string
	Privacy   string
}

// SessionPicker renders eligible Session candidates and owns no editor.
type SessionPicker struct {
	list pickerList[SessionCandidate]
}

// NewSessionPicker creates one closed Session Picker.
func NewSessionPicker(styles PickerStyles) SessionPicker {
	return SessionPicker{list: newPickerList("No resumable Sessions", func(candidate SessionCandidate) string {
		title := candidate.Title
		if title == "" {
			title = "Untitled Session"
		}
		parts := []string{title}
		if candidate.UpdatedAt != "" {
			parts = append(parts, candidate.UpdatedAt)
		}
		if candidate.Context != "" && candidate.Namespace != "" {
			parts = append(parts, candidate.Context+" / "+candidate.Namespace)
		}
		if privacy := sessionPrivacyLabel(candidate.Privacy); privacy != "" {
			parts = append(parts, privacy)
		}
		return strings.Join(parts, " · ")
	}, styles)}
}

func sessionPrivacyLabel(value string) string {
	switch value {
	case "standard":
		return "history saved"
	case "minimal":
		return "memory only"
	default:
		return ""
	}
}

func (picker *SessionPicker) SetLoading()                             { picker.list.setLoading() }
func (picker *SessionPicker) SetFailed()                              { picker.list.setFailed() }
func (picker *SessionPicker) SetCandidates(values []SessionCandidate) { picker.list.setItems(values) }
func (picker *SessionPicker) SetMaxVisible(limit int)                 { picker.list.setMaxVisible(limit) }
func (picker *SessionPicker) SetWidth(width int)                      { picker.list.setWidth(width) }
func (picker *SessionPicker) Close()                                  { picker.list.close() }
func (picker *SessionPicker) Move(delta int)                          { picker.list.move(delta) }
func (picker SessionPicker) Open() bool                               { return picker.list.isOpen() }
func (picker SessionPicker) Height() int                              { return picker.list.height() }
func (picker SessionPicker) View() string                             { return picker.list.view() }
func (picker SessionPicker) SourceCount() int                         { return picker.list.sourceCount() }
func (picker SessionPicker) VisibleCount() int                        { return picker.list.visibleCount() }
func (picker SessionPicker) Selected() (SessionCandidate, bool)       { return picker.list.selectedItem() }

// Remove deletes one committed Session result from the current bounded view.
func (picker *SessionPicker) Remove(id string) bool {
	if id == "" {
		return false
	}
	for index := range picker.list.items {
		if picker.list.items[index].ID != id {
			continue
		}
		copy(picker.list.items[index:], picker.list.items[index+1:])
		picker.list.items = picker.list.items[:len(picker.list.items)-1]
		if len(picker.list.items) == 0 {
			picker.list.selected = 0
			picker.list.offset = 0
		} else if picker.list.selected >= len(picker.list.items) {
			picker.list.selected = len(picker.list.items) - 1
		}
		picker.list.ensureVisible()
		return true
	}
	return false
}
