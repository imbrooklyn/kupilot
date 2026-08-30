package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestMessageRepositoryCRUDNullableAndStablePaging(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "message-contract")
	sessions := NewSessionRepository(db)
	repository := NewMessageRepository(db)
	base := time.UnixMilli(30_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000101", "Message history", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create(Session) error = %v", err)
	}

	messages := []domain.Message{
		testMessage("00000000-0000-7000-8000-000000000111", sessionValue.ID, nil, "First safe message", base.Add(time.Millisecond)),
		testMessage("00000000-0000-7000-8000-000000000112", sessionValue.ID, nil, "Tie low", base.Add(2*time.Millisecond)),
		testMessage("00000000-0000-7000-8000-000000000113", sessionValue.ID, nil, "Tie high", base.Add(2*time.Millisecond)),
		testMessage("00000000-0000-7000-8000-000000000114", sessionValue.ID, nil, "Last safe message", base.Add(3*time.Millisecond)),
	}
	messages[3].Role = domain.MessageRoleSystemNotice
	messages[3].Format = domain.MessageFormatMarkdown
	messages[3].Scope = &domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7}
	messages[3].Resource = &domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod"}
	for index, message := range messages {
		if err := repository.Append(context.Background(), message); err != nil {
			t.Fatalf("Append(%d) error = %v", index, err)
		}
	}
	redacted := testMessage("00000000-0000-7000-8000-000000000115", sessionValue.ID, nil, "Redacted safe placeholder", base.Add(4*time.Millisecond))
	redacted.Status = domain.MessageStatusRedacted
	if err := repository.Append(context.Background(), redacted); err != nil {
		t.Fatalf("Append(redacted) error = %v", err)
	}

	got, err := repository.GetByID(context.Background(), messages[3].ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !reflect.DeepEqual(got, messages[3]) {
		t.Fatalf("GetByID() = %#v, want %#v", got, messages[3])
	}

	first, err := repository.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{
		SessionID: sessionValue.ID,
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("ListCommittedBySession(first) error = %v", err)
	}
	if got := messageIDs(first.Messages); !reflect.DeepEqual(got, []domain.MessageID{messages[0].ID, messages[1].ID}) {
		t.Fatalf("first Message IDs = %v", got)
	}
	if first.Next == nil {
		t.Fatal("first Message page Next = nil")
	}
	second, err := repository.ListCommittedBySession(context.Background(), sessioncontract.MessagePageRequest{
		SessionID: sessionValue.ID,
		Limit:     2,
		After:     first.Next,
	})
	if err != nil {
		t.Fatalf("ListCommittedBySession(second) error = %v", err)
	}
	if got := messageIDs(second.Messages); !reflect.DeepEqual(got, []domain.MessageID{messages[2].ID, messages[3].ID}) {
		t.Fatalf("second Message IDs = %v", got)
	}
	if second.Next != nil {
		t.Fatalf("second Message page Next = %#v", second.Next)
	}

	missingID := domain.MessageID("00000000-0000-7000-8000-000000000199")
	if _, err := repository.GetByID(context.Background(), missingID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
		t.Fatalf("GetByID(missing) error = %v, want ErrMessageNotFound", err)
	}
}

