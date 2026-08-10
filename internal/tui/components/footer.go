package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// FooterStyles distinguish primary scope state from lower-priority metadata.
type FooterStyles struct {
	Primary   lipgloss.Style
	Secondary lipgloss.Style
	Warning   lipgloss.Style
}

// FooterStatus is the complete bounded display input for the two-row footer.
type FooterStatus struct {
	Context        string
	Namespace      string
	ReadOnly       bool
	ScopeSwitching bool
	Resource       string
	Run            string
	Model          string
	Privacy        string
}

// Footer renders scope-first status without owning application state.
type Footer struct {
	styles FooterStyles
}

// NewFooter creates a scope-first footer renderer.
func NewFooter(styles FooterStyles) Footer { return Footer{styles: styles} }

// View renders at most two rows and drops optional fields from lowest priority.
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
	access := "scope unverified"
	if status.ReadOnly {
		access = "read-only"
	}
	if status.ScopeSwitching {
		access = "scope switching"
	}

	lineOne, accessOnSecond := requiredFooterLine(width, contextName, namespace, access)
	lineTwo := ""
	if accessOnSecond {
		lineTwo = access
	}
	for _, value := range []string{status.Resource, status.Run, status.Model, status.Privacy} {
		if value == "" {
			continue
		}
		lineTwo = appendFooterSegment(lineTwo, value, width)
	}

	lines := []string{footer.styles.Primary.Render(lineOne)}
	if lineTwo != "" {
		style := footer.styles.Secondary
		if status.ScopeSwitching {
			style = footer.styles.Warning
		}
		lines = append(lines, style.Render(clipFooterColumns(lineTwo, width)))
	}
	return strings.Join(lines, "\n")
}

func requiredFooterLine(width int, contextName, namespace, access string) (string, bool) {
	full := "ctx/" + contextName + " · ns/" + namespace + " · " + access
	if lipgloss.Width(full) <= width {
		return full, false
	}
	access = compactFooterAccess(access)

	withoutAccessOverhead := lipgloss.Width("ctx/ · ns/")
	if width < withoutAccessOverhead+2 {
		return clipFooterColumns("ctx/… ns/…", width), true
	}
	nameBudget := width - withoutAccessOverhead
	contextBudget := max(1, nameBudget/2)
	namespaceBudget := max(1, nameBudget-contextBudget)
	withoutAccess := "ctx/" + middleElideColumns(contextName, contextBudget) +
		" · ns/" + middleElideColumns(namespace, namespaceBudget)

	withAccessOverhead := lipgloss.Width("ctx/ · ns/ · " + access)
	if width >= withAccessOverhead+2 {
		nameBudget = width - withAccessOverhead
		contextBudget = max(1, nameBudget/2)
		namespaceBudget = max(1, nameBudget-contextBudget)
		return "ctx/" + middleElideColumns(contextName, contextBudget) +
			" · ns/" + middleElideColumns(namespace, namespaceBudget) + " · " + access, false
	}
	return clipFooterColumns(withoutAccess, width), true
}

func compactFooterAccess(value string) string {
	switch value {
	case "read-only":
		return "RO"
	case "scope switching":
		return "switching"
	default:
		return "unverified"
	}
}

func appendFooterSegment(line, value string, width int) string {
	if line == "" {
		return middleElideColumns(value, width)
	}
	candidate := line + " · " + value
	if lipgloss.Width(candidate) <= width {
		return candidate
	}
	remaining := width - lipgloss.Width(line+" · ")
	if remaining < 4 {
		return line
	}
	return line + " · " + middleElideColumns(value, remaining)
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
