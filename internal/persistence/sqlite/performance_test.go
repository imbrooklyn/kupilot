package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

var (
	sqliteResumeBenchmarkCount int
	sqliteLifecycleBenchmark   domain.Diagnosis
)

// BenchmarkSQLiteMigrationFreshV1 opens, validates, and closes one fresh real
// temporary SQLite database per operation.
func BenchmarkSQLiteMigrationFreshV1(b *testing.B) {
	root := performanceRealTempDir(b)
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		stateDir := filepath.Join(root, fmt.Sprintf("migration-%06d", index))
		db, err := Open(context.Background(), OpenOptions{
			StateDir: stateDir, ApplicationVersion: testApplicationVersion,
			CorrelationID: "perf-migration",
		})
		if err != nil {
			b.Fatalf("Open(fresh migration) error = %v", err)
		}
		if err := validateSQLitePerformanceDatabase(db); err != nil {
			b.Fatalf("validate fresh migration: %v", err)
		}
		if err := db.Close(); err != nil {
			b.Fatalf("Close(fresh migration) error = %v", err)
		}
		b.StopTimer()
		if err := validateSQLitePerformanceFiles(stateDir); err != nil {
			b.Fatalf("validate fresh migration files: %v", err)
		}
		if err := os.RemoveAll(stateDir); err != nil {
			b.Fatal("remove isolated migration directory")
		}
		b.StartTimer()
	}
}

// BenchmarkSQLiteResumeQueryV1 measures the fixed maximum picker, literal
// search, and exact-history queries against one seeded temporary database.
func BenchmarkSQLiteResumeQueryV1(b *testing.B) {
	fixture := newSQLiteResumeBenchmarkFixture(b)
	b.Run("Picker50", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			page, err := fixture.sessions.ListResumable(context.Background(), sessioncontract.ResumePageRequest{Limit: 50})
			if err != nil || len(page.Sessions) != 50 {
				b.Fatalf("ListResumable() count=%d error=%v", len(page.Sessions), err)
			}
			sqliteResumeBenchmarkCount = len(page.Sessions)
		}
	})
	b.Run("Search50", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			matches, err := fixture.sessions.SearchResumable(context.Background(), application.SessionSearchRequest{
				Filter: "Synthetic", Limit: 50,
			})
			if err != nil || len(matches) != 50 {
				b.Fatalf("SearchResumable() count=%d error=%v", len(matches), err)
			}
			sqliteResumeBenchmarkCount = len(matches)
		}
	})
	b.Run("ExactHistory100", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			history, err := fixture.service.ResumeByID(context.Background(), fixture.target)
			if err != nil || len(history.Messages) != sessioncontract.MaxMessagePageSize || history.Next != nil {
				b.Fatalf("ResumeByID() count=%d next=%v error=%v", len(history.Messages), history.Next != nil, err)
			}
			sqliteResumeBenchmarkCount = len(history.Messages)
		}
	})
}

// BenchmarkSQLiteDiagnosticLifecycleV1 persists one bounded synthetic
// Session/run/Tool/Evidence/Diagnosis lifecycle, queries it, applies retention,
// validates the database, and closes it cleanly.
func BenchmarkSQLiteDiagnosticLifecycleV1(b *testing.B) {
	root := performanceRealTempDir(b)
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		stateDir := filepath.Join(root, fmt.Sprintf("lifecycle-%06d", index))
		diagnosis, err := runSQLiteDiagnosticLifecycle(context.Background(), stateDir)
		if err != nil {
			b.Fatalf("synthetic SQLite lifecycle failed: %v", err)
		}
		b.StopTimer()
		if err := validateSQLitePerformanceFiles(stateDir); err != nil {
			b.Fatalf("validate lifecycle files: %v", err)
		}
		if err := os.RemoveAll(stateDir); err != nil {
			b.Fatal("remove isolated lifecycle directory")
		}
		b.StartTimer()
		sqliteLifecycleBenchmark = diagnosis
	}
}

type sqliteResumeBenchmarkFixture struct {
	db       *DB
	sessions *SessionRepository
	service  *sessioncontract.Service
	target   domain.SessionID
}

