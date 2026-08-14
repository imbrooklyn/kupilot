package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestSessionLifecycleRepositoryUsesAtomicTighteningWithInjectedTime(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-lifecycle-retention")
	repository := NewSessionRepository(db)

	days, found, err := repository.LoadOperationalDetailRetention(context.Background())
	if err != nil || found || days != 0 {
		t.Fatalf("LoadOperationalDetailRetention(default) = %d, %v, %v", days, found, err)
	}

	updatedAt := time.UnixMilli(70_000).UTC()
	update := application.RetentionSettingUpdate{
		ExpectedDays: application.DefaultOperationalDetailRetentionDays,
		Days:         14,
		UpdatedAt:    updatedAt,
	}
	if err := repository.TightenOperationalDetailRetention(context.Background(), update); err != nil {
		t.Fatalf("TightenOperationalDetailRetention() error = %v", err)
	}
	days, found, err = repository.LoadOperationalDetailRetention(context.Background())
	if err != nil || !found || days != 14 {
		t.Fatalf("LoadOperationalDetailRetention(tightened) = %d, %v, %v", days, found, err)
	}
	stored, err := NewSettingsRepository(db).Get(context.Background(), domain.SettingOperationalDetailRetentionDays)
	if err != nil || !stored.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("Get(injected update time) = %#v, %v", stored, err)
	}

	stale := application.RetentionSettingUpdate{
		ExpectedDays: application.DefaultOperationalDetailRetentionDays,
		Days:         7,
		UpdatedAt:    updatedAt.Add(time.Millisecond),
	}
	if err := repository.TightenOperationalDetailRetention(context.Background(), stale); !errors.Is(err, application.ErrRetentionSettingConflict) {
		t.Fatalf("TightenOperationalDetailRetention(stale) error = %v, want ErrRetentionSettingConflict", err)
	}
	widen := application.RetentionSettingUpdate{
		ExpectedDays: 14,
		Days:         30,
		UpdatedAt:    updatedAt.Add(2 * time.Millisecond),
	}
	if err := repository.TightenOperationalDetailRetention(context.Background(), widen); !errors.Is(err, application.ErrRetentionWouldWiden) {
		t.Fatalf("TightenOperationalDetailRetention(widen) error = %v, want ErrRetentionWouldWiden", err)
	}
	days, found, err = repository.LoadOperationalDetailRetention(context.Background())
	if err != nil || !found || days != 14 {
		t.Fatalf("retention after rejected writes = %d, %v, %v", days, found, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repository.TightenOperationalDetailRetention(cancelled, application.RetentionSettingUpdate{
		ExpectedDays: 14,
		Days:         7,
		UpdatedAt:    updatedAt.Add(3 * time.Millisecond),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("TightenOperationalDetailRetention(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestRetentionCleanupUsesPersistedTightenedPolicy(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-lifecycle-cleanup")
	now := time.Date(2026, time.August, 14, 2, 0, 0, 0, time.UTC)
	observedAt := now.Add(-20 * 24 * time.Hour)
	run := seedStandardRun(
		t,
		db,
		"00000000-0000-7000-8000-000000006401",
		"00000000-0000-7000-8000-000000006402",
		"00000000-0000-7000-8000-000000006403",
		observedAt.Add(-time.Hour),
	)
	invocation := testToolInvocation("00000000-0000-7000-8000-000000006404", run, 1, observedAt.Add(-time.Millisecond))
	evidence := testEvidence("00000000-0000-7000-8000-000000006405", invocation, observedAt)
	invocation.EvidenceCount = 1
	if err := NewToolInvocationRepository(db).Save(context.Background(), invocation, []domain.Evidence{evidence}); err != nil {
		t.Fatalf("Save(ToolInvocation) error = %v", err)
	}
	model := testModelRequest("00000000-0000-7000-8000-000000006406", run.ID, 1, observedAt)
	if err := NewModelRequestRepository(db).Save(context.Background(), model); err != nil {
		t.Fatalf("Save(ModelRequest) error = %v", err)
	}
	if err := NewSessionRepository(db).TightenOperationalDetailRetention(context.Background(), application.RetentionSettingUpdate{
		ExpectedDays: application.DefaultOperationalDetailRetentionDays,
		Days:         14,
		UpdatedAt:    now.Add(-time.Millisecond),
	}); err != nil {
		t.Fatalf("TightenOperationalDetailRetention() error = %v", err)
	}

	result, err := NewRetentionRepository(db).Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now:                            now,
		OperationalDetailRetentionDays: application.DefaultOperationalDetailRetentionDays,
		BatchSize:                      10,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if result.EvidenceItems != 1 || result.ToolInvocations != 1 || result.ModelRequests != 1 {
		t.Fatalf("Cleanup() = %#v, want persisted 14-day policy", result)
	}
}

func TestRetentionCleanupFailsClosedOnInvalidPersistedPolicy(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-lifecycle-invalid-cleanup")
	now := time.Date(2026, time.August, 14, 3, 0, 0, 0, time.UTC)
	run := seedStandardRun(
		t,
		db,
		"00000000-0000-7000-8000-000000006411",
		"00000000-0000-7000-8000-000000006412",
		"00000000-0000-7000-8000-000000006413",
		now.Add(-40*24*time.Hour),
	)
	model := testModelRequest("00000000-0000-7000-8000-000000006414", run.ID, 1, now.Add(-40*24*time.Hour))
	if err := NewModelRequestRepository(db).Save(context.Background(), model); err != nil {
		t.Fatalf("Save(ModelRequest) error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO settings (key, value_json, schema_version, updated_at_ms)
		VALUES (?, ?, ?, ?)
	`, domain.SettingOperationalDetailRetentionDays, "3651", 1, now.UnixMilli()); err != nil {
		t.Fatalf("invalid setting setup error = %v", err)
	}

	_, err := NewRetentionRepository(db).Cleanup(context.Background(), auditcontract.CleanupRequest{
		Now:                            now,
		OperationalDetailRetentionDays: application.DefaultOperationalDetailRetentionDays,
		BatchSize:                      10,
	})
	assertStorageError(t, err, ClassPersistenceUnavailable, "retention_cleanup_failed")
	var remaining int
	if countErr := db.handle.GetContext(context.Background(), &remaining, `SELECT count(id) FROM model_requests`); countErr != nil {
		t.Fatalf("model-request count error = %v", countErr)
	}
	if remaining != 1 {
		t.Fatalf("model requests after invalid retention cleanup = %d, want 1", remaining)
	}
}
