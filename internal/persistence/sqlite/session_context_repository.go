package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	listEligibleModelContextSQL = `
		SELECT
			m.id, m.session_id, m.run_id, m.run_sequence, m.role, m.content, m.content_format, m.status,
			m.scope_context, m.scope_namespace, m.scope_generation,
			m.resource_refs_json, m.content_hash, m.created_at_ms, r.started_at_ms
		FROM messages AS m
		JOIN agent_runs AS r ON r.id = m.run_id
		WHERE m.session_id = ?
			AND m.status = 'committed'
			AND m.role IN ('user', 'assistant')
			AND r.status = 'completed'
		ORDER BY r.started_at_ms, r.id, m.run_sequence, m.id
		LIMIT ?
	`
	listEligibleModelContextAfterSQL = `
		SELECT
			m.id, m.session_id, m.run_id, m.run_sequence, m.role, m.content, m.content_format, m.status,
			m.scope_context, m.scope_namespace, m.scope_generation,
			m.resource_refs_json, m.content_hash, m.created_at_ms, r.started_at_ms
		FROM messages AS m
		JOIN agent_runs AS r ON r.id = m.run_id
		WHERE m.session_id = ?
			AND m.status = 'committed'
			AND m.role IN ('user', 'assistant')
			AND r.status = 'completed'
			AND (
				r.started_at_ms > ?
				OR (r.started_at_ms = ? AND r.id > ?)
				OR (r.started_at_ms = ? AND r.id = ? AND m.run_sequence > ?)
				OR (r.started_at_ms = ? AND r.id = ? AND m.run_sequence = ? AND m.id > ?)
			)
		ORDER BY r.started_at_ms, r.id, m.run_sequence, m.id
		LIMIT ?
	`
	loadSessionContextSummarySQL = `
		SELECT
			session_id, summary_text, summary_hash, schema_version, policy_version,
			covered_first_message_id, covered_through_message_id,
			covered_count, covered_bytes, coverage_digest, generated_at_ms,
			agent_profile, agent_origin_hash, truncated, degraded
		FROM session_context_summaries
		WHERE session_id = ?
	`
	saveSessionContextSummarySQL = `
		INSERT INTO session_context_summaries (
			session_id, summary_text, summary_hash, schema_version, policy_version,
			covered_first_message_id, covered_through_message_id,
			covered_count, covered_bytes, coverage_digest, generated_at_ms,
			agent_profile, agent_origin_hash, truncated, degraded
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET
			summary_text = excluded.summary_text,
			summary_hash = excluded.summary_hash,
			schema_version = excluded.schema_version,
			policy_version = excluded.policy_version,
			covered_first_message_id = excluded.covered_first_message_id,
			covered_through_message_id = excluded.covered_through_message_id,
			covered_count = excluded.covered_count,
			covered_bytes = excluded.covered_bytes,
			coverage_digest = excluded.coverage_digest,
			generated_at_ms = excluded.generated_at_ms,
			agent_profile = excluded.agent_profile,
			agent_origin_hash = excluded.agent_origin_hash,
			truncated = excluded.truncated,
			degraded = excluded.degraded
		WHERE excluded.agent_profile != session_context_summaries.agent_profile
			OR excluded.agent_origin_hash != session_context_summaries.agent_origin_hash
			OR excluded.policy_version != session_context_summaries.policy_version
			OR excluded.schema_version != session_context_summaries.schema_version
			OR excluded.covered_count > session_context_summaries.covered_count
	`
)

type sessionContextSummaryRow struct {
	SessionID        string `db:"session_id"`
	SummaryText      string `db:"summary_text"`
	SummaryHash      string `db:"summary_hash"`
	SchemaVersion    string `db:"schema_version"`
	PolicyVersion    string `db:"policy_version"`
	CoveredFirstID   string `db:"covered_first_message_id"`
	CoveredThroughID string `db:"covered_through_message_id"`
	CoveredCount     int    `db:"covered_count"`
	CoveredBytes     int    `db:"covered_bytes"`
	CoverageDigest   string `db:"coverage_digest"`
	GeneratedAtMS    int64  `db:"generated_at_ms"`
	AgentProfile     string `db:"agent_profile"`
	AgentOriginHash  string `db:"agent_origin_hash"`
	Truncated        int    `db:"truncated"`
	Degraded         int    `db:"degraded"`
}

type modelContextRow struct {
	messageRow
	RunStartedAtMS int64
}

var _ application.ModelContextPersistence = (*MessageRepository)(nil)

