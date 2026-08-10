package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMigrateFreshDatabaseAndRepeatedOpen(t *testing.T) {
	stateDir := testStateDir(t)
	db, err := Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: "first-version",
		CorrelationID:      "fresh-migration",
	})
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	assertInitialSchema(t, db.handle.DB)
	assertMigrationRecord(t, db.handle.DB, "first-version")
	if err := db.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	reopened, err := Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: "second-version",
		CorrelationID:      "repeated-migration",
	})
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertInitialSchema(t, reopened.handle.DB)
	assertMigrationRecord(t, reopened.handle.DB, "first-version")
}

func TestMigrateLegacyFixture(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "sqlite", "000000_pre_migrations.sql"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	if _, err := raw.ExecContext(context.Background(), string(fixture)); err != nil {
		_ = raw.Close()
		t.Fatalf("legacy fixture error = %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("legacy fixture Close() error = %v", err)
	}

	db := openTestDB(t, context.Background(), stateDir, "legacy-upgrade")
	assertInitialSchema(t, db.handle.DB)
	assertMigrationRecord(t, db.handle.DB, testApplicationVersion)
}

func TestMigrateRejectsChecksumMismatch(t *testing.T) {
	stateDir := testStateDir(t)
	db := openTestDB(t, context.Background(), stateDir, "checksum-setup")
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	badChecksum := strings.Repeat("0", 64)
	if _, err := raw.ExecContext(context.Background(),
		`UPDATE schema_migrations SET checksum = ? WHERE version = ?`,
		badChecksum,
		1,
	); err != nil {
		_ = raw.Close()
		t.Fatalf("checksum update error = %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw Close() error = %v", err)
	}

	_, err := Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "checksum-mismatch",
	})
	assertStorageError(t, err, ClassPersistenceUnavailable, "storage_migration_checksum_mismatch")
	if strings.Contains(err.Error(), badChecksum) {
		t.Fatal("checksum mismatch error disclosed the stored checksum")
	}
}

func TestMigrateRejectsSchemaTooNew(t *testing.T) {
	stateDir := testStateDir(t)
	db := openTestDB(t, context.Background(), stateDir, "schema-new-setup")
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	if _, err := raw.ExecContext(context.Background(), `
		INSERT INTO schema_migrations (
			version, name, checksum, applied_at_ms, app_version
		) VALUES (?, ?, ?, ?, ?)
	`, 3, "000003_future.sql", strings.Repeat("1", 64), 1, "future-version"); err != nil {
		_ = raw.Close()
		t.Fatalf("future migration insert error = %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw Close() error = %v", err)
	}

	_, err := Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "schema-too-new",
	})
	assertStorageError(t, err, ClassPersistenceUnavailable, "storage_schema_too_new")
}

func TestMigrateRejectsUnknownSchemaWithoutChangingIt(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	if _, err := raw.ExecContext(context.Background(), `CREATE TABLE unknown_table (id INTEGER PRIMARY KEY) STRICT`); err != nil {
		_ = raw.Close()
		t.Fatalf("unknown schema setup error = %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw Close() error = %v", err)
	}

	_, err := Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "unknown-schema",
	})
	assertStorageError(t, err, ClassPersistenceUnavailable, "storage_schema_unknown")

	raw = openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	defer raw.Close()
	var ledgerCount int
	if err := raw.QueryRowContext(context.Background(), `
		SELECT count(name)
		FROM sqlite_schema
		WHERE type = 'table' AND name = 'schema_migrations'
	`).Scan(&ledgerCount); err != nil {
		t.Fatalf("schema inspection error = %v", err)
	}
	if ledgerCount != 0 {
		t.Fatalf("schema_migrations table count = %d, want 0", ledgerCount)
	}
	var journalMode string
	if err := raw.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("journal mode query error = %v", err)
	}
	if journalMode != "delete" {
		t.Fatalf("journal mode after rejected schema = %q, want delete", journalMode)
	}
	if _, statErr := os.Lstat(filepath.Join(stateDir, databaseFilename+"-wal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected schema created a WAL sidecar: %v", statErr)
	}
}

