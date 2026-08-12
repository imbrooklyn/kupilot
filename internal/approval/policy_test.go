package approval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalRequestValidatesCanonicalShape(t *testing.T) {
	t.Parallel()
	request := digestFixtureRequest(t)
	digest, err := OperationDigest(request)
	if err != nil {
		t.Fatalf("OperationDigest() error = %v", err)
	}
	request.Digest = digest
	if err := request.Validate(); err != nil {
		t.Fatalf("ApprovalRequest.Validate() error = %v", err)
	}
	if request.ShownDigest() != digest {
		t.Fatalf("ShownDigest() = %q, want %q", request.ShownDigest(), digest)
	}

	tests := []struct {
		name   string
		mutate func(*domain.ApprovalRequest)
	}{
		{name: "invalid id", mutate: func(value *domain.ApprovalRequest) { value.ID = "request" }},
		{name: "invalid intent", mutate: func(value *domain.ApprovalRequest) { value.Intent.Operation = "scale" }},
		{name: "invalid nonce", mutate: func(value *domain.ApprovalRequest) { value.Nonce = domain.ApprovalNonce{} }},
		{name: "invalid digest", mutate: func(value *domain.ApprovalRequest) { value.Digest = "digest" }},
		{name: "invalid state", mutate: func(value *domain.ApprovalRequest) { value.State = "executing" }},
		{name: "ttl shorter", mutate: func(value *domain.ApprovalRequest) { value.ExpiresAt = value.ExpiresAt.Add(-time.Millisecond) }},
		{name: "ttl longer", mutate: func(value *domain.ApprovalRequest) { value.ExpiresAt = value.ExpiresAt.Add(time.Millisecond) }},
		{name: "sub-millisecond time", mutate: func(value *domain.ApprovalRequest) { value.RequestedAt = value.RequestedAt.Add(time.Nanosecond) }},
		{name: "state before request", mutate: func(value *domain.ApprovalRequest) { value.StateChangedAt = value.RequestedAt.Add(-time.Millisecond) }},
		{name: "pending state changed later", mutate: func(value *domain.ApprovalRequest) { value.StateChangedAt = value.StateChangedAt.Add(time.Millisecond) }},
		{name: "reason on pending", mutate: func(value *domain.ApprovalRequest) { value.StateReason = domain.ApprovalReasonUserApproved }},
		{name: "approved at expiry", mutate: func(value *domain.ApprovalRequest) {
			value.State = domain.ApprovalStateApproved
			value.StateReason = domain.ApprovalReasonUserApproved
			value.StateChangedAt = value.ExpiresAt
		}},
		{name: "expired before expiry", mutate: func(value *domain.ApprovalRequest) {
			value.State = domain.ApprovalStateExpired
			value.StateReason = domain.ApprovalReasonTTLExpired
			value.StateChangedAt = value.ExpiresAt.Add(-time.Millisecond)
		}},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			value := request
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("ApprovalRequest.Validate() error = nil")
			}
		})
	}
}

func TestApprovalDecisionValidationAndDefaultReject(t *testing.T) {
	t.Parallel()
	request := digestFixtureRequest(t)
	digest, err := OperationDigest(request)
	if err != nil {
		t.Fatalf("OperationDigest() error = %v", err)
	}
	decision := domain.ApprovalDecision{
		RequestID:   request.ID,
		Choice:      domain.ApprovalDecisionReject,
		ShownDigest: digest,
		Nonce:       request.Nonce,
		Actor:       domain.ApprovalActorLocalUser,
		DecidedAt:   request.RequestedAt,
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("ApprovalDecision.Validate() error = %v", err)
	}
	var defaultChoice domain.ApprovalDecisionChoice
	if defaultChoice != domain.ApprovalDecisionReject || defaultChoice.String() != "reject" {
		t.Fatalf("zero ApprovalDecisionChoice = %q/%q, want reject", defaultChoice, defaultChoice.String())
	}
	if domain.ApprovalDecisionApprove.String() != "approve" || domain.ApprovalDecisionChoice(2).Valid() {
		t.Fatal("ApprovalDecisionChoice admitted an unknown value")
	}

	tests := []struct {
		name   string
		mutate func(*domain.ApprovalDecision)
	}{
		{name: "request id", mutate: func(value *domain.ApprovalDecision) { value.RequestID = "request" }},
		{name: "choice", mutate: func(value *domain.ApprovalDecision) { value.Choice = 2 }},
		{name: "shown digest", mutate: func(value *domain.ApprovalDecision) { value.ShownDigest = "digest" }},
		{name: "nonce", mutate: func(value *domain.ApprovalDecision) { value.Nonce = domain.ApprovalNonce{} }},
		{name: "actor", mutate: func(value *domain.ApprovalDecision) { value.Actor = "model" }},
		{name: "time", mutate: func(value *domain.ApprovalDecision) { value.DecidedAt = time.Time{} }},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			value := decision
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("ApprovalDecision.Validate() error = nil")
			}
		})
	}
}

func TestApprovalDigestDetectsAuthoritativeIntentTampering(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	service, _, executor := newTestService(t, baseTime, testNonce(t, 101))
	request, err := service.Request(context.Background(), testRequestCommand())
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}

	service.mu.Lock()
	record := service.records[request.ID]
	record.request.Intent.ReasonSummary = "Changed after the user saw the request."
	service.mu.Unlock()

	updated, _, err := service.Decide(context.Background(), approveCommand(request))
	requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeDigestMismatch)
	if updated.State != domain.ApprovalStateInvalidated || updated.StateReason != domain.ApprovalReasonDigestMismatch {
		t.Fatalf("tampered request state/reason = %q/%q", updated.State, updated.StateReason)
	}
	if executor.WriteCount() != 0 {
		t.Fatalf("fake executor write count = %d, want 0", executor.WriteCount())
	}
}

