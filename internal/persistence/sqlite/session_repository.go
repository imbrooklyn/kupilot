package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const (
	createSessionSQL = `
		INSERT INTO sessions (
			id, title, status, privacy_mode, last_context, last_namespace,
			selected_resource_json, summary, version, created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING
	`
	getSessionByIDSQL = `
		SELECT
			id, title, status, privacy_mode, last_context, last_namespace,
			selected_resource_json, summary, version, created_at_ms, updated_at_ms
		FROM sessions
		WHERE id = ?
	`
	renameSessionSQL = `
		UPDATE sessions
		SET title = ?, updated_at_ms = ?, version = version + 1
		WHERE id = ?
			AND privacy_mode = 'standard'
			AND version = ?
			AND updated_at_ms <= ?
	`
	deleteSessionSQL = `
		DELETE FROM sessions
		WHERE id = ?
	`
	listResumableSessionsSQL = `
		SELECT
			s.id, s.title, s.updated_at_ms, s.privacy_mode,
			s.last_context, s.last_namespace
		FROM sessions AS s
		WHERE s.status = 'active'
			AND s.privacy_mode = 'standard'
			AND EXISTS (
				SELECT 1
				FROM messages AS m
				WHERE m.session_id = s.id AND m.status = 'committed'
			)
		ORDER BY s.updated_at_ms DESC, s.id DESC
		LIMIT ?
	`
	listResumableSessionsBeforeSQL = `
		SELECT
			s.id, s.title, s.updated_at_ms, s.privacy_mode,
			s.last_context, s.last_namespace
		FROM sessions AS s
		WHERE s.status = 'active'
			AND s.privacy_mode = 'standard'
			AND (
				s.updated_at_ms < ?
				OR (s.updated_at_ms = ? AND s.id < ?)
			)
			AND EXISTS (
				SELECT 1
				FROM messages AS m
				WHERE m.session_id = s.id AND m.status = 'committed'
			)
		ORDER BY s.updated_at_ms DESC, s.id DESC
		LIMIT ?
	`
	getLatestResumableSessionSQL = `
		SELECT
			s.id, s.title, s.updated_at_ms, s.privacy_mode,
			s.last_context, s.last_namespace
		FROM sessions AS s
		WHERE s.status = 'active'
			AND s.privacy_mode = 'standard'
			AND EXISTS (
				SELECT 1
				FROM messages AS m
				WHERE m.session_id = s.id AND m.status = 'committed'
			)
		ORDER BY s.updated_at_ms DESC, s.id DESC
		LIMIT 1
	`
	getSessionPersistenceStateSQL = `
		SELECT status, privacy_mode
		FROM sessions
		WHERE id = ?
	`
	touchSessionSQL = `
		UPDATE sessions
		SET
			updated_at_ms = CASE WHEN updated_at_ms < ? THEN ? ELSE updated_at_ms END,
			version = version + 1
		WHERE id = ?
	`
)

var (
	_ sessioncontract.SessionStore = (*SessionRepository)(nil)
	_ sessioncontract.ResumeReader = (*SessionRepository)(nil)
)

type sessionRow struct {
	ID                   string         `db:"id"`
	Title                string         `db:"title"`
	Status               string         `db:"status"`
	PrivacyMode          string         `db:"privacy_mode"`
	LastContext          sql.NullString `db:"last_context"`
	LastNamespace        sql.NullString `db:"last_namespace"`
	SelectedResourceJSON sql.NullString `db:"selected_resource_json"`
	Summary              sql.NullString `db:"summary"`
	Version              int64          `db:"version"`
	CreatedAtMS          int64          `db:"created_at_ms"`
	UpdatedAtMS          int64          `db:"updated_at_ms"`
}

type resumeCandidateRow struct {
	ID            string         `db:"id"`
	Title         string         `db:"title"`
	UpdatedAtMS   int64          `db:"updated_at_ms"`
	PrivacyMode   string         `db:"privacy_mode"`
	LastContext   sql.NullString `db:"last_context"`
	LastNamespace sql.NullString `db:"last_namespace"`
}

