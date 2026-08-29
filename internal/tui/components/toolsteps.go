package components

import (
	"fmt"
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

// View renders compact Tool headers with optional details on separate lines.
func (steps ToolSteps) View() string {
	lines := make([]string, 0, len(steps.steps)*3)
	for index, step := range steps.steps {
		branch := "├─"
		if index == len(steps.steps)-1 {
			branch = "└─"
		}
		symbol, label, style := steps.status(step.Status)
		header := fmt.Sprintf("%s %s %s · %s", branch, symbol, toolStepDisplayName(step.Name), label)
		if step.Truncated {
			header += " · limited"
		}
		lines = append(lines, steps.styles.Muted.Render(branch+" ")+style.Render(strings.TrimPrefix(header, branch+" ")))
		if step.Purpose != "" {
			lines = append(lines, steps.styles.Muted.Render("   Purpose: ")+style.Render(indentToolStepDetail(step.Purpose)))
		}
		if step.Summary != "" && step.Summary != step.Purpose {
			lines = append(lines, steps.styles.Muted.Render("   Result: ")+style.Render(indentToolStepDetail(step.Summary)))
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
	default:
		return "Cluster activity"
	}
}

func indentToolStepDetail(value string) string {
	return strings.ReplaceAll(value, "\n", "\n   ")
}

func (steps ToolSteps) status(status string) (string, string, lipgloss.Style) {
	switch status {
	case "succeeded":
		return "✓", "done", steps.styles.Success
	case "partial":
		return "!", "partial", steps.styles.Warning
	case "denied":
		return "×", "blocked", steps.styles.Danger
	case "failed":
		return "×", "failed", steps.styles.Danger
	case "cancelled":
		return "×", "cancelled", steps.styles.Warning
	case "running":
		return "◌", "reading", steps.styles.Muted
	default:
		return "○", "queued", steps.styles.Muted
	}
}
