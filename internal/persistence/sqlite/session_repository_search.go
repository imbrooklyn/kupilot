package sqlite

import (
	"context"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const searchResumableSessionsSQL = `
	WITH search(filter_value) AS (
		VALUES (lower(?))
	), ranked AS (
		SELECT
			s.id, s.title, s.last_activity_at_ms, s.privacy_mode,
			s.last_context, s.last_namespace,
			CASE
				WHEN search.filter_value = '' THEN 0
				WHEN lower(s.title) = search.filter_value THEN 0
				WHEN substr(lower(s.title), 1, length(search.filter_value)) = search.filter_value THEN 1
				WHEN instr(lower(s.title), search.filter_value) > 0 THEN 2
				WHEN instr(lower('ctx/' || coalesce(s.last_context, '')), search.filter_value) > 0
					OR instr(lower('ns/' || coalesce(s.last_namespace, '')), search.filter_value) > 0 THEN 3
				WHEN instr(lower(strftime('%Y-%m-%d %H:%MZ', s.last_activity_at_ms / 1000.0, 'unixepoch')), search.filter_value) > 0 THEN 4
				ELSE 5
			END AS match_rank
		FROM sessions AS s
		CROSS JOIN search
		WHERE s.status = 'active'
			AND s.privacy_mode = 'standard'
			AND EXISTS (
				SELECT 1
				FROM messages AS m
				WHERE m.session_id = s.id AND m.status = 'committed'
			)
	)
	SELECT
		id, title, last_activity_at_ms, privacy_mode,
		last_context, last_namespace, match_rank
	FROM ranked
	WHERE match_rank < 5
	ORDER BY match_rank ASC, last_activity_at_ms DESC, id DESC
	LIMIT ?
`

var _ application.SessionSearchReader = (*SessionRepository)(nil)

type sessionSearchRow struct {
	resumeCandidateRow
	MatchRank int `db:"match_rank"`
}

// SearchResumable applies one fixed bounded ranking over safe display metadata.
func (repository *SessionRepository) SearchResumable(
	ctx context.Context,
	request application.SessionSearchRequest,
) ([]application.ResumeSessionRecord, error) {
	if err := repositoryContext(ctx, repository.db, "search_resumable_sessions"); err != nil {
		return nil, err
	}
	if request.Validate() != nil {
		return nil, application.ErrInvalidSessionSearch
	}
	rows, err := repository.db.handle.QueryxContext(ctx, searchResumableSessionsSQL, request.Filter, request.Limit)
	if err != nil {
		return nil, repositoryFailure(repository.db, "session_search_failed", "search_resumable_sessions", "Kupilot could not search resumable Sessions.", err)
	}
	defer rows.Close()

	result := make([]application.ResumeSessionRecord, 0, request.Limit)
	for rows.Next() {
		var row sessionSearchRow
		if err := rows.StructScan(&row); err != nil {
			return nil, repositoryFailure(repository.db, "session_row_invalid", "search_resumable_sessions", "Kupilot could not read resumable Session metadata safely.", err)
		}
		if row.MatchRank < 0 || row.MatchRank > 4 {
			return nil, repositoryFailure(repository.db, "session_row_invalid", "search_resumable_sessions", "Kupilot could not read resumable Session metadata safely.", domain.ErrInvalidSession)
		}
		candidate, err := row.resumeCandidateRow.resumeCandidate()
		if err != nil {
			return nil, repositoryFailure(repository.db, "session_row_invalid", "search_resumable_sessions", "Kupilot could not read resumable Session metadata safely.", err)
		}
		result = append(result, application.ResumeSessionRecord{
			ID: candidate.ID, Title: candidate.Title, LastActivityAt: candidate.LastActivityAt.UTC().Truncate(time.Millisecond),
			PrivacyMode: candidate.PrivacyMode, LastScope: cloneSearchScope(candidate.LastScope),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, repositoryFailure(repository.db, "session_search_failed", "search_resumable_sessions", "Kupilot could not search resumable Sessions.", err)
	}
	return result, nil
}

func cloneSearchScope(value *domain.ScopeCandidate) *domain.ScopeCandidate {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
