package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
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
	return model.configureView(view)
}

func (model Model) configureView(view tea.View) tea.View {
	// Keep every mutable runtime surface inside one full-screen buffer. After
	// Bubble Tea restores the primary screen, the composition root writes one
	// completed terminal-safe transcript for terminal-owned scrollback.
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
	content, _, _ := model.renderLayout()
	return content
}

func (model Model) renderLayout() (content string, composerY int, composerVisible bool) {
	gap := model.layoutGap()
	topSections := make([]string, 0, 2)
	if transcript := model.transcript.View(); transcript != "" {
		topSections = append(topSections, transcript)
	}
	if working := model.workingView(); working != "" {
		topSections = append(topSections, working)
	}
	top := joinLayoutSections(topSections, gap)

	bottomSections := make([]string, 0, 4)
	composerOffset := 0
	if label := model.inputLabelView(); label != "" {
		bottomSections = append(bottomSections, label)
		composerOffset = lipgloss.Height(label)
	}
	bottomSections = append(bottomSections, model.composer.View())
	if model.pickerOpen() {
		bottomSections = append(bottomSections, model.pickerView())
	} else if model.slashMenu.Open() {
		bottomSections = append(bottomSections, model.slashMenu.View())
	}
	bottom := lipgloss.JoinVertical(lipgloss.Left, bottomSections...)
	if footer := model.footerView(); footer != "" {
		bottom = joinLayoutSections([]string{bottom, footer}, gap)
	}

	spacer := max(0, model.height-lipgloss.Height(top)-lipgloss.Height(bottom))
	main := bottom
	if top != "" {
		spacer = max(gap, spacer)
		main = top + strings.Repeat("\n", spacer+1) + bottom
		composerY = lipgloss.Height(top) + spacer + composerOffset
	} else {
		main = strings.Repeat("\n", spacer) + bottom
		composerY = spacer + composerOffset
	}
	contentWidth := model.contentWidth()
	main = lipgloss.NewStyle().Width(contentWidth).Render(main)
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
		return main, composerY, true
	}

	x := max(0, (model.width-lipgloss.Width(overlay))/2)
	y := max(0, (model.height-lipgloss.Height(overlay))/2)
	baseLayer := lipgloss.NewLayer(main).Z(0)
	overlayLayer := lipgloss.NewLayer(overlay).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlayLayer).Render(), composerY, false
}

func (model Model) inputLabelView() string {
	label := ""
	hint := ""
	switch {
	case model.modelSetup != nil:
		parts := strings.SplitN(model.modelSetupView(), " · ", 2)
		label = parts[0]
		if len(parts) == 2 {
			hint = parts[1]
		}
	case model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetEntry:
		label = "Export target"
		hint = "absolute .md path"
	case model.pickerOpen():
		hint = "type to filter"
		switch model.activePicker {
		case application.UICompletionContext:
			label = "Context"
		case application.UICompletionNamespace:
			label = "Namespace"
		case application.UICompletionResource:
			label = "Resource"
		case application.UICompletionSession:
			label = "Session"
		}
	case model.slashMenu.Open():
		label = "Command"
		hint = "fixed Slash commands"
	}
	if label == "" {
		return ""
	}
	result := model.styles.footer.Value.Render(label)
	if hint != "" {
		result += model.styles.footer.Separator.Render(" · ") + model.styles.footer.Label.Render(hint)
	}
	return result
}

func joinLayoutSections(sections []string, gap int) string {
	visible := make([]string, 0, len(sections))
	for _, section := range sections {
		if section != "" {
			visible = append(visible, section)
		}
	}
	return strings.Join(visible, strings.Repeat("\n", max(0, gap)+1))
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
	return lipgloss.NewStyle().MaxWidth(model.contentWidth()).Render(line)
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
	return model.footer.View(model.contentWidth(), components.FooterStatus{
		Context: model.scope.Context, Namespace: model.scope.Namespace,
		ReadOnly: model.scope.ReadOnly, ScopeSwitching: model.scope.Switching,
		Approval: approvalStatus,
	})
}
