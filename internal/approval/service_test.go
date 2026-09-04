package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalLifecycleClaimsAndConsumesWithoutExecutor(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	service, clock, store := newTestService(t, now, testNonce(t, 1))
	request, err := service.Request(context.Background(), testRequestCommand())
	if err != nil || request.State != domain.ApprovalStatePending || request.ActionEnvelope().Validate() != nil {
		t.Fatalf("Request() = %#v/%v", request, err)
	}
	clock.Set(now.Add(time.Millisecond))
	approved, decision, err := service.Decide(context.Background(), approveCommand(request))
	if err != nil || approved.State != domain.ApprovalStateApproved || decision.Validate() != nil {
		t.Fatalf("Decide() = %#v/%#v/%v", approved, decision, err)
	}
	claim, err := service.Claim(context.Background(), consumeCommand(approved))
	if err != nil || claim.Validate() != nil || store.WriteCount() != 0 {
		t.Fatalf("Claim() = %#v/%v, executor calls = %d", claim, err, store.WriteCount())
	}
	clock.Set(now.Add(2 * time.Millisecond))
	consumed, err := service.CommitConsume(context.Background(), claim)
	if err != nil || consumed.State != domain.ApprovalStateConsumed || store.auditCommits != 1 || store.WriteCount() != 0 {
		t.Fatalf("CommitConsume() = %#v/%v commits/writes=%d/%d", consumed, err, store.auditCommits, store.WriteCount())
	}
	if _, err := service.CommitConsume(context.Background(), claim); err == nil {
		t.Fatal("replayed claim was accepted")
	}
}

func TestApprovalTTLIsHalfOpenAcrossDecisionClaimAndCommit(t *testing.T) {
	base := time.UnixMilli(1_700_000_000_000).UTC()
	t.Run("decision at boundary", func(t *testing.T) {
		service, clock, _ := newTestService(t, base, testNonce(t, 2))
		request, _ := service.Request(context.Background(), testRequestCommand())
		clock.Set(request.ExpiresAt)
		updated, _, err := service.Decide(context.Background(), approveCommand(request))
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeExpired)
		if updated.State != domain.ApprovalStateExpired {
			t.Fatalf("state = %q", updated.State)
		}
	})
	t.Run("claim at boundary", func(t *testing.T) {
		service, clock, _ := newTestService(t, base, testNonce(t, 3))
		request, _ := service.Request(context.Background(), testRequestCommand())
		clock.Set(base.Add(time.Millisecond))
		approved, _, _ := service.Decide(context.Background(), approveCommand(request))
		clock.Set(request.ExpiresAt)
		claim, err := service.Claim(context.Background(), consumeCommand(approved))
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeExpired)
		if claim.Request.State != domain.ApprovalStateExpired {
			t.Fatalf("claim state = %q", claim.Request.State)
		}
	})
	t.Run("commit at boundary", func(t *testing.T) {
		service, clock, store := newTestService(t, base, testNonce(t, 4))
		request, _ := service.Request(context.Background(), testRequestCommand())
		clock.Set(base.Add(time.Millisecond))
		approved, _, _ := service.Decide(context.Background(), approveCommand(request))
		claim, _ := service.Claim(context.Background(), consumeCommand(approved))
		clock.Set(request.ExpiresAt)
		updated, err := service.CommitConsume(context.Background(), claim)
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeExpired)
		if updated.State != domain.ApprovalStateExpired || store.auditCommits != 0 {
			t.Fatalf("commit state/audits = %q/%d", updated.State, store.auditCommits)
		}
	})
}

