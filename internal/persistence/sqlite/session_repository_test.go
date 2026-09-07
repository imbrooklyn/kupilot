package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestSessionRepositoryCRUDAndStrictNullableMapping(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-crud")
	repository := NewSessionRepository(db)
	createdAt := time.UnixMilli(1_000).UTC()

	minimal := domain.Session{
		ID:             domain.SessionID("00000000-0000-7000-8000-000000000001"),
		Status:         domain.SessionStatusActive,
		PrivacyMode:    domain.PrivacyModeMinimal,
		Version:        1,
		CreatedAt:      createdAt,
		LastActivityAt: createdAt,
		UpdatedAt:      createdAt,
	}
	if err := repository.Create(context.Background(), minimal); err != nil {
		t.Fatalf("Create(minimal) error = %v", err)
	}
	gotMinimal, err := repository.GetByID(context.Background(), minimal.ID)
	if err != nil {
		t.Fatalf("GetByID(minimal) error = %v", err)
	}
	if !reflect.DeepEqual(gotMinimal, minimal) {
		t.Fatalf("GetByID(minimal) = %#v, want %#v", gotMinimal, minimal)
	}

	summary := "Bounded safe summary"
	standard := domain.Session{
		ID:          domain.SessionID("00000000-0000-7000-8000-000000000002"),
		Title:       "Investigate unavailable workload",
		Status:      domain.SessionStatusActive,
		PrivacyMode: domain.PrivacyModeStandard,
		LastScope: &domain.ScopeCandidate{
			Context:   "test-context",
			Namespace: "test-namespace",
		},
		SelectedResource: &domain.ResourceRef{
			APIVersion:      "apps/v1",
			Kind:            "Deployment",
			Namespace:       "test-namespace",
			Name:            "sample-workload",
			UID:             "synthetic-uid",
			ResourceVersion: "17",
		},
		Summary:        &summary,
		Version:        1,
		CreatedAt:      createdAt,
		LastActivityAt: time.UnixMilli(2_000).UTC(),
		UpdatedAt:      time.UnixMilli(2_000).UTC(),
	}
	if err := repository.Create(context.Background(), standard); err != nil {
		t.Fatalf("Create(standard) error = %v", err)
	}
	gotStandard, err := repository.GetByID(context.Background(), standard.ID)
	if err != nil {
		t.Fatalf("GetByID(standard) error = %v", err)
	}
	if !reflect.DeepEqual(gotStandard, standard) {
		t.Fatalf("GetByID(standard) = %#v, want %#v", gotStandard, standard)
	}

	rename := sessioncontract.RenameSession{
		ID:              standard.ID,
		Title:           "Renamed safe title",
		ExpectedVersion: 1,
		UpdatedAt:       time.UnixMilli(3_000).UTC(),
	}
	if err := repository.Rename(context.Background(), rename); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	renamed, err := repository.GetByID(context.Background(), standard.ID)
	if err != nil {
		t.Fatalf("GetByID(renamed) error = %v", err)
	}
	if renamed.Title != rename.Title || renamed.Version != 2 || !renamed.UpdatedAt.Equal(rename.UpdatedAt) ||
		!renamed.LastActivityAt.Equal(rename.UpdatedAt) {
		t.Fatalf("renamed Session = %#v", renamed)
	}
	if err := repository.Rename(context.Background(), rename); !errors.Is(err, sessioncontract.ErrSessionConflict) {
		t.Fatalf("stale Rename() error = %v, want ErrSessionConflict", err)
	}
	if err := repository.Rename(context.Background(), sessioncontract.RenameSession{
		ID:              minimal.ID,
		Title:           "Must not persist",
		ExpectedVersion: 1,
		UpdatedAt:       time.UnixMilli(3_000).UTC(),
	}); !errors.Is(err, sessioncontract.ErrDurableContentDisabled) {
		t.Fatalf("minimal Rename() error = %v, want ErrDurableContentDisabled", err)
	}

	missingID := domain.SessionID("00000000-0000-7000-8000-000000000099")
	if _, err := repository.GetByID(context.Background(), missingID); !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("GetByID(missing) error = %v, want ErrSessionNotFound", err)
	}

	corruptID := domain.SessionID("00000000-0000-7000-8000-000000000003")
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO sessions (
			id, title, status, privacy_mode, last_context,
			version, created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, corruptID, "Corrupt scope candidate", "active", "standard", "context-only", 1, 1, 1); err != nil {
		t.Fatalf("corrupt Session setup error = %v", err)
	}
	_, err = repository.GetByID(context.Background(), corruptID)
	assertStorageError(t, err, ClassPersistenceUnavailable, "session_row_invalid")
	if strings.Contains(err.Error(), "context-only") {
		t.Fatal("strict row mapping error disclosed a stored value")
	}
}

