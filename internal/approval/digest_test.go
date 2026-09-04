package approval

import (
	"bytes"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalCanonicalOperationIsExactlyActionEnvelope(t *testing.T) {
	request := digestFixtureRequest(t)
	got, err := CanonicalOperation(request)
	if err != nil {
		t.Fatalf("CanonicalOperation() error = %v", err)
	}
	want, err := domain.CanonicalAction(request.ActionEnvelope())
	if err != nil {
		t.Fatalf("CanonicalAction() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("approval canonical bytes diverged from ActionEnvelope")
	}
	digest, err := OperationDigest(request)
	if err != nil || !digest.Equal(request.Digest) {
		t.Fatalf("OperationDigest() = %q/%v, want %q", digest, err, request.Digest)
	}
}

func TestApprovalDigestExcludesLifecycleAndNonceButBindsGenericIntent(t *testing.T) {
	base := digestFixtureRequest(t)
	canonical, _ := CanonicalOperation(base)
	changedState := base
	changedState.State = domain.ApprovalStateApproved
	changedState.StateReason = domain.ApprovalReasonUserApproved
	changedState.StateChangedAt = base.RequestedAt.Add(time.Millisecond)
	changedState.Nonce = testNonce(t, 91)
	got, err := CanonicalOperation(changedState)
	if err != nil || !bytes.Equal(got, canonical) {
		t.Fatalf("lifecycle changed canonical authority: %v", err)
	}

	mutations := []func(*domain.ApprovalRequest){
		func(value *domain.ApprovalRequest) {
			value.Intent.PermissionProfile = domain.PermissionProfileAutoReview
		},
		func(value *domain.ApprovalRequest) { value.Intent.PolicyGeneration++ },
		func(value *domain.ApprovalRequest) { value.Intent.Target.Resource.ResourceVersion = "changed" },
		func(value *domain.ApprovalRequest) { value.Intent.Target.Fingerprint = domain.SHA256Hex("changed") },
		func(value *domain.ApprovalRequest) { value.Intent.NetworkEffects = domain.ActionNetworkNone },
	}
	for index, mutate := range mutations {
		changed := base
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatalf("mutation %d retained envelope digest", index)
		}
	}
}

func TestApprovalNonceIsOpaqueStableAndComparedByValue(t *testing.T) {
	left := testNonce(t, 3)
	right := testNonce(t, 3)
	other := testNonce(t, 4)
	if !left.Equal(right) || left.Equal(other) || left.String() != "[approval nonce]" || left.GoString() != "[approval nonce]" {
		t.Fatal("ApprovalNonce opacity or equality changed")
	}
	if !left.Hash().Valid() || left.Hash() == other.Hash() {
		t.Fatal("ApprovalNonce hash is invalid")
	}
}

func digestFixtureRequest(t *testing.T) domain.ApprovalRequest {
	t.Helper()
	now := time.UnixMilli(1_700_000_100_000).UTC()
	command := testRequestCommand()
	envelope, err := domain.NewActionEnvelope(command.ID, command.SessionID, command.RunID, command.Intent, now)
	if err != nil {
		t.Fatalf("NewActionEnvelope() error = %v", err)
	}
	request := domain.ApprovalRequest{
		ID: envelope.RequestID, RunID: envelope.RunID, SessionID: envelope.SessionID,
		Intent: envelope.Intent, Digest: envelope.Digest, Nonce: testNonce(t, 7),
		State: domain.ApprovalStatePending, RequestedAt: now, ExpiresAt: now.Add(domain.ActionApprovalTTL),
		StateChangedAt: now,
	}
	if request.Validate() != nil {
		t.Fatalf("request.Validate() error = %v", request.Validate())
	}
	return request
}
