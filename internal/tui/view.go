package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// View deterministically renders transcript, composer, suggestions, then footer.
func (model Model) View() tea.View {
	view := tea.NewView(model.render())
	view.AltScreen = true
	view.ReportFocus = false
	view.DisableBracketedPasteMode = false
	return view
}

func (model Model) render() string {
	sections := []string{
		model.transcript.View(),
		model.composer.View(),
	}
	if model.slashMenu.Open() {
		sections = append(sections, model.slashMenu.View())
	}
	sections = append(sections, model.footerView())
	main := lipgloss.JoinVertical(lipgloss.Left, sections...)
	main = lipgloss.Place(model.width, model.height, lipgloss.Left, lipgloss.Top, main)
	if !model.dialog.Open() {
		return main
	}

	dialog := model.dialog.View(model.width)
	x := max(0, (model.width-lipgloss.Width(dialog))/2)
	y := max(0, (model.height-lipgloss.Height(dialog))/2)
	baseLayer := lipgloss.NewLayer(main).Z(0)
	dialogLayer := lipgloss.NewLayer(dialog).X(x).Y(y).Z(1)
	return lipgloss.NewCompositor(baseLayer, dialogLayer).Render()
}

func (model Model) footerView() string {
	contextName := model.scope.Context
	if contextName == "" {
		contextName = "unavailable"
	}
	namespace := model.scope.Namespace
	if namespace == "" {
		namespace = "unavailable"
	}
	access := "scope unverified"
	if model.scope.ReadOnly {
		access = "read-only"
	}
	run := ""
	if model.run.Active {
		run = " · run active"
	}

	fixedWidth := lipgloss.Width("ctx/ · ns/ · " + access + run)
	nameBudget := max(4, model.width-fixedWidth)
	contextBudget := max(2, nameBudget/2)
	namespaceBudget := max(2, nameBudget-contextBudget)
	line := "ctx/" + middleElide(contextName, contextBudget) +
		" · ns/" + middleElide(namespace, namespaceBudget) + " · " + access + run
	if lipgloss.Width(line) > model.width {
		line = "ctx/" + middleElide(contextName, 3) + " · ns/" + middleElide(namespace, 3) + " · " + shortAccess(access)
	}
	return model.styles.footer.MaxWidth(model.width).Render(line)
}

func middleElide(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	left := (width - 1) / 2
	right := width - left - 1
	return prefixColumns(value, left) + "…" + suffixColumns(value, right)
}

func prefixColumns(value string, width int) string {
	var result strings.Builder
	used := 0
	for _, current := range value {
		currentWidth := lipgloss.Width(string(current))
		if used+currentWidth > width {
			break
		}
		result.WriteRune(current)
		used += currentWidth
	}
	return result.String()
}

func suffixColumns(value string, width int) string {
	runes := []rune(value)
	used := 0
	start := len(runes)
	for start > 0 {
		currentWidth := lipgloss.Width(string(runes[start-1]))
		if used+currentWidth > width {
			break
		}
		start--
		used += currentWidth
	}
	return string(runes[start:])
}

func shortAccess(value string) string {
	if strings.Contains(value, "read-only") {
		return "RO"
	}
	return "unverified"
}
