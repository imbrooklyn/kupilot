package components

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// ToolStep is the bounded visible subset of one ToolInvocation.
type ToolStep struct {
	InvocationID  string
	Name          string
	Purpose       string
	Status        string
	Summary       string
	EvidenceCount int
	Truncated     bool
}

// ToolStepStyles maps semantic status to local presentation only.
type ToolStepStyles struct {
	Normal  lipgloss.Style
	Muted   lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Danger  lipgloss.Style
}

// ToolSteps preserves stable invocation order and owns no Tool execution state.
type ToolSteps struct {
	steps  []ToolStep
	styles ToolStepStyles
}

// NewToolSteps creates an empty inline list.
func NewToolSteps(styles ToolStepStyles) ToolSteps { return ToolSteps{styles: styles} }

// SetStyles updates presentation without changing invocation state.
func (steps *ToolSteps) SetStyles(styles ToolStepStyles) { steps.styles = styles }

// Reset removes steps when a new Agent turn starts.
func (steps *ToolSteps) Reset() { steps.steps = nil }

// Upsert inserts or updates one invocation without changing its stable position.
func (steps *ToolSteps) Upsert(step ToolStep) {
	for index := range steps.steps {
		if steps.steps[index].InvocationID == step.InvocationID {
			if terminalToolStepStatus(steps.steps[index].Status) {
				return
			}
			steps.steps[index] = step
			return
		}
	}
	steps.steps = append(steps.steps, step)
}

func terminalToolStepStatus(status string) bool {
	switch status {
	case "succeeded", "partial", "denied", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// Items returns a defensive copy.
func (steps ToolSteps) Items() []ToolStep { return append([]ToolStep(nil), steps.steps...) }

// View renders one compact Tool status row and, when useful, one detail row.
func (steps ToolSteps) View() string {
	lines := make([]string, 0, len(steps.steps)*2)
	for _, step := range steps.steps {
		label, style := steps.status(step.Status)
		header := steps.styles.Normal.Render("• "+toolStepDisplayName(step.Name)+" · ") + style.Render(label)
		if step.Truncated {
			header += steps.styles.Warning.Render(" · limited")
		}
		lines = append(lines, header)
		if detail := toolStepDetail(step); detail != "" {
			lines = append(lines, steps.styles.Muted.Render("    └ "+indentToolStepDetail(detail)))
		}
	}
	return strings.Join(lines, "\n")
}

func toolStepDisplayName(name string) string {
	switch name {
	case "get_resource":
		return "Inspect resource"
	case "list_resources":
		return "List resources"
	case "get_events":
		return "Read events"
	case "get_pod_logs":
		return "Read current logs"
	case "get_previous_pod_logs":
		return "Read previous logs"
	case "get_related_resources":
		return "Inspect related resources"
	case "get_cluster_overview":
		return "Inspect cluster overview"
	default:
		return "Cluster activity"
	}
}

func toolStepDetail(step ToolStep) string {
	switch {
	case step.Purpose != "" && step.Summary != "" && step.Purpose != step.Summary:
		return step.Purpose + " → " + step.Summary
	case step.Summary != "":
		return step.Summary
	default:
		return step.Purpose
	}
}

func indentToolStepDetail(value string) string {
	return strings.ReplaceAll(value, "\n", "\n      ")
}

func (steps ToolSteps) status(status string) (string, lipgloss.Style) {
	switch status {
	case "succeeded":
		return "done", steps.styles.Success
	case "partial":
		return "partial", steps.styles.Warning
	case "denied":
		return "blocked", steps.styles.Danger
	case "failed":
		return "failed", steps.styles.Danger
	case "cancelled":
		return "cancelled", steps.styles.Warning
	case "running":
		return "reading", steps.styles.Muted
	default:
		return "queued", steps.styles.Muted
	}
}
