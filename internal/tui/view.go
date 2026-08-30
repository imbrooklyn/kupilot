package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

// View deterministically renders transcript, composer, suggestions, then footer.
func (model Model) View() tea.View {
	view := tea.NewView(model.render())
	view.AltScreen = true
	view.ReportFocus = true
	view.DisableBracketedPasteMode = false
	// Mouse reporting prevents ordinary terminal drag-selection. Keep it off
	// so visible transcript text can be selected and copied natively.
	view.MouseMode = tea.MouseModeNone
	view.OnMouse = func(message tea.MouseMsg) tea.Cmd {
		wheel, ok := message.(tea.MouseWheelMsg)
		if !ok {
			return nil
		}
		return func() tea.Msg { return wheel }
	}
	return view
}

func (model Model) render() string {
	sections := []string{model.transcript.View()}
	if prompt := model.modelSetupView(); prompt != "" {
		sections = append(sections, prompt)
	}
	sections = append(sections, model.composer.View())
	if model.pickerOpen() {
		sections = append(sections, model.pickerView())
	} else if model.slashMenu.Open() {
		sections = append(sections, model.slashMenu.View())
	}
	sections = append(sections, model.footerView())
	main := lipgloss.JoinVertical(lipgloss.Left, sections...)
	main = lipgloss.Place(model.width, model.height, lipgloss.Left, lipgloss.Top, main)

	overlay := ""
	switch {
	case model.approvalDialog.Open():
		overlay = model.approvalDialog.View(model.width)
	case model.scopeConflict.Open():
		overlay = model.scopeConflict.View(model.width)
	case model.evidenceDialog.Open():
		overlay = model.evidenceDialog.View(model.width, model.height)
	case model.dialog.Open():
		overlay = model.dialog.View(model.width)
	}
	if overlay == "" {
		return main
	}

	x := max(0, (model.width-lipgloss.Width(overlay))/2)
	y := max(0, (model.height-lipgloss.Height(overlay))/2)
	baseLayer := lipgloss.NewLayer(main).Z(0)
	overlayLayer := lipgloss.NewLayer(overlay).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlayLayer).Render()
}

func (model Model) footerView() string {
	approvalStatus := ""
	if model.pendingApproval != nil {
		approvalStatus = "approval pending"
		if model.approvalState == domain.ApprovalStateApproved {
			approvalStatus = "approved · not executed"
		} else if model.pendingApprovalID != 0 {
			approvalStatus = "approval in progress"
		}
	}
	return model.footer.View(model.width, components.FooterStatus{
		Context: model.scope.Context, Namespace: model.scope.Namespace,
		ReadOnly: model.scope.ReadOnly, ScopeSwitching: model.scope.Switching,
		Approval: approvalStatus,
	})
}