func TestSessionRepositoryResumableQueriesUseStableGlobalPaging(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-resume-page")
	repository := NewSessionRepository(db)
	messageRepository := NewMessageRepository(db)
	base := time.UnixMilli(10_000).UTC()

	eligible := []domain.Session{
		testSession("00000000-0000-7000-8000-000000000011", "Oldest", domain.PrivacyModeStandard, base.Add(time.Millisecond)),
		testSession("00000000-0000-7000-8000-000000000012", "Tie low", domain.PrivacyModeStandard, base.Add(2*time.Millisecond)),
		testSession("00000000-0000-7000-8000-000000000013", "Tie high", domain.PrivacyModeStandard, base.Add(2*time.Millisecond)),
		testSession("00000000-0000-7000-8000-000000000014", "Newest", domain.PrivacyModeStandard, base.Add(3*time.Millisecond)),
	}
	for index, value := range eligible {
		if err := repository.Create(context.Background(), value); err != nil {
			t.Fatalf("Create(eligible %d) error = %v", index, err)
		}
		message := testMessage(
			domain.MessageID("00000000-0000-7000-8000-00000000001"+string(rune('1'+index))),
			value.ID,
			nil,
			"Safe resumable content",
			value.UpdatedAt,
		)
		if err := messageRepository.Append(context.Background(), message); err != nil {
			t.Fatalf("Append(eligible %d) error = %v", index, err)
		}
	}

	minimal := testSession("00000000-0000-7000-8000-000000000021", "", domain.PrivacyModeMinimal, base.Add(5*time.Millisecond))
	empty := testSession("00000000-0000-7000-8000-000000000022", "No history", domain.PrivacyModeStandard, base.Add(6*time.Millisecond))
	archived := testSession("00000000-0000-7000-8000-000000000023", "Archived", domain.PrivacyModeStandard, base.Add(7*time.Millisecond))
	archived.Status = domain.SessionStatusArchived
	for _, value := range []domain.Session{minimal, empty, archived} {
		if err := repository.Create(context.Background(), value); err != nil {
			t.Fatalf("Create(ineligible) error = %v", err)
		}
	}
	seedCommittedMessage(t, db, archived.ID, "00000000-0000-7000-8000-000000000131", archived.UpdatedAt)

	first, err := repository.ListResumable(context.Background(), sessioncontract.ResumePageRequest{Limit: 2})
	if err != nil {
		t.Fatalf("ListResumable(first) error = %v", err)
	}
	wantFirst := []domain.SessionID{eligible[3].ID, eligible[2].ID}
	if got := candidateIDs(first.Sessions); !reflect.DeepEqual(got, wantFirst) {
		t.Fatalf("first candidate IDs = %v, want %v", got, wantFirst)
	}
	if first.Next == nil {
		t.Fatal("first page Next = nil")
	}

	second, err := repository.ListResumable(context.Background(), sessioncontract.ResumePageRequest{
		Limit:  2,
		Before: first.Next,
	})
	if err != nil {
		t.Fatalf("ListResumable(second) error = %v", err)
	}
	wantSecond := []domain.SessionID{eligible[1].ID, eligible[0].ID}
	if got := candidateIDs(second.Sessions); !reflect.DeepEqual(got, wantSecond) {
		t.Fatalf("second candidate IDs = %v, want %v", got, wantSecond)
	}
	if second.Next != nil {
		t.Fatalf("second page Next = %#v, want nil", second.Next)
	}

	latest, err := repository.GetLatestResumable(context.Background())
	if err != nil {
		t.Fatalf("GetLatestResumable() error = %v", err)
	}
	if latest.ID != eligible[3].ID {
		t.Fatalf("latest ID = %q, want %q", latest.ID, eligible[3].ID)
	}
	if latest.LastScope == nil || latest.LastScope.Context != "test-context" || latest.LastScope.Namespace != "test-namespace" {
		t.Fatalf("latest LastScope = %#v", latest.LastScope)
	}
}

