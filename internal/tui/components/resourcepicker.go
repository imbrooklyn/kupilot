package components

import "strings"

// ResourceCandidate is one bounded current-Namespace target summary.
type ResourceCandidate struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	Status     string
}

// ResourcePicker renders at most eight of at most fifty source candidates.
type ResourcePicker struct {
	list pickerList[ResourceCandidate]
}

// NewResourcePicker creates one closed Resource Picker.
func NewResourcePicker(styles PickerStyles) ResourcePicker {
	return ResourcePicker{list: newPickerList("No matching resources", func(candidate ResourceCandidate) string {
		parts := []string{candidate.Kind + "/" + candidate.Name}
		if candidate.Status != "" {
			parts = append(parts, candidate.Status)
		}
		return strings.Join(parts, " · ")
	}, styles)}
}

func (picker *ResourcePicker) SetLoading()                              { picker.list.setLoading() }
func (picker *ResourcePicker) SetStyles(styles PickerStyles)            { picker.list.setStyles(styles) }
func (picker *ResourcePicker) SetFailed()                               { picker.list.setFailed() }
func (picker *ResourcePicker) SetCandidates(values []ResourceCandidate) { picker.list.setItems(values) }
func (picker *ResourcePicker) SetMaxVisible(limit int)                  { picker.list.setMaxVisible(limit) }
func (picker *ResourcePicker) SetWidth(width int)                       { picker.list.setWidth(width) }
func (picker *ResourcePicker) Close()                                   { picker.list.close() }
func (picker *ResourcePicker) Move(delta int)                           { picker.list.move(delta) }
func (picker ResourcePicker) Open() bool                                { return picker.list.isOpen() }
func (picker ResourcePicker) Height() int                               { return picker.list.height() }
func (picker ResourcePicker) View() string                              { return picker.list.view() }
func (picker ResourcePicker) SourceCount() int                          { return picker.list.sourceCount() }
func (picker ResourcePicker) VisibleCount() int                         { return picker.list.visibleCount() }
func (picker ResourcePicker) Selected() (ResourceCandidate, bool)       { return picker.list.selectedItem() }
