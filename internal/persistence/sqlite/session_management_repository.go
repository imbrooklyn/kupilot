package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const sessionMetadataColumnsSQL = `
	s.id,
	s.title,
	s.status,
	s.privacy_mode,
	s.version,
	s.created_at_ms,
	s.last_activity_at_ms,
	s.updated_at_ms,
	(SELECT count(m.id) FROM messages AS m WHERE m.session_id = s.id AND m.status = 'committed') AS committed_messages,
	(SELECT count(r.id) FROM agent_runs AS r WHERE r.session_id = s.id AND r.status IN ('queued', 'running')) AS active_runs,
	(SELECT count(a.id) FROM approvals AS a
		WHERE a.session_id = s.id
			AND (
				a.status IN ('pending', 'approved')
				OR (a.status = 'consumed' AND NOT EXISTS (
					SELECT 1 FROM audit_events AS terminal_action
					WHERE terminal_action.session_id = s.id
						AND terminal_action.run_id = a.run_id
						AND (
							terminal_action.event_type IN ('write_outcome_unknown', 'write_verified', 'write_verification_failed')
							OR (terminal_action.event_type = 'write_attempted' AND terminal_action.outcome IN ('failure', 'denied'))
						)
				))
			)
	) AS active_authorities
`

const (
	listSessionMetadataSQL = `
		SELECT ` + sessionMetadataColumnsSQL + `
		FROM sessions AS s
		ORDER BY s.last_activity_at_ms DESC, s.id DESC
		LIMIT ?
	`
	listSessionMetadataBeforeSQL = `
		SELECT ` + sessionMetadataColumnsSQL + `
		FROM sessions AS s
		WHERE s.last_activity_at_ms < ?
			OR (s.last_activity_at_ms = ? AND s.id < ?)
		ORDER BY s.last_activity_at_ms DESC, s.id DESC
		LIMIT ?
	`
	exactSessionDeletionMetadataSQL = `
		SELECT ` + sessionMetadataColumnsSQL + `
		FROM sessions AS s
		WHERE s.id = ?
	`
	countSessionsSQL         = `SELECT count(id) FROM sessions`
	currentSchemaRevisionSQL = `SELECT coalesce(max(version), 0) FROM schema_migrations`
	batchDeletionCountsSQL   = `
		SELECT
			count(s.id) AS matched,
			coalesce(sum(CASE WHEN
				s.last_activity_at_ms >= s.created_at_ms
				AND s.updated_at_ms >= s.last_activity_at_ms
				AND s.last_activity_at_ms <= ?
				AND (? = '' OR s.id <> ?)
				AND NOT EXISTS (
					SELECT 1 FROM agent_runs AS r
					WHERE r.session_id = s.id AND r.status IN ('queued', 'running')
				)
				AND NOT EXISTS (
					SELECT 1 FROM approvals AS a
					WHERE a.session_id = s.id
						AND (
							a.status IN ('pending', 'approved')
							OR (a.status = 'consumed' AND NOT EXISTS (
								SELECT 1 FROM audit_events AS terminal_action
								WHERE terminal_action.session_id = s.id
									AND terminal_action.run_id = a.run_id
									AND (
										terminal_action.event_type IN ('write_outcome_unknown', 'write_verified', 'write_verification_failed')
										OR (terminal_action.event_type = 'write_attempted' AND terminal_action.outcome IN ('failure', 'denied'))
									)
							))
						)
				)
			THEN 1 ELSE 0 END), 0) AS eligible
		FROM sessions AS s
		WHERE s.last_activity_at_ms < ?
	`
	batchDeletionSelectedSQL = `
		SELECT ` + sessionMetadataColumnsSQL + `
		FROM sessions AS s
		WHERE s.last_activity_at_ms < ?
			AND s.last_activity_at_ms >= s.created_at_ms
			AND s.updated_at_ms >= s.last_activity_at_ms
			AND s.last_activity_at_ms <= ?
			AND (? = '' OR s.id <> ?)
			AND NOT EXISTS (
				SELECT 1 FROM agent_runs AS r
				WHERE r.session_id = s.id AND r.status IN ('queued', 'running')
			)
			AND NOT EXISTS (
				SELECT 1 FROM approvals AS a
				WHERE a.session_id = s.id
					AND (
						a.status IN ('pending', 'approved')
						OR (a.status = 'consumed' AND NOT EXISTS (
							SELECT 1 FROM audit_events AS terminal_action
							WHERE terminal_action.session_id = s.id
								AND terminal_action.run_id = a.run_id
								AND (
									terminal_action.event_type IN ('write_outcome_unknown', 'write_verified', 'write_verification_failed')
									OR (terminal_action.event_type = 'write_attempted' AND terminal_action.outcome IN ('failure', 'denied'))
								)
						))
					)
			)
		ORDER BY s.last_activity_at_ms ASC, s.id ASC
		LIMIT ?
	`
	sessionStorageHealthSQL = `
		SELECT
			count(s.id) AS session_count,
			coalesce(sum(CASE WHEN s.last_activity_at_ms < 0
				OR s.created_at_ms < 0
				OR s.updated_at_ms < 0
				OR s.last_activity_at_ms < s.created_at_ms
				OR s.updated_at_ms < s.last_activity_at_ms
				OR s.last_activity_at_ms > ? THEN 1 ELSE 0 END), 0) AS protected_activity,
			coalesce(sum(CASE WHEN s.last_activity_at_ms > ? THEN 1 ELSE 0 END), 0) AS future_activity,
			(SELECT count(r.id) FROM agent_runs AS r WHERE r.status IN ('queued', 'running')) AS pending_recovery_runs
		FROM sessions AS s
	`
)

