package components

// NamespaceCandidate is one safe current-Context Namespace name.
type NamespaceCandidate struct {
	Name string
}

// NamespacePicker renders typed Namespace candidates and owns no editor.
type NamespacePicker struct {
	list pickerList[NamespaceCandidate]
}

// NewNamespacePicker creates one closed Namespace Picker.
func NewNamespacePicker(styles PickerStyles) NamespacePicker {
	return NamespacePicker{list: newPickerList("No matching Namespaces", func(candidate NamespaceCandidate) string {
		return candidate.Name
	}, styles)}
}

func (picker *NamespacePicker) SetLoading() { picker.list.setLoading() }
func (picker *NamespacePicker) SetFailed()  { picker.list.setFailed() }
func (picker *NamespacePicker) SetCandidates(values []NamespaceCandidate) {
	picker.list.setItems(values)
}
func (picker *NamespacePicker) SetMaxVisible(limit int) { picker.list.setMaxVisible(limit) }
func (picker *NamespacePicker) SetWidth(width int)      { picker.list.setWidth(width) }
func (picker *NamespacePicker) Close()                  { picker.list.close() }
func (picker *NamespacePicker) Move(delta int)          { picker.list.move(delta) }
func (picker NamespacePicker) Open() bool               { return picker.list.isOpen() }
func (picker NamespacePicker) Height() int              { return picker.list.height() }
func (picker NamespacePicker) View() string             { return picker.list.view() }
func (picker NamespacePicker) SourceCount() int         { return picker.list.sourceCount() }
func (picker NamespacePicker) VisibleCount() int        { return picker.list.visibleCount() }
func (picker NamespacePicker) Selected() (NamespaceCandidate, bool) {
	return picker.list.selectedItem()
}
