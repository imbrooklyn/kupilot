package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testApplicationVersion = "test-version"

func TestOpenRespectsExistingUserModesAndConfiguresConnectionPragmas(t *testing.T) {
	stateDir := filepath.Join(testRealTempDir(t), "state directory")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	databasePath := filepath.Join(stateDir, databaseFilename)
	if err := os.WriteFile(databasePath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	db := openTestDB(t, context.Background(), stateDir, "permissions")

	assertMode(t, stateDir, 0o755)
	assertMode(t, databasePath, 0o644)
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Lstat(databasePath + suffix)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("SQLite sidecar %q is unavailable: %v", suffix, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("mode for new SQLite sidecar %q = %04o, want 0600", suffix, info.Mode().Perm())
		}
	}

	if got := db.handle.Stats().MaxOpenConnections; got != maxOpenConnections {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, maxOpenConnections)
	}
	checks := []struct {
		query string
		want  string
	}{
		{query: `PRAGMA foreign_keys`, want: "1"},
		{query: `PRAGMA busy_timeout`, want: "5000"},
		{query: `PRAGMA journal_mode`, want: "wal"},
		{query: `PRAGMA synchronous`, want: "1"},
		{query: `PRAGMA secure_delete`, want: "2"},
	}
	for _, check := range checks {
		var got string
		if err := db.handle.GetContext(context.Background(), &got, check.query); err != nil {
			t.Fatalf("%s error = %v", check.query, err)
		}
		if got != check.want {
			t.Errorf("%s = %q, want %q", check.query, got, check.want)
		}
	}
}

func TestOpenCreatesPrivateStateAndDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode contract")
	}
	stateDir := filepath.Join(testRealTempDir(t), "new-state")
	db := openTestDB(t, context.Background(), stateDir, "new-permissions")
	assertMode(t, stateDir, 0o700)
	assertMode(t, db.databasePath, 0o600)
}

func TestDeleteAllLocalStateRemovesOnlyDatabaseFilesAndKeepsDirectory(t *testing.T) {
	stateDir := filepath.Join(testRealTempDir(t), "delete-all-state")
	database := openTestDB(t, context.Background(), stateDir, "delete-all")
	databasePath := filepath.Join(stateDir, databaseFilename)
	unrelatedPath := filepath.Join(stateDir, "keep.txt")
	if err := os.WriteFile(unrelatedPath, []byte("unrelated"), 0o600); err != nil {
		t.Fatalf("WriteFile(unrelated) error = %v", err)
	}

	result, err := database.DeleteAllLocalState(context.Background())
	if err != nil || !result.StorageClosed || !result.Complete {
		t.Fatalf("DeleteAllLocalState() = %#v, %v", result, err)
	}
	for _, path := range knownStoragePaths(databasePath) {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("storage path still exists after delete-all: %v", statErr)
		}
	}
	if info, statErr := os.Lstat(stateDir); statErr != nil || !info.IsDir() {
		t.Fatalf("state directory was removed or changed: %#v/%v", info, statErr)
	}
	if content, readErr := os.ReadFile(unrelatedPath); readErr != nil || string(content) != "unrelated" {
		t.Fatalf("unrelated file changed = %q/%v", content, readErr)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatalf("Close() after delete-all error = %v", closeErr)
	}
}

func TestDeleteAllLocalStatePreflightDenialsLeaveDatabaseOpenAndUntouched(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("symlink contract applies to supported platforms")
	}
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
		ctx     func() context.Context
	}{
		{
			name: "cancelled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "sidecar symlink",
			prepare: func(t *testing.T, databasePath string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(databasePath), "symlink-target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatalf("WriteFile(target) error = %v", err)
				}
				if err := os.Symlink(target, databasePath+"-journal"); err != nil {
					t.Fatalf("Symlink(sidecar) error = %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateDir := filepath.Join(testRealTempDir(t), "delete-all-preflight")
			database := openTestDB(t, context.Background(), stateDir, "delete-all-preflight")
			databasePath := filepath.Join(stateDir, databaseFilename)
			if test.prepare != nil {
				test.prepare(t, databasePath)
			}
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			result, err := database.DeleteAllLocalState(ctx)
			if err == nil || result.StorageClosed || result.Complete {
				t.Fatalf("DeleteAllLocalState(preflight) = %#v, %v", result, err)
			}
			if strings.Contains(err.Error(), stateDir) {
				t.Fatal("delete-all safe error disclosed the local state path")
			}
			if _, statErr := os.Lstat(databasePath); statErr != nil {
				t.Fatalf("database changed after preflight denial: %v", statErr)
			}
			if pingErr := database.handle.PingContext(context.Background()); pingErr != nil {
				t.Fatalf("database closed after preflight denial: %v", pingErr)
			}
		})
	}
}