func TestMigrateRollsBackFailedInitialMigration(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "sqlite", "000000_pre_migrations.sql"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	if _, err := raw.ExecContext(context.Background(), string(fixture)); err != nil {
		_ = raw.Close()
		t.Fatalf("legacy fixture error = %v", err)
	}
	if _, err := raw.ExecContext(context.Background(), `CREATE TABLE sessions (conflict INTEGER) STRICT`); err != nil {
		_ = raw.Close()
		t.Fatalf("conflict setup error = %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw Close() error = %v", err)
	}

	_, err = Open(context.Background(), OpenOptions{
		StateDir:           stateDir,
		ApplicationVersion: testApplicationVersion,
		CorrelationID:      "migration-rollback",
	})
	assertStorageError(t, err, ClassPersistenceUnavailable, "storage_migration_failed")

	raw = openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	defer raw.Close()
	var migrationCount int
	if err := raw.QueryRowContext(context.Background(), `SELECT count(version) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("migration count error = %v", err)
	}
	if migrationCount != 0 {
		t.Fatalf("migration record count = %d, want 0", migrationCount)
	}
	var unexpectedTables int
	if err := raw.QueryRowContext(context.Background(), `
		SELECT count(name)
		FROM sqlite_schema
		WHERE type = 'table' AND name NOT IN ('schema_migrations', 'sessions')
	`).Scan(&unexpectedTables); err != nil {
		t.Fatalf("table count error = %v", err)
	}
	if unexpectedTables != 0 {
		t.Fatalf("tables left by failed migration = %d, want 0", unexpectedTables)
	}
}

func TestInitialSchemaContainsOnlyAllowlistedStorageColumns(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "schema-safety")
	rows, err := db.handle.QueryxContext(context.Background(), `
		SELECT schema_table.name AS table_name, table_column.name AS column_name
		FROM sqlite_schema AS schema_table
		JOIN pragma_table_info(schema_table.name) AS table_column
		WHERE schema_table.type = 'table'
		ORDER BY schema_table.name, table_column.cid
	`)
	if err != nil {
		t.Fatalf("schema column query error = %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tableName string
		var columnName string
		if err := rows.Scan(&tableName, &columnName); err != nil {
			t.Fatalf("schema column scan error = %v", err)
		}
		lower := strings.ToLower(columnName)
		for _, forbidden := range []string{
			"api_key", "kubeconfig", "credential", "certificate", "private_key",
			"bearer_token", "auth_token", "raw_log", "full_prompt", "raw_prompt",
			"raw_model", "model_body", "raw_tool", "tool_result_body",
		} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("forbidden column %s.%s", tableName, columnName)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schema rows error = %v", err)
	}
}

func assertInitialSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT name
		FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		t.Fatalf("table query error = %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("table name scan error = %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table rows error = %v", err)
	}
	want := []string{
		"agent_runs",
		"approvals",
		"audit_events",
		"diagnoses",
		"evidence_items",
		"messages",
		"model_requests",
		"privacy_consents",
		"schema_migrations",
		"sessions",
		"settings",
		"tool_invocations",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
}

func assertMigrationRecord(t *testing.T, db *sql.DB, wantApplicationVersion string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT version, name, checksum, applied_at_ms, app_version
		FROM schema_migrations
		ORDER BY version
	`)
	if err != nil {
		t.Fatalf("migration record query error = %v", err)
	}
	defer rows.Close()
	wantNames := []string{"000001_initial.sql", "000002_privacy_consent.sql"}
	count := 0
	for rows.Next() {
		var version int
		var name string
		var checksum string
		var appliedAt int64
		var applicationVersion string
		if err := rows.Scan(&version, &name, &checksum, &appliedAt, &applicationVersion); err != nil {
			t.Fatalf("migration record scan error = %v", err)
		}
		if count >= len(wantNames) || version != count+1 || name != wantNames[count] || len(checksum) != 64 ||
			appliedAt <= 0 || applicationVersion != wantApplicationVersion {
			t.Fatalf("migration record = (%d, %q, checksum=%d bytes, %d, %q)", version, name, len(checksum), appliedAt, applicationVersion)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("migration record rows error = %v", err)
	}
	if count != len(wantNames) {
		t.Fatalf("migration count = %d, want %d", count, len(wantNames))
	}
}

func openRawDatabase(t *testing.T, databasePath string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, databaseURI(databasePath))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("PingContext() error = %v", err)
	}
	return db
}