type sessionPersistenceRow struct {
	Status      string `db:"status"`
	PrivacyMode string `db:"privacy_mode"`
}

type resourceRefJSON struct {
	APIVersion      string `json:"api_version"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resource_version,omitempty"`
}

type strictGetter interface {
	GetContext(context.Context, any, string, ...any) error
}

// SessionRepository implements Session metadata and resume queries with fixed SQL.
type SessionRepository struct {
	db *DB
}

// NewSessionRepository binds Session operations to one validated database.
func NewSessionRepository(db *DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// Create inserts one complete Session without deriving an identifier in SQLite.
func (repository *SessionRepository) Create(ctx context.Context, value domain.Session) error {
	if err := repositoryContext(ctx, repository.db, "create_session"); err != nil {
		return err
	}
	if value.Validate() != nil {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	selectedResource, err := encodeSelectedResource(value.SelectedResource)
	if err != nil {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	result, err := repository.db.handle.ExecContext(
		ctx,
		createSessionSQL,
		value.ID,
		value.Title,
		value.Status,
		value.PrivacyMode,
		nullableString(scopeContext(value.LastScope)),
		nullableString(scopeNamespace(value.LastScope)),
		nullableString(selectedResource),
		nullableString(optionalString(value.Summary)),
		value.Version,
		value.CreatedAt.UTC().UnixMilli(),
		value.UpdatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return repositoryFailure(repository.db, "session_create_failed", "create_session", "Kupilot could not create the Session.", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return repositoryFailure(repository.db, "session_create_failed", "create_session", "Kupilot could not create the Session.", err)
	}
	if affected != 1 {
		return sessioncontract.ErrSessionConflict
	}
	return nil
}

// GetByID returns one exact Session and strictly validates every mapped field.
func (repository *SessionRepository) GetByID(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	if err := repositoryContext(ctx, repository.db, "get_session"); err != nil {
		return domain.Session{}, err
	}
	if !id.Valid() {
		return domain.Session{}, sessioncontract.ErrInvalidRepositoryRequest
	}
	value, err := getSession(ctx, repository.db.handle, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, sessioncontract.ErrSessionNotFound
	}
	if err != nil {
		return domain.Session{}, repositoryFailure(repository.db, "session_row_invalid", "get_session", "Kupilot could not read the Session safely.", err)
	}
	return value, nil
}

// Rename changes only a standard Session title and advances its version.
func (repository *SessionRepository) Rename(ctx context.Context, command sessioncontract.RenameSession) error {
	if err := repositoryContext(ctx, repository.db, "rename_session"); err != nil {
		return err
	}
	if command.Validate() != nil {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		state, err := getSessionPersistenceState(ctx, tx, command.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return sessioncontract.ErrSessionNotFound
		}
		if err != nil {
			return err
		}
		if state.PrivacyMode == string(domain.PrivacyModeMinimal) {
			return sessioncontract.ErrDurableContentDisabled
		}
		result, err := tx.ExecContext(
			ctx,
			renameSessionSQL,
			command.Title,
			command.UpdatedAt.UTC().UnixMilli(),
			command.ID,
			command.ExpectedVersion,
			command.UpdatedAt.UTC().UnixMilli(),
		)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return sessioncontract.ErrSessionConflict
		}
		return nil
	})
	if isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "session_rename_failed", "rename_session", "Kupilot could not rename the Session.", err)
	}
	return nil
}

// Delete removes the complete Session-owned graph in one short transaction.
func (repository *SessionRepository) Delete(ctx context.Context, id domain.SessionID) error {
	if err := repositoryContext(ctx, repository.db, "delete_session"); err != nil {
		return err
	}
	if !id.Valid() {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		result, err := tx.ExecContext(ctx, deleteSessionSQL, id)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return sessioncontract.ErrSessionNotFound
		}
		return nil
	})
	if errors.Is(err, sessioncontract.ErrSessionNotFound) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "session_delete_failed", "delete_session", "Kupilot could not delete the Session.", err)
	}
	return nil
}

