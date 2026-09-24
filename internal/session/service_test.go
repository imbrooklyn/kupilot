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
		ID:             "00000000-0000-7000-8000-000000000001",
		Status:         domain.SessionStatusActive,
		PrivacyMode:    domain.PrivacyModeMinimal,
		Version:        1,
		CreatedAt:      now,
		LastActivityAt: now,
		UpdatedAt:      now,
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
		ID:             "00000000-0000-7000-8000-000000000011",
		Title:          "Resumable",
		Status:         domain.SessionStatusActive,
		PrivacyMode:    domain.PrivacyModeStandard,
		Version:        1,
		CreatedAt:      now,
		LastActivityAt: now,
		UpdatedAt:      now,
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

func TestResumeDerivesOptionalDurationOnlyFromCompletedOwnedRuns(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	finished := now.Add(38 * time.Second)
	id := domain.SessionID("00000000-0000-7000-8000-000000000011")
	run := domain.AgentRun{
		ID: "00000000-0000-7000-8000-000000000012", SessionID: id,
		RequestMessageID: "00000000-0000-7000-8000-000000000013",
		Status:           domain.AgentRunStatusCompleted, StartedAt: &now, FinishedAt: &finished,
		Scope:         domain.ScopeSnapshot{Context: "example-context", Namespace: "example-namespace", Generation: 1},
		PromptVersion: "1", ToolCatalogVersion: "1",
	}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*domain.AgentRun)
		err    error
		want   bool
	}{
		{name: "completed", want: true},
		{name: "missing", err: ErrAgentRunNotFound},
		{name: "unavailable optional metadata", err: errors.New("synthetic read failure")},
		{name: "foreign Session", mutate: func(run *domain.AgentRun) { run.SessionID = "00000000-0000-7000-8000-000000000021" }},
		{name: "foreign run", mutate: func(run *domain.AgentRun) { run.ID = "00000000-0000-7000-8000-000000000022" }},
		{name: "running", mutate: func(run *domain.AgentRun) { run.Status, run.FinishedAt = domain.AgentRunStatusRunning, nil }},
		{name: "recovered after restart", mutate: func(run *domain.AgentRun) { run.Status = domain.AgentRunStatusInterrupted }},
		{name: "missing start", mutate: func(run *domain.AgentRun) { run.StartedAt = nil }},
		{name: "negative duration", mutate: func(run *domain.AgentRun) { run.StartedAt, run.FinishedAt = &finished, &now }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := run
			if test.mutate != nil {
				test.mutate(&value)
			}
			sessions := &fakeSessionStore{session: domain.Session{
				ID: id, Title: "Example Session", Status: domain.SessionStatusActive,
				PrivacyMode: domain.PrivacyModeStandard, Version: 1,
				CreatedAt: now, LastActivityAt: now, UpdatedAt: now,
			}}
			messages := &fakeMessageStore{page: MessagePage{Messages: []domain.Message{
				{Role: domain.MessageRoleUser, RunID: &run.ID},
				{Role: domain.MessageRoleAssistant, RunID: &run.ID},
				{Role: domain.MessageRoleAssistant},
			}}}
			reads := 0
			runs := resumeRunStore{read: func(context.Context, domain.AgentRunID) (domain.AgentRun, error) {
				reads++
				return value, test.err
			}}
			service := NewService(sessions, fakeResumeReader{}, messages, runs, fakeRecovery{})
			history, err := service.ResumeByID(t.Context(), id)
			if err != nil || len(history.Messages) != 3 || reads != 1 {
				t.Fatalf("history=%d reads=%d error=%v", len(history.Messages), reads, err)
			}
			duration, found := history.RunDurations[run.ID]
			if found != test.want || found && duration != 38*time.Second {
				t.Fatalf("duration=%s found=%t, want found=%t", duration, found, test.want)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runs.read = func(context.Context, domain.AgentRunID) (domain.AgentRun, error) {
				cancel()
				return run, nil
			}
			service.runs = runs
			if history, err := service.ResumeByID(ctx, id); !errors.Is(err, context.Canceled) || len(history.Messages) != 0 {
				t.Fatalf("cancelled timing read restored history: %v", err)
			}
			expired, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))
			defer stop()
			if history, err := service.ResumeByID(expired, id); !errors.Is(err, context.DeadlineExceeded) || len(history.Messages) != 0 {
				t.Fatalf("expired resume restored history: %v", err)
			}
		})
	}
}

type resumeRunStore struct {
	fakeRunStore
	read func(context.Context, domain.AgentRunID) (domain.AgentRun, error)
}

func (store resumeRunStore) GetByID(ctx context.Context, id domain.AgentRunID) (domain.AgentRun, error) {
	return store.read(ctx, id)
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