func TestApprovalSnapshotCannotMutateAuthoritativeState(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t, time.UnixMilli(1_700_000_000_000).UTC(), testNonce(t, 102))
	request, err := service.Request(context.Background(), testRequestCommand())
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	request.Intent.DeploymentName = "changed-copy"
	request.State = domain.ApprovalStateConsumed

	snapshot, ok := service.Snapshot(request.ID)
	if !ok {
		t.Fatal("Snapshot() did not find request")
	}
	if snapshot.Intent.DeploymentName != "sample-deployment" || snapshot.State != domain.ApprovalStatePending {
		t.Fatalf("authoritative snapshot was mutated: %#v", snapshot)
	}
}

func TestApprovalRequestFailurePathsDoNotCreateAuthority(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()

	t.Run("invalid intent", func(t *testing.T) {
		nonces := &sequenceNonceSource{values: []domain.ApprovalNonce{testNonce(t, 111)}}
		executor := &fakeRestartExecutor{}
		service, err := NewService(ServiceConfig{
			Clock: &fakeClock{now: baseTime}, Nonces: nonces,
			Store: executor, Scope: executor, Revalidator: executor, Executor: executor, AuditIDs: executor,
		})
		if err != nil {
			t.Fatalf("NewService() error = %v", err)
		}
		command := testRequestCommand()
		command.Intent.ReasonSummary = strings.Repeat("r", domain.MaxApprovalReasonSummaryBytes+1)
		_, err = service.Request(context.Background(), command)
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeInvalidIntent)
		if nonces.calls != 0 || executor.WriteCount() != 0 {
			t.Fatalf("invalid request nonce/write count = %d/%d, want 0/0", nonces.calls, executor.WriteCount())
		}
	})

	t.Run("cancelled before nonce", func(t *testing.T) {
		nonces := &sequenceNonceSource{values: []domain.ApprovalNonce{testNonce(t, 112)}}
		executor := &fakeRestartExecutor{}
		service, err := NewService(ServiceConfig{
			Clock: &fakeClock{now: baseTime}, Nonces: nonces,
			Store: executor, Scope: executor, Revalidator: executor, Executor: executor, AuditIDs: executor,
		})
		if err != nil {
			t.Fatalf("NewService() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = service.Request(ctx, testRequestCommand())
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeCancelled)
		if nonces.calls != 0 || executor.WriteCount() != 0 {
			t.Fatalf("cancelled request nonce/write count = %d/%d, want 0/0", nonces.calls, executor.WriteCount())
		}
	})

	t.Run("nonce source", func(t *testing.T) {
		nonces := &sequenceNonceSource{err: errors.New("nonce canary detail")}
		executor := &fakeRestartExecutor{}
		service, err := NewService(ServiceConfig{
			Clock: &fakeClock{now: baseTime}, Nonces: nonces,
			Store: executor, Scope: executor, Revalidator: executor, Executor: executor, AuditIDs: executor,
		})
		if err != nil {
			t.Fatalf("NewService() error = %v", err)
		}
		_, err = service.Request(context.Background(), testRequestCommand())
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeInternal)
		if strings.Contains(err.Error(), "canary") || executor.WriteCount() != 0 {
			t.Fatalf("nonce failure leaked detail or invoked executor: %q/%d", err, executor.WriteCount())
		}
	})
}

func TestApprovalErrorCatalogIsStableSafeAndNonRetrying(t *testing.T) {
	t.Parallel()
	codes := []domain.ApprovalErrorCode{
		domain.ApprovalErrorCodeInvalidConfiguration,
		domain.ApprovalErrorCodeInvalidIntent,
		domain.ApprovalErrorCodeInvalidRequest,
		domain.ApprovalErrorCodeRequestNotFound,
		domain.ApprovalErrorCodeRequestConflict,
		domain.ApprovalErrorCodeInvalidTransition,
		domain.ApprovalErrorCodeDecisionReplayed,
		domain.ApprovalErrorCodeDigestMismatch,
		domain.ApprovalErrorCodeNonceMismatch,
		domain.ApprovalErrorCodeStaleScope,
		domain.ApprovalErrorCodeNotExpired,
		domain.ApprovalErrorCodeExpired,
		domain.ApprovalErrorCodeCancelled,
		domain.ApprovalErrorCodeExecutorFailed,
		domain.ApprovalErrorCodeInternal,
	}
	for _, code := range codes {
		err := domain.NewApprovalError(code)
		if err.Code() != code || !err.Class().Valid() || err.Retryable() || err.Validate() != nil {
			t.Fatalf("NewApprovalError(%q) = %#v", code, err)
		}
		if err.Error() == "" || err.SafeMessage() == "" || !strings.Contains(err.Error(), string(code)) {
			t.Fatalf("ApprovalError(%q) lacks stable safe output", code)
		}
	}
	if domain.NewApprovalError("free_form").Code() != domain.ApprovalErrorCodeInternal {
		t.Fatal("unknown ApprovalErrorCode did not fail closed")
	}
}
