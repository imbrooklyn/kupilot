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
		Operation: "restart_deployment", Scope: "test-context / test-namespace / generation 7",
		Resource: "apps/v1 Deployment test-namespace/sample-deployment",
		Current:  "Deployment generation 8 with Pod template fingerprint " + strings.Repeat("a", 64) + ".",
		Proposed: "Update only the KuPilot-owned restart annotation to create a new Pod template revision.",
		Reason:   "Restart after diagnosis.", Risk: "Pods may be replaced.",
		Digest: strings.Repeat("b", 64), ExpiresAt: deadline,
	}, deadline.Add(-17*time.Second))
	if !dialog.Open() || dialog.ApproveSelected() {
		t.Fatalf("dialog open/selection = %v/%v, want open/reject", dialog.Open(), dialog.ApproveSelected())
	}
	view := dialog.View(100)
	for _, want := range []string{
		"Restart approval", "Operation: restart_deployment", "Scope: test-context / test-namespace / generation 7",
		"Current: Deployment generation 8", "Proposed: Update only", "TTL: 17s", strings.Repeat("b", 64),
		"› Reject", "  Approve",
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
		Operation: "restart_deployment", Scope: "context / namespace / generation 7",
		Resource: "apps/v1 Deployment namespace/sample", Current: "Current.", Proposed: "Proposed.",
		Reason: "Reason.", Risk: "Risk.", Digest: strings.Repeat("c", 64), ExpiresAt: now.Add(time.Minute),
	}, now)
	dialog.Move(1)
	if !dialog.ApproveSelected() {
		t.Fatal("Move() did not select Approve")
	}
	if !dialog.MarkSubmitted() || dialog.MarkSubmitted() {
		t.Fatal("submission was not exactly once")
	}
	dialog.Close()
	if dialog.Open() || dialog.ApproveSelected() || dialog.MarkSubmitted() {
		t.Fatal("Close() did not clear authority and restore default Reject")
	}
	dialog.Show(ApprovalDialogContent{
		Operation: "restart_deployment", Scope: "context / namespace / generation 7",
		Resource: "apps/v1 Deployment namespace/sample", Current: "Current.", Proposed: "Proposed.",
		Reason: "Reason.", Risk: "Risk.", Digest: strings.Repeat("d", 64), ExpiresAt: now,
	}, now)
	if dialog.Open() {
		t.Fatal("expired dialog opened")
	}
}
