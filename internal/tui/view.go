package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

// View deterministically renders transcript, composer, suggestions, then footer.
func (model Model) View() tea.View {
	content, composerY, composerVisible := model.renderLayout()
	view := tea.NewView(content)
	if composerVisible {
		if cursor := model.composer.Cursor(); cursor != nil {
			cursor.Position.Y += composerY
			view.Cursor = cursor
		}
	}
	// The primary buffer keeps committed conversation blocks in terminal
	// scrollback after Kupilot exits. Dialogs remain managed overlays inside
	// the same bounded inline frame.
	view.AltScreen = false
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
	content, _, _ := model.renderLayout()
	return content
}

func (model Model) renderLayout() (content string, composerY int, composerVisible bool) {
	sections := make([]string, 0, 6)
	if transcript := model.transcript.View(); transcript != "" {
		sections = append(sections, transcript)
	}
	if prompt := model.modelSetupView(); prompt != "" {
		sections = append(sections, prompt)
	}
	if working := model.workingView(); working != "" {
		sections = append(sections, working)
	}
	for _, section := range sections {
		composerY += lipgloss.Height(section)
	}
	sections = append(sections, model.composer.View())
	if model.pickerOpen() {
		sections = append(sections, model.pickerView())
	} else if model.slashMenu.Open() {
		sections = append(sections, model.slashMenu.View())
	}
	sections = append(sections, model.footerView())
	main := lipgloss.JoinVertical(lipgloss.Left, sections...)
	main = lipgloss.NewStyle().Width(model.width).Render(main)

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
		return main, composerY, true
	}

	x := max(0, (model.width-lipgloss.Width(overlay))/2)
	y := max(0, (model.height-lipgloss.Height(overlay))/2)
	baseLayer := lipgloss.NewLayer(main).Z(0)
	overlayLayer := lipgloss.NewLayer(overlay).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlayLayer).Render(), composerY, false
}

func (model Model) workingView() string {
	if !model.run.Active || model.run.Terminal {
		return ""
	}
	bullet := "•"
	bulletStyle := model.styles.working.Normal
	if model.workingFrame/6%2 == 1 {
		bullet = "◦"
		bulletStyle = model.styles.working.Muted
	}
	line := bulletStyle.Render(bullet) + " " + model.shimmerText("Working")
	line += model.styles.working.Muted.Render(" (" + components.FormatElapsedCompact(model.workingElapsed()) + " • ")
	line += model.styles.working.Normal.Render("esc")
	line += model.styles.working.Muted.Render(" to interrupt)")
	return lipgloss.NewStyle().MaxWidth(max(1, model.width)).Render(line)
}

func (model Model) workingElapsed() time.Duration {
	if model.run.StartedAt.IsZero() || model.workingAt.IsZero() || model.workingAt.Before(model.run.StartedAt) {
		return 0
	}
	return model.workingAt.Sub(model.run.StartedAt)
}

func (model Model) shimmerText(value string) string {
	const shimmerFrames = 20
	const padding = 10
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	period := len(runes) + padding*2
	position := int(model.workingFrame%shimmerFrames) * period / shimmerFrames
	var result strings.Builder
	for index, current := range runes {
		distance := index + padding - position
		if distance < 0 {
			distance = -distance
		}
		style := model.styles.working.Muted
		switch {
		case distance <= 1:
			style = model.styles.working.Highlight
		case distance <= 3:
			style = model.styles.working.Normal
		}
		result.WriteString(style.Render(string(current)))
	}
	return result.String()
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
