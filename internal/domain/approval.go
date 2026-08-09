package domain

import (
	"errors"
	"time"
)

const (
	maxApprovalParametersBytes = 4096
	maxApprovalSummaryBytes    = 4096
	approvalExecutionTTL       = 60 * time.Second
)

// ErrInvalidApprovalSchemaRecord reports an invalid dormant schema DTO.
var ErrInvalidApprovalSchemaRecord = errors.New("Approval schema record is invalid")

// ApprovalID is an opaque application-generated UUIDv7 future approval identifier.
type ApprovalID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id ApprovalID) Valid() bool {
	return validUUIDv7(string(id))
}

// ApprovalOperation is the only operation admitted for future v0.2 evolution.
type ApprovalOperation string

const ApprovalOperationRestartDeployment ApprovalOperation = "restart_deployment"

// ApprovalSchemaStatus mirrors the dormant initial schema without enabling transitions.
type ApprovalSchemaStatus string

const (
	ApprovalSchemaStatusPending   ApprovalSchemaStatus = "pending"
	ApprovalSchemaStatusApproved  ApprovalSchemaStatus = "approved"
	ApprovalSchemaStatusRejected  ApprovalSchemaStatus = "rejected"
	ApprovalSchemaStatusExpired   ApprovalSchemaStatus = "expired"
	ApprovalSchemaStatusCancelled ApprovalSchemaStatus = "cancelled"
	ApprovalSchemaStatusExecuted  ApprovalSchemaStatus = "executed"
	ApprovalSchemaStatusFailed    ApprovalSchemaStatus = "failed"
)

func (status ApprovalSchemaStatus) valid() bool {
	switch status {
	case ApprovalSchemaStatusPending,
		ApprovalSchemaStatusApproved,
		ApprovalSchemaStatusRejected,
		ApprovalSchemaStatusExpired,
		ApprovalSchemaStatusCancelled,
		ApprovalSchemaStatusExecuted,
		ApprovalSchemaStatusFailed:
		return true
	default:
		return false
	}
}

// ApprovalSchemaRecord is an inert migration-compatible DTO. It grants no authority.
type ApprovalSchemaRecord struct {
	ID                      ApprovalID
	RunID                   AgentRunID
	SessionID               SessionID
	Operation               ApprovalOperation
	Scope                   ScopeSnapshot
	Target                  ResourceRef
	CanonicalParametersJSON string
	OperationDigest         string
	HumanSummary            string
	RiskSummary             string
	Status                  ApprovalSchemaStatus
	PolicyVersion           string
	RequestedAt             time.Time
	ExpiresAt               time.Time
	ResolvedAt              *time.Time
	ExecutionOutcome        *string
	VerificationSummary     *string
}

// ValidateSchemaShape checks dormant schema compatibility without defining a service.
func (record ApprovalSchemaRecord) ValidateSchemaShape() error {
	if !record.ID.Valid() || !record.RunID.Valid() || !record.SessionID.Valid() ||
		record.Operation != ApprovalOperationRestartDeployment || record.Scope.Validate() != nil ||
		record.Target.Validate() != nil || record.Target.APIVersion != "apps/v1" || record.Target.Kind != "Deployment" ||
		record.Target.Namespace != record.Scope.Namespace || record.Target.UID == "" ||
		record.CanonicalParametersJSON != "{}" || len(record.CanonicalParametersJSON) > maxApprovalParametersBytes ||
		!validSHA256Hex(record.OperationDigest) ||
		!validBoundedText(record.HumanSummary, 1, maxApprovalSummaryBytes) ||
		!validBoundedText(record.RiskSummary, 1, maxApprovalSummaryBytes) ||
		!record.Status.valid() || !validBoundedText(record.PolicyVersion, 1, maxPromptVersionBytes) ||
		!validPersistenceTime(record.RequestedAt) || !validPersistenceTime(record.ExpiresAt) ||
		!record.ExpiresAt.Equal(record.RequestedAt.Add(approvalExecutionTTL)) {
		return ErrInvalidApprovalSchemaRecord
	}
	if record.Status == ApprovalSchemaStatusPending {
		if record.ResolvedAt != nil {
			return ErrInvalidApprovalSchemaRecord
		}
	} else if record.ResolvedAt == nil || !validPersistenceTime(*record.ResolvedAt) || record.ResolvedAt.Before(record.RequestedAt) {
		return ErrInvalidApprovalSchemaRecord
	}
	if record.ExecutionOutcome != nil && !validBoundedText(*record.ExecutionOutcome, 1, 1024) ||
		record.VerificationSummary != nil && !validBoundedText(*record.VerificationSummary, 1, maxApprovalSummaryBytes) {
		return ErrInvalidApprovalSchemaRecord
	}
	return nil
}
