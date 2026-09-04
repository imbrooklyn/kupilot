package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestPermissionEnumsAndPolicyGenerationAreClosed(t *testing.T) {
	for _, profile := range []PermissionProfile{
		PermissionProfileReadOnly, PermissionProfileAsk, PermissionProfileAutoReview,
		PermissionProfileFullAccess, PermissionProfileCustom,
	} {
		if !profile.Valid() {
			t.Fatalf("profile %q is invalid", profile)
		}
	}
	for _, risk := range []RiskClass{RiskSafe, RiskReview, RiskCritical, RiskDeny} {
		if !risk.Valid() {
			t.Fatalf("risk %q is invalid", risk)
		}
	}
	if CapabilityEffectClusterMutation.ReadOnlyHumanEligible() ||
		!CapabilityEffectSensitiveRead.ReadOnlyHumanEligible() {
		t.Fatal("read-only human eligibility widened")
	}
	if next, err := PolicyGeneration(1).Next(); err != nil || next != 2 {
		t.Fatalf("PolicyGeneration.Next() = %d/%v", next, err)
	}
	if _, err := PolicyGeneration(math.MaxInt64).Next(); err == nil {
		t.Fatal("exhausted generation advanced")
	}
	if PermissionProfile("future").Valid() || RiskClass("low").Valid() ||
		ReviewDisposition("fallback").Valid() || CapabilityEffectClass("write").Valid() {
		t.Fatal("unknown permission enum became valid")
	}
	if !RiskSafe.CompatibleWithEffect(CapabilityEffectSafeRead) ||
		RiskSafe.CompatibleWithEffect(CapabilityEffectClusterMutation) ||
		RiskReview.CompatibleWithEffect(CapabilityEffectSafeRead) ||
		!RiskReview.CompatibleWithEffect(CapabilityEffectSensitiveRead) ||
		RiskCritical.CompatibleWithEffect(CapabilityEffectSensitiveRead) ||
		!RiskCritical.CompatibleWithEffect(CapabilityEffectRemoteExecute) {
		t.Fatal("risk/effect compatibility permits risk lowering")
	}
}

func TestProgrammaticApprovalActorsCannotRecordRejection(t *testing.T) {
	nonceBytes := make([]byte, ApprovalNonceBytes)
	nonceBytes[0] = 1
	nonce, err := NewApprovalNonce(nonceBytes)
	if err != nil {
		t.Fatal(err)
	}
	decision := ApprovalDecision{
		RequestID: "00000000-0000-7000-8000-000000000061", Choice: ApprovalDecisionReject,
		ShownDigest: ActionDigest(strings.Repeat("a", 64)), Nonce: nonce,
		Actor: ApprovalActorPermissionPolicy, Disposition: ReviewDispositionAutomatic,
		DecidedAt: time.UnixMilli(1_700_000_000_000).UTC(),
	}
	if decision.Validate() == nil {
		t.Fatal("permission policy recorded a reject decision")
	}
	decision.Actor = ApprovalActorSessionRule
	decision.RuleID = "00000000-0000-7000-8000-000000000062"
	if decision.Validate() == nil {
		t.Fatal("Session rule recorded a reject decision")
	}
	decision.Choice = ApprovalDecisionApprove
	if decision.Validate() != nil {
		t.Fatal("valid Session-rule approval was rejected")
	}
}
