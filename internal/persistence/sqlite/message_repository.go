package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

const (
	insertMessageSQL = `
		INSERT INTO messages (
			id, session_id, run_id, role, content, content_format, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, content_hash, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	getMessageByIDSQL = `
		SELECT
			id, session_id, run_id, role, content, content_format, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, content_hash, created_at_ms
		FROM messages
		WHERE id = ?
	`
	listCommittedMessagesSQL = `
		SELECT
			id, session_id, run_id, role, content, content_format, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, content_hash, created_at_ms
		FROM messages
		WHERE session_id = ? AND status = 'committed'
		ORDER BY created_at_ms, id
		LIMIT ?
	`
	listCommittedMessagesAfterSQL = `
		SELECT
			id, session_id, run_id, role, content, content_format, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, content_hash, created_at_ms
		FROM messages
		WHERE session_id = ?
			AND status = 'committed'
			AND (
				created_at_ms > ?
				OR (created_at_ms = ? AND id > ?)
			)
		ORDER BY created_at_ms, id
		LIMIT ?
	`
)

var _ sessioncontract.MessageStore = (*MessageRepository)(nil)

type messageRow struct {
	ID               string         `db:"id"`
	SessionID        string         `db:"session_id"`
	RunID            sql.NullString `db:"run_id"`
	Role             string         `db:"role"`
	Content          string         `db:"content"`
	ContentFormat    string         `db:"content_format"`
	Status           string         `db:"status"`
	ScopeContext     sql.NullString `db:"scope_context"`
	ScopeNamespace   sql.NullString `db:"scope_namespace"`
	ScopeGeneration  sql.NullInt64  `db:"scope_generation"`
	ResourceRefsJSON sql.NullString `db:"resource_refs_json"`
	ContentHash      string         `db:"content_hash"`
	CreatedAtMS      int64          `db:"created_at_ms"`
}

// MessageRepository persists only independently safe Message values.
type MessageRepository struct {
	db *DB
}

// NewMessageRepository binds Message operations to one validated database.
func NewMessageRepository(db *DB) *MessageRepository {
	return &MessageRepository{db: db}
}

// Append writes one Message only when its Session uses standard persistence.
func (repository *MessageRepository) Append(ctx context.Context, message domain.Message) error {
	if err := repositoryContext(ctx, repository.db, "append_message"); err != nil {
		return err
	}
	if message.Validate() != nil {
		return sessioncontract.ErrInvalidRepositoryRequest
	}
	err := withTx(ctx, repository.db.handle, func(tx *sqlx.Tx) error {
		if err := ensureStandardActiveSession(ctx, tx, message.SessionID); err != nil {
			return err
		}
		if err := insertMessage(ctx, tx, message); err != nil {
			return err
		}
		return touchSession(ctx, tx, message.SessionID, message.CreatedAt)
	})
	if isSessionContractError(err) {
		return err
	}
	if err != nil {
		return repositoryFailure(repository.db, "message_append_failed", "append_message", "KuPilot could not store the safe Message.", err)
	}
	return nil
}

// GetByID returns one exact strictly mapped Message.
func (repository *MessageRepository) GetByID(ctx context.Context, id domain.MessageID) (domain.Message, error) {
	if err := repositoryContext(ctx, repository.db, "get_message"); err != nil {
		return domain.Message{}, err
	}
	if !id.Valid() {
		return domain.Message{}, sessioncontract.ErrInvalidRepositoryRequest
	}
	message, err := getMessage(ctx, repository.db.handle, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, sessioncontract.ErrMessageNotFound
	}
	if err != nil {
		return domain.Message{}, repositoryFailure(repository.db, "message_row_invalid", "get_message", "KuPilot could not read the Message safely.", err)
	}
	return message, nil
}

// ListCommittedBySession returns one bounded ascending history page.
func (repository *MessageRepository) ListCommittedBySession(ctx context.Context, request sessioncontract.MessagePageRequest) (sessioncontract.MessagePage, error) {
	if err := repositoryContext(ctx, repository.db, "list_session_messages"); err != nil {
		return sessioncontract.MessagePage{}, err
	}
	if request.Validate() != nil {
		return sessioncontract.MessagePage{}, sessioncontract.ErrInvalidRepositoryRequest
	}
	limit := request.Limit + 1
	var (
		rows *sqlx.Rows
		err  error
	)
	if request.After == nil {
		rows, err = repository.db.handle.QueryxContext(ctx, listCommittedMessagesSQL, request.SessionID, limit)
	} else {
		boundary := request.After.CreatedAt.UTC().UnixMilli()
		rows, err = repository.db.handle.QueryxContext(
			ctx,
			listCommittedMessagesAfterSQL,
			request.SessionID,
			boundary,
			boundary,
			request.After.ID,
			limit,
		)
	}
	if err != nil {
		return sessioncontract.MessagePage{}, repositoryFailure(repository.db, "message_list_failed", "list_session_messages", "KuPilot could not read Session history.", err)
	}
	defer rows.Close()

	values := make([]domain.Message, 0, limit)
	for rows.Next() {
		var row messageRow
		if err := rows.StructScan(&row); err != nil {
			return sessioncontract.MessagePage{}, repositoryFailure(repository.db, "message_row_invalid", "list_session_messages", "KuPilot could not read Session history safely.", err)
		}
		message, err := row.domainMessage()
		if err != nil {
			return sessioncontract.MessagePage{}, repositoryFailure(repository.db, "message_row_invalid", "list_session_messages", "KuPilot could not read Session history safely.", err)
		}
		values = append(values, message)
	}
	if err := rows.Err(); err != nil {
		return sessioncontract.MessagePage{}, repositoryFailure(repository.db, "message_list_failed", "list_session_messages", "KuPilot could not read Session history.", err)
	}

	page := sessioncontract.MessagePage{Messages: values}
	if len(values) > request.Limit {
		page.Messages = values[:request.Limit]
		last := page.Messages[len(page.Messages)-1]
		page.Next = &sessioncontract.MessageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func insertMessage(ctx context.Context, tx *sqlx.Tx, message domain.Message) error {
	resourceRefs, err := encodeResourceRefs(message.Resource)
	if err != nil {
		return err
	}
	var (
		runID           any
		scopeContext    any
		scopeNamespace  any
		scopeGeneration any
	)
	if message.RunID != nil {
		runID = *message.RunID
	}
	if message.Scope != nil {
		scopeContext = message.Scope.Context
		scopeNamespace = message.Scope.Namespace
		scopeGeneration = message.Scope.Generation
	}
	_, err = tx.ExecContext(
		ctx,
		insertMessageSQL,
		message.ID,
		message.SessionID,
		runID,
		message.Role,
		message.Content,
		message.Format,
		message.Status,
		scopeContext,
		scopeNamespace,
		scopeGeneration,
		nullableString(resourceRefs),
		message.Hash,
		message.CreatedAt.UTC().UnixMilli(),
	)
	return err
}

func getMessage(ctx context.Context, getter strictGetter, id domain.MessageID) (domain.Message, error) {
	var row messageRow
	if err := getter.GetContext(ctx, &row, getMessageByIDSQL, id); err != nil {
		return domain.Message{}, err
	}
	return row.domainMessage()
}

func (row messageRow) domainMessage() (domain.Message, error) {
	runID, err := optionalRunID(row.RunID)
	if err != nil {
		return domain.Message{}, err
	}
	scope, err := decodeScopeSnapshot(row.ScopeContext, row.ScopeNamespace, row.ScopeGeneration)
	if err != nil {
		return domain.Message{}, err
	}
	resource, err := decodeResourceRefs(row.ResourceRefsJSON)
	if err != nil {
		return domain.Message{}, err
	}
	value := domain.Message{
		ID:        domain.MessageID(row.ID),
		SessionID: domain.SessionID(row.SessionID),
		RunID:     runID,
		Role:      domain.MessageRole(row.Role),
		Content:   row.Content,
		Format:    domain.MessageFormat(row.ContentFormat),
		Status:    domain.MessageStatus(row.Status),
		Scope:     scope,
		Resource:  resource,
		Hash:      row.ContentHash,
		CreatedAt: time.UnixMilli(row.CreatedAtMS).UTC(),
	}
	if err := value.Validate(); err != nil {
		return domain.Message{}, err
	}
	return value, nil
}

func optionalRunID(value sql.NullString) (*domain.AgentRunID, error) {
	if !value.Valid {
		return nil, nil
	}
	result := domain.AgentRunID(value.String)
	if !result.Valid() {
		return nil, domain.ErrInvalidMessage
	}
	return &result, nil
}

func decodeScopeSnapshot(contextValue, namespaceValue sql.NullString, generationValue sql.NullInt64) (*domain.ScopeSnapshot, error) {
	if contextValue.Valid != namespaceValue.Valid || contextValue.Valid != generationValue.Valid {
		return nil, domain.ErrInvalidScope
	}
	if !contextValue.Valid {
		return nil, nil
	}
	result := &domain.ScopeSnapshot{
		Context:    contextValue.String,
		Namespace:  namespaceValue.String,
		Generation: generationValue.Int64,
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}
