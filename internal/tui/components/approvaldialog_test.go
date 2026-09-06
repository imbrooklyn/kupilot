package components

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func TestApprovalDialogDefaultsToRejectAndRendersBoundSummary(t *testing.T) {
	dialog := NewApprovalDialog(ApprovalDialogStyles{})
	deadline := time.UnixMilli(1_700_000_060_000).UTC()
	dialog.Show(ApprovalDialogContent{
		Operation: "Restart Deployment", Scope: "test-context / test-namespace · scope revision 7",
		Resource:   "Deployment test-namespace/sample-deployment · API apps/v1",
		Current:    "Deployment generation 8 with Pod template fingerprint " + strings.Repeat("a", 64) + ".",
		Proposed:   "Update only the Kupilot-owned restart annotation to create a new Pod template revision.",
		Parameters: "none", Effects: "cluster mutation through Kubernetes API",
		Reason: "Restart after diagnosis.", Risk: "Pods may be replaced.", Timeout: 2 * time.Minute,
		Digest: strings.Repeat("b", 64), ExpiresAt: deadline,
	}, deadline.Add(-17*time.Second))
	if !dialog.Open() || dialog.ApproveSelected() {
		t.Fatalf("dialog open/selection = %v/%v, want open/reject", dialog.Open(), dialog.ApproveSelected())
	}
	view := dialog.View(100, 40)
	for _, want := range []string{
		"Action approval", "Operation: Restart Deployment", "Scope: test-context / test-namespace · scope revision 7",
		"Current: Deployment generation 8", "Proposed: Update only", "TTL: 17s", strings.Repeat("b", 64),
		"› Deny", "  Approve once",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "\x1b[") {
		t.Fatal("unstyled dialog unexpectedly rendered ANSI controls")
	}
}

func TestApprovalDialogSelectionSubmissionAndExpiryAreFailClosed(t *testing.T) {
	styles := ApprovalDialogStyles{Selected: lipgloss.NewStyle().Bold(true)}
	dialog := NewApprovalDialog(styles)
	now := time.UnixMilli(1_700_000_100_000).UTC()
	dialog.Show(ApprovalDialogContent{
		Operation: "Restart Deployment", Scope: "context / namespace · scope revision 7",
		Resource: "Deployment namespace/sample · API apps/v1", Current: "Current.", Proposed: "Proposed.",
		Parameters: "none", Effects: "cluster mutation", Reason: "Reason.", Risk: "Risk.",
		Timeout: time.Minute, Digest: strings.Repeat("c", 64), ExpiresAt: now.Add(time.Minute),
	}, now)
	dialog.Move(1)
	dialog.Move(1)
	if !dialog.ApproveSelected() {
		t.Fatal("Move() did not select Approve")
	}
	if !dialog.MarkSubmitted() || dialog.MarkSubmitted() {
		t.Fatal("submission was not exactly once")
	}
	if !dialog.Open() || !dialog.Submitted() || !strings.Contains(dialog.View(100, 40), "Submitting the one-time approval") {
		t.Fatal("submitted approval status is not visible")
	}
	if dialog.SetExecutionStatus(2, "Out of order.", false) ||
		!dialog.SetExecutionStatus(1, "Restart request accepted. Observing rollout.", false) ||
		!strings.Contains(dialog.View(100, 40), "Restart request accepted. Observing rollout.") {
		t.Fatal("ordered nonterminal execution status is not enforced or visible")
	}
	if dialog.SetExecutionStatus(1, "Duplicate.", false) ||
		!dialog.SetExecutionStatus(2, "Rollout verified.", true) || !dialog.Terminal() ||
		dialog.ExecutionIndex() != 2 ||
		!strings.Contains(dialog.View(100, 40), "Enter, Esc, or Ctrl+C closes this result.") {
		t.Fatal("terminal execution result is not visible")
	}
	if dialog.SetExecutionStatus(3, "Late progress.", false) {
		t.Fatal("terminal execution result accepted a later event")
	}
	dialog.Close()
	if dialog.Open() || dialog.ApproveSelected() || dialog.MarkSubmitted() || dialog.ExecutionIndex() != 0 {
		t.Fatal("Close() did not clear authority and restore default Reject")
	}
	dialog.Show(ApprovalDialogContent{
		Operation: "Restart Deployment", Scope: "context / namespace · scope revision 7",
		Resource: "Deployment namespace/sample · API apps/v1", Current: "Current.", Proposed: "Proposed.",
		Parameters: "none", Effects: "cluster mutation", Reason: "Reason.", Risk: "Risk.",
		Timeout: time.Minute, Digest: strings.Repeat("d", 64), ExpiresAt: now,
	}, now)
	if dialog.Open() {
		t.Fatal("expired dialog opened")
	}
}

func TestApprovalDialogTinyTerminalHidesApprovalAndKeepsOnlySafeChoices(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_000_100_000).UTC()
	dialog := NewApprovalDialog(ApprovalDialogStyles{})
	dialog.Show(ApprovalDialogContent{
		Operation: "Restart Deployment", Scope: "context / namespace · scope revision 7",
		Resource: "Deployment namespace/sample · API apps/v1", Current: "Current.", Proposed: "Proposed.",
		Parameters: "none", Effects: "cluster mutation", Reason: "Reason.", Risk: "Risk.",
		Timeout: time.Minute, Digest: strings.Repeat("e", 64), ExpiresAt: now.Add(time.Minute),
	}, now)
	dialog.Move(2)
	if dialog.Reviewable(20, 8) {
		t.Fatal("tiny terminal was incorrectly reviewable")
	}
	view := compactApprovalText(dialog.View(20, 8))
	for _, want := range []string{"Action approval", "Deny", "Cancel review"} {
		if !strings.Contains(view, want) {
			t.Fatalf("tiny approval fallback missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Approve once") || strings.Contains(view, "Create narrow") {
		t.Fatalf("tiny approval fallback exposed an approval choice:\n%s", view)
	}
}

func compactApprovalText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
