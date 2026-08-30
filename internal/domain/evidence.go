package domain

import (
	"errors"
	"time"
)

const (
	maxEvidenceFactBytes       = 2048
	maxEvidenceSourcePathBytes = 1024
)

// ErrInvalidEvidence reports an invalid bounded Evidence derivative.
var ErrInvalidEvidence = errors.New("Evidence data is invalid")

// EvidenceID is an opaque application-generated UUIDv7 Evidence identifier.
type EvidenceID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id EvidenceID) Valid() bool {
	return validUUIDv7(string(id))
}

// EvidenceCategory is one code-defined projected observation category.
type EvidenceCategory string

const (
	EvidenceCategoryCondition       EvidenceCategory = "condition"
	EvidenceCategoryEvent           EvidenceCategory = "event"
	EvidenceCategoryContainerState  EvidenceCategory = "container_state"
	EvidenceCategoryLogExcerpt      EvidenceCategory = "log_excerpt"
	EvidenceCategoryOwner           EvidenceCategory = "owner"
	EvidenceCategoryRollout         EvidenceCategory = "rollout"
	EvidenceCategoryServiceEndpoint EvidenceCategory = "service_endpoint"
	EvidenceCategoryResourceStatus  EvidenceCategory = "resource_status"
)

// Valid reports whether the category is admitted by the safe Evidence contract.
func (category EvidenceCategory) Valid() bool {
	switch category {
	case EvidenceCategoryCondition,
		EvidenceCategoryEvent,
		EvidenceCategoryContainerState,
		EvidenceCategoryLogExcerpt,
		EvidenceCategoryOwner,
		EvidenceCategoryRollout,
		EvidenceCategoryServiceEndpoint,
		EvidenceCategoryResourceStatus:
		return true
	default:
		return false
	}
}

// EvidenceSeverity is an optional deterministic severity projection.
type EvidenceSeverity string

const (
	EvidenceSeverityInfo     EvidenceSeverity = "info"
	EvidenceSeverityWarning  EvidenceSeverity = "warning"
	EvidenceSeverityCritical EvidenceSeverity = "critical"
)

// Valid reports whether the severity is one admitted deterministic value.
func (severity EvidenceSeverity) Valid() bool {
	return severity == EvidenceSeverityInfo || severity == EvidenceSeverityWarning || severity == EvidenceSeverityCritical
}

// EvidenceDetailState explains whether historic supporting detail is complete.
type EvidenceDetailState string

const (
	EvidenceDetailAvailable EvidenceDetailState = "available"
	EvidenceDetailPartial   EvidenceDetailState = "partial"
	EvidenceDetailExpired   EvidenceDetailState = "expired"
)

// Valid reports whether the state can be shown for historic detail.
func (state EvidenceDetailState) Valid() bool {
	return state == EvidenceDetailAvailable || state == EvidenceDetailPartial || state == EvidenceDetailExpired
}

// Evidence is a concise accepted observation, never a raw source payload.
type Evidence struct {
	ID             EvidenceID
	RunID          AgentRunID
	InvocationID   ToolInvocationID
	Category       EvidenceCategory
	Scope          ScopeSnapshot
	Resource       ResourceRef
	Fact           string
	SourcePath     *string
	Severity       *EvidenceSeverity
	RedactionCount int
	Truncated      bool
	Fingerprint    string
	ObservedAt     time.Time
}

// Validate checks provenance, scope, safe field bounds, and fingerprints.
func (evidence Evidence) Validate() error {
	if !evidence.ID.Valid() || !evidence.RunID.Valid() || !evidence.InvocationID.Valid() ||
		!evidence.Category.Valid() || evidence.Scope.Validate() != nil ||
		!ValidContextName(evidence.Scope.Context) || !ValidNamespaceName(evidence.Scope.Namespace) || evidence.Scope.Generation < 1 ||
		ValidateLiveResourceRef(evidence.Resource) != nil ||
		!validModelText(evidence.Fact, maxEvidenceFactBytes, false) ||
		evidence.RedactionCount < 0 || !validSHA256Hex(evidence.Fingerprint) ||
		!validPersistenceTime(evidence.ObservedAt) {
		return ErrInvalidEvidence
	}
	if evidence.SourcePath != nil && !validModelText(*evidence.SourcePath, maxEvidenceSourcePathBytes, false) ||
		evidence.Severity != nil && !evidence.Severity.Valid() {
		return ErrInvalidEvidence
	}
	return nil
}

// DetailState returns partial only when the accepted observation was truncated.
func (evidence Evidence) DetailState() EvidenceDetailState {
	if evidence.Truncated {
		return EvidenceDetailPartial
	}
	return EvidenceDetailAvailable
}