func TestApprovalProofFailuresInvalidateAndPerformNoPreWrite(t *testing.T) {
	base := time.UnixMilli(1_700_000_000_000).UTC()
	tests := []struct {
		name   string
		mutate func(*ConsumeCommand)
		code   domain.ApprovalErrorCode
	}{
		{"digest", func(command *ConsumeCommand) {
			command.ShownDigest = domain.ActionDigest(domain.SHA256Hex("different"))
		}, domain.ApprovalErrorCodeDigestMismatch},
		{"nonce", func(command *ConsumeCommand) { command.Nonce = testNonce(t, 90) }, domain.ApprovalErrorCodeNonceMismatch},
		{"scope", func(command *ConsumeCommand) { command.CurrentScope.Generation++ }, domain.ApprovalErrorCodeStaleScope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, clock, store := newTestService(t, base, testNonce(t, 5))
			request, _ := service.Request(context.Background(), testRequestCommand())
			clock.Set(base.Add(time.Millisecond))
			approved, _, _ := service.Decide(context.Background(), approveCommand(request))
			command := consumeCommand(approved)
			test.mutate(&command)
			claim, err := service.Claim(context.Background(), command)
			requireApprovalErrorCode(t, err, test.code)
			if claim.Request.State != domain.ApprovalStateInvalidated || store.auditCommits != 0 || store.WriteCount() != 0 {
				t.Fatalf("claim/store = %#v/%d/%d", claim, store.auditCommits, store.WriteCount())
			}
		})
	}
}

func TestApprovalClaimIsSingleOwnerUnderConcurrency(t *testing.T) {
	base := time.UnixMilli(1_700_000_000_000).UTC()
	service, clock, _ := newTestService(t, base, testNonce(t, 6))
	request, _ := service.Request(context.Background(), testRequestCommand())
	clock.Set(base.Add(time.Millisecond))
	approved, _, _ := service.Decide(context.Background(), approveCommand(request))
	const callers = 16
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(callers)
	claims := make(chan ExecutionClaim, callers)
	for range callers {
		go func() {
			defer wait.Done()
			<-start
			claim, err := service.Claim(context.Background(), consumeCommand(approved))
			if err == nil {
				claims <- claim
			}
		}()
	}
	close(start)
	wait.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatalf("successful claims = %d, want 1", len(claims))
	}
}

func TestApprovalCommitStorageFailureClosesAuthorityWithoutRetry(t *testing.T) {
	base := time.UnixMilli(1_700_000_000_000).UTC()
	store := &failingPreWriteStore{}
	clock := &fakeClock{now: base}
	service, err := NewService(ServiceConfig{
		Clock: clock, Nonces: &sequenceNonceSource{values: []domain.ApprovalNonce{testNonce(t, 7)}},
		Store: store, AuditIDs: fixedApprovalAuditIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := service.Request(context.Background(), testRequestCommand())
	clock.Set(base.Add(time.Millisecond))
	approved, _, _ := service.Decide(context.Background(), approveCommand(request))
	claim, err := service.Claim(context.Background(), consumeCommand(approved))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(base.Add(2 * time.Millisecond))
	updated, err := service.CommitConsume(context.Background(), claim)
	if !errors.Is(err, ErrPreWritePersistenceUnavailable) || updated.State != domain.ApprovalStateInvalidated || store.consumeCalls != 1 {
		t.Fatalf("CommitConsume() = %#v/%v calls=%d", updated, err, store.consumeCalls)
	}
	if _, err := service.CommitConsume(context.Background(), claim); err == nil || store.consumeCalls != 1 {
		t.Fatalf("replay error/calls = %v/%d", err, store.consumeCalls)
	}
}

type failingPreWriteStore struct{ consumeCalls int }

func (*failingPreWriteStore) VerifyApproved(context.Context, StoredRequest, StoredDecision) error {
	return nil
}

func (store *failingPreWriteStore) ConsumeWithAudit(context.Context, StoredRequest, StoredDecision, StoredRequest, domain.AuditEvent) error {
	store.consumeCalls++
	return errors.New("synthetic storage failure")
}

type fixedApprovalAuditIDs struct{}

func (fixedApprovalAuditIDs) NewAuditEventID() (domain.AuditEventID, error) {
	return "00000000-0000-7000-8000-000000003091", nil
}