func TestMessageRepositoryRejectsMinimalPersistenceAndBindsExternalText(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "message-privacy")
	sessions := NewSessionRepository(db)
	repository := NewMessageRepository(db)
	base := time.UnixMilli(40_000).UTC()
	minimal := testSession("00000000-0000-7000-8000-000000000201", "", domain.PrivacyModeMinimal, base)
	standard := testSession("00000000-0000-7000-8000-000000000202", "Bound parameters", domain.PrivacyModeStandard, base)
	for _, value := range []domain.Session{minimal, standard} {
		if err := sessions.Create(context.Background(), value); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	minimalMessage := testMessage("00000000-0000-7000-8000-000000000211", minimal.ID, nil, "Must remain in memory", base)
	if err := repository.Append(context.Background(), minimalMessage); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("Append(minimal) error = %v, want ErrDurableContentDisabled", err)
	}
	if _, err := repository.GetByID(context.Background(), minimalMessage.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
		t.Fatalf("minimal Message was persisted: %v", err)
	}

	externalText := string(rune(0x03bb)) + "'); DELETE FROM sessions; --"
	boundMessage := testMessage("00000000-0000-7000-8000-000000000212", standard.ID, nil, externalText, base.Add(time.Millisecond))
	if err := repository.Append(context.Background(), boundMessage); err != nil {
		t.Fatalf("Append(bound text) error = %v", err)
	}
	got, err := repository.GetByID(context.Background(), boundMessage.ID)
	if err != nil {
		t.Fatalf("GetByID(bound text) error = %v", err)
	}
	if got.Content != externalText {
		t.Fatalf("bound content = %q, want exact external data", got.Content)
	}
	var sessionCount int
	if err := db.handle.GetContext(context.Background(), &sessionCount, `SELECT count(id) FROM sessions`); err != nil {
		t.Fatalf("Session count query error = %v", err)
	}
	if sessionCount != 2 {
		t.Fatalf("Session count = %d, want 2", sessionCount)
	}
}

func TestMessageRepositoryUsesRoleSpecificContentLimits(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "message-role-limits")
	sessions := NewSessionRepository(db)
	repository := NewMessageRepository(db)
	base := time.UnixMilli(45_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000251", "Role limits", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create(Session) error = %v", err)
	}

	assistant := testMessage(
		"00000000-0000-7000-8000-000000000252",
		sessionValue.ID,
		nil,
		strings.Repeat("a", 128*1024),
		base.Add(time.Millisecond),
	)
	assistant.Role = domain.MessageRoleAssistant
	assistant.Format = domain.MessageFormatMarkdown
	if err := repository.Append(context.Background(), assistant); err != nil {
		t.Fatalf("Append(assistant at limit) error = %v", err)
	}
	stored, err := repository.GetByID(context.Background(), assistant.ID)
	if err != nil || stored.Content != assistant.Content {
		t.Fatalf("GetByID(assistant) bytes/error = %d/%v", len(stored.Content), err)
	}

	user := testMessage(
		"00000000-0000-7000-8000-000000000253",
		sessionValue.ID,
		nil,
		strings.Repeat("u", 64*1024+1),
		base.Add(2*time.Millisecond),
	)
	if err := repository.Append(context.Background(), user); !errors.Is(err, sessioncontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("Append(oversized user) error = %v, want ErrInvalidRepositoryRequest", err)
	}
	var messageCount int
	if err := db.handle.GetContext(context.Background(), &messageCount, `SELECT count(id) FROM messages`); err != nil || messageCount != 1 {
		t.Fatalf("stored Message count/error = %d/%v, want 1/nil", messageCount, err)
	}
}

func TestMessageRepositoryRejectsCorruptNullableRowWithoutDisclosure(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "message-row-invalid")
	sessions := NewSessionRepository(db)
	repository := NewMessageRepository(db)
	base := time.UnixMilli(50_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000301", "Strict mapping", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	content := "Scope canary"
	messageID := domain.MessageID("00000000-0000-7000-8000-000000000302")
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO messages (
			id, session_id, role, content, content_format, status,
			scope_context, content_hash, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, messageID, sessionValue.ID, "user", content, "plain", "committed", "context-only-canary", domain.MessageContentHash(content), base.UnixMilli()); err != nil {
		t.Fatalf("corrupt Message setup error = %v", err)
	}
	_, err := repository.GetByID(context.Background(), messageID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "message_row_invalid")
	if strings.Contains(err.Error(), "context-only-canary") {
		t.Fatal("Message row error disclosed stored content")
	}
}

