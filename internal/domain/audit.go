package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	maxAuditCorrelationIDBytes        = 128
	maxAuditDetailTextBytes           = 128
	maxAuditDetailsBytes              = 4096
	maxSettingKeyBytes                = 64
	maxOperationalDetailRetentionDays = 3650
)

var (
	// ErrInvalidAuditEvent reports invalid allowlisted audit metadata.
	ErrInvalidAuditEvent = errors.New("AuditEvent data is invalid")
	// ErrInvalidSetting reports invalid non-secret typed local settings.
	ErrInvalidSetting = errors.New("setting data is invalid")
)

// AuditEventID is an opaque application-generated UUIDv7 AuditEvent identifier.
type AuditEventID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id AuditEventID) Valid() bool {
	return validUUIDv7(string(id))
}

// AuditRetentionClass selects the accepted category-specific lifetime.
type AuditRetentionClass string

const (
	AuditRetentionRead  AuditRetentionClass = "read"
	AuditRetentionWrite AuditRetentionClass = "write"
)

// Valid reports whether the class has an accepted retention policy.
func (class AuditRetentionClass) Valid() bool {
	return class == AuditRetentionRead || class == AuditRetentionWrite
}

// AuditEventType is a stable event catalog, not arbitrary log text.
type AuditEventType string

const (
	AuditEventSessionCreated          AuditEventType = "session_created"
	AuditEventSessionDeleted          AuditEventType = "session_deleted"
	AuditEventSessionExportRequested  AuditEventType = "session_export_requested"
	AuditEventRunStarted              AuditEventType = "run_started"
	AuditEventRunCompleted            AuditEventType = "run_completed"
	AuditEventRunFailed               AuditEventType = "run_failed"
	AuditEventRunCancelled            AuditEventType = "run_cancelled"
	AuditEventRunTimedOut             AuditEventType = "run_timed_out"
	AuditEventRunStaleScope           AuditEventType = "run_stale_scope"
	AuditEventRunInterrupted          AuditEventType = "run_interrupted"
	AuditEventScopeChanged            AuditEventType = "scope_changed"
	AuditEventToolRequested           AuditEventType = "tool_requested"
	AuditEventToolCompleted           AuditEventType = "tool_completed"
	AuditEventToolDenied              AuditEventType = "tool_denied"
	AuditEventModelRequested          AuditEventType = "model_requested"
	AuditEventModelCompleted          AuditEventType = "model_completed"
	AuditEventConsentGranted          AuditEventType = "consent_granted"
	AuditEventConsentRevoked          AuditEventType = "consent_revoked"
	AuditEventPolicyDenied            AuditEventType = "policy_denied"
	AuditEventPersistenceDegraded     AuditEventType = "persistence_degraded"
	AuditEventApprovalRequested       AuditEventType = "approval_requested"
	AuditEventApprovalApproved        AuditEventType = "approval_approved"
	AuditEventApprovalRejected        AuditEventType = "approval_rejected"
	AuditEventApprovalExpired         AuditEventType = "approval_expired"
	AuditEventApprovalCancelled       AuditEventType = "approval_cancelled"
	AuditEventWriteIntent             AuditEventType = "write_intent"
	AuditEventWriteAttempted          AuditEventType = "write_attempted"
	AuditEventWriteOutcomeUnknown     AuditEventType = "write_outcome_unknown"
	AuditEventWriteVerified           AuditEventType = "write_verified"
	AuditEventWriteVerificationFailed AuditEventType = "write_verification_failed"
)

