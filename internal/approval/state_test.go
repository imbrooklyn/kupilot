package approval

import (
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalStateTransitionTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current domain.ApprovalState
		action  transitionAction
		want    domain.ApprovalState
		allowed bool
	}{
		{name: "approve pending", current: domain.ApprovalStatePending, action: actionApprove, want: domain.ApprovalStateApproved, allowed: true},
		{name: "reject pending", current: domain.ApprovalStatePending, action: actionReject, want: domain.ApprovalStateRejected, allowed: true},
		{name: "expire pending", current: domain.ApprovalStatePending, action: actionExpire, want: domain.ApprovalStateExpired, allowed: true},
		{name: "cancel pending", current: domain.ApprovalStatePending, action: actionCancel, want: domain.ApprovalStateCancelled, allowed: true},
		{name: "invalidate pending", current: domain.ApprovalStatePending, action: actionInvalidate, want: domain.ApprovalStateInvalidated, allowed: true},
		{name: "consume pending", current: domain.ApprovalStatePending, action: actionConsume, want: domain.ApprovalStatePending},
		{name: "consume approved", current: domain.ApprovalStateApproved, action: actionConsume, want: domain.ApprovalStateConsumed, allowed: true},
		{name: "expire approved", current: domain.ApprovalStateApproved, action: actionExpire, want: domain.ApprovalStateExpired, allowed: true},
		{name: "cancel approved", current: domain.ApprovalStateApproved, action: actionCancel, want: domain.ApprovalStateCancelled, allowed: true},
		{name: "invalidate approved", current: domain.ApprovalStateApproved, action: actionInvalidate, want: domain.ApprovalStateInvalidated, allowed: true},
		{name: "approve approved", current: domain.ApprovalStateApproved, action: actionApprove, want: domain.ApprovalStateApproved},
		{name: "reject approved", current: domain.ApprovalStateApproved, action: actionReject, want: domain.ApprovalStateApproved},
		{name: "unknown action", current: domain.ApprovalStatePending, action: "unknown", want: domain.ApprovalStatePending},
		{name: "unknown state", current: "unknown", action: actionApprove, want: "unknown"},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			got, ok := nextState(current.current, current.action)
			if ok != current.allowed || got != current.want {
				t.Fatalf("nextState(%q, %q) = (%q, %t), want (%q, %t)", current.current, current.action, got, ok, current.want, current.allowed)
			}
		})
	}
}

func TestApprovalTerminatedStatesAreIrreversible(t *testing.T) {
	t.Parallel()
	terminatedStates := []domain.ApprovalState{
		domain.ApprovalStateRejected,
		domain.ApprovalStateExpired,
		domain.ApprovalStateCancelled,
		domain.ApprovalStateInvalidated,
		domain.ApprovalStateConsumed,
	}
	actions := []transitionAction{actionApprove, actionReject, actionExpire, actionCancel, actionInvalidate, actionConsume}
	for _, state := range terminatedStates {
		for _, action := range actions {
			if got, ok := nextState(state, action); ok || got != state {
				t.Fatalf("nextState(%q, %q) = (%q, %t), want unchanged and denied", state, action, got, ok)
			}
		}
		if !state.Terminated() {
			t.Fatalf("ApprovalState(%q).Terminated() = false", state)
		}
	}
	if domain.ApprovalStatePending.Terminated() || domain.ApprovalStateApproved.Terminated() {
		t.Fatal("a non-terminated ApprovalState reported terminated")
	}
}

func TestApprovalStateAuditEventsAreClosedAndStable(t *testing.T) {
	t.Parallel()
	want := map[domain.ApprovalState]domain.ApprovalAuditEventType{
		domain.ApprovalStatePending:     domain.ApprovalAuditRequested,
		domain.ApprovalStateApproved:    domain.ApprovalAuditApproved,
		domain.ApprovalStateRejected:    domain.ApprovalAuditRejected,
		domain.ApprovalStateExpired:     domain.ApprovalAuditExpired,
		domain.ApprovalStateCancelled:   domain.ApprovalAuditCancelled,
		domain.ApprovalStateInvalidated: domain.ApprovalAuditInvalidated,
		domain.ApprovalStateConsumed:    domain.ApprovalAuditConsumed,
	}
	for state, eventType := range want {
		if got := state.AuditEventType(); got != eventType || !got.Valid() {
			t.Fatalf("ApprovalState(%q).AuditEventType() = %q, want valid %q", state, got, eventType)
		}
	}
	if domain.ApprovalState("executing").AuditEventType() != "" || domain.ApprovalAuditEventType("free_form").Valid() {
		t.Fatal("an open-ended Approval state or audit event was admitted")
	}
}