// ListEligibleModelContext returns only complete-run user/final-assistant rows.
func (repository *MessageRepository) ListEligibleModelContext(ctx context.Context, request application.ModelContextPageRequest) (application.ModelContextPage, error) {
	if err := repositoryContext(ctx, repository.db, "list_model_context"); err != nil {
		return application.ModelContextPage{}, err
	}
	if request.Validate() != nil {
		return application.ModelContextPage{}, application.ErrModelContextUnavailable
	}
	limit := request.Limit + 1
	var (
		rows *sql.Rows
		err  error
	)
	if request.After == nil {
		rows, err = repository.db.handle.QueryContext(ctx, listEligibleModelContextSQL, request.SessionID, limit)
	} else {
		boundary := request.After.RunStartedAt.UTC().UnixMilli()
		rows, err = repository.db.handle.QueryContext(
			ctx, listEligibleModelContextAfterSQL, request.SessionID,
			boundary,
			boundary, request.After.RunID,
			boundary, request.After.RunID, request.After.RunSequence,
			boundary, request.After.RunID, request.After.RunSequence, request.After.ID,
			limit,
		)
	}
	if err != nil {
		return application.ModelContextPage{}, repositoryFailure(repository.db, "model_context_read_failed", "list_model_context", "Kupilot could not read safe Session model context.", err)
	}
	defer rows.Close()
	values := make([]domain.Message, 0, limit)
	orders := make([]application.ModelContextCursor, 0, limit)
	for rows.Next() {
		var row modelContextRow
		if err := rows.Scan(
			&row.ID, &row.SessionID, &row.RunID, &row.RunSequence, &row.Role, &row.Content, &row.ContentFormat, &row.Status,
			&row.ScopeContext, &row.ScopeNamespace, &row.ScopeGeneration, &row.ResourceRefsJSON, &row.ContentHash, &row.CreatedAtMS,
			&row.RunStartedAtMS,
		); err != nil {
			return application.ModelContextPage{}, repositoryFailure(repository.db, "model_context_row_invalid", "list_model_context", "Kupilot could not read safe Session model context.", err)
		}
		message, err := row.domainMessage()
		if err != nil {
			return application.ModelContextPage{}, repositoryFailure(repository.db, "model_context_row_invalid", "list_model_context", "Kupilot could not read safe Session model context.", err)
		}
		values = append(values, message)
		if message.RunID == nil || message.RunSequence == nil || row.RunStartedAtMS < 0 {
			return application.ModelContextPage{}, repositoryFailure(repository.db, "model_context_row_invalid", "list_model_context", "Kupilot could not read safe Session model context safely.", domain.ErrInvalidMessage)
		}
		orders = append(orders, application.ModelContextCursor{
			RunStartedAt: time.UnixMilli(row.RunStartedAtMS).UTC(), RunID: *message.RunID,
			RunSequence: *message.RunSequence, ID: message.ID,
		})
	}
	if err := rows.Err(); err != nil {
		return application.ModelContextPage{}, repositoryFailure(repository.db, "model_context_read_failed", "list_model_context", "Kupilot could not read safe Session model context.", err)
	}
	page := application.ModelContextPage{Messages: values}
	if len(values) > request.Limit {
		page.Messages = values[:request.Limit]
		value := orders[request.Limit-1]
		page.Next = &value
	}
	return page, nil
}

// LoadSessionContextSummary returns one exact safe derivative when present.
func (repository *MessageRepository) LoadSessionContextSummary(ctx context.Context, sessionID domain.SessionID) (domain.SessionContextSummary, bool, error) {
	if err := repositoryContext(ctx, repository.db, "load_session_context_summary"); err != nil {
		return domain.SessionContextSummary{}, false, err
	}
	if !sessionID.Valid() {
		return domain.SessionContextSummary{}, false, application.ErrModelContextUnavailable
	}
	var row sessionContextSummaryRow
	if err := repository.db.handle.GetContext(ctx, &row, loadSessionContextSummarySQL, sessionID); errors.Is(err, sql.ErrNoRows) {
		return domain.SessionContextSummary{}, false, nil
	} else if err != nil {
		return domain.SessionContextSummary{}, false, repositoryFailure(repository.db, "session_context_summary_read_failed", "load_session_context_summary", "Kupilot could not read the Session context summary.", err)
	}
	summary := row.domainSummary()
	if summary.Validate() != nil || summary.SessionID != sessionID {
		return domain.SessionContextSummary{}, false, repositoryFailure(repository.db, "session_context_summary_invalid", "load_session_context_summary", "Kupilot could not verify the Session context summary.", domain.ErrInvalidSessionContextSummary)
	}
	return summary, true, nil
}

// SaveSessionContextSummary advances coverage only; an equal or stale prefix is rejected.
func (repository *MessageRepository) SaveSessionContextSummary(ctx context.Context, summary domain.SessionContextSummary) error {
	if err := repositoryContext(ctx, repository.db, "save_session_context_summary"); err != nil {
		return err
	}
	if summary.Validate() != nil {
		return application.ErrModelContextUnavailable
	}
	result, err := repository.db.handle.ExecContext(ctx, saveSessionContextSummarySQL,
		summary.SessionID, summary.Text, summary.SummaryHash, summary.SchemaVersion, summary.PolicyVersion,
		summary.CoveredFirstID, summary.CoveredThroughID, summary.CoveredCount, summary.CoveredBytes,
		summary.CoverageDigest, summary.GeneratedAt.UTC().UnixMilli(), summary.AgentProfile,
		summary.AgentOriginHash, boolInt(summary.Truncated), boolInt(summary.Degraded),
	)
	if err != nil {
		return repositoryFailure(repository.db, "session_context_summary_write_failed", "save_session_context_summary", "Kupilot could not store the Session context summary.", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return repositoryFailure(repository.db, "session_context_summary_conflict", "save_session_context_summary", "The Session context summary changed before it could be stored.", err)
	}
	return nil
}

func (row sessionContextSummaryRow) domainSummary() domain.SessionContextSummary {
	return domain.SessionContextSummary{
		SessionID: domain.SessionID(row.SessionID), Text: row.SummaryText, SummaryHash: row.SummaryHash,
		SchemaVersion: row.SchemaVersion, PolicyVersion: row.PolicyVersion,
		CoveredFirstID: domain.MessageID(row.CoveredFirstID), CoveredThroughID: domain.MessageID(row.CoveredThroughID),
		CoveredCount: row.CoveredCount, CoveredBytes: row.CoveredBytes, CoverageDigest: row.CoverageDigest,
		GeneratedAt: time.UnixMilli(row.GeneratedAtMS).UTC(), AgentProfile: row.AgentProfile,
		AgentOriginHash: row.AgentOriginHash, Truncated: row.Truncated == 1, Degraded: row.Degraded == 1,
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
