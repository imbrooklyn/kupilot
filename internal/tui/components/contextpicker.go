package components

// ContextCandidate is safe Context completion text with no server or user data.
type ContextCandidate struct {
	Name    string
	Current bool
}

// ContextPicker renders typed Context candidates and owns no editor.
type ContextPicker struct {
	list pickerList[ContextCandidate]
}

// NewContextPicker creates one closed Context Picker.
func NewContextPicker(styles PickerStyles) ContextPicker {
	return ContextPicker{list: newPickerList("No matching Contexts", func(candidate ContextCandidate) string {
		if candidate.Current {
			return candidate.Name + " · current"
		}
		return candidate.Name
	}, styles)}
}

func (picker *ContextPicker) SetLoading()                             { picker.list.setLoading() }
func (picker *ContextPicker) SetStyles(styles PickerStyles)           { picker.list.setStyles(styles) }
func (picker *ContextPicker) SetFailed()                              { picker.list.setFailed() }
func (picker *ContextPicker) SetCandidates(values []ContextCandidate) { picker.list.setItems(values) }
func (picker *ContextPicker) SetMaxVisible(limit int)                 { picker.list.setMaxVisible(limit) }
func (picker *ContextPicker) SetWidth(width int)                      { picker.list.setWidth(width) }
func (picker *ContextPicker) Close()                                  { picker.list.close() }
func (picker *ContextPicker) Move(delta int)                          { picker.list.move(delta) }
func (picker ContextPicker) Open() bool                               { return picker.list.isOpen() }
func (picker ContextPicker) Height() int                              { return picker.list.height() }
func (picker ContextPicker) View() string                             { return picker.list.view() }
func (picker ContextPicker) SourceCount() int                         { return picker.list.sourceCount() }
func (picker ContextPicker) VisibleCount() int                        { return picker.list.visibleCount() }
func (picker ContextPicker) Selected() (ContextCandidate, bool)       { return picker.list.selectedItem() }