func TestDeleteAllLocalStateReportsPartialRemovalAfterClose(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Unix directory permissions provide the deterministic removal denial")
	}
	stateDir := filepath.Join(testRealTempDir(t), "delete-all-partial")
	database := openTestDB(t, context.Background(), stateDir, "delete-all-partial")
	databasePath := filepath.Join(stateDir, databaseFilename)
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatalf("Chmod(read-only state directory) error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	result, err := database.DeleteAllLocalState(context.Background())
	if err == nil || !result.StorageClosed || result.Complete {
		t.Fatalf("DeleteAllLocalState(partial) = %#v, %v", result, err)
	}
	if _, statErr := os.Lstat(databasePath); statErr != nil {
		t.Fatalf("partial deletion did not leave the denied database path visible: %v", statErr)
	}
	if pingErr := database.handle.PingContext(context.Background()); pingErr == nil {
		t.Fatal("database remained usable after partial delete-all result")
	}
}

func TestOpenRejectsUnsafePathsWithoutDisclosingThem(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("symlink and Unix permission contract applies to supported platforms")
	}

	tests := []struct {
		name       string
		prepare    func(*testing.T) string
		wantClass  ErrorClass
		wantCode   string
		wantAbsent string
	}{
		{
			name: "relative state directory",
			prepare: func(*testing.T) string {
				return "relative-state"
			},
			wantClass: ClassConfigurationInvalid,
			wantCode:  "storage_path_invalid",
		},
		{
			name: "root state directory",
			prepare: func(*testing.T) string {
				return string(filepath.Separator)
			},
			wantClass: ClassConfigurationInvalid,
			wantCode:  "storage_path_invalid",
		},
		{
			name: "state directory is a file",
			prepare: func(t *testing.T) string {
				stateDir := filepath.Join(testRealTempDir(t), "state-file")
				if err := os.WriteFile(stateDir, nil, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				return stateDir
			},
			wantClass: ClassPolicyDenied,
			wantCode:  "storage_path_unsafe",
		},
		{
			name: "symlinked state directory",
			prepare: func(t *testing.T) string {
				root := testRealTempDir(t)
				target := filepath.Join(root, "target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				stateDir := filepath.Join(root, "unsafe-state-canary")
				if err := os.Symlink(target, stateDir); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
				return stateDir
			},
			wantClass:  ClassPolicyDenied,
			wantCode:   "storage_path_unsafe",
			wantAbsent: "unsafe-state-canary",
		},
		{
			name: "symlinked database file",
			prepare: func(t *testing.T) string {
				stateDir := testStateDir(t)
				if err := os.Mkdir(stateDir, 0o700); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				target := filepath.Join(filepath.Dir(stateDir), "database-target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				if err := os.Symlink(target, filepath.Join(stateDir, databaseFilename)); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
				return stateDir
			},
			wantClass: ClassPolicyDenied,
			wantCode:  "storage_path_unsafe",
		},
		{
			name: "symlinked sidecar file",
			prepare: func(t *testing.T) string {
				stateDir := testStateDir(t)
				if err := os.Mkdir(stateDir, 0o700); err != nil {
					t.Fatalf("Mkdir() error = %v", err)
				}
				target := filepath.Join(filepath.Dir(stateDir), "sidecar-target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
				if err := os.Symlink(target, filepath.Join(stateDir, databaseFilename+"-wal")); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
				return stateDir
			},
			wantClass: ClassPolicyDenied,
			wantCode:  "storage_path_unsafe",
		},
	}

	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			stateDir := current.prepare(t)
			_, err := Open(context.Background(), OpenOptions{
				StateDir:           stateDir,
				ApplicationVersion: testApplicationVersion,
				CorrelationID:      "unsafe-path-test",
			})
			assertStorageError(t, err, current.wantClass, current.wantCode)
			if strings.Contains(err.Error(), stateDir) || current.wantAbsent != "" && strings.Contains(err.Error(), current.wantAbsent) {
				t.Fatalf("error disclosed a storage path: %q", err)
			}
		})
	}
}

func TestOpenHonorsCancelledContextBeforeFilesystemIO(t *testing.T) {
	stateDir := testStateDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Open(ctx, OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "cancelled-open",
	})
	assertStorageError(t, err, ClassCancelled, "storage_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context.Canceled semantics", err)
	}
	if _, statErr := os.Lstat(stateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state directory was touched after cancellation: %v", statErr)
	}
}

func TestOpenHonorsExpiredDeadlineBeforeFilesystemIO(t *testing.T) {
	stateDir := testStateDir(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()

	_, err := Open(ctx, OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "expired-open",
	})
	assertStorageError(t, err, ClassTimeout, "storage_timeout")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open() error = %v, want context.DeadlineExceeded semantics", err)
	}
	if _, statErr := os.Lstat(stateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state directory was touched after deadline: %v", statErr)
	}
}

