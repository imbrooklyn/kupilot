package domain

import (
	"errors"
	"time"
)

const (
	maxEvidenceFactBytes       = 2048
	maxEvidenceSourcePathBytes = 1024
	maxEvidenceSeriesBytes     = 512
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
	EvidenceCategoryMetricSnapshot  EvidenceCategory = "metric_snapshot"
	EvidenceCategoryPrometheus      EvidenceCategory = "prometheus_sample"
	EvidenceCategoryLoki            EvidenceCategory = "loki_excerpt"
	EvidenceCategoryRemoteCommand   EvidenceCategory = "remote_command"
	EvidenceCategoryContainerFile   EvidenceCategory = "container_file"
	EvidenceCategoryDiagnosticPod   EvidenceCategory = "diagnostic_pod"
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
	case EvidenceCategoryMetricSnapshot,
		EvidenceCategoryPrometheus,
		EvidenceCategoryLoki,
		EvidenceCategoryRemoteCommand,
		EvidenceCategoryContainerFile,
		EvidenceCategoryDiagnosticPod:
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
	ID               EvidenceID
	RunID            AgentRunID
	InvocationID     ToolInvocationID
	Category         EvidenceCategory
	Scope            ScopeSnapshot
	Resource         ResourceRef
	ResourceType     ResourceType
	PolicyVersion    string
	PolicyGeneration PolicyGeneration
	Fact             string
	SourcePath       *string
	SourceOriginHash string
	Series           string
	ObservedFrom     *time.Time
	ObservedThrough  *time.Time
	Severity         *EvidenceSeverity
	RedactionCount   int
	Truncated        bool
	Partial          bool
	Fingerprint      string
	ObservedAt       time.Time
}

// Validate checks provenance, scope, safe field bounds, and fingerprints.
func (evidence Evidence) Validate() error {
	resourceType := evidence.ResourceType
	if resourceType == (ResourceType{}) {
		kind, found := ResourceKindForReference(evidence.Resource)
		if !found {
			return ErrInvalidEvidence
		}
		resourceType = BuiltInResourceType(kind)
	}
	if !evidence.ID.Valid() || !evidence.RunID.Valid() || !evidence.InvocationID.Valid() ||
		!evidence.Category.Valid() || evidence.Scope.Validate() != nil ||
		!ValidContextName(evidence.Scope.Context) || !ValidNamespaceName(evidence.Scope.Namespace) || evidence.Scope.Generation < 1 ||
		resourceType.Validate() != nil || ValidateResourceRefForType(evidence.Resource, resourceType) != nil ||
		!ValidModelText(evidence.Fact, maxEvidenceFactBytes, false) ||
		evidence.RedactionCount < 0 || !validSHA256Hex(evidence.Fingerprint) ||
		!validPersistenceTime(evidence.ObservedAt) {
		return ErrInvalidEvidence
	}
	if evidence.PolicyVersion == "" && evidence.PolicyGeneration != 0 ||
		evidence.PolicyVersion != "" && (evidence.PolicyVersion != ResourcePolicyVersion && evidence.PolicyVersion != ObservabilityPolicyVersion && evidence.PolicyVersion != RemoteDiagnosticsPolicyVersion || !evidence.PolicyGeneration.Valid()) {
		return ErrInvalidEvidence
	}
	if evidence.SourcePath != nil && !ValidModelText(*evidence.SourcePath, maxEvidenceSourcePathBytes, false) ||
		evidence.SourceOriginHash != "" && !validSHA256Hex(evidence.SourceOriginHash) ||
		evidence.Series != "" && !ValidModelText(evidence.Series, maxEvidenceSeriesBytes, false) ||
		evidence.Severity != nil && !evidence.Severity.Valid() {
		return ErrInvalidEvidence
	}
	if (evidence.ObservedFrom == nil) != (evidence.ObservedThrough == nil) {
		return ErrInvalidEvidence
	}
	if evidence.ObservedFrom != nil && (!validPersistenceTime(*evidence.ObservedFrom) ||
		!validPersistenceTime(*evidence.ObservedThrough) || evidence.ObservedThrough.Before(*evidence.ObservedFrom) ||
		evidence.ObservedAt.Before(*evidence.ObservedThrough)) {
		return ErrInvalidEvidence
	}
	if !evidence.validCategoryProvenance() {
		return ErrInvalidEvidence
	}
	return nil
}

func (evidence Evidence) validCategoryProvenance() bool {
	hasSource := evidence.SourcePath != nil
	hasOrigin := evidence.SourceOriginHash != ""
	hasSeries := evidence.Series != ""
	hasWindow := evidence.ObservedFrom != nil
	switch evidence.Category {
	case EvidenceCategoryPrometheus, EvidenceCategoryLoki:
		return evidence.PolicyVersion == ObservabilityPolicyVersion && hasSource && hasOrigin && hasSeries && hasWindow
	case EvidenceCategoryMetricSnapshot:
		return evidence.PolicyVersion == ObservabilityPolicyVersion && hasSource && !hasOrigin && !hasSeries && hasWindow
	case EvidenceCategoryEvent, EvidenceCategoryLogExcerpt:
		if evidence.PolicyVersion == ObservabilityPolicyVersion {
			return hasSource && !hasOrigin && !hasSeries && hasWindow
		}
		// Records created before the observability policy version remain readable.
		return !hasOrigin && !hasSeries
	default:
		return evidence.PolicyVersion != ObservabilityPolicyVersion && !hasOrigin && !hasSeries && !hasWindow
	}
}

// DetailState returns partial when the accepted observation was incomplete or
// truncated.
func (evidence Evidence) DetailState() EvidenceDetailState {
	if evidence.Truncated || evidence.Partial {
		return EvidenceDetailPartial
	}
	return EvidenceDetailAvailable
}
