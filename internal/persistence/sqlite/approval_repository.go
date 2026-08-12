package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	approvalcontract "github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	insertApprovalSQL = `
		INSERT INTO approvals (
			id, run_id, session_id, operation, operation_schema_version,
			policy_version, scope_context, scope_namespace, scope_generation,
			target_api_version, target_kind, target_namespace,
			deployment_name, deployment_uid, template_fingerprint,
			deployment_generation, reason_summary, risk_summary,
			operation_digest, nonce_hash, status, state_reason,
			requested_at_ms, expires_at_ms, state_changed_at_ms
		) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (
			SELECT id FROM agent_runs WHERE id = ? AND session_id = ?
		)
	`
	selectApprovalSQL = `
		SELECT
			id, run_id, session_id, operation, operation_schema_version,
			policy_version, scope_context, scope_namespace, scope_generation,
			target_api_version, target_kind, target_namespace,
			deployment_name, deployment_uid, template_fingerprint,
			deployment_generation, reason_summary, risk_summary,
			operation_digest, nonce_hash, status, state_reason,
			requested_at_ms, expires_at_ms, state_changed_at_ms
		FROM approvals
		WHERE id = ?
	`
	selectApprovalDecisionSQL = `
		SELECT approval_id, shown_digest, nonce_hash, decision, actor, decided_at_ms
		FROM approval_decisions
		WHERE approval_id = ?
	`
	insertApprovalDecisionSQL = `
		INSERT INTO approval_decisions (
			approval_id, shown_digest, nonce_hash, decision, actor, decided_at_ms
		) VALUES (?, ?, ?, ?, ?, ?)
	`
	updateApprovalStateSQL = `
		UPDATE approvals
		SET status = ?, state_reason = ?, state_changed_at_ms = ?
		WHERE id = ? AND status = ? AND operation_digest = ? AND nonce_hash = ?
	`
	listRecoverableApprovalsSQL = `
		SELECT
			id, run_id, session_id, operation, operation_schema_version,
			policy_version, scope_context, scope_namespace, scope_generation,
			target_api_version, target_kind, target_namespace,
			deployment_name, deployment_uid, template_fingerprint,
			deployment_generation, reason_summary, risk_summary,
			operation_digest, nonce_hash, status, state_reason,
			requested_at_ms, expires_at_ms, state_changed_at_ms
		FROM approvals
		WHERE status IN ('pending', 'approved')
		ORDER BY requested_at_ms, id
		LIMIT ?
	`
	maxRecoverableApprovals = 1000
)

type approvalRequestRow struct {
	ID                     string `db:"id"`
	RunID                  string `db:"run_id"`
	SessionID              string `db:"session_id"`
	Operation              string `db:"operation"`
	OperationSchemaVersion string `db:"operation_schema_version"`
	PolicyVersion          string `db:"policy_version"`
	ScopeContext           string `db:"scope_context"`
	ScopeNamespace         string `db:"scope_namespace"`
	ScopeGeneration        int64  `db:"scope_generation"`
	TargetAPIVersion       string `db:"target_api_version"`
	TargetKind             string `db:"target_kind"`
	TargetNamespace        string `db:"target_namespace"`
	DeploymentName         string `db:"deployment_name"`
	DeploymentUID          string `db:"deployment_uid"`
	TemplateFingerprint    string `db:"template_fingerprint"`
	DeploymentGeneration   int64  `db:"deployment_generation"`
	ReasonSummary          string `db:"reason_summary"`
	RiskSummary            string `db:"risk_summary"`
	OperationDigest        string `db:"operation_digest"`
	NonceHash              string `db:"nonce_hash"`
	Status                 string `db:"status"`
	StateReason            string `db:"state_reason"`
	RequestedAtMS          int64  `db:"requested_at_ms"`
	ExpiresAtMS            int64  `db:"expires_at_ms"`
	StateChangedAtMS       int64  `db:"state_changed_at_ms"`
}

type approvalDecisionRow struct {
	ApprovalID  string `db:"approval_id"`
	ShownDigest string `db:"shown_digest"`
	NonceHash   string `db:"nonce_hash"`
	Decision    string `db:"decision"`
	Actor       string `db:"actor"`
	DecidedAtMS int64  `db:"decided_at_ms"`
}

// ApprovalRepository persists approval state and its audit event atomically.
type ApprovalRepository struct {
	db *DB
}

// NewApprovalRepository binds approval operations to one validated database.
func NewApprovalRepository(db *DB) *ApprovalRepository {
	return &ApprovalRepository{db: db}
}

// CreateWithAudit stores one pending request and its request audit atomically.
func (repository *ApprovalRepository) CreateWithAudit(
	ctx context.Context,
	request domain.ApprovalRequest,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.database(), "create_approval_request"); err != nil {
		return err
	}
	stored, err := approvalcontract.NewStoredRequest(request)
	if err != nil || stored.State != domain.ApprovalStatePending || validateApprovalAudit(stored, audit) != nil {
		return approvalcontract.ErrInvalidStoredApproval
	}
	err = withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := insertApproval(ctx, tx, stored); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	return repository.translateError(err, "approval_request_create_failed", "create_approval_request", "KuPilot could not store the approval request.")
}

// Get returns one request and its optional decision without a raw nonce.
func (repository *ApprovalRepository) Get(
	ctx context.Context,
	id domain.ApprovalID,
) (approvalcontract.StoredRequest, *approvalcontract.StoredDecision, error) {
	if err := repositoryContext(ctx, repository.database(), "get_approval_request"); err != nil {
		return approvalcontract.StoredRequest{}, nil, err
	}
	if !id.Valid() {
		return approvalcontract.StoredRequest{}, nil, approvalcontract.ErrInvalidStoredApproval
	}
	var row approvalRequestRow
	if err := repository.db.handle.GetContext(ctx, &row, selectApprovalSQL, id); errors.Is(err, sql.ErrNoRows) {
		return approvalcontract.StoredRequest{}, nil, approvalcontract.ErrStoredApprovalNotFound
	} else if err != nil {
		return approvalcontract.StoredRequest{}, nil, repository.translateError(err, "approval_request_read_failed", "get_approval_request", "KuPilot could not read the approval request.")
	}
	request, err := row.storedRequest()
	if err != nil {
		return approvalcontract.StoredRequest{}, nil, repository.translateError(err, "approval_request_row_invalid", "get_approval_request", "KuPilot could not read the approval request safely.")
	}
	var decisionRow approvalDecisionRow
	if err := repository.db.handle.GetContext(ctx, &decisionRow, selectApprovalDecisionSQL, id); errors.Is(err, sql.ErrNoRows) {
		if request.State == domain.ApprovalStateApproved || request.State == domain.ApprovalStateRejected {
			return approvalcontract.StoredRequest{}, nil, repository.translateError(approvalcontract.ErrInvalidStoredApproval, "approval_decision_row_invalid", "get_approval_request", "KuPilot could not read the approval decision safely.")
		}
		return request, nil, nil
	} else if err != nil {
		return approvalcontract.StoredRequest{}, nil, repository.translateError(err, "approval_decision_read_failed", "get_approval_request", "KuPilot could not read the approval decision.")
	}
	decision, err := decisionRow.storedDecision()
	if err != nil || decision.RequestID != request.ID || !decision.ShownDigest.Equal(request.Digest) || decision.NonceHash != request.NonceHash {
		return approvalcontract.StoredRequest{}, nil, repository.translateError(approvalcontract.ErrInvalidStoredApproval, "approval_decision_row_invalid", "get_approval_request", "KuPilot could not read the approval decision safely.")
	}
	if decision.DecidedAt.Before(request.RequestedAt) || !decision.DecidedAt.Before(request.ExpiresAt) ||
		decision.DecidedAt.After(request.StateChangedAt) {
		return approvalcontract.StoredRequest{}, nil, repository.translateError(approvalcontract.ErrInvalidStoredApproval, "approval_decision_row_invalid", "get_approval_request", "KuPilot could not read the approval decision safely.")
	}
	if request.State == domain.ApprovalStatePending ||
		request.State == domain.ApprovalStateApproved && decision.Choice != domain.ApprovalDecisionApprove ||
		request.State == domain.ApprovalStateRejected && decision.Choice != domain.ApprovalDecisionReject ||
		(request.State == domain.ApprovalStateApproved || request.State == domain.ApprovalStateRejected) &&
			!decision.DecidedAt.Equal(request.StateChangedAt) {
		return approvalcontract.StoredRequest{}, nil, repository.translateError(approvalcontract.ErrInvalidStoredApproval, "approval_decision_row_invalid", "get_approval_request", "KuPilot could not read the approval decision safely.")
	}
	return request, &decision, nil
}

// VerifyApproved proves that the exact durable request and local decision still
// match the authority held by the approval service before any Kubernetes read.
func (repository *ApprovalRepository) VerifyApproved(
	ctx context.Context,
	request approvalcontract.StoredRequest,
	decision approvalcontract.StoredDecision,
) error {
	if err := repositoryContext(ctx, repository.database(), "verify_approved_request"); err != nil {
		return err
	}
	if request.Validate() != nil || request.State != domain.ApprovalStateApproved ||
		decision.Validate() != nil || decision.RequestID != request.ID ||
		decision.Choice != domain.ApprovalDecisionApprove ||
		!decision.ShownDigest.Equal(request.Digest) || decision.NonceHash != request.NonceHash ||
		!decision.DecidedAt.Equal(request.StateChangedAt) {
		return approvalcontract.ErrInvalidStoredApproval
	}
	stored, storedDecision, err := readStoredApproval(ctx, repository.db.handle, request.ID)
	if err != nil {
		return repository.translateError(
			err,
			"approval_request_verify_failed",
			"verify_approved_request",
			"KuPilot could not verify the approved request.",
		)
	}
	if stored != request || storedDecision == nil || *storedDecision != decision {
		return approvalcontract.ErrStoredApprovalConflict
	}
	return nil
}

// ConsumeWithAudit atomically changes one exact approved request to consumed
// and inserts its pre-write intent audit. The transaction contains no external
// Kubernetes operation.
func (repository *ApprovalRepository) ConsumeWithAudit(
	ctx context.Context,
	expected approvalcontract.StoredRequest,
	expectedDecision approvalcontract.StoredDecision,
	consumed approvalcontract.StoredRequest,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.database(), "consume_approved_request"); err != nil {
		return err
	}
	if expected.Validate() != nil || consumed.Validate() != nil ||
		expected.State != domain.ApprovalStateApproved || consumed.State != domain.ApprovalStateConsumed ||
		expectedDecision.Validate() != nil || expectedDecision.RequestID != expected.ID ||
		expectedDecision.Choice != domain.ApprovalDecisionApprove ||
		!expectedDecision.ShownDigest.Equal(expected.Digest) || expectedDecision.NonceHash != expected.NonceHash ||
		!expectedDecision.DecidedAt.Equal(expected.StateChangedAt) ||
		expected.ID != consumed.ID || expected.RunID != consumed.RunID || expected.SessionID != consumed.SessionID ||
		expected.Intent != consumed.Intent || expected.Digest != consumed.Digest || expected.NonceHash != consumed.NonceHash ||
		!expected.RequestedAt.Equal(consumed.RequestedAt) || !expected.ExpiresAt.Equal(consumed.ExpiresAt) ||
		consumed.StateChangedAt.Before(expected.StateChangedAt) ||
		consumed.ValidateAudit(audit) != nil {
		return approvalcontract.ErrInvalidStoredApproval
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		stored, decision, err := readStoredApproval(ctx, tx, expected.ID)
		if err != nil {
			return err
		}
		if stored != expected || decision == nil || *decision != expectedDecision {
			return approvalcontract.ErrStoredApprovalConflict
		}
		if err := updateStoredApprovalState(ctx, tx, expected.State, consumed); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	return repository.translateError(err, "approval_request_consume_failed", "consume_approved_request", "KuPilot could not store the pre-write approval intent.")
}

// ResolveWithAudit atomically stores one local approve or reject decision.
func (repository *ApprovalRepository) ResolveWithAudit(
	ctx context.Context,
	expectedState domain.ApprovalState,
	next domain.ApprovalRequest,
	decision domain.ApprovalDecision,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.database(), "resolve_approval_request"); err != nil {
		return err
	}
	stored, err := approvalcontract.NewStoredRequest(next)
	if err != nil || expectedState != domain.ApprovalStatePending ||
		(stored.State != domain.ApprovalStateApproved && stored.State != domain.ApprovalStateRejected) ||
		validateApprovalAudit(stored, audit) != nil {
		return approvalcontract.ErrInvalidStoredApproval
	}
	storedDecision, err := approvalcontract.NewStoredDecision(decision)
	if err != nil || storedDecision.RequestID != stored.ID ||
		!storedDecision.ShownDigest.Equal(stored.Digest) || storedDecision.NonceHash != stored.NonceHash ||
		!storedDecision.DecidedAt.Equal(stored.StateChangedAt) ||
		stored.State == domain.ApprovalStateApproved && storedDecision.Choice != domain.ApprovalDecisionApprove ||
		stored.State == domain.ApprovalStateRejected && storedDecision.Choice != domain.ApprovalDecisionReject {
		return approvalcontract.ErrInvalidStoredApproval
	}
	err = withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := updateApprovalState(ctx, tx, expectedState, stored); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, insertApprovalDecisionSQL,
			storedDecision.RequestID, storedDecision.ShownDigest, storedDecision.NonceHash,
			storedDecision.Choice.String(), storedDecision.Actor, storedDecision.DecidedAt.UnixMilli(),
		); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	return repository.translateError(err, "approval_request_resolve_failed", "resolve_approval_request", "KuPilot could not store the approval decision.")
}

// CloseWithAudit atomically persists expiry, cancellation, or invalidation.
func (repository *ApprovalRepository) CloseWithAudit(
	ctx context.Context,
	expectedState domain.ApprovalState,
	next domain.ApprovalRequest,
	audit domain.AuditEvent,
) error {
	if err := repositoryContext(ctx, repository.database(), "close_approval_request"); err != nil {
		return err
	}
	stored, err := approvalcontract.NewStoredRequest(next)
	if err != nil || (expectedState != domain.ApprovalStatePending && expectedState != domain.ApprovalStateApproved) ||
		(stored.State != domain.ApprovalStateExpired && stored.State != domain.ApprovalStateCancelled && stored.State != domain.ApprovalStateInvalidated) ||
		validateApprovalAudit(stored, audit) != nil {
		return approvalcontract.ErrInvalidStoredApproval
	}
	err = withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := updateApprovalState(ctx, tx, expectedState, stored); err != nil {
			return err
		}
		return insertAuditEvent(ctx, tx, audit)
	})
	return repository.translateError(err, "approval_request_close_failed", "close_approval_request", "KuPilot could not close the approval request.")
}

// ListRecoverable returns a bounded oldest-first startup recovery batch.
func (repository *ApprovalRepository) ListRecoverable(ctx context.Context, limit int) ([]approvalcontract.StoredRequest, error) {
	if err := repositoryContext(ctx, repository.database(), "list_recoverable_approvals"); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maxRecoverableApprovals {
		return nil, approvalcontract.ErrInvalidStoredApproval
	}
	rows, err := repository.db.handle.QueryxContext(ctx, listRecoverableApprovalsSQL, limit)
	if err != nil {
		return nil, repository.translateError(err, "approval_recovery_list_failed", "list_recoverable_approvals", "KuPilot could not read recoverable approval requests.")
	}
	defer rows.Close()
	result := make([]approvalcontract.StoredRequest, 0, limit)
	for rows.Next() {
		var row approvalRequestRow
		if err := rows.StructScan(&row); err != nil {
			return nil, repository.translateError(err, "approval_request_row_invalid", "list_recoverable_approvals", "KuPilot could not read approval requests safely.")
		}
		request, err := row.storedRequest()
		if err != nil {
			return nil, repository.translateError(err, "approval_request_row_invalid", "list_recoverable_approvals", "KuPilot could not read approval requests safely.")
		}
		result = append(result, request)
	}
	if err := rows.Err(); err != nil {
		return nil, repository.translateError(err, "approval_recovery_list_failed", "list_recoverable_approvals", "KuPilot could not read recoverable approval requests.")
	}
	return result, nil
}

// RecoverWithAudits closes a complete startup batch in one transaction.
func (repository *ApprovalRepository) RecoverWithAudits(ctx context.Context, transitions []approvalcontract.RecoveryTransition) error {
	if err := repositoryContext(ctx, repository.database(), "recover_approval_requests"); err != nil {
		return err
	}
	if len(transitions) < 1 || len(transitions) > maxRecoverableApprovals {
		return approvalcontract.ErrInvalidStoredApproval
	}
	seen := make(map[domain.ApprovalID]struct{}, len(transitions))
	for _, transition := range transitions {
		if transition.Validate() != nil {
			return approvalcontract.ErrInvalidStoredApproval
		}
		if _, exists := seen[transition.Before.ID]; exists {
			return approvalcontract.ErrStoredApprovalConflict
		}
		seen[transition.Before.ID] = struct{}{}
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		for _, transition := range transitions {
			if err := updateStoredApprovalState(ctx, tx, transition.Before.State, transition.After); err != nil {
				return err
			}
			if err := insertAuditEvent(ctx, tx, transition.Audit); err != nil {
				return err
			}
		}
		return nil
	})
	return repository.translateError(err, "approval_recovery_failed", "recover_approval_requests", "KuPilot could not invalidate recovered approval requests.")
}

func insertApproval(ctx context.Context, tx *sqlx.Tx, request approvalcontract.StoredRequest) error {
	result, err := tx.ExecContext(ctx, insertApprovalSQL,
		request.ID, request.RunID, request.SessionID, request.Intent.Operation,
		domain.RestartDeploymentOperationSchemaVersion, request.Intent.PolicyVersion,
		request.Intent.Scope.Context, request.Intent.Scope.Namespace, request.Intent.Scope.Generation,
		domain.RestartDeploymentTargetAPIVersion, domain.RestartDeploymentTargetKind, request.Intent.Scope.Namespace,
		request.Intent.DeploymentName, request.Intent.DeploymentUID, request.Intent.TemplateFingerprint,
		request.Intent.DeploymentGeneration, request.Intent.ReasonSummary, domain.RestartDeploymentRiskSummary,
		request.Digest, request.NonceHash, request.State, request.StateReason,
		request.RequestedAt.UnixMilli(), request.ExpiresAt.UnixMilli(), request.StateChangedAt.UnixMilli(),
		request.RunID, request.SessionID,
	)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return approvalcontract.ErrStoredApprovalConflict
	}
	return nil
}

func updateApprovalState(ctx context.Context, tx *sqlx.Tx, expected domain.ApprovalState, request approvalcontract.StoredRequest) error {
	return updateStoredApprovalState(ctx, tx, expected, request)
}

func updateStoredApprovalState(ctx context.Context, tx *sqlx.Tx, expected domain.ApprovalState, request approvalcontract.StoredRequest) error {
	result, err := tx.ExecContext(ctx, updateApprovalStateSQL,
		request.State, request.StateReason, request.StateChangedAt.UnixMilli(),
		request.ID, expected, request.Digest, request.NonceHash,
	)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return approvalcontract.ErrStoredApprovalConflict
	}
	return nil
}

func (row approvalRequestRow) storedRequest() (approvalcontract.StoredRequest, error) {
	if row.OperationSchemaVersion != domain.RestartDeploymentOperationSchemaVersion ||
		row.TargetAPIVersion != domain.RestartDeploymentTargetAPIVersion ||
		row.TargetKind != domain.RestartDeploymentTargetKind || row.TargetNamespace != row.ScopeNamespace ||
		row.RiskSummary != domain.RestartDeploymentRiskSummary {
		return approvalcontract.StoredRequest{}, approvalcontract.ErrInvalidStoredApproval
	}
	request := approvalcontract.StoredRequest{
		ID: domain.ApprovalID(row.ID), RunID: domain.AgentRunID(row.RunID), SessionID: domain.SessionID(row.SessionID),
		Intent: domain.OperationIntent{
			Operation:      domain.ApprovalOperation(row.Operation),
			Scope:          domain.ScopeSnapshot{Context: row.ScopeContext, Namespace: row.ScopeNamespace, Generation: row.ScopeGeneration},
			DeploymentName: row.DeploymentName, DeploymentUID: row.DeploymentUID,
			TemplateFingerprint: row.TemplateFingerprint, DeploymentGeneration: row.DeploymentGeneration,
			PolicyVersion: row.PolicyVersion, ReasonSummary: row.ReasonSummary,
		},
		Digest: domain.ApprovalDigest(row.OperationDigest), NonceHash: domain.ApprovalNonceHash(row.NonceHash),
		State: domain.ApprovalState(row.Status), StateReason: domain.ApprovalStateReason(row.StateReason),
		RequestedAt: time.UnixMilli(row.RequestedAtMS).UTC(), ExpiresAt: time.UnixMilli(row.ExpiresAtMS).UTC(),
		StateChangedAt: time.UnixMilli(row.StateChangedAtMS).UTC(),
	}
	if request.Validate() != nil {
		return approvalcontract.StoredRequest{}, approvalcontract.ErrInvalidStoredApproval
	}
	return request, nil
}

func (row approvalDecisionRow) storedDecision() (approvalcontract.StoredDecision, error) {
	choice := domain.ApprovalDecisionReject
	if row.Decision == domain.ApprovalDecisionApprove.String() {
		choice = domain.ApprovalDecisionApprove
	} else if row.Decision != domain.ApprovalDecisionReject.String() {
		return approvalcontract.StoredDecision{}, approvalcontract.ErrInvalidStoredApproval
	}
	decision := approvalcontract.StoredDecision{
		RequestID: domain.ApprovalID(row.ApprovalID), Choice: choice,
		ShownDigest: domain.ApprovalDigest(row.ShownDigest), NonceHash: domain.ApprovalNonceHash(row.NonceHash),
		Actor: domain.ApprovalActor(row.Actor), DecidedAt: time.UnixMilli(row.DecidedAtMS).UTC(),
	}
	if decision.Validate() != nil {
		return approvalcontract.StoredDecision{}, approvalcontract.ErrInvalidStoredApproval
	}
	return decision, nil
}

func validateApprovalAudit(request approvalcontract.StoredRequest, event domain.AuditEvent) error {
	return request.ValidateAudit(event)
}

func (repository *ApprovalRepository) database() *DB {
	if repository == nil {
		return nil
	}
	return repository.db
}

func (repository *ApprovalRepository) translateError(err error, code, operation, message string) error {
	if err == nil || errors.Is(err, approvalcontract.ErrInvalidStoredApproval) ||
		errors.Is(err, approvalcontract.ErrStoredApprovalNotFound) || errors.Is(err, approvalcontract.ErrStoredApprovalConflict) {
		return err
	}
	return repositoryFailure(repository.database(), code, operation, message, err)
}

func readStoredApproval(
	ctx context.Context,
	getter strictGetter,
	id domain.ApprovalID,
) (approvalcontract.StoredRequest, *approvalcontract.StoredDecision, error) {
	var row approvalRequestRow
	if err := getter.GetContext(ctx, &row, selectApprovalSQL, id); errors.Is(err, sql.ErrNoRows) {
		return approvalcontract.StoredRequest{}, nil, approvalcontract.ErrStoredApprovalNotFound
	} else if err != nil {
		return approvalcontract.StoredRequest{}, nil, err
	}
	request, err := row.storedRequest()
	if err != nil {
		return approvalcontract.StoredRequest{}, nil, err
	}
	var decisionRow approvalDecisionRow
	if err := getter.GetContext(ctx, &decisionRow, selectApprovalDecisionSQL, id); errors.Is(err, sql.ErrNoRows) {
		return request, nil, nil
	} else if err != nil {
		return approvalcontract.StoredRequest{}, nil, err
	}
	decision, err := decisionRow.storedDecision()
	if err != nil {
		return approvalcontract.StoredRequest{}, nil, err
	}
	return request, &decision, nil
}