// ListResumable returns active standard Sessions with committed safe history.
func (repository *SessionRepository) ListResumable(ctx context.Context, request sessioncontract.ResumePageRequest) (sessioncontract.ResumePage, error) {
	if err := repositoryContext(ctx, repository.db, "list_resumable_sessions"); err != nil {
		return sessioncontract.ResumePage{}, err
	}
	if request.Validate() != nil {
		return sessioncontract.ResumePage{}, sessioncontract.ErrInvalidRepositoryRequest
	}
	limit := request.Limit + 1
	var (
		rows *sqlx.Rows
		err  error
	)
	if request.Before == nil {
		rows, err = repository.db.handle.QueryxContext(ctx, listResumableSessionsSQL, limit)
	} else {
		boundary := request.Before.UpdatedAt.UTC().UnixMilli()
		rows, err = repository.db.handle.QueryxContext(
			ctx,
			listResumableSessionsBeforeSQL,
			boundary,
			boundary,
			request.Before.ID,
			limit,
		)
	}
	if err != nil {
		return sessioncontract.ResumePage{}, repositoryFailure(repository.db, "session_list_failed", "list_resumable_sessions", "Kupilot could not list resumable Sessions.", err)
	}
	defer rows.Close()

	values := make([]sessioncontract.ResumeCandidate, 0, limit)
	for rows.Next() {
		var row resumeCandidateRow
		if err := rows.StructScan(&row); err != nil {
			return sessioncontract.ResumePage{}, repositoryFailure(repository.db, "session_row_invalid", "list_resumable_sessions", "Kupilot could not read resumable Session metadata safely.", err)
		}
		value, err := row.resumeCandidate()
		if err != nil {
			return sessioncontract.ResumePage{}, repositoryFailure(repository.db, "session_row_invalid", "list_resumable_sessions", "Kupilot could not read resumable Session metadata safely.", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return sessioncontract.ResumePage{}, repositoryFailure(repository.db, "session_list_failed", "list_resumable_sessions", "Kupilot could not list resumable Sessions.", err)
	}

	page := sessioncontract.ResumePage{Sessions: values}
	if len(values) > request.Limit {
		page.Sessions = values[:request.Limit]
		last := page.Sessions[len(page.Sessions)-1]
		page.Next = &sessioncontract.ResumeCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

// GetLatestResumable uses the same global ordering as the picker first row.
func (repository *SessionRepository) GetLatestResumable(ctx context.Context) (sessioncontract.ResumeCandidate, error) {
	if err := repositoryContext(ctx, repository.db, "get_latest_resumable_session"); err != nil {
		return sessioncontract.ResumeCandidate{}, err
	}
	var row resumeCandidateRow
	if err := repository.db.handle.GetContext(ctx, &row, getLatestResumableSessionSQL); errors.Is(err, sql.ErrNoRows) {
		return sessioncontract.ResumeCandidate{}, sessioncontract.ErrNoResumableSession
	} else if err != nil {
		return sessioncontract.ResumeCandidate{}, repositoryFailure(repository.db, "session_latest_failed", "get_latest_resumable_session", "Kupilot could not find the latest resumable Session.", err)
	}
	value, err := row.resumeCandidate()
	if err != nil {
		return sessioncontract.ResumeCandidate{}, repositoryFailure(repository.db, "session_row_invalid", "get_latest_resumable_session", "Kupilot could not read resumable Session metadata safely.", err)
	}
	return value, nil
}

func getSession(ctx context.Context, getter strictGetter, id domain.SessionID) (domain.Session, error) {
	var row sessionRow
	if err := getter.GetContext(ctx, &row, getSessionByIDSQL, id); err != nil {
		return domain.Session{}, err
	}
	return row.domainSession()
}

func (row sessionRow) domainSession() (domain.Session, error) {
	lastScope, err := decodeScopeCandidate(row.LastContext, row.LastNamespace)
	if err != nil {
		return domain.Session{}, err
	}
	selectedResource, err := decodeSelectedResource(row.SelectedResourceJSON)
	if err != nil {
		return domain.Session{}, err
	}
	value := domain.Session{
		ID:               domain.SessionID(row.ID),
		Title:            row.Title,
		Status:           domain.SessionStatus(row.Status),
		PrivacyMode:      domain.PrivacyMode(row.PrivacyMode),
		LastScope:        lastScope,
		SelectedResource: selectedResource,
		Summary:          stringPointer(row.Summary),
		Version:          row.Version,
		CreatedAt:        time.UnixMilli(row.CreatedAtMS).UTC(),
		UpdatedAt:        time.UnixMilli(row.UpdatedAtMS).UTC(),
	}
	if err := value.Validate(); err != nil {
		return domain.Session{}, err
	}
	return value, nil
}

func (row resumeCandidateRow) resumeCandidate() (sessioncontract.ResumeCandidate, error) {
	lastScope, err := decodeScopeCandidate(row.LastContext, row.LastNamespace)
	if err != nil {
		return sessioncontract.ResumeCandidate{}, err
	}
	value := domain.Session{
		ID:          domain.SessionID(row.ID),
		Title:       row.Title,
		Status:      domain.SessionStatusActive,
		PrivacyMode: domain.PrivacyMode(row.PrivacyMode),
		LastScope:   lastScope,
		Version:     1,
		CreatedAt:   time.UnixMilli(row.UpdatedAtMS).UTC(),
		UpdatedAt:   time.UnixMilli(row.UpdatedAtMS).UTC(),
	}
	if value.Validate() != nil || !value.HasResumableMetadata() {
		return sessioncontract.ResumeCandidate{}, domain.ErrInvalidSession
	}
	return sessioncontract.ResumeCandidate{
		ID:          value.ID,
		Title:       value.Title,
		UpdatedAt:   value.UpdatedAt,
		PrivacyMode: value.PrivacyMode,
		LastScope:   value.LastScope,
	}, nil
}

func getSessionPersistenceState(ctx context.Context, getter strictGetter, id domain.SessionID) (sessionPersistenceRow, error) {
	var state sessionPersistenceRow
	err := getter.GetContext(ctx, &state, getSessionPersistenceStateSQL, id)
	return state, err
}

func ensureStandardActiveSession(ctx context.Context, getter strictGetter, id domain.SessionID) error {
	mode, err := activeSessionPrivacyMode(ctx, getter, id)
	if err != nil {
		return err
	}
	if mode == domain.PrivacyModeMinimal {
		return sessioncontract.ErrDurableContentDisabled
	}
	return nil
}

func activeSessionPrivacyMode(ctx context.Context, getter strictGetter, id domain.SessionID) (domain.PrivacyMode, error) {
	state, err := getSessionPersistenceState(ctx, getter, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sessioncontract.ErrSessionNotFound
	}
	if err != nil {
		return "", err
	}
	mode := domain.PrivacyMode(state.PrivacyMode)
	if state.Status != string(domain.SessionStatusActive) ||
		mode != domain.PrivacyModeStandard && mode != domain.PrivacyModeMinimal {
		return "", sessioncontract.ErrSessionUnavailable
	}
	return mode, nil
}

func touchSession(ctx context.Context, tx *sqlx.Tx, id domain.SessionID, activityAt time.Time) error {
	millis := activityAt.UTC().UnixMilli()
	result, err := tx.ExecContext(ctx, touchSessionSQL, millis, millis, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sessioncontract.ErrSessionNotFound
	}
	return nil
}

func encodeSelectedResource(value *domain.ResourceRef) (string, error) {
	if value == nil {
		return "", nil
	}
	if err := value.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(resourceRefToJSON(*value))
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeSelectedResource(value sql.NullString) (*domain.ResourceRef, error) {
	if !value.Valid {
		return nil, nil
	}
	var encoded resourceRefJSON
	if err := decodeStrictJSON(value.String, &encoded); err != nil {
		return nil, err
	}
	result := encoded.domainResourceRef()
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func encodeResourceRefs(value *domain.ResourceRef) (string, error) {
	if value == nil {
		return "", nil
	}
	if err := value.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal([]resourceRefJSON{resourceRefToJSON(*value)})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeResourceRefs(value sql.NullString) (*domain.ResourceRef, error) {
	if !value.Valid {
		return nil, nil
	}
	var encoded []resourceRefJSON
	if err := decodeStrictJSON(value.String, &encoded); err != nil {
		return nil, err
	}
	if len(encoded) != 1 {
		return nil, domain.ErrInvalidResourceRef
	}
	result := encoded[0].domainResourceRef()
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return &result, nil
}

func resourceRefToJSON(value domain.ResourceRef) resourceRefJSON {
	return resourceRefJSON{
		APIVersion:      value.APIVersion,
		Kind:            value.Kind,
		Namespace:       value.Namespace,
		Name:            value.Name,
		UID:             value.UID,
		ResourceVersion: value.ResourceVersion,
	}
}

func (value resourceRefJSON) domainResourceRef() domain.ResourceRef {
	return domain.ResourceRef{
		APIVersion:      value.APIVersion,
		Kind:            value.Kind,
		Namespace:       value.Namespace,
		Name:            value.Name,
		UID:             value.UID,
		ResourceVersion: value.ResourceVersion,
	}
}

func decodeStrictJSON(value string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON projection has trailing content")
	}
	return nil
}

func decodeScopeCandidate(contextValue, namespaceValue sql.NullString) (*domain.ScopeCandidate, error) {
	if contextValue.Valid != namespaceValue.Valid {
		return nil, domain.ErrInvalidScopeCandidate
	}
	if !contextValue.Valid {
		return nil, nil
	}
	result := &domain.ScopeCandidate{Context: contextValue.String, Namespace: namespaceValue.String}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func scopeContext(value *domain.ScopeCandidate) string {
	if value == nil {
		return ""
	}
	return value.Context
}

func scopeNamespace(value *domain.ScopeCandidate) string {
	if value == nil {
		return ""
	}
	return value.Namespace
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func repositoryContext(ctx context.Context, db *DB, operation string) error {
	correlationID := "storage"
	if db != nil {
		correlationID = db.correlationID
	}
	if err := contextFailure(ctx, operation, correlationID); err != nil {
		return err
	}
	if db == nil || db.handle == nil {
		return newError(
			ClassPersistenceUnavailable,
			"storage_unavailable",
			operation,
			"Kupilot local storage is unavailable.",
			correlationID,
			nil,
		)
	}
	return nil
}

func repositoryFailure(db *DB, code, operation, message string, cause error) error {
	correlationID := "storage"
	if db != nil {
		correlationID = db.correlationID
	}
	return newError(ClassPersistenceUnavailable, code, operation, message, correlationID, cause)
}

func isSessionContractError(err error) bool {
	return errors.Is(err, sessioncontract.ErrInvalidRepositoryRequest) ||
		errors.Is(err, sessioncontract.ErrSessionNotFound) ||
		errors.Is(err, sessioncontract.ErrSessionUnavailable) ||
		errors.Is(err, sessioncontract.ErrSessionNotResumable) ||
		errors.Is(err, sessioncontract.ErrNoResumableSession) ||
		errors.Is(err, sessioncontract.ErrSessionConflict) ||
		errors.Is(err, sessioncontract.ErrMessageNotFound) ||
		errors.Is(err, sessioncontract.ErrDurableContentDisabled) ||
		errors.Is(err, sessioncontract.ErrAgentRunNotFound) ||
		errors.Is(err, sessioncontract.ErrAgentRunConflict)
}