var _ application.SessionManagementPersistence = (*SessionRepository)(nil)

type sessionManagementRow struct {
	ID                   string `db:"id"`
	Title                string `db:"title"`
	Status               string `db:"status"`
	PrivacyMode          string `db:"privacy_mode"`
	Version              int64  `db:"version"`
	CreatedAtUnixMillis  int64  `db:"created_at_ms"`
	LastActiveUnixMillis int64  `db:"last_activity_at_ms"`
	UpdatedAtUnixMillis  int64  `db:"updated_at_ms"`
	CommittedMessages    int64  `db:"committed_messages"`
	ActiveRuns           int64  `db:"active_runs"`
	ActiveAuthorities    int64  `db:"active_authorities"`
}

func (row sessionManagementRow) record() application.SessionMetadataRecord {
	return application.SessionMetadataRecord{
		ID: domain.SessionID(row.ID), Title: row.Title, Status: domain.SessionStatus(row.Status),
		PrivacyMode: domain.PrivacyMode(row.PrivacyMode), Version: row.Version,
		CreatedAtUnixMillis: row.CreatedAtUnixMillis, LastActiveUnixMillis: row.LastActiveUnixMillis,
		UpdatedAtUnixMillis: row.UpdatedAtUnixMillis, CommittedMessages: row.CommittedMessages,
		ActiveRuns: row.ActiveRuns, ActiveAuthorities: row.ActiveAuthorities,
	}
}