// RetentionClass returns the code-defined read or future-write audit lifetime.
func (eventType AuditEventType) RetentionClass() AuditRetentionClass {
	switch eventType {
	case AuditEventApprovalRequested,
		AuditEventApprovalApproved,
		AuditEventApprovalRejected,
		AuditEventApprovalExpired,
		AuditEventApprovalCancelled,
		AuditEventWriteIntent,
		AuditEventWriteAttempted,
		AuditEventWriteOutcomeUnknown,
		AuditEventWriteVerified,
		AuditEventWriteVerificationFailed:
		return AuditRetentionWrite
	case AuditEventSessionCreated,
		AuditEventSessionDeleted,
		AuditEventSessionExportRequested,
		AuditEventRunStarted,
		AuditEventRunCompleted,
		AuditEventRunFailed,
		AuditEventRunCancelled,
		AuditEventRunTimedOut,
		AuditEventRunStaleScope,
		AuditEventRunInterrupted,
		AuditEventScopeChanged,
		AuditEventToolRequested,
		AuditEventToolCompleted,
		AuditEventToolDenied,
		AuditEventModelRequested,
		AuditEventModelCompleted,
		AuditEventConsentGranted,
		AuditEventConsentRevoked,
		AuditEventPolicyDenied,
		AuditEventPersistenceDegraded:
		return AuditRetentionRead
	default:
		return ""
	}
}

// AllowedInMinimalPersistence reports whether the event is minimum lifecycle or policy audit.
func (eventType AuditEventType) AllowedInMinimalPersistence() bool {
	switch eventType {
	case AuditEventSessionCreated,
		AuditEventSessionDeleted,
		AuditEventRunStarted,
		AuditEventRunCompleted,
		AuditEventRunFailed,
		AuditEventRunCancelled,
		AuditEventRunTimedOut,
		AuditEventRunStaleScope,
		AuditEventRunInterrupted,
		AuditEventScopeChanged,
		AuditEventConsentGranted,
		AuditEventConsentRevoked,
		AuditEventPolicyDenied,
		AuditEventPersistenceDegraded:
		return true
	default:
		return eventType.RetentionClass() == AuditRetentionWrite
	}
}

// AuditActor is one local actor category.
type AuditActor string

const (
	AuditActorUser   AuditActor = "user"
	AuditActorAgent  AuditActor = "agent"
	AuditActorSystem AuditActor = "system"
)

func (actor AuditActor) valid() bool {
	return actor == AuditActorUser || actor == AuditActorAgent || actor == AuditActorSystem
}

// AuditOutcome is one safe event outcome.
type AuditOutcome string

const (
	AuditOutcomeSuccess AuditOutcome = "success"
	AuditOutcomeFailure AuditOutcome = "failure"
	AuditOutcomeDenied  AuditOutcome = "denied"
	AuditOutcomeUnknown AuditOutcome = "unknown"
)

func (outcome AuditOutcome) valid() bool {
	return outcome == AuditOutcomeSuccess || outcome == AuditOutcomeFailure || outcome == AuditOutcomeDenied || outcome == AuditOutcomeUnknown
}

// AuditDetails is a fixed scalar projection and cannot carry an arbitrary payload.
type AuditDetails struct {
	Operation     *string         `json:"operation,omitempty"`
	ErrorClass    *SafeErrorClass `json:"error_class,omitempty"`
	ToolName      *ToolName       `json:"tool_name,omitempty"`
	Sequence      *int            `json:"sequence,omitempty"`
	DetailCode    *string         `json:"detail_code,omitempty"`
	PolicyVersion *string         `json:"policy_version,omitempty"`
	Count         *int64          `json:"count,omitempty"`
}

func (details AuditDetails) validate() bool {
	if details.Operation != nil && !validBoundedText(*details.Operation, 1, maxAuditDetailTextBytes) ||
		details.ErrorClass != nil && !details.ErrorClass.Valid() ||
		details.ToolName != nil && !details.ToolName.Valid() ||
		details.Sequence != nil && (*details.Sequence < 1 || *details.Sequence > maxToolCalls) ||
		details.DetailCode != nil && !validBoundedText(*details.DetailCode, 1, maxAuditDetailTextBytes) ||
		details.PolicyVersion != nil && !validBoundedText(*details.PolicyVersion, 1, maxPromptVersionBytes) ||
		details.Count != nil && *details.Count < 0 {
		return false
	}
	encoded, err := json.Marshal(details)
	return err == nil && len(encoded) <= maxAuditDetailsBytes
}