func TestSessionRepositoryResumableQueriesReturnEmptyResults(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-resume-empty")
	repository := NewSessionRepository(db)
	page, err := repository.ListResumable(context.Background(), sessioncontract.ResumePageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("ListResumable() error = %v", err)
	}
	if len(page.Sessions) != 0 || page.Next != nil {
		t.Fatalf("empty page = %#v", page)
	}
	if _, err := repository.GetLatestResumable(context.Background()); !errors.Is(err, sessioncontract.ErrNoResumableSession) {
		t.Fatalf("GetLatestResumable() error = %v, want ErrNoResumableSession", err)
	}
}

func TestSessionRepositorySearchesSafeDisplayMetadataBeforeApplyingBound(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-search")
	repository := NewSessionRepository(db)
	base := time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)

	for index := 0; index < 55; index++ {
		id := fmt.Sprintf("00000000-0000-7000-8000-%012d", 1_000+index)
		value := testSession(id, fmt.Sprintf("Recent Session %02d", index), domain.PrivacyModeStandard, base.Add(time.Duration(index+10)*time.Minute))
		if err := repository.Create(context.Background(), value); err != nil {
			t.Fatalf("Create(recent %d) error = %v", index, err)
		}
		seedCommittedMessage(t, db, value.ID, fmt.Sprintf("00000000-0000-7000-8001-%012d", 1_000+index), value.UpdatedAt)
	}

	exact := testSession("00000000-0000-7000-8000-000000002001", "payment", domain.PrivacyModeStandard, base)
	prefix := testSession("00000000-0000-7000-8000-000000002002", "Payment incident", domain.PrivacyModeStandard, base.Add(time.Minute))
	substring := testSession("00000000-0000-7000-8000-000000002003", "Historic payment review", domain.PrivacyModeStandard, base.Add(2*time.Minute))
	scope := testSession("00000000-0000-7000-8000-000000002004", "Historic scope", domain.PrivacyModeStandard, base.Add(3*time.Minute))
	scope.LastScope = &domain.ScopeCandidate{Context: "payment-control", Namespace: "payments"}
	for index, value := range []domain.Session{exact, prefix, substring, scope} {
		if err := repository.Create(context.Background(), value); err != nil {
			t.Fatalf("Create(ranked %d) error = %v", index, err)
		}
		seedCommittedMessage(t, db, value.ID, fmt.Sprintf("00000000-0000-7000-8001-00000000200%d", index+1), value.UpdatedAt)
	}

	results, err := repository.SearchResumable(context.Background(), application.SessionSearchRequest{Filter: "payment", Limit: 4})
	if err != nil {
		t.Fatalf("SearchResumable(payment) error = %v", err)
	}
	want := []domain.SessionID{exact.ID, prefix.ID, substring.ID, scope.ID}
	got := make([]domain.SessionID, len(results))
	for index := range results {
		got[index] = results[index].ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked IDs = %v, want %v", got, want)
	}

	scopeResults, err := repository.SearchResumable(context.Background(), application.SessionSearchRequest{Filter: "ctx/payment-control", Limit: 1})
	if err != nil || len(scopeResults) != 1 || scopeResults[0].ID != scope.ID {
		t.Fatalf("scope search = %#v, error = %v", scopeResults, err)
	}
	timeResults, err := repository.SearchResumable(context.Background(), application.SessionSearchRequest{Filter: "2026-08-14 09:30Z", Limit: 1})
	if err != nil || len(timeResults) != 1 || timeResults[0].ID != exact.ID {
		t.Fatalf("timestamp search = %#v, error = %v", timeResults, err)
	}
	if _, err := repository.SearchResumable(context.Background(), application.SessionSearchRequest{Filter: "payment", Limit: application.MaxUIQueryCandidates + 1}); !errors.Is(err, application.ErrInvalidSessionSearch) {
		t.Fatalf("oversized SearchResumable() error = %v, want ErrInvalidSessionSearch", err)
	}
}

