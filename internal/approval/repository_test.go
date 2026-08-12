package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestRecoverAfterRestartUsesHalfOpenTTLForPendingAndApproved(t *testing.T) {
	base := time.UnixMilli(1_700_000_900_000).UTC()
	service, clock, executor := newTestService(t, base, testNonce(t, 0x67))
	pending, err := service.Request(context.Background(), testRequestCommand())
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	pendingStored, err := NewStoredRequest(pending)
	if err != nil {
		t.Fatalf("NewStoredRequest(pending) error = %v", err)
	}

	clock.Set(base.Add(time.Second))
	approved, _, err := service.Decide(context.Background(), approveCommand(pending))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	approvedStored, err := NewStoredRequest(approved)
	if err != nil {
		t.Fatalf("NewStoredRequest(approved) error = %v", err)
	}

	tests := []struct {
		name   string
		input  StoredRequest
		now    time.Time
		state  domain.ApprovalState
		reason domain.ApprovalStateReason
	}{
		{name: "pending before expiry", input: pendingStored, now: pending.ExpiresAt.Add(-time.Millisecond), state: domain.ApprovalStateCancelled, reason: domain.ApprovalReasonProcessRestarted},
		{name: "pending at expiry", input: pendingStored, now: pending.ExpiresAt, state: domain.ApprovalStateExpired, reason: domain.ApprovalReasonTTLExpired},
		{name: "approved before expiry", input: approvedStored, now: pending.ExpiresAt.Add(-time.Millisecond), state: domain.ApprovalStateCancelled, reason: domain.ApprovalReasonProcessRestarted},
		{name: "approved after expiry", input: approvedStored, now: pending.ExpiresAt.Add(time.Millisecond), state: domain.ApprovalStateExpired, reason: domain.ApprovalReasonTTLExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recovered, err := RecoverAfterRestart(test.input, test.now)
			if err != nil || recovered.State != test.state || recovered.StateReason != test.reason ||
				!recovered.StateChangedAt.Equal(test.now) {
				t.Fatalf("RecoverAfterRestart() = %#v/%v", recovered, err)
			}
		})
	}
	if _, err := RecoverAfterRestart(approvedStored, approved.StateChangedAt.Add(-time.Millisecond)); !errors.Is(err, ErrInvalidStoredApproval) {
		t.Fatalf("RecoverAfterRestart(clock rollback) error = %v", err)
	}
	if executor.WriteCount() != 0 {
		t.Fatalf("fake executor writes = %d, want 0", executor.WriteCount())
	}
}
