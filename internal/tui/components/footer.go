package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// FooterStyles distinguish primary scope state from lower-priority metadata.
type FooterStyles struct {
	Label     lipgloss.Style
	Value     lipgloss.Style
	Separator lipgloss.Style
	State     lipgloss.Style
	Warning   lipgloss.Style
}

// FooterStatus is the bounded scope and action-state input for the footer.
type FooterStatus struct {
	Context         string
	Namespace       string
	ReadOnly        bool
	ScopeSwitching  bool
	Permission      string
	Supervision     string
	Approval        string
	ContextPressure string
	Plan            string
}

// Footer renders scope-first status without owning application state.
type Footer struct {
	styles FooterStyles
}

// NewFooter creates a scope-first footer renderer.
func NewFooter(styles FooterStyles) Footer { return Footer{styles: styles} }

// SetStyles updates presentation without changing scope or action state.
func (footer *Footer) SetStyles(styles FooterStyles) { footer.styles = styles }

// View renders scope continuously and uses a second row only when width or an
// active approval state requires it. Detailed runtime state belongs to /status.
func (footer Footer) View(width int, status FooterStatus) string {
	width = max(1, width)
	contextName := status.Context
	if contextName == "" {
		contextName = "unavailable"
	}
	namespace := status.Namespace
	if namespace == "" {
		namespace = "unavailable"
	}
	permission := status.Permission
	if permission == "" {
		permission = "permission unavailable"
	}
	if status.Supervision != "" {
		permission += " · " + status.Supervision
	}
	access := "scope unverified · " + permission
	if status.ReadOnly {
		access = permission
	}
	if status.ScopeSwitching {
		access = "scope switching · " + permission
	}
	ambient := strings.Trim(strings.Join([]string{status.ContextPressure, status.Plan}, " · "), " ·")

	lineOne, accessOnSecond := requiredFooterLine(width, contextName, namespace, access)
	lineTwo := ""
	if accessOnSecond {
		lineTwo = access
	}
	if status.Approval != "" {
		if lineTwo == "" {
			lineTwo = middleElideColumns(status.Approval, width)
		} else if candidate := lineTwo + " · " + status.Approval; lipgloss.Width(candidate) <= width {
			lineTwo = candidate
		} else {
			// An active approval is more urgent than the repeated permission
			// detail. Context and Namespace remain continuously visible above.
			lineTwo = middleElideColumns(status.Approval, width)
		}
	}
	if status.Approval == "" && ambient != "" {
		if lineTwo == "" {
			lineTwo = middleElideColumns(ambient, width)
		} else if candidate := lineTwo + " · " + ambient; lipgloss.Width(candidate) <= width {
			lineTwo = candidate
		}
	}

	lines := []string{footer.renderScopeLine(lineOne, status)}
	if lineTwo != "" {
		lines = append(lines, footer.renderStateLine(clipFooterColumns(lineTwo, width), status))
	}
	return strings.Join(lines, "\n")
}

func (footer Footer) renderScopeLine(line string, status FooterStatus) string {
	if contextPart, remainder, ok := strings.Cut(line, " · Namespace "); ok {
		contextValue := strings.TrimPrefix(contextPart, "Context ")
		namespaceValue := remainder
		access := ""
		if value, state, found := strings.Cut(remainder, " · "); found {
			namespaceValue = value
			access = state
		}
		rendered := footer.styles.Label.Render("Context ") + footer.styles.Value.Render(contextValue) +
			footer.styles.Separator.Render(" · ") + footer.styles.Label.Render("Namespace ") + footer.styles.Value.Render(namespaceValue)
		if access != "" {
			stateStyle := footer.styles.Warning
			if status.ReadOnly && !status.ScopeSwitching {
				stateStyle = footer.styles.State
			}
			rendered += footer.styles.Separator.Render(" · ") + stateStyle.Render(access)
		}
		return rendered
	}
	if contextValue, namespaceValue, ok := strings.Cut(line, " / "); ok {
		return footer.styles.Value.Render(contextValue) + footer.styles.Separator.Render(" / ") + footer.styles.Value.Render(namespaceValue)
	}
	return footer.styles.Value.Render(line)
}

func (footer Footer) renderStateLine(line string, status FooterStatus) string {
	stateStyle := footer.styles.Warning
	if status.ReadOnly && !status.ScopeSwitching {
		stateStyle = footer.styles.State
	}
	if status.Approval == "" {
		return stateStyle.Render(line)
	}
	if state, approval, ok := strings.Cut(line, " · "); ok {
		return stateStyle.Render(state) + footer.styles.Separator.Render(" · ") + footer.styles.Warning.Render(approval)
	}
	return footer.styles.Warning.Render(line)
}

func requiredFooterLine(width int, contextName, namespace, access string) (string, bool) {
	full := "Context " + contextName + " · Namespace " + namespace + " · " + access
	if lipgloss.Width(full) <= width {
		return full, false
	}

	withoutAccessOverhead := lipgloss.Width("Context  · Namespace ")
	if width < withoutAccessOverhead+2 {
		separatorWidth := lipgloss.Width(" / ")
		nameBudget := max(2, width-separatorWidth)
		contextBudget := max(1, nameBudget/2)
		namespaceBudget := max(1, nameBudget-contextBudget)
		return clipFooterColumns(
			middleElideColumns(contextName, contextBudget)+" / "+middleElideColumns(namespace, namespaceBudget),
			width,
		), true
	}
	nameBudget := width - withoutAccessOverhead
	contextBudget := max(1, nameBudget/2)
	namespaceBudget := max(1, nameBudget-contextBudget)
	withoutAccess := "Context " + middleElideColumns(contextName, contextBudget) +
		" · Namespace " + middleElideColumns(namespace, namespaceBudget)

	withAccessOverhead := lipgloss.Width("Context  · Namespace  · " + access)
	if width >= withAccessOverhead+2 {
		nameBudget = width - withAccessOverhead
		contextBudget = max(1, nameBudget/2)
		namespaceBudget = max(1, nameBudget-contextBudget)
		return "Context " + middleElideColumns(contextName, contextBudget) +
			" · Namespace " + middleElideColumns(namespace, namespaceBudget) + " · " + access, false
	}
	return clipFooterColumns(withoutAccess, width), true
}

func middleElideColumns(value string, width int) string {
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
	return prefixFooterColumns(value, left) + "…" + suffixFooterColumns(value, right)
}

func clipFooterColumns(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	return prefixFooterColumns(value, width)
}

func prefixFooterColumns(value string, width int) string {
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

func suffixFooterColumns(value string, width int) string {
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