func TestResumeByIDLoadsOnlyEligibleSafeHistory(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-resume-exact")
	sessions := NewSessionRepository(db)
	messages := NewMessageRepository(db)
	runs := NewAgentRunRepository(db)
	service := sessioncontract.NewService(sessions, sessions, messages, runs, runs)
	base := time.UnixMilli(20_000).UTC()

	standard := testSession("00000000-0000-7000-8000-000000000031", "Resumable", domain.PrivacyModeStandard, base)
	minimal := testSession("00000000-0000-7000-8000-000000000032", "", domain.PrivacyModeMinimal, base.Add(time.Millisecond))
	empty := testSession("00000000-0000-7000-8000-000000000035", "Empty", domain.PrivacyModeStandard, base.Add(2*time.Millisecond))
	archived := testSession("00000000-0000-7000-8000-000000000036", "Archived", domain.PrivacyModeStandard, base.Add(3*time.Millisecond))
	archived.Status = domain.SessionStatusArchived
	for _, value := range []domain.Session{standard, minimal, empty, archived} {
		if err := sessions.Create(context.Background(), value); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	message := testMessage("00000000-0000-7000-8000-000000000033", standard.ID, nil, "Persisted safe history", base)
	if err := messages.Append(context.Background(), message); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	redacted := testMessage("00000000-0000-7000-8000-000000000034", standard.ID, nil, "Redacted placeholder", base.Add(time.Millisecond))
	redacted.Status = domain.MessageStatusRedacted
	if err := messages.Append(context.Background(), redacted); err != nil {
		t.Fatalf("Append(redacted) error = %v", err)
	}
	seedCommittedMessage(t, db, archived.ID, "00000000-0000-7000-8000-000000000037", archived.UpdatedAt)

	history, err := service.ResumeByID(context.Background(), standard.ID)
	if err != nil {
		t.Fatalf("ResumeByID(standard) error = %v", err)
	}
	if history.Session.ID != standard.ID || len(history.Messages) != 1 || history.Messages[0].ID != message.ID {
		t.Fatalf("ResumeByID(standard) = %#v", history)
	}
	if _, err := service.ResumeByID(context.Background(), minimal.ID); !errors.Is(err, sessioncontract.ErrSessionNotResumable) {
		t.Fatalf("ResumeByID(minimal) error = %v, want ErrSessionNotResumable", err)
	}
	if _, err := service.ResumeByID(context.Background(), empty.ID); !errors.Is(err, sessioncontract.ErrSessionUnavailable) {
		t.Fatalf("ResumeByID(empty) error = %v, want ErrSessionUnavailable", err)
	}
	if _, err := service.ResumeByID(context.Background(), archived.ID); !errors.Is(err, sessioncontract.ErrSessionUnavailable) {
		t.Fatalf("ResumeByID(archived) error = %v, want ErrSessionUnavailable", err)
	}
	if _, err := service.ResumeByID(context.Background(), "00000000-0000-7000-8000-000000000099"); !errors.Is(err, sessioncontract.ErrSessionUnavailable) {
		t.Fatalf("ResumeByID(missing) error = %v, want ErrSessionUnavailable", err)
	}
}

func TestSessionRepositoryDeleteCascadesCompleteGraphAndRollsBack(t *testing.T) {
	t.Run("complete cascade", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "session-delete-cascade")
		repository := NewSessionRepository(db)
		sessionID := seedCompleteSessionGraph(t, db, "00000000-0000-7000-8000-000000000041")

		if err := repository.Delete(context.Background(), sessionID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		assertSessionGraphRowCount(t, db, 0)
	})

	t.Run("rollback", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "session-delete-rollback")
		repository := NewSessionRepository(db)
		sessionID := seedCompleteSessionGraph(t, db, "00000000-0000-7000-8000-000000000051")
		if _, err := db.handle.ExecContext(context.Background(), `
			CREATE TRIGGER prevent_agent_run_delete
			BEFORE DELETE ON agent_runs
			BEGIN
				SELECT RAISE(ABORT, 'synthetic delete rollback canary');
			END
		`); err != nil {
			t.Fatalf("rollback trigger setup error = %v", err)
		}

		err := repository.Delete(context.Background(), sessionID)
		assertStorageError(t, err, ClassPersistenceUnavailable, "session_delete_failed")
		if strings.Contains(err.Error(), "synthetic delete rollback canary") {
			t.Fatal("Delete() error disclosed driver text")
		}
		assertSessionGraphRowCount(t, db, 11)
	})

	t.Run("serialized with retention cleanup", func(t *testing.T) {
		db := openTestDB(t, context.Background(), testStateDir(t), "session-delete-retention")
		repository := NewSessionRepository(db)
		sessionID := seedCompleteSessionGraph(t, db, "00000000-0000-7000-8000-000000000061")
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- repository.Delete(context.Background(), sessionID)
		}()
		go func() {
			<-start
			_, err := NewRetentionRepository(db).Cleanup(context.Background(), auditcontract.CleanupRequest{
				Now:                            time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC),
				OperationalDetailRetentionDays: application.DefaultOperationalDetailRetentionDays,
				BatchSize:                      100,
			})
			results <- err
		}()
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("concurrent deletion/retention error = %v", err)
			}
		}
		assertSessionGraphRowCount(t, db, 0)
	})
}

