package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestResumeByIDRejectsMinimalAndMissingSessionsBeforeHistoryRead(t *testing.T) {
	now := time.UnixMilli(1).UTC()
	minimal := domain.Session{
		ID:          "00000000-0000-7000-8000-000000000001",
		Status:      domain.SessionStatusActive,
		PrivacyMode: domain.PrivacyModeMinimal,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	sessions := &fakeSessionStore{session: minimal}
	messages := &fakeMessageStore{}
	service := NewService(sessions, fakeResumeReader{}, messages, fakeRunStore{}, fakeRecovery{})
	if _, err := service.ResumeByID(context.Background(), minimal.ID); !errors.Is(err, ErrSessionNotResumable) {
		t.Fatalf("ResumeByID(minimal) error = %v, want ErrSessionNotResumable", err)
	}
	if messages.listCalls != 0 {
		t.Fatalf("minimal Message list calls = %d, want 0", messages.listCalls)
	}

	sessions.getErr = ErrSessionNotFound
	if _, err := service.ResumeByID(context.Background(), "00000000-0000-7000-8000-000000000002"); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("ResumeByID(missing) error = %v, want ErrSessionUnavailable", err)
	}
	if messages.listCalls != 0 {
		t.Fatalf("missing Message list calls = %d, want 0", messages.listCalls)
	}

	sessions.getErr = nil
	if _, err := service.ResumeByID(context.Background(), "invalid"); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("ResumeByID(invalid) error = %v, want ErrSessionUnavailable", err)
	}
	if sessions.getCalls != 2 {
		t.Fatalf("invalid ID reached Session store; total calls = %d, want 2", sessions.getCalls)
	}
}

func TestResumeByIDReturnsOnlyBoundedCommittedHistory(t *testing.T) {
	now := time.UnixMilli(2).UTC()
	standard := domain.Session{
		ID:          "00000000-0000-7000-8000-000000000011",
		Title:       "Resumable",
		Status:      domain.SessionStatusActive,
		PrivacyMode: domain.PrivacyModeStandard,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	message := domain.Message{
		ID:        "00000000-0000-7000-8000-000000000012",
		SessionID: standard.ID,
		Role:      domain.MessageRoleUser,
		Content:   "Safe history",
		Format:    domain.MessageFormatPlain,
		Status:    domain.MessageStatusCommitted,
		Hash:      domain.MessageContentHash("Safe history"),
		CreatedAt: now,
	}
	next := &MessageCursor{CreatedAt: now, ID: message.ID}
	sessions := &fakeSessionStore{session: standard}
	messages := &fakeMessageStore{
		page: MessagePage{
			Messages: []domain.Message{message},
			Next:     next,
		},
	}
	service := NewService(sessions, fakeResumeReader{}, messages, fakeRunStore{}, fakeRecovery{})
	history, err := service.ResumeByID(context.Background(), standard.ID)
	if err != nil {
		t.Fatalf("ResumeByID() error = %v", err)
	}
	if history.Session.ID != standard.ID || len(history.Messages) != 1 || history.Messages[0].ID != message.ID || history.Next != next {
		t.Fatalf("ResumeByID() = %#v", history)
	}
	if messages.lastRequest.Limit != MaxMessagePageSize || messages.lastRequest.SessionID != standard.ID {
		t.Fatalf("history request = %#v", messages.lastRequest)
	}

	messages.page = MessagePage{}
	if _, err := service.ResumeByID(context.Background(), standard.ID); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("ResumeByID(empty history) error = %v, want ErrSessionUnavailable", err)
	}
}

type fakeSessionStore struct {
	session  domain.Session
	getErr   error
	getCalls int
}

func (store *fakeSessionStore) Create(context.Context, domain.Session) error {
	return nil
}

func (store *fakeSessionStore) GetByID(_ context.Context, id domain.SessionID) (domain.Session, error) {
	store.getCalls++
	if store.getErr != nil {
		return domain.Session{}, store.getErr
	}
	value := store.session
	value.ID = id
	return value, nil
}

func (store *fakeSessionStore) Rename(context.Context, RenameSession) error {
	return nil
}

func (store *fakeSessionStore) Delete(context.Context, domain.SessionID) error {
	return nil
}

type fakeResumeReader struct{}

func (fakeResumeReader) ListResumable(context.Context, ResumePageRequest) (ResumePage, error) {
	return ResumePage{}, nil
}

func (fakeResumeReader) GetLatestResumable(context.Context) (ResumeCandidate, error) {
	return ResumeCandidate{}, ErrNoResumableSession
}

type fakeMessageStore struct {
	page        MessagePage
	listCalls   int
	lastRequest MessagePageRequest
}

func (store *fakeMessageStore) Append(context.Context, domain.Message) error {
	return nil
}

func (store *fakeMessageStore) GetByID(context.Context, domain.MessageID) (domain.Message, error) {
	return domain.Message{}, ErrMessageNotFound
}

func (store *fakeMessageStore) ListCommittedBySession(_ context.Context, request MessagePageRequest) (MessagePage, error) {
	store.listCalls++
	store.lastRequest = request
	return store.page, nil
}

type fakeRunStore struct{}

func (fakeRunStore) Begin(context.Context, domain.Message, domain.AgentRun) error {
	return nil
}

func (fakeRunStore) GetByID(context.Context, domain.AgentRunID) (domain.AgentRun, error) {
	return domain.AgentRun{}, ErrAgentRunNotFound
}

func (fakeRunStore) Finish(context.Context, domain.AgentRun) error {
	return nil
}

func (fakeRunStore) FinishWithMessage(context.Context, domain.Message, domain.AgentRun) error {
	return nil
}

type fakeRecovery struct{}

func (fakeRecovery) RecoverInterrupted(context.Context, time.Time) (RecoveryResult, error) {
	return RecoveryResult{}, nil
}
