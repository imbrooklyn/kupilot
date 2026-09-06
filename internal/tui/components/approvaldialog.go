package components

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ApprovalDialogStyles keep approval meaning explicit without relying on color.
type ApprovalDialogStyles struct {
	Frame    lipgloss.Style
	Title    lipgloss.Style
	Body     lipgloss.Style
	Selected lipgloss.Style
	Muted    lipgloss.Style
	Danger   lipgloss.Style
}

// ApprovalDialogContent is already projected, normalized, and digest-bound.
type ApprovalDialogContent struct {
	Operation      string
	Scope          string
	Resource       string
	Current        string
	Proposed       string
	Parameters     string
	Effects        string
	Reason         string
	Risk           string
	Reviewer       string
	ReviewerReason string
	Digest         string
	Timeout        time.Duration
	ExpiresAt      time.Time
	SessionRule    bool
}

// ApprovalChoice is local display state only. Deny is the safe default.
type ApprovalChoice uint8

const (
	ApprovalDeny ApprovalChoice = iota
	ApprovalCancel
	ApprovalOnce
	ApprovalSessionRule
)

// ApprovalDialog owns only local display selection. Deny is its zero and
// default choice; it owns no decision object, Session rule, or executor.
type ApprovalDialog struct {
	open           bool
	choice         ApprovalChoice
	submitted      bool
	terminal       bool
	executionIndex int64
	detailOffset   int
	remaining      time.Duration
	status         string
	content        ApprovalDialogContent
	styles         ApprovalDialogStyles
}

// NewApprovalDialog creates one closed default-reject dialog.
func NewApprovalDialog(styles ApprovalDialogStyles) ApprovalDialog {
	return ApprovalDialog{styles: styles}
}

// SetStyles updates presentation without changing approval selection or state.
func (dialog *ApprovalDialog) SetStyles(styles ApprovalDialogStyles) { dialog.styles = styles }

// Show opens a bounded non-editable dialog only before its expiry.
func (dialog *ApprovalDialog) Show(content ApprovalDialogContent, now time.Time) {
	dialog.Close()
	if content.Operation == "" || content.Scope == "" || content.Resource == "" ||
		content.Current == "" || content.Proposed == "" || content.Reason == "" ||
		content.Parameters == "" || content.Effects == "" || content.Risk == "" ||
		content.Digest == "" || content.Timeout <= 0 || content.ExpiresAt.IsZero() ||
		!now.Before(content.ExpiresAt) {
		return
	}
	dialog.open = true
	dialog.content = content
	dialog.remaining = content.ExpiresAt.Sub(now)
}

// Close clears all displayed proposal state and restores default Reject.
func (dialog *ApprovalDialog) Close() {
	dialog.open = false
	dialog.choice = ApprovalDeny
	dialog.submitted = false
	dialog.terminal = false
	dialog.executionIndex = 0
	dialog.detailOffset = 0
	dialog.remaining = 0
	dialog.status = ""
	dialog.content = ApprovalDialogContent{}
}

// Move cycles through only the choices admitted by this exact action.
func (dialog *ApprovalDialog) Move(delta int) {
	if !dialog.open || dialog.submitted {
		return
	}
	maximum := int(ApprovalOnce)
	if dialog.content.SessionRule {
		maximum = int(ApprovalSessionRule)
	}
	next := (int(dialog.choice) + delta) % (maximum + 1)
	if next < 0 {
		next += maximum + 1
	}
	dialog.choice = ApprovalChoice(next)
}

// Scroll moves only the read-only envelope details. It never changes the
// selected decision or creates authority.
func (dialog *ApprovalDialog) Scroll(delta int) {
	if !dialog.open || delta == 0 {
		return
	}
	dialog.detailOffset = max(0, dialog.detailOffset+delta)
}

// SelectDeny restores the safe choice after a resize makes the complete
// review surface unavailable.
func (dialog *ApprovalDialog) SelectDeny() { dialog.choice = ApprovalDeny }