// ListSessionMetadata returns at most the caller's already-bounded look-ahead
// page and never touches Last active.
func (repository *SessionRepository) ListSessionMetadata(
	ctx context.Context,
	request application.SessionListStoreRequest,
) (application.SessionListStorePage, error) {
	if err := repositoryContext(ctx, repository.db, "list_session_metadata"); err != nil {
		return application.SessionListStorePage{}, err
	}
	if !validSessionListStoreRequest(request) {
		return application.SessionListStorePage{}, application.ErrInvalidSessionManagementRequest
	}
	var (
		rows *sqlx.Rows
		err  error
	)
	if request.BeforeLastActiveMillis == nil {
		rows, err = repository.db.handle.QueryxContext(ctx, listSessionMetadataSQL, request.Limit)
	} else {
		rows, err = repository.db.handle.QueryxContext(ctx, listSessionMetadataBeforeSQL,
			*request.BeforeLastActiveMillis, *request.BeforeLastActiveMillis, request.BeforeID, request.Limit)
	}
	if err != nil {
		return application.SessionListStorePage{}, repositoryFailure(repository.db, "session_list_failed", "list_session_metadata", "Kupilot could not list Session metadata.", err)
	}
	defer rows.Close()
	result := application.SessionListStorePage{Sessions: make([]application.SessionMetadataRecord, 0, request.Limit)}
	for rows.Next() {
		var row sessionManagementRow
		if err := rows.StructScan(&row); err != nil {
			return application.SessionListStorePage{}, repositoryFailure(repository.db, "session_row_invalid", "list_session_metadata", "Kupilot could not read Session metadata safely.", err)
		}
		result.Sessions = append(result.Sessions, row.record())
	}
	if err := rows.Err(); err != nil {
		return application.SessionListStorePage{}, repositoryFailure(repository.db, "session_list_failed", "list_session_metadata", "Kupilot could not list Session metadata.", err)
	}
	return result, nil
}

func validSessionListStoreRequest(request application.SessionListStoreRequest) bool {
	if request.Limit < 1 || request.Limit > application.SessionListMaximumLimit+1 {
		return false
	}
	return (request.BeforeLastActiveMillis == nil) == (request.BeforeID == "") &&
		(request.BeforeID == "" || request.BeforeID.Valid())
}

// PreviewSessionDeletion reads one content-free snapshot and performs no write.
func (repository *SessionRepository) PreviewSessionDeletion(
	ctx context.Context,
	request application.SessionDeletionSelectionRequest,
) (application.SessionDeletionSnapshot, error) {
	if err := repositoryContext(ctx, repository.db, "preview_session_deletion"); err != nil {
		return application.SessionDeletionSnapshot{}, err
	}
	if !validSessionDeletionSelectionRequest(request) {
		return application.SessionDeletionSnapshot{}, application.ErrInvalidSessionManagementRequest
	}
	snapshot, err := selectSessionDeletionSnapshot(ctx, repository.db.handle, request)
	if err != nil {
		if isSessionManagementContractError(err) {
			return application.SessionDeletionSnapshot{}, err
		}
		return application.SessionDeletionSnapshot{}, repositoryFailure(repository.db, "session_delete_preview_failed", "preview_session_deletion", "Kupilot could not preview Session deletion safely.", err)
	}
	return snapshot, nil
}

type sessionManagementQueryer interface {
	GetContext(context.Context, any, string, ...any) error
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
}

