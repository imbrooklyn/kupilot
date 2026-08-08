package session

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// Service applies Session-history semantics before calling consumer-owned ports.
// It never activates scope or invokes a model, Tool, or Kubernetes client.
type Service struct {
	sessions SessionStore
	resume   ResumeReader
	messages MessageStore
	runs     AgentRunStore
	recovery RunRecovery
}

// NewService constructs the Session-history consumer.
func NewService(
	sessions SessionStore,
	resume ResumeReader,
	messages MessageStore,
	runs AgentRunStore,
	recovery RunRecovery,
) *Service {
	return &Service{
		sessions: sessions,
		resume:   resume,
		messages: messages,
		runs:     runs,
		recovery: recovery,
	}
}

// Create persists one already-validated Session shell.
func (service *Service) Create(ctx context.Context, value domain.Session) error {
	if value.Validate() != nil {
		return ErrInvalidRepositoryRequest
	}
	return service.sessions.Create(ctx, value)
}

// GetByID reads one exact Session without turning historic scope into authority.
func (service *Service) GetByID(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	if !id.Valid() {
		return domain.Session{}, ErrInvalidRepositoryRequest
	}
	return service.sessions.GetByID(ctx, id)
}

// Rename changes only a standard Session title through optimistic concurrency.
func (service *Service) Rename(ctx context.Context, command RenameSession) error {
	if command.Validate() != nil {
		return ErrInvalidRepositoryRequest
	}
	return service.sessions.Rename(ctx, command)
}

// Delete removes the complete Session-owned graph transactionally.
func (service *Service) Delete(ctx context.Context, id domain.SessionID) error {
	if !id.Valid() {
		return ErrInvalidRepositoryRequest
	}
	return service.sessions.Delete(ctx, id)
}

// Append stores one independently safe Message under standard persistence.
func (service *Service) Append(ctx context.Context, message domain.Message) error {
	if message.Validate() != nil {
		return ErrInvalidRepositoryRequest
	}
	return service.messages.Append(ctx, message)
}

// ListMessages reads one bounded page of committed safe history.
func (service *Service) ListMessages(ctx context.Context, request MessagePageRequest) (MessagePage, error) {
	if request.Validate() != nil {
		return MessagePage{}, ErrInvalidRepositoryRequest
	}
	return service.messages.ListCommittedBySession(ctx, request)
}

// BeginRun atomically persists the safe request Message and running AgentRun.
func (service *Service) BeginRun(ctx context.Context, message domain.Message, run domain.AgentRun) error {
	if message.Validate() != nil || run.Validate() != nil {
		return ErrInvalidRepositoryRequest
	}
	return service.runs.Begin(ctx, message, run)
}

// GetRunByID reads bounded run lifecycle metadata only.
func (service *Service) GetRunByID(ctx context.Context, id domain.AgentRunID) (domain.AgentRun, error) {
	if !id.Valid() {
		return domain.AgentRun{}, ErrInvalidRepositoryRequest
	}
	return service.runs.GetByID(ctx, id)
}

// FinishRun transitions a running run to one terminal state without a Message.
func (service *Service) FinishRun(ctx context.Context, run domain.AgentRun) error {
	if run.Validate() != nil || !run.Status.Terminal() {
		return ErrInvalidRepositoryRequest
	}
	return service.runs.Finish(ctx, run)
}

// FinishRunWithMessage atomically stores a final assistant Message and terminal run.
func (service *Service) FinishRunWithMessage(ctx context.Context, message domain.Message, run domain.AgentRun) error {
	if message.Validate() != nil || run.Validate() != nil || !run.Status.Terminal() {
		return ErrInvalidRepositoryRequest
	}
	return service.runs.FinishWithMessage(ctx, message, run)
}

// RecoverInterruptedRuns makes durable running records terminal without replay.
func (service *Service) RecoverInterruptedRuns(ctx context.Context, recoveredAt time.Time) (RecoveryResult, error) {
	if recoveredAt.IsZero() || recoveredAt.UnixMilli() < 0 {
		return RecoveryResult{}, ErrInvalidRepositoryRequest
	}
	return service.recovery.RecoverInterrupted(ctx, recoveredAt)
}

// ListResumable returns one bounded global picker page.
func (service *Service) ListResumable(ctx context.Context, request ResumePageRequest) (ResumePage, error) {
	if request.Validate() != nil {
		return ResumePage{}, ErrInvalidRepositoryRequest
	}
	return service.resume.ListResumable(ctx, request)
}

// GetLatestResumable returns the first item under the global picker ordering.
func (service *Service) GetLatestResumable(ctx context.Context) (ResumeCandidate, error) {
	return service.resume.GetLatestResumable(ctx)
}

// ResumeByID reconstructs only bounded committed history and historic candidates.
func (service *Service) ResumeByID(ctx context.Context, id domain.SessionID) (History, error) {
	if !id.Valid() {
		return History{}, ErrSessionUnavailable
	}
	value, err := service.sessions.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return History{}, ErrSessionUnavailable
		}
		return History{}, err
	}
	if value.PrivacyMode == domain.PrivacyModeMinimal {
		return History{}, ErrSessionNotResumable
	}
	if !value.HasResumableMetadata() {
		return History{}, ErrSessionUnavailable
	}
	page, err := service.messages.ListCommittedBySession(ctx, MessagePageRequest{
		SessionID: value.ID,
		Limit:     MaxMessagePageSize,
	})
	if err != nil {
		return History{}, err
	}
	if len(page.Messages) == 0 {
		return History{}, ErrSessionUnavailable
	}
	return History{Session: value, Messages: page.Messages, Next: page.Next}, nil
}
