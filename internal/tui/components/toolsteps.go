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

// View renders Tool steps inline beneath Agent prose.
func (steps ToolSteps) View() string {
	lines := make([]string, 0, len(steps.steps))
	for index, step := range steps.steps {
		branch := "├─"
		if index == len(steps.steps)-1 {
			branch = "└─"
		}
		symbol, label, style := steps.status(step.Status)
		line := fmt.Sprintf("%s %s %s · %s", branch, symbol, step.Name, label)
		if step.Purpose != "" {
			line += " · " + step.Purpose
		}
		if step.Summary != "" {
			line += " · " + step.Summary
		}
		if step.EvidenceCount > 0 {
			line += fmt.Sprintf(" · %d evidence", step.EvidenceCount)
		}
		if step.Truncated {
			line += " · truncated"
		}
		lines = append(lines, steps.styles.Muted.Render(branch+" ")+style.Render(strings.TrimPrefix(line, branch+" ")))
	}
	return strings.Join(lines, "\n")
}

func (steps ToolSteps) status(status string) (string, string, lipgloss.Style) {
	switch status {
	case "succeeded":
		return "✓", "succeeded", steps.styles.Success
	case "partial":
		return "!", "partial", steps.styles.Warning
	case "denied":
		return "×", "denied", steps.styles.Danger
	case "failed":
		return "×", "failed", steps.styles.Danger
	case "cancelled":
		return "×", "cancelled", steps.styles.Warning
	case "running":
		return "◌", "running", steps.styles.Muted
	default:
		return "○", "requested", steps.styles.Muted
	}
}