func TestMessageRepositoryHonorsCancelledContext(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "message-cancel")
	sessions := NewSessionRepository(db)
	repository := NewMessageRepository(db)
	base := time.UnixMilli(60_000).UTC()
	sessionValue := testSession("00000000-0000-7000-8000-000000000401", "Cancellation", domain.PrivacyModeStandard, base)
	if err := sessions.Create(context.Background(), sessionValue); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	message := testMessage("00000000-0000-7000-8000-000000000402", sessionValue.ID, nil, "Cancelled append", base)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := repository.Append(ctx, message)
	assertStorageError(t, err, ClassCancelled, "storage_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Append() error = %v, want context.Canceled semantics", err)
	}
	if _, err := repository.GetByID(context.Background(), message.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
		t.Fatalf("cancelled Message was persisted: %v", err)
	}
}

func TestMessageRepositoryAppendRollsBackAndHidesBoundValues(t *testing.T) {
	t.Run("touch failure rolls back insert", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "message-rollback")
		sessions := NewSessionRepository(db)
		repository := NewMessageRepository(db)
		base := time.UnixMilli(65_000).UTC()
		sessionValue := testSession("00000000-0000-7000-8000-000000000451", "Rollback", domain.PrivacyModeStandard, base)
		if err := sessions.Create(context.Background(), sessionValue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if _, err := db.handle.ExecContext(context.Background(), `
			CREATE TRIGGER fail_session_touch
			BEFORE UPDATE OF updated_at_ms ON sessions
			BEGIN
				SELECT RAISE(ABORT, 'synthetic append rollback canary');
			END
		`); err != nil {
			t.Fatalf("append rollback trigger setup error = %v", err)
		}
		message := testMessage("00000000-0000-7000-8000-000000000452", sessionValue.ID, nil, "Rollback-safe content", base.Add(time.Millisecond))
		err := repository.Append(context.Background(), message)
		assertStorageError(t, err, ClassPersistenceUnavailable, "message_append_failed")
		if strings.Contains(err.Error(), "synthetic append rollback canary") {
			t.Fatal("Append() error disclosed driver text")
		}
		if _, err := repository.GetByID(context.Background(), message.ID); !errors.Is(err, sessioncontract.ErrMessageNotFound) {
			t.Fatalf("failed Append left Message: %v", err)
		}
	})

	t.Run("duplicate error hides bound content", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "message-bound-error")
		sessions := NewSessionRepository(db)
		repository := NewMessageRepository(db)
		base := time.UnixMilli(66_000).UTC()
		sessionValue := testSession("00000000-0000-7000-8000-000000000461", "Bound error", domain.PrivacyModeStandard, base)
		if err := sessions.Create(context.Background(), sessionValue); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		first := testMessage("00000000-0000-7000-8000-000000000462", sessionValue.ID, nil, "Original safe content", base)
		if err := repository.Append(context.Background(), first); err != nil {
			t.Fatalf("Append(first) error = %v", err)
		}
		boundCanary := "bound-value-disclosure-canary"
		duplicate := testMessage(first.ID, sessionValue.ID, nil, boundCanary, base.Add(time.Millisecond))
		err := repository.Append(context.Background(), duplicate)
		assertStorageError(t, err, ClassPersistenceUnavailable, "message_append_failed")
		if strings.Contains(err.Error(), boundCanary) {
			t.Fatal("Append() error disclosed a bound value")
		}
	})
}

func testMessage(id domain.MessageID, sessionID domain.SessionID, runID *domain.AgentRunID, content string, createdAt time.Time) domain.Message {
	return domain.Message{
		ID:        id,
		SessionID: sessionID,
		RunID:     runID,
		Role:      domain.MessageRoleUser,
		Content:   content,
		Format:    domain.MessageFormatPlain,
		Status:    domain.MessageStatusCommitted,
		Hash:      domain.MessageContentHash(content),
		CreatedAt: createdAt,
	}
}

func messageIDs(values []domain.Message) []domain.MessageID {
	result := make([]domain.MessageID, 0, len(values))
	for _, value := range values {
		result = append(result, value.ID)
	}
	return result
}