func selectSessionDeletionSnapshot(
	ctx context.Context,
	queryer sessionManagementQueryer,
	request application.SessionDeletionSelectionRequest,
) (application.SessionDeletionSnapshot, error) {
	schemaRevision, total, err := sessionCounts(ctx, queryer)
	if err != nil {
		return application.SessionDeletionSnapshot{}, err
	}
	snapshot := application.SessionDeletionSnapshot{Request: request, SchemaRevision: schemaRevision, Remaining: total}
	if request.Kind == application.SessionDeletionExact {
		var row sessionManagementRow
		err := queryer.GetContext(ctx, &row, exactSessionDeletionMetadataSQL, request.SessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot, nil
		}
		if err != nil {
			return application.SessionDeletionSnapshot{}, err
		}
		record := row.record()
		snapshot.Matched = 1
		if sessionRecordDeletionEligible(record, request) {
			snapshot.Eligible = 1
			snapshot.Selected = []application.SessionMetadataRecord{record}
			snapshot.Remaining = total - 1
		} else {
			snapshot.Protected = 1
		}
		return snapshot, nil
	}

	var counts struct {
		Matched  int `db:"matched"`
		Eligible int `db:"eligible"`
	}
	current := string(request.CurrentSessionID)
	if err := queryer.GetContext(ctx, &counts, batchDeletionCountsSQL,
		request.FrozenNow.UTC().UnixMilli(), current, current, request.Cutoff.UTC().UnixMilli()); err != nil {
		return application.SessionDeletionSnapshot{}, err
	}
	if counts.Matched < 0 || counts.Eligible < 0 || counts.Eligible > counts.Matched {
		return application.SessionDeletionSnapshot{}, application.ErrPersistenceUnavailable
	}
	snapshot.Matched = counts.Matched
	snapshot.Eligible = counts.Eligible
	snapshot.Protected = counts.Matched - counts.Eligible
	snapshot.OverLimit = counts.Eligible > request.Limit
	selectionLimit := request.Limit
	rows, err := queryer.QueryxContext(ctx, batchDeletionSelectedSQL,
		request.Cutoff.UTC().UnixMilli(), request.FrozenNow.UTC().UnixMilli(), current, current, selectionLimit)
	if err != nil {
		return application.SessionDeletionSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row sessionManagementRow
		if err := rows.StructScan(&row); err != nil {
			return application.SessionDeletionSnapshot{}, err
		}
		record := row.record()
		if !sessionRecordDeletionEligible(record, request) {
			return application.SessionDeletionSnapshot{}, application.ErrPersistenceUnavailable
		}
		snapshot.Selected = append(snapshot.Selected, record)
	}
	if err := rows.Err(); err != nil {
		return application.SessionDeletionSnapshot{}, err
	}
	if !snapshot.OverLimit {
		snapshot.Remaining = total - len(snapshot.Selected)
	}
	return snapshot, nil
}

func sessionCounts(ctx context.Context, queryer sessionManagementQueryer) (schemaRevision int64, total int, err error) {
	if err = queryer.GetContext(ctx, &schemaRevision, currentSchemaRevisionSQL); err != nil {
		return 0, 0, err
	}
	if err = queryer.GetContext(ctx, &total, countSessionsSQL); err != nil {
		return 0, 0, err
	}
	if schemaRevision < 1 || total < 0 {
		return 0, 0, application.ErrPersistenceUnavailable
	}
	return schemaRevision, total, nil
}

func validSessionDeletionSelectionRequest(request application.SessionDeletionSelectionRequest) bool {
	if request.FrozenNow.IsZero() || request.FrozenNow.UnixMilli() < 0 || request.Limit < 1 || request.Limit > application.SessionDeleteMaximumLimit ||
		request.CurrentSessionID != "" && !request.CurrentSessionID.Valid() {
		return false
	}
	switch request.Kind {
	case application.SessionDeletionExact:
		return request.SessionID.Valid() && request.Cutoff.IsZero()
	case application.SessionDeletionBefore:
		return request.SessionID == "" && !request.Cutoff.IsZero() && request.Cutoff.UnixMilli() >= 0
	default:
		return false
	}
}

func sessionRecordDeletionEligible(record application.SessionMetadataRecord, request application.SessionDeletionSelectionRequest) bool {
	if !record.ID.Valid() || record.Version < 1 || record.ActiveRuns != 0 || record.ActiveAuthorities != 0 ||
		record.CreatedAtUnixMillis < 0 || record.LastActiveUnixMillis < record.CreatedAtUnixMillis ||
		record.UpdatedAtUnixMillis < record.LastActiveUnixMillis ||
		record.LastActiveUnixMillis > request.FrozenNow.UTC().UnixMilli() ||
		request.Kind == application.SessionDeletionBefore && record.ID == request.CurrentSessionID {
		return false
	}
	return request.Kind == application.SessionDeletionExact && record.ID == request.SessionID ||
		request.Kind == application.SessionDeletionBefore && record.LastActiveUnixMillis < request.Cutoff.UTC().UnixMilli()
}

