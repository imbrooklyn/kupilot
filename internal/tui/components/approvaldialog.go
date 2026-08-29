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
	Operation string
	Scope     string
	Resource  string
	Current   string
	Proposed  string
	Reason    string
	Risk      string
	Digest    string
	ExpiresAt time.Time
}

// ApprovalDialog owns only local display selection. Reject is its zero and
// default choice; it owns no decision object or executor.
type ApprovalDialog struct {
	open            bool
	approveSelected bool
	submitted       bool
	terminal        bool
	executionIndex  int64
	remaining       time.Duration
	status          string
	content         ApprovalDialogContent
	styles          ApprovalDialogStyles
}

// NewApprovalDialog creates one closed default-reject dialog.
func NewApprovalDialog(styles ApprovalDialogStyles) ApprovalDialog {
	return ApprovalDialog{styles: styles}
}

// Show opens a bounded non-editable dialog only before its expiry.
func (dialog *ApprovalDialog) Show(content ApprovalDialogContent, now time.Time) {
	dialog.Close()
	if content.Operation == "" || content.Scope == "" || content.Resource == "" ||
		content.Current == "" || content.Proposed == "" || content.Reason == "" ||
		content.Risk == "" || content.Digest == "" || content.ExpiresAt.IsZero() ||
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
	dialog.approveSelected = false
	dialog.submitted = false
	dialog.terminal = false
	dialog.executionIndex = 0
	dialog.remaining = 0
	dialog.status = ""
	dialog.content = ApprovalDialogContent{}
}

// Move switches between Reject and Approve while the dialog is actionable.
func (dialog *ApprovalDialog) Move(_ int) {
	if dialog.open && !dialog.submitted {
		dialog.approveSelected = !dialog.approveSelected
	}
}

// MarkSubmitted accepts the first Enter only.
func (dialog *ApprovalDialog) MarkSubmitted() bool {
	if !dialog.open || dialog.submitted {
		return false
	}
	dialog.submitted = true
	if dialog.approveSelected {
		dialog.status = "Submitting the one-time approval..."
	} else {
		dialog.status = "Submitting rejection..."
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

func (dialog ApprovalDialog) Open() bool            { return dialog.open }
func (dialog ApprovalDialog) ApproveSelected() bool { return dialog.approveSelected }
func (dialog ApprovalDialog) Submitted() bool       { return dialog.submitted }
func (dialog ApprovalDialog) Terminal() bool        { return dialog.terminal }
func (dialog ApprovalDialog) ExecutionIndex() int64 { return dialog.executionIndex }

// View renders the exact digest and fixed current-to-proposed summary.
func (dialog ApprovalDialog) View(width int) string {
	if !dialog.open {
		return ""
	}
	rejectMarker, approveMarker := "› ", "  "
	rejectStyle, approveStyle := dialog.styles.Selected, dialog.styles.Body
	if dialog.approveSelected {
		rejectMarker, approveMarker = "  ", "› "
		rejectStyle, approveStyle = dialog.styles.Body, dialog.styles.Selected
	}
	seconds := int64((dialog.remaining + time.Second - 1) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	content := []string{
		dialog.styles.Title.Render("Restart approval"),
		dialog.styles.Body.Render("Operation: " + dialog.content.Operation),
		dialog.styles.Body.Render("Scope: " + dialog.content.Scope),
		dialog.styles.Body.Render("Resource: " + dialog.content.Resource),
		dialog.styles.Muted.Render("Current: " + dialog.content.Current),
		dialog.styles.Muted.Render("Proposed: " + dialog.content.Proposed),
		dialog.styles.Body.Render("Reason: " + dialog.content.Reason),
		dialog.styles.Danger.Render("Risk: " + dialog.content.Risk),
		dialog.styles.Muted.Render("Digest: " + dialog.content.Digest),
		dialog.styles.Muted.Render(fmt.Sprintf("TTL: %ds · Expires: %s", seconds, dialog.content.ExpiresAt.UTC().Format(time.RFC3339))),
	}
	if dialog.submitted {
		content = append(content, dialog.styles.Body.Render("Status: "+dialog.status))
		if dialog.terminal {
			content = append(content, dialog.styles.Muted.Render("Enter or Esc closes this result."))
		} else {
			content = append(content, dialog.styles.Muted.Render("Verification stops if its owning operation is cancelled. The restart request will not be retried."))
		}
	} else {
		content = append(content,
			rejectStyle.Render(rejectMarker+"Reject"),
			approveStyle.Render(approveMarker+"Approve"),
			dialog.styles.Muted.Render("Enter confirms the selected choice. Tab or arrows change it. Esc rejects."),
		)
	}
	return dialog.styles.Frame.Width(max(1, min(width-6, 96))).Render(strings.Join(content, "\n"))
}