// MarkSubmitted accepts the first Enter only.
func (dialog *ApprovalDialog) MarkSubmitted() bool {
	if !dialog.open || dialog.submitted {
		return false
	}
	dialog.submitted = true
	switch dialog.choice {
	case ApprovalOnce:
		dialog.status = "Submitting the one-time approval..."
	case ApprovalSessionRule:
		dialog.status = "Creating the narrow Session rule..."
	case ApprovalCancel:
		dialog.status = "Cancelling this approval..."
	default:
		dialog.status = "Submitting denial..."
	}
	return true
}

// SetStatus replaces the code-defined execution status. A terminal status is
// display-only and carries no remaining approval authority.
func (dialog *ApprovalDialog) SetStatus(status string, terminal bool) bool {
	if !dialog.open || !dialog.submitted || status == "" || len(status) > 256 || strings.ContainsAny(status, "\r\n") {
		return false
	}
	dialog.status = status
	dialog.terminal = terminal
	return true
}

// SetExecutionStatus accepts only the next request-bound execution projection.
// Ordering remains local display state and carries no approval authority.
func (dialog *ApprovalDialog) SetExecutionStatus(eventIndex int64, status string, terminal bool) bool {
	if dialog.terminal || eventIndex != dialog.executionIndex+1 || !dialog.SetStatus(status, terminal) {
		return false
	}
	dialog.executionIndex = eventIndex
	return true
}

func (dialog ApprovalDialog) Open() bool             { return dialog.open }
func (dialog ApprovalDialog) ApproveSelected() bool  { return dialog.choice == ApprovalOnce }
func (dialog ApprovalDialog) Choice() ApprovalChoice { return dialog.choice }
func (dialog ApprovalDialog) Submitted() bool        { return dialog.submitted }
func (dialog ApprovalDialog) Terminal() bool         { return dialog.terminal }
func (dialog ApprovalDialog) ExecutionIndex() int64  { return dialog.executionIndex }

// Reviewable reports whether the current terminal can keep the decision rows
// visible while exposing the complete envelope through bounded local paging.
func (dialog ApprovalDialog) Reviewable(width, height int) bool {
	_, reviewable := dialog.viewRows(width, height)
	return reviewable
}

// View renders the exact digest and fixed current-to-proposed summary. Details
// page inside this one read-only surface; decision rows never scroll away.
func (dialog ApprovalDialog) View(width, height int) string {
	if !dialog.open {
		return ""
	}
	rows, reviewable := dialog.viewRows(width, height)
	contentWidth := max(1, min(width-6, 96))
	if !reviewable {
		rows = []string{
			dialog.styles.Title.Render("Action approval"),
			dialog.styles.Body.Render("› Deny"),
			dialog.styles.Body.Render("  Cancel review"),
			dialog.styles.Danger.Render("Approval unavailable: the terminal is too small to review every field."),
			dialog.styles.Muted.Render("Resize to inspect every field. Enter denies; Esc or Ctrl+C cancels. Approval is unavailable while details are hidden."),
		}
		rows = wrapApprovalRows(rows, contentWidth)
	}
	return dialog.styles.Frame.Width(contentWidth).MaxHeight(max(1, height)).Render(strings.Join(rows, "\n"))
}