func TestClosePreventsFurtherDatabaseUse(t *testing.T) {
	stateDir := testStateDir(t)
	db, err := Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "close-test",
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := db.handle.PingContext(context.Background()); err == nil {
		t.Fatal("PingContext() after Close() error = nil")
	}
}

func TestOpenRejectsCorruptDatabaseWithoutReplacingIt(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	databasePath := filepath.Join(stateDir, databaseFilename)
	corruptBytes := []byte("synthetic corrupt database bytes")
	if err := os.WriteFile(databasePath, corruptBytes, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "corrupt-database",
	})
	if err == nil {
		t.Fatal("Open() error = nil")
	}
	var storageError *Error
	if !errors.As(err, &storageError) || storageError.Class() != ClassPersistenceUnavailable {
		t.Fatalf("Open() error = %v, want persistence_unavailable", err)
	}
	if strings.Contains(err.Error(), databasePath) || strings.Contains(err.Error(), string(corruptBytes)) {
		t.Fatalf("Open() disclosed corrupt storage content: %q", err)
	}
	got, readErr := os.ReadFile(databasePath)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(got) != string(corruptBytes) {
		t.Fatal("corrupt database was replaced or rewritten")
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "foreign-key-contract")
	_, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO messages (
			id, session_id, run_id, role, content, content_format, status,
			scope_context, scope_namespace, scope_generation,
			resource_refs_json, content_hash, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"00000000-0000-7000-8000-000000000001",
		"00000000-0000-7000-8000-000000000002",
		nil,
		"user",
		"safe text",
		"plain",
		"committed",
		nil,
		nil,
		nil,
		nil,
		strings.Repeat("0", 64),
		1,
	)
	if err == nil {
		t.Fatal("foreign-key violating insert error = nil")
	}
}

func TestSessionDeletionCascadesStoredHistory(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "session-cascade")
	ctx := context.Background()
	sessionID := "00000000-0000-7000-8000-000000000010"
	messageID := "00000000-0000-7000-8000-000000000011"
	runID := "00000000-0000-7000-8000-000000000012"
	if _, err := db.handle.ExecContext(ctx, `
		INSERT INTO sessions (
			id, title, status, privacy_mode, version, created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, sessionID, "Stored history", "active", "standard", 1, 1, 1); err != nil {
		t.Fatalf("session insert error = %v", err)
	}
	if _, err := db.handle.ExecContext(ctx, `
		INSERT INTO messages (
			id, session_id, run_id, role, content, content_format, status,
			content_hash, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		messageID,
		sessionID,
		nil,
		"user",
		"safe text",
		"plain",
		"committed",
		strings.Repeat("0", 64),
		1,
	); err != nil {
		t.Fatalf("message insert error = %v", err)
	}
	if _, err := db.handle.ExecContext(ctx, `
		INSERT INTO agent_runs (
			id, session_id, request_message_id, status,
			scope_context, scope_namespace, scope_generation,
			prompt_version, tool_catalog_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		runID,
		sessionID,
		messageID,
		"completed",
		"test-context",
		"test-namespace",
		1,
		"test-prompt",
		"test-tools",
	); err != nil {
		t.Fatalf("AgentRun insert error = %v", err)
	}
	if _, err := db.handle.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		t.Fatalf("Session delete error = %v", err)
	}

	var remaining int
	if err := db.handle.GetContext(ctx, &remaining, `
		SELECT
			(SELECT count(id) FROM sessions)
			+ (SELECT count(id) FROM messages)
			+ (SELECT count(id) FROM agent_runs)
	`); err != nil {
		t.Fatalf("cascade count error = %v", err)
	}
	if remaining != 0 {
		t.Fatalf("rows after Session delete = %d, want 0", remaining)
	}
}

func TestSettingsRejectCredentialShapedKeys(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "settings-key-policy")
	_, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO settings (key, value_json, schema_version, updated_at_ms)
		VALUES (?, ?, ?, ?)
	`, "model_api_key", `{}`, 1, 1)
	if err == nil {
		t.Fatal("credential-shaped settings key insert error = nil")
	}
}

func openTestDB(t *testing.T, ctx context.Context, stateDir, correlationID string) *DB {
	t.Helper()
	db, err := Open(ctx, OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      correlationID,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return db
}

func testStateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(testRealTempDir(t), "state")
}

func testRealTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	return realRoot
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q) error = %v", filepath.Base(path), err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %q = %04o, want %04o", filepath.Base(path), got, want)
	}
}

func assertStorageError(t *testing.T, err error, wantClass ErrorClass, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil")
	}
	var storageError *Error
	if !errors.As(err, &storageError) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if got := storageError.Class(); got != wantClass {
		t.Errorf("error class = %q, want %q", got, wantClass)
	}
	if got := storageError.Code(); got != wantCode {
		t.Errorf("error code = %q, want %q", got, wantCode)
	}
}
