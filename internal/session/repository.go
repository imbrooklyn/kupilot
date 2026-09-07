// Package session owns the consumer contracts for durable Session history.
package session

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// MaxResumePageSize bounds one Session picker query.
	MaxResumePageSize = 50
	// MaxMessagePageSize bounds one safe history query.
	MaxMessagePageSize = 100
)

var (
	ErrInvalidRepositoryRequest = errors.New("Session repository request is invalid")
	ErrSessionNotFound          = errors.New("the requested Session was not found")
	ErrSessionUnavailable       = errors.New("the requested Session is unavailable")
	ErrSessionNotResumable      = errors.New("the requested Session cannot be resumed (session_not_resumable)")
	ErrNoResumableSession       = errors.New("no resumable Session is available")
	ErrSessionConflict          = errors.New("the Session changed before the operation completed")
	ErrMessageNotFound          = errors.New("the requested Message was not found")
	ErrDurableContentDisabled   = errors.New("the Session does not permit durable conversation content")
	ErrAgentRunNotFound         = errors.New("the requested AgentRun was not found")
	ErrAgentRunConflict         = errors.New("the AgentRun changed before the operation completed")
)

// SessionStore owns the minimal Session metadata lifecycle needed by Application.
type SessionStore interface {
	Create(context.Context, domain.Session) error
	GetByID(context.Context, domain.SessionID) (domain.Session, error)
	Rename(context.Context, RenameSession) error
	Delete(context.Context, domain.SessionID) error
}

// ResumeReader owns the two bounded global Session-selection queries.
type ResumeReader interface {
	ListResumable(context.Context, ResumePageRequest) (ResumePage, error)
	GetLatestResumable(context.Context) (ResumeCandidate, error)
}

// MessageStore owns immutable safe Message writes and bounded history reads.
type MessageStore interface {
	Append(context.Context, domain.Message) error
	GetByID(context.Context, domain.MessageID) (domain.Message, error)
	ListCommittedBySession(context.Context, MessagePageRequest) (MessagePage, error)
}

// AgentRunStore owns the atomic durable start and terminal updates of a run.
type AgentRunStore interface {
	Begin(context.Context, domain.Message, domain.AgentRun) error
	GetByID(context.Context, domain.AgentRunID) (domain.AgentRun, error)
	Finish(context.Context, domain.AgentRun) error
	FinishWithMessage(context.Context, domain.Message, domain.AgentRun) error
}

// RunRecovery owns the startup-only running-to-interrupted transition.
type RunRecovery interface {
	RecoverInterrupted(context.Context, time.Time) (RecoveryResult, error)
}

// RenameSession is an optimistic, time-monotonic title change.
type RenameSession struct {
	ID              domain.SessionID
	Title           string
	ExpectedVersion int64
	UpdatedAt       time.Time
}

// Validate checks the closed rename command shape without exposing its values.
func (command RenameSession) Validate() error {
	probe := domain.Session{
		ID:             command.ID,
		Title:          command.Title,
		Status:         domain.SessionStatusActive,
		PrivacyMode:    domain.PrivacyModeStandard,
		Version:        command.ExpectedVersion,
		CreatedAt:      command.UpdatedAt,
		LastActivityAt: command.UpdatedAt,
		UpdatedAt:      command.UpdatedAt,
	}
	if probe.Validate() != nil {
		return ErrInvalidRepositoryRequest
	}
	return nil
}

// ResumeCursor is an exclusive descending keyset boundary.
type ResumeCursor struct {
	LastActivityAt time.Time
	ID             domain.SessionID
}

// ResumePageRequest selects one bounded global picker page.
type ResumePageRequest struct {
	Limit  int
	Before *ResumeCursor
}

// Validate checks page size and the optional complete keyset cursor.
func (request ResumePageRequest) Validate() error {
	if request.Limit < 1 || request.Limit > MaxResumePageSize {
		return ErrInvalidRepositoryRequest
	}
	if request.Before != nil &&
		(!request.Before.ID.Valid() || request.Before.LastActivityAt.IsZero() || request.Before.LastActivityAt.UnixMilli() < 0) {
		return ErrInvalidRepositoryRequest
	}
	return nil
}

// ResumeCandidate contains only safe picker metadata and no Message preview.
type ResumeCandidate struct {
	ID             domain.SessionID
	Title          string
	LastActivityAt time.Time
	PrivacyMode    domain.PrivacyMode
	LastScope      *domain.ScopeCandidate
}

// ResumePage is one descending stable page and its exclusive continuation.
type ResumePage struct {
	Sessions []ResumeCandidate
	Next     *ResumeCursor
}

// MessageCursor is an exclusive ascending keyset boundary.
type MessageCursor struct {
	CreatedAt time.Time
	ID        domain.MessageID
}

// MessagePageRequest selects committed safe history for one Session.
type MessagePageRequest struct {
	SessionID domain.SessionID
	Limit     int
	After     *MessageCursor
}

// Validate checks the bounded history request and optional cursor.
func (request MessagePageRequest) Validate() error {
	if !request.SessionID.Valid() || request.Limit < 1 || request.Limit > MaxMessagePageSize {
		return ErrInvalidRepositoryRequest
	}
	if request.After != nil &&
		(!request.After.ID.Valid() || request.After.CreatedAt.IsZero() || request.After.CreatedAt.UnixMilli() < 0) {
		return ErrInvalidRepositoryRequest
	}
	return nil
}

// MessagePage is one ascending stable page of committed safe history.
type MessagePage struct {
	Messages []domain.Message
	Next     *MessageCursor
}

// RecoveryResult summarizes only the number of runs made terminal at startup.
type RecoveryResult struct {
	Interrupted int64
}

// History is the bounded safe data reconstructed by an explicit resume intent.
type History struct {
	Session  domain.Session
	Messages []domain.Message
	Next     *MessageCursor
}