// CommitSessionDeletion reselects the exact snapshot inside one transaction
// and deletes every selected graph or none.
func (repository *SessionRepository) CommitSessionDeletion(
	ctx context.Context,
	expected application.SessionDeletionSnapshot,
) (int, error) {
	if err := repositoryContext(ctx, repository.db, "commit_session_deletion"); err != nil {
		return 0, err
	}
	if !validSessionDeletionSelectionRequest(expected.Request) || expected.OverLimit || len(expected.Selected) == 0 {
		return 0, application.ErrInvalidSessionManagementRequest
	}
	remaining := 0
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		current, err := selectSessionDeletionSnapshot(ctx, tx, expected.Request)
		if err != nil {
			return err
		}
		if !sameSessionDeletionSnapshot(current, expected) {
			return application.ErrSessionDeletionStale
		}
		for _, record := range expected.Selected {
			result, err := tx.ExecContext(ctx, deleteSessionSQL, record.ID)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				return application.ErrSessionDeletionStale
			}
		}
		return tx.GetContext(ctx, &remaining, countSessionsSQL)
	})
	if isSessionManagementContractError(err) {
		return 0, err
	}
	if err != nil {
		return 0, repositoryFailure(repository.db, "session_delete_failed", "commit_session_deletion", "Kupilot could not delete the selected Session graph safely.", err)
	}
	return remaining, nil
}

func sameSessionDeletionSnapshot(left, right application.SessionDeletionSnapshot) bool {
	return left.Request == right.Request && left.Matched == right.Matched && left.Eligible == right.Eligible &&
		left.Protected == right.Protected && left.Remaining == right.Remaining && left.OverLimit == right.OverLimit &&
		left.SchemaRevision == right.SchemaRevision && reflect.DeepEqual(left.Selected, right.Selected)
}

func isSessionManagementContractError(err error) bool {
	return errors.Is(err, application.ErrInvalidSessionManagementRequest) ||
		errors.Is(err, application.ErrSessionDeletionProtected) ||
		errors.Is(err, application.ErrSessionDeletionLimit) ||
		errors.Is(err, application.ErrSessionDeletionStale) ||
		errors.Is(err, sessioncontract.ErrSessionNotFound)
}

type sessionStorageHealthRow struct {
	SessionCount        int `db:"session_count"`
	ProtectedActivity   int `db:"protected_activity"`
	FutureActivity      int `db:"future_activity"`
	PendingRecoveryRuns int `db:"pending_recovery_runs"`
}

// SessionStorageHealth returns bounded integrity counters without content.
func (repository *SessionRepository) SessionStorageHealth(ctx context.Context, now time.Time) (application.SessionStorageHealth, error) {
	if err := repositoryContext(ctx, repository.db, "session_storage_health"); err != nil {
		return application.SessionStorageHealth{}, err
	}
	if now.IsZero() || now.UnixMilli() < 0 {
		return application.SessionStorageHealth{}, application.ErrInvalidSessionManagementRequest
	}
	var row sessionStorageHealthRow
	if err := repository.db.handle.GetContext(ctx, &row, sessionStorageHealthSQL, now.UTC().UnixMilli(), now.UTC().UnixMilli()); err != nil {
		return application.SessionStorageHealth{}, repositoryFailure(repository.db, "session_health_failed", "session_storage_health", "Kupilot could not inspect local Session storage safely.", err)
	}
	var schemaRevision int
	if err := repository.db.handle.GetContext(ctx, &schemaRevision, currentSchemaRevisionSQL); err != nil {
		return application.SessionStorageHealth{}, repositoryFailure(repository.db, "session_health_failed", "session_storage_health", "Kupilot could not inspect local Session storage safely.", err)
	}
	return application.SessionStorageHealth{
		SchemaRevision: int64(schemaRevision), SessionCount: row.SessionCount,
		ProtectedActivity: row.ProtectedActivity, FutureActivity: row.FutureActivity,
		PendingRecoveryRuns: row.PendingRecoveryRuns,
	}, nil
}
