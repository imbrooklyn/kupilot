package domain

import (
	"errors"
	"math"
)

var (
	// ErrInvalidPermissionPolicy reports an invalid project-owned permission value.
	ErrInvalidPermissionPolicy = errors.New("permission policy data is invalid")
	// ErrPolicyGenerationExhausted reports that fail-closed generation advance is no longer possible.
	ErrPolicyGenerationExhausted = errors.New("policy generation is exhausted")
)

// CapabilityEffectClass is the deterministic technical effect of one
// normalized capability. It is independent from risk and review routing.
type CapabilityEffectClass string

const (
	CapabilityEffectSafeRead        CapabilityEffectClass = "safe_read"
	CapabilityEffectSensitiveRead   CapabilityEffectClass = "sensitive_read"
	CapabilityEffectNetworkEgress   CapabilityEffectClass = "network_egress"
	CapabilityEffectRemoteExecute   CapabilityEffectClass = "remote_execute"
	CapabilityEffectLocalExecute    CapabilityEffectClass = "local_execute"
	CapabilityEffectClusterMutation CapabilityEffectClass = "cluster_mutation"
)

// Valid reports whether the effect is one closed code-owned value.
func (effect CapabilityEffectClass) Valid() bool {
	switch effect {
	case CapabilityEffectSafeRead,
		CapabilityEffectSensitiveRead,
		CapabilityEffectNetworkEgress,
		CapabilityEffectRemoteExecute,
		CapabilityEffectLocalExecute,
		CapabilityEffectClusterMutation:
		return true
	default:
		return false
	}
}

// ReadOnlyHumanEligible reports the sole non-automatic effect that the
// read-only profile may route to a human.
func (effect CapabilityEffectClass) ReadOnlyHumanEligible() bool {
	return effect == CapabilityEffectSensitiveRead
}

// RiskClass is a deterministic local classification. Neither a profile nor a
// model result may lower it.
type RiskClass string

const (
	RiskSafe     RiskClass = "safe"
	RiskReview   RiskClass = "review"
	RiskCritical RiskClass = "critical"
	RiskDeny     RiskClass = "deny"
)

// Valid reports whether the risk is in the fixed policy catalog.
func (risk RiskClass) Valid() bool {
	return risk == RiskSafe || risk == RiskReview || risk == RiskCritical || risk == RiskDeny
}

// CompatibleWithEffect prevents a caller-provided risk label from lowering a
// technical side effect. Operation-specific policy may raise risk further.
func (risk RiskClass) CompatibleWithEffect(effect CapabilityEffectClass) bool {
	if !risk.Valid() || !effect.Valid() {
		return false
	}
	switch risk {
	case RiskSafe:
		return effect == CapabilityEffectSafeRead
	case RiskReview:
		return effect != CapabilityEffectSafeRead
	case RiskCritical:
		return effect == CapabilityEffectNetworkEgress || effect == CapabilityEffectRemoteExecute ||
			effect == CapabilityEffectLocalExecute || effect == CapabilityEffectClusterMutation
	case RiskDeny:
		return true
	default:
		return false
	}
}

// PermissionProfile selects review routing only; it grants no capability,
// scope, consent, RBAC, or budget.
type PermissionProfile string

const (
	PermissionProfileReadOnly   PermissionProfile = "read-only"
	PermissionProfileAsk        PermissionProfile = "ask"
	PermissionProfileAutoReview PermissionProfile = "auto-review"
	PermissionProfileFullAccess PermissionProfile = "full-access"
	PermissionProfileCustom     PermissionProfile = "custom"
)

// Valid reports whether the profile is selectable by Application.
func (profile PermissionProfile) Valid() bool {
	switch profile {
	case PermissionProfileReadOnly,
		PermissionProfileAsk,
		PermissionProfileAutoReview,
		PermissionProfileFullAccess,
		PermissionProfileCustom:
		return true
	default:
		return false
	}
}

// ReviewDisposition is the only routing outcome produced by deterministic
// permission evaluation.
type ReviewDisposition string

const (
	ReviewDispositionAutomatic ReviewDisposition = "automatic"
	ReviewDispositionHuman     ReviewDisposition = "human"
	ReviewDispositionReviewer  ReviewDisposition = "reviewer"
	ReviewDispositionDeny      ReviewDisposition = "deny"
)

// Valid reports whether the disposition is in the closed routing catalog.
func (disposition ReviewDisposition) Valid() bool {
	return disposition == ReviewDispositionAutomatic || disposition == ReviewDispositionHuman ||
		disposition == ReviewDispositionReviewer || disposition == ReviewDispositionDeny
}

// PolicyGeneration invalidates permission, Reviewer, Session-rule, and action
// state independently from Kubernetes scope generation.
type PolicyGeneration int64

// Valid reports whether the generation can carry current authority.
func (generation PolicyGeneration) Valid() bool {
	return generation > 0
}

// Next returns the next generation or fails closed on exhaustion.
func (generation PolicyGeneration) Next() (PolicyGeneration, error) {
	if !generation.Valid() || generation == PolicyGeneration(math.MaxInt64) {
		return 0, ErrPolicyGenerationExhausted
	}
	return generation + 1, nil
}

// PermissionRuleID is an opaque application-generated UUIDv7 identifier for
// one process-local Session rule. It is never persisted as resumable authority.
type PermissionRuleID string

func (id PermissionRuleID) Valid() bool { return validUUIDv7(string(id)) }