func TestSessionRepositoryHonorsCancelledContext(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-cancel")
	repository := NewSessionRepository(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	value := testSession("00000000-0000-7000-8000-000000000061", "Cancelled", domain.PrivacyModeStandard, time.UnixMilli(1).UTC())
	err := repository.Create(ctx, value)
	assertStorageError(t, err, ClassCancelled, "storage_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context.Canceled semantics", err)
	}
	if _, err := repository.GetByID(context.Background(), value.ID); !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("GetByID() after cancelled Create error = %v", err)
	}
}

func TestSessionRepositoryHonorsExpiredDeadline(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-timeout")
	repository := NewSessionRepository(db)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	_, err := repository.ListResumable(ctx, sessioncontract.ResumePageRequest{Limit: 1})
	assertStorageError(t, err, ClassTimeout, "storage_timeout")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ListResumable() error = %v, want context.DeadlineExceeded semantics", err)
	}
}

func testSession(id, title string, mode domain.PrivacyMode, updatedAt time.Time) domain.Session {
	value := domain.Session{
		ID:             domain.SessionID(id),
		Title:          title,
		Status:         domain.SessionStatusActive,
		PrivacyMode:    mode,
		Version:        1,
		CreatedAt:      updatedAt,
		LastActivityAt: updatedAt,
		UpdatedAt:      updatedAt,
	}
	if mode == domain.PrivacyModeStandard {
		value.LastScope = &domain.ScopeCandidate{
			Context:   "test-context",
			Namespace: "test-namespace",
		}
	}
	return value
}

func candidateIDs(values []sessioncontract.ResumeCandidate) []domain.SessionID {
	result := make([]domain.SessionID, 0, len(values))
	for _, value := range values {
		result = append(result, value.ID)
	}
	return result
}

func seedCommittedMessage(t *testing.T, db *DB, sessionID domain.SessionID, messageID string, createdAt time.Time) {
	t.Helper()
	content := "Safe committed history"
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO messages (
			id, session_id, run_id, role, content, content_format, status,
			content_hash, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, messageID, sessionID, nil, "user", content, "plain", "committed", domain.MessageContentHash(content), createdAt.UnixMilli()); err != nil {
		t.Fatalf("committed Message setup error = %v", err)
	}
}