func newSQLiteResumeBenchmarkFixture(tb testing.TB) sqliteResumeBenchmarkFixture {
	tb.Helper()
	stateDir := filepath.Join(performanceRealTempDir(tb), "resume")
	db, err := Open(context.Background(), OpenOptions{
		StateDir: stateDir, ApplicationVersion: testApplicationVersion,
		CorrelationID: "perf-resume",
	})
	if err != nil {
		tb.Fatalf("Open(resume fixture) error = %v", err)
	}
	tb.Cleanup(func() {
		if err := db.Close(); err != nil {
			tb.Errorf("Close(resume fixture) error = %v", err)
		}
	})
	sessions := NewSessionRepository(db)
	messages := NewMessageRepository(db)
	runs := NewAgentRunRepository(db)
	base := time.UnixMilli(1_700_000_000_000).UTC()
	target := performanceIdentifier[domain.SessionID](10_000)
	for index := range 50 {
		sessionID := performanceIdentifier[domain.SessionID](10_000 + index)
		value := testSession(string(sessionID), fmt.Sprintf("Synthetic session %02d", index), domain.PrivacyModeStandard, base.Add(time.Duration(index)*time.Second))
		if err := sessions.Create(context.Background(), value); err != nil {
			tb.Fatalf("Create(resume fixture) error = %v", err)
		}
		message := testMessage(
			performanceIdentifier[domain.MessageID](20_000+index), sessionID, nil,
			"Bounded synthetic history", base.Add(time.Duration(index)*time.Second),
		)
		if err := messages.Append(context.Background(), message); err != nil {
			tb.Fatalf("Append(resume fixture) error = %v", err)
		}
	}
	for index := 1; index < sessioncontract.MaxMessagePageSize; index++ {
		message := testMessage(
			performanceIdentifier[domain.MessageID](21_000+index), target, nil,
			"Bounded synthetic retained history", base.Add(time.Duration(50+index)*time.Second),
		)
		if err := messages.Append(context.Background(), message); err != nil {
			tb.Fatalf("Append(exact history fixture) error = %v", err)
		}
	}
	return sqliteResumeBenchmarkFixture{
		db: db, sessions: sessions,
		service: sessioncontract.NewService(sessions, sessions, messages, runs, runs),
		target:  target,
	}
}

func runSQLiteDiagnosticLifecycle(ctx context.Context, stateDir string) (_ domain.Diagnosis, returnErr error) {
	db, err := Open(ctx, OpenOptions{
		StateDir: stateDir, ApplicationVersion: testApplicationVersion,
		CorrelationID: "perf-lifecycle",
	})
	if err != nil {
		return domain.Diagnosis{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, db.Close())
	}()

	base := time.UnixMilli(1_700_000_000_000).UTC()
	sessionID := performanceIdentifier[domain.SessionID](30_001)
	sessionValue := testSession(string(sessionID), "Synthetic diagnostic lifecycle", domain.PrivacyModeStandard, base)
	sessions := NewSessionRepository(db)
	if err := sessions.Create(ctx, sessionValue); err != nil {
		return domain.Diagnosis{}, err
	}
	request, running := testRunningPair(
		performanceIdentifier[domain.MessageID](30_002),
		performanceIdentifier[domain.AgentRunID](30_003), sessionID, base.Add(time.Second),
	)
	runs := NewAgentRunRepository(db)
	if err := runs.Begin(ctx, request, running); err != nil {
		return domain.Diagnosis{}, err
	}
	invocation := testToolInvocation(performanceIdentifier[domain.ToolInvocationID](30_004), running, 1, base.Add(2*time.Second))
	invocation.EvidenceCount = 1
	evidence := testEvidence(performanceIdentifier[domain.EvidenceID](30_005), invocation, base.Add(2*time.Second+5*time.Millisecond))
	if err := NewToolInvocationRepository(db).Save(ctx, invocation, []domain.Evidence{evidence}); err != nil {
		return domain.Diagnosis{}, err
	}
	diagnosis := testDiagnosis(performanceIdentifier[domain.DiagnosisID](30_006), running, []domain.Evidence{evidence})
	if err := NewDiagnosisRepository(db).Save(ctx, diagnosis); err != nil {
		return domain.Diagnosis{}, err
	}
	terminal := testTerminalRun(running, domain.AgentRunStatusCompleted, base.Add(3*time.Second))
	terminal.StepCount = 1
	terminal.ToolCallCount = 1
	runID := terminal.ID
	scope := terminal.Scope
	answer := domain.Message{
		ID: performanceIdentifier[domain.MessageID](30_007), SessionID: sessionID, RunID: &runID,
		Role: domain.MessageRoleAssistant, Content: diagnosis.AnswerMarkdown, Format: domain.MessageFormatMarkdown,
		Status: domain.MessageStatusCommitted, Hash: domain.MessageContentHash(diagnosis.AnswerMarkdown),
		Scope: &scope, CreatedAt: *terminal.FinishedAt,
	}
	if err := runs.FinishWithMessage(ctx, answer, terminal); err != nil {
		return domain.Diagnosis{}, err
	}
	stored, err := NewDiagnosisRepository(db).GetByRunID(ctx, running.ID)
	if err != nil || stored.ID != diagnosis.ID {
		return domain.Diagnosis{}, errors.New("synthetic Diagnosis was not reconstructed")
	}
	page, err := sessions.ListResumable(ctx, sessioncontract.ResumePageRequest{Limit: 1})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].ID != sessionID {
		return domain.Diagnosis{}, errors.New("synthetic Session was not resumable")
	}
	cleanup, err := NewRetentionRepository(db).Cleanup(ctx, auditcontract.CleanupRequest{
		Now: base.Add(24 * time.Hour), OperationalDetailRetentionDays: 30, BatchSize: auditcontract.MaxCleanupBatchSize,
	})
	if err != nil || cleanup != (auditcontract.CleanupResult{}) {
		return domain.Diagnosis{}, errors.New("synthetic retention result was not empty")
	}
	if err := validateSQLitePerformanceDatabase(db); err != nil {
		return domain.Diagnosis{}, err
	}
	return stored, nil
}