func (dialog ApprovalDialog) viewRows(width, height int) ([]string, bool) {
	if !dialog.open {
		return nil, false
	}
	contentWidth := max(1, min(width-6, 96))
	seconds := int64((dialog.remaining + time.Second - 1) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	details := wrapApprovalRows([]string{
		dialog.styles.Body.Render("Operation: " + dialog.content.Operation),
		dialog.styles.Body.Render("Scope: " + dialog.content.Scope),
		dialog.styles.Body.Render("Resource: " + dialog.content.Resource),
		dialog.styles.Muted.Render("Current: " + dialog.content.Current),
		dialog.styles.Muted.Render("Proposed: " + dialog.content.Proposed),
		dialog.styles.Body.Render("Parameters: " + dialog.content.Parameters),
		dialog.styles.Body.Render("Effects: " + dialog.content.Effects),
		dialog.styles.Body.Render("Reason: " + dialog.content.Reason),
		dialog.styles.Danger.Render("Risk: " + dialog.content.Risk),
		dialog.styles.Muted.Render("Timeout: " + dialog.content.Timeout.String()),
		dialog.styles.Muted.Render("Digest: " + dialog.content.Digest),
		dialog.styles.Muted.Render(fmt.Sprintf("TTL: %ds · Expires: %s", seconds, dialog.content.ExpiresAt.UTC().Format(time.RFC3339))),
	}, contentWidth)
	if dialog.content.Reviewer != "" {
		details = append(details, wrapApprovalRows([]string{dialog.styles.Body.Render("Reviewer: " + dialog.content.Reviewer)}, contentWidth)...)
		if dialog.content.ReviewerReason != "" {
			details = append(details, wrapApprovalRows([]string{dialog.styles.Muted.Render("Reviewer rationale: " + dialog.content.ReviewerReason)}, contentWidth)...)
		}
	}
	footer := make([]string, 0, 8)
	if dialog.submitted {
		footer = append(footer, dialog.styles.Body.Render("Status: "+dialog.status))
		if dialog.terminal {
			footer = append(footer, dialog.styles.Muted.Render("Enter, Esc, or Ctrl+C closes this result."))
		} else {
			footer = append(footer, dialog.styles.Muted.Render("Verification stops if its owning operation is cancelled. The action will not be retried."))
		}
	} else {
		choices := []struct {
			choice ApprovalChoice
			label  string
		}{
			{ApprovalDeny, "Deny"},
			{ApprovalCancel, "Cancel review"},
			{ApprovalOnce, "Approve once"},
		}
		if dialog.content.SessionRule {
			choices = append(choices, struct {
				choice ApprovalChoice
				label  string
			}{ApprovalSessionRule, "Create narrow Session rule (current action will not execute)"})
		}
		for _, item := range choices {
			marker := "  "
			style := dialog.styles.Body
			if dialog.choice == item.choice {
				marker = "› "
				style = dialog.styles.Selected
			}
			footer = append(footer, style.Render(marker+item.label))
		}
		footer = append(footer, dialog.styles.Muted.Render("Deny is selected by default. Enter confirms. Tab or arrows move. Page Up/Down reviews details. Esc or Ctrl+C cancels without approval."))
	}
	footer = wrapApprovalRows(footer, contentWidth)
	// The configured rounded frame contributes two border rows and two padding
	// rows. Keep a conservative extra row so a terminal resize cannot obscure a
	// decision label.
	innerHeight := max(0, height-5)
	fixedHeight := 1 + len(footer)
	if contentWidth < 16 || innerHeight-fixedHeight < 3 {
		return nil, false
	}
	detailHeight := innerHeight - fixedHeight
	truncated := len(details) > detailHeight
	if truncated {
		detailHeight--
		if detailHeight < 2 {
			return nil, false
		}
	}
	maximumOffset := max(0, len(details)-detailHeight)
	offset := min(dialog.detailOffset, maximumOffset)
	end := min(len(details), offset+detailHeight)
	rows := make([]string, 0, innerHeight)
	rows = append(rows, dialog.styles.Title.Render("Action approval"))
	rows = append(rows, details[offset:end]...)
	if truncated {
		rows = append(rows, dialog.styles.Muted.Render(fmt.Sprintf("Details %d-%d of %d · Page Up/Down", offset+1, end, len(details))))
	}
	rows = append(rows, footer...)
	return rows, true
}

func wrapApprovalRows(rows []string, width int) []string {
	if len(rows) == 0 {
		return nil
	}
	wrapped := lipgloss.NewStyle().Width(max(1, width)).Render(strings.Join(rows, "\n"))
	return strings.Split(wrapped, "\n")
}