func seedCompleteSessionGraph(t *testing.T, db *DB, rawSessionID string) domain.SessionID {
	t.Helper()
	ctx := context.Background()
	sessionID := domain.SessionID(rawSessionID)
	messageID := rawSessionID[:len(rawSessionID)-2] + "52"
	runID := rawSessionID[:len(rawSessionID)-2] + "53"
	modelID := rawSessionID[:len(rawSessionID)-2] + "54"
	toolID := rawSessionID[:len(rawSessionID)-2] + "55"
	evidenceID := rawSessionID[:len(rawSessionID)-2] + "56"
	diagnosisID := rawSessionID[:len(rawSessionID)-2] + "57"
	approvalID := rawSessionID[:len(rawSessionID)-2] + "58"
	auditID := rawSessionID[:len(rawSessionID)-2] + "59"
	actionReviewID := rawSessionID[:len(rawSessionID)-2] + "60"
	content := "Safe graph message"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions (id, title, status, privacy_mode, version, created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`, []any{sessionID, "Cascade graph", "active", "standard", 1, 1, 1}},
		{`INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{messageID, sessionID, "user", content, "plain", "committed", domain.MessageContentHash(content), 1}},
		{`INSERT INTO agent_runs (id, session_id, request_message_id, status, scope_context, scope_namespace, scope_generation, prompt_version, tool_catalog_version, started_at_ms, finished_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, []any{runID, sessionID, messageID, "completed", "test-context", "test-namespace", 1, "prompt-v1", "tools-v1", 1, 2}},
		{`INSERT INTO model_requests (id, run_id, sequence, provider_kind, model, status, prompt_version, prompt_fingerprint, started_at_ms, finished_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, []any{modelID, runID, 1, "openai_compatible", "test-model", "succeeded", "prompt-v1", strings.Repeat("1", 64), 1, 2}},
		{`INSERT INTO tool_invocations (id, run_id, sequence, tool_name, tool_version, arguments_json, arguments_digest, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{toolID, runID, 1, "get_resource", "tools-v1", `{}`, strings.Repeat("2", 64), "succeeded"}},
		{`INSERT INTO evidence_items (id, run_id, invocation_id, category, resource_ref_json, fact, fingerprint, observed_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{evidenceID, runID, toolID, "condition", `{"api_version":"v1","kind":"Pod","namespace":"test-namespace","name":"sample-pod"}`, "Synthetic safe fact", strings.Repeat("3", 64), 1}},
		{`INSERT INTO diagnoses (id, run_id, confirmed_json, hypotheses_json, missing_json, actions_json, answer_markdown, created_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{diagnosisID, runID, `[]`, `[]`, `[]`, `[]`, "Safe answer", 2}},
		{`
			INSERT INTO approvals (
				id, run_id, session_id, envelope_schema_version, digest_version,
				operation, operation_schema_version, policy_version, permission_profile,
				policy_generation, risk, effect, scope_context, scope_namespace,
				namespace_access, scope_generation, target_api_version, target_kind,
				target_namespace, target_name, target_uid, target_resource_version,
				target_subresource, target_fingerprint, target_generation, target_revision,
				target_set_digest, target_count, parameter_kind, parameter_digest,
				stdin, tty, shell, data_categories, allowed_sinks, network_effects,
				network_destination_hash,
				timeout_ms, maximum_items, maximum_lines, maximum_bytes,
				maximum_output, verification_plan_id, reason_summary, risk_summary,
				operation_digest, nonce_hash, status, state_reason,
				requested_at_ms, expires_at_ms, state_changed_at_ms
			) VALUES (
				?, ?, ?, 'kupilot.action-envelope/v1', 'kupilot.action-digest/v1',
				'restart_deployment', 'restart_deployment/v1', 'kupilot.action-policy/2026-09-04', 'ask',
				1, 'review', 'cluster_mutation', 'test-context', 'test-namespace',
				'current', 1, 'apps/v1', 'Deployment',
				'test-namespace', 'sample-workload', 'synthetic-deployment-uid', '17', '',
				?, 1, 0, '', 0, 'none', ?,
				0, 0, 0, 1, 5, 2, '', 120000, 1, 0, 0, 0,
				'restart-rollout/v1', 'Synthetic request', ?,
				?, ?, 'rejected', 'user_rejected', 1001, 61001, 2000
			)
		`, []any{
			approvalID, runID, sessionID, strings.Repeat("5", 64), strings.Repeat("8", 64),
			domain.RestartDeploymentRiskSummary, strings.Repeat("4", 64), strings.Repeat("6", 64),
		}},
		{`
			INSERT INTO approval_decisions (
				approval_id, shown_digest, nonce_hash, decision, actor,
				permission_disposition, decided_at_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, []any{approvalID, strings.Repeat("4", 64), strings.Repeat("6", 64), "reject", "local_user", "human", 1_500}},
		{`
			INSERT INTO action_reviews (
				approval_id, model_request_id, profile_name, origin_hash,
				policy_generation, disposition, rationale_summary, occurred_at_ms
			) VALUES (?, ?, 'approval_reviewer', ?, 1, 'deny', 'Synthetic safe rationale.', 1600)
		`, []any{approvalID, actionReviewID, strings.Repeat("9", 64)}},
		{`INSERT INTO audit_events (id, session_id, run_id, event_type, actor, outcome, details_json, occurred_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, []any{auditID, sessionID, runID, "run_completed", "system", "success", `{}`, 2}},
	}
	for index, statement := range statements {
		if _, err := db.handle.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("graph statement %d error = %v", index, err)
		}
	}
	return sessionID
}

func assertSessionGraphRowCount(t *testing.T, db *DB, want int) {
	t.Helper()
	var count int
	if err := db.handle.GetContext(context.Background(), &count, `
		SELECT
			(SELECT count(id) FROM sessions)
			+ (SELECT count(id) FROM messages)
			+ (SELECT count(id) FROM agent_runs)
			+ (SELECT count(id) FROM model_requests)
			+ (SELECT count(id) FROM tool_invocations)
			+ (SELECT count(id) FROM evidence_items)
			+ (SELECT count(id) FROM diagnoses)
			+ (SELECT count(id) FROM approvals)
			+ (SELECT count(approval_id) FROM approval_decisions)
			+ (SELECT count(model_request_id) FROM action_reviews)
			+ (SELECT count(id) FROM audit_events)
	`); err != nil {
		t.Fatalf("graph count query error = %v", err)
	}
	if count != want {
		t.Fatalf("Session graph row count = %d, want %d", count, want)
	}
}