// AuditEvent is bounded structured audit metadata, not a logging envelope.
type AuditEvent struct {
	ID            AuditEventID
	SessionID     *SessionID
	RunID         *AgentRunID
	Type          AuditEventType
	Actor         AuditActor
	Outcome       AuditOutcome
	Scope         *ScopeSnapshot
	Subject       *ResourceRef
	Details       AuditDetails
	CorrelationID string
	IntegrityHash string
	OccurredAt    time.Time
}

// Validate checks the fixed catalog and every optional relationship shape.
func (event AuditEvent) Validate() error {
	if !event.ID.Valid() || !event.Type.RetentionClass().Valid() || !event.Actor.valid() || !event.Outcome.valid() ||
		event.SessionID != nil && !event.SessionID.Valid() || event.RunID != nil && !event.RunID.Valid() ||
		!event.Details.validate() || !validOptionalText(event.CorrelationID, maxAuditCorrelationIDBytes) ||
		event.IntegrityHash != "" && !validSHA256Hex(event.IntegrityHash) ||
		!validPersistenceTime(event.OccurredAt) {
		return ErrInvalidAuditEvent
	}
	if event.Scope != nil && event.Scope.Validate() != nil {
		return ErrInvalidAuditEvent
	}
	if event.Subject != nil {
		kind, known := ResourceKindForReference(*event.Subject)
		if event.Scope == nil || event.Subject.Validate() != nil || !known ||
			kind.Namespaced() && event.Subject.Namespace != event.Scope.Namespace && !event.validCrossNamespaceDrainPhase() ||
			kind.ClusterScoped() && event.Subject.Namespace != "" {
			return ErrInvalidAuditEvent
		}
	}
	return nil
}

// validCrossNamespaceDrainPhase admits only one exact exception to the
// working-Namespace audit subject rule. A drain approved under its separately
// validated all-Namespace ActionEnvelope must retain each real Pod Namespace;
// the approval identifier and envelope digest keep that subject linked to the
// durable authority that carries the namespace-access policy.
func (event AuditEvent) validCrossNamespaceDrainPhase() bool {
	return (event.Type == AuditEventWriteAttempted || event.Type == AuditEventWriteOutcomeUnknown) &&
		event.Actor == AuditActorSystem && event.SessionID != nil && event.RunID != nil &&
		event.Details.Operation != nil && *event.Details.Operation == string(ActionOperationDrainNode) &&
		ApprovalID(event.CorrelationID).Valid() && validSHA256Hex(event.IntegrityHash)
}

// SettingKey identifies one code-defined non-secret durable preference.
type SettingKey string

const SettingOperationalDetailRetentionDays SettingKey = "retention.operational_detail_days"

// Valid reports whether the key is in the current explicit allowlist.
func (key SettingKey) Valid() bool {
	return key == SettingOperationalDetailRetentionDays && !DeniedSettingKey(key)
}

// DeniedSettingKey applies defense-in-depth to credential-shaped key names.
func DeniedSettingKey(key SettingKey) bool {
	value := strings.ToLower(string(key))
	if !validBoundedText(value, 1, maxSettingKeyBytes) || value != string(key) {
		return true
	}
	for _, denied := range []string{"api_key", "token", "kubeconfig", "credential", "cert", "private_key", "secret"} {
		if strings.Contains(value, denied) {
			return true
		}
	}
	return false
}

// Setting is a key-specific integer value; no arbitrary JSON crosses the port.
type Setting struct {
	Key           SettingKey
	IntegerValue  int64
	SchemaVersion int
	UpdatedAt     time.Time
}

// Validate checks the current typed settings allowlist and retention bound.
func (setting Setting) Validate() error {
	if !setting.Key.Valid() || setting.Key != SettingOperationalDetailRetentionDays ||
		setting.IntegerValue < 0 || setting.IntegerValue > maxOperationalDetailRetentionDays ||
		setting.SchemaVersion != 1 || !validPersistenceTime(setting.UpdatedAt) {
		return ErrInvalidSetting
	}
	return nil
}