func validateSQLitePerformanceDatabase(db *DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return errors.New("embedded migrations are invalid")
	}
	var applied int
	if err := db.handle.GetContext(context.Background(), &applied, `SELECT count(version) FROM schema_migrations`); err != nil || applied != len(migrations) {
		return errors.New("migration ledger does not match embedded migrations")
	}
	var foreignKeys int
	if err := db.handle.GetContext(context.Background(), &foreignKeys, `PRAGMA foreign_keys`); err != nil || foreignKeys != 1 {
		return errors.New("foreign-key enforcement is disabled")
	}
	var integrity string
	if err := db.handle.GetContext(context.Background(), &integrity, `PRAGMA quick_check`); err != nil || integrity != "ok" {
		return errors.New("SQLite quick check failed")
	}
	return nil
}

func validateSQLitePerformanceFiles(stateDir string) error {
	stateInfo, err := os.Lstat(stateDir)
	if err != nil || stateInfo.Mode().Perm() != 0o700 || !stateInfo.IsDir() {
		return errors.New("state directory permissions are invalid")
	}
	databasePath := filepath.Join(stateDir, databaseFilename)
	databaseInfo, err := os.Lstat(databasePath)
	if err != nil || databaseInfo.Mode().Perm() != 0o600 || !databaseInfo.Mode().IsRegular() {
		return errors.New("database permissions are invalid")
	}
	prohibited := []string{
		"prohibited-prompt-body-canary",
		"prohibited-tool-result-canary",
		"prohibited-log-body-canary",
		"prohibited-kubeconfig-canary",
		"prohibited-model-credential-canary",
	}
	for _, suffix := range append([]string{""}, knownSidecarSuffixes[:]...) {
		content, err := os.ReadFile(databasePath + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("database artifact could not be inspected")
		}
		for _, value := range prohibited {
			if strings.Contains(string(content), value) {
				return errors.New("prohibited synthetic content entered SQLite")
			}
		}
	}
	return nil
}

func performanceIdentifier[T ~string](value int) T {
	return T(fmt.Sprintf("00000000-0000-7000-8000-%012d", value))
}

func performanceRealTempDir(tb testing.TB) string {
	tb.Helper()
	root, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatal("temporary performance directory could not be resolved")
	}
	return root
}
