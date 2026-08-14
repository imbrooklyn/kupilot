package sqlite

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

const migrationLedgerSQL = `
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    checksum TEXT NOT NULL CHECK (
        length(checksum) = 64
        AND checksum NOT GLOB '*[^0-9a-f]*'
    ),
    applied_at_ms INTEGER NOT NULL CHECK (applied_at_ms >= 0),
    app_version TEXT NOT NULL CHECK (length(app_version) BETWEEN 1 AND 64)
) STRICT
`

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

type migrationRecord struct {
	Version            int64  `db:"version"`
	Name               string `db:"name"`
	Checksum           string `db:"checksum"`
	AppliedAtMS        int64  `db:"applied_at_ms"`
	ApplicationVersion string `db:"app_version"`
}

func migrate(ctx context.Context, db *sqlx.DB, applicationVersion, correlationID string) error {
	migrations, err := loadMigrations()
	if err != nil || len(migrations) == 0 {
		return newError(
			ClassPersistenceUnavailable,
			"storage_migration_invalid",
			"load_storage_migrations",
			"KuPilot could not verify its embedded storage migrations.",
			correlationID,
			err,
		)
	}
	hasLedger, userTableCount, err := inspectSchema(ctx, db)
	if err != nil {
		return newError(
			ClassPersistenceUnavailable,
			"storage_schema_unknown",
			"inspect_storage_schema",
			"KuPilot could not recognize the local database schema.",
			correlationID,
			err,
		)
	}
	if !hasLedger && userTableCount != 0 {
		return newError(
			ClassPersistenceUnavailable,
			"storage_schema_unknown",
			"inspect_storage_schema",
			"KuPilot could not recognize the local database schema.",
			correlationID,
			nil,
		)
	}

	applied := 0
	if hasLedger {
		applied, err = validateAppliedMigrations(ctx, db, migrations, correlationID)
		if err != nil {
			return err
		}
	}
	for index := applied; index < len(migrations); index++ {
		createLedger := !hasLedger && index == 0
		if err := applyMigration(ctx, db, migrations[index], applicationVersion, createLedger); err != nil {
			return newError(
				ClassPersistenceUnavailable,
				"storage_migration_failed",
				"apply_storage_migration",
				"KuPilot could not update the local database schema.",
				correlationID,
				err,
			)
		}
		hasLedger = true
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(embeddedMigrations, "migrations")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})
	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("migration directory entry is not a file")
		}
		name := entry.Name()
		version, ok := migrationVersion(name)
		if !ok || version != int64(len(result)+1) {
			return nil, fmt.Errorf("migration sequence is invalid")
		}
		content, err := embeddedMigrations.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		if len(content) == 0 || !utf8.Valid(content) {
			return nil, fmt.Errorf("migration content is invalid")
		}
		digest := sha256.Sum256(content)
		result = append(result, migration{
			version:  version,
			name:     name,
			checksum: hex.EncodeToString(digest[:]),
			sql:      string(content),
		})
	}
	return result, nil
}

func migrationVersion(name string) (int64, bool) {
	if len(name) < len("000001_a.sql") || !strings.HasSuffix(name, ".sql") || name[6] != '_' {
		return 0, false
	}
	for _, current := range name[7 : len(name)-4] {
		if current < 'a' || current > 'z' {
			if current != '_' && (current < '0' || current > '9') {
				return 0, false
			}
		}
	}
	version, err := strconv.ParseInt(name[:6], 10, 64)
	return version, err == nil && version > 0
}

func inspectSchema(ctx context.Context, db *sqlx.DB) (hasLedger bool, userTableCount int, err error) {
	if err := db.GetContext(ctx, &userTableCount, `
		SELECT count(name)
		FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
	`); err != nil {
		return false, 0, err
	}
	var ledgerCount int
	if err := db.GetContext(ctx, &ledgerCount, `
		SELECT count(name)
		FROM sqlite_schema
		WHERE type = 'table' AND name = 'schema_migrations'
	`); err != nil {
		return false, 0, err
	}
	return ledgerCount == 1, userTableCount, nil
}

func validateAppliedMigrations(
	ctx context.Context,
	db *sqlx.DB,
	migrations []migration,
	correlationID string,
) (int, error) {
	rows, err := db.QueryxContext(ctx, `
		SELECT version, name, checksum, applied_at_ms, app_version
		FROM schema_migrations
		ORDER BY version
	`)
	if err != nil {
		return 0, newError(
			ClassPersistenceUnavailable,
			"storage_schema_unknown",
			"verify_migration_history",
			"KuPilot could not recognize the local database schema.",
			correlationID,
			err,
		)
	}
	defer rows.Close()

	applied := 0
	for rows.Next() {
		var record migrationRecord
		if err := rows.StructScan(&record); err != nil {
			return 0, newError(
				ClassPersistenceUnavailable,
				"storage_schema_unknown",
				"verify_migration_history",
				"KuPilot could not recognize the local database schema.",
				correlationID,
				err,
			)
		}
		if record.Version > int64(len(migrations)) {
			return 0, newError(
				ClassPersistenceUnavailable,
				"storage_schema_too_new",
				"verify_migration_history",
				"The local database schema is newer than this KuPilot build supports.",
				correlationID,
				nil,
			)
		}
		if record.Version != int64(applied+1) {
			return 0, newError(
				ClassPersistenceUnavailable,
				"storage_migration_history_invalid",
				"verify_migration_history",
				"KuPilot could not verify the local database migration history.",
				correlationID,
				nil,
			)
		}
		if record.AppliedAtMS < 0 || !validMetadata(record.ApplicationVersion, maxVersionBytes) {
			return 0, newError(
				ClassPersistenceUnavailable,
				"storage_migration_history_invalid",
				"verify_migration_history",
				"KuPilot could not verify the local database migration history.",
				correlationID,
				nil,
			)
		}
		expected := migrations[applied]
		if record.Name != expected.name || record.Checksum != expected.checksum {
			return 0, newError(
				ClassPersistenceUnavailable,
				"storage_migration_checksum_mismatch",
				"verify_migration_history",
				"KuPilot could not verify the local database migration history.",
				correlationID,
				nil,
			)
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		return 0, newError(
			ClassPersistenceUnavailable,
			"storage_schema_unknown",
			"verify_migration_history",
			"KuPilot could not read the local database migration history.",
			correlationID,
			err,
		)
	}
	return applied, nil
}

func applyMigration(
	ctx context.Context,
	db *sqlx.DB,
	migration migration,
	applicationVersion string,
	createLedger bool,
) error {
	if migration.version == 4 && migration.name == "000004_minimal_run_identity.sql" {
		return applyMinimalRunIdentityMigration(ctx, db, migration, applicationVersion)
	}
	return withTx(ctx, db, func(tx *sqlx.Tx) error {
		return executeMigration(ctx, tx, migration, applicationVersion, createLedger)
	})
}

// Migration 4 rebuilds the AgentRun parent table while preserving all child
// rows. SQLite requires foreign-key enforcement to be changed before the
// transaction; the dedicated connection is checked and restored before reuse.
func applyMinimalRunIdentityMigration(
	ctx context.Context,
	db *sqlx.DB,
	migration migration,
	applicationVersion string,
) error {
	connection, err := db.Connx(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	var foreignKeys int
	if err := connection.GetContext(ctx, &foreignKeys, `PRAGMA foreign_keys`); err != nil {
		return err
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign-key enforcement was not enabled before migration")
	}
	if _, err := connection.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	migrationErr := withMigrationConnectionTx(ctx, connection, func(tx *sqlx.Tx) error {
		if err := executeMigration(ctx, tx, migration, applicationVersion, false); err != nil {
			return err
		}
		return verifyForeignKeyGraph(ctx, tx)
	})
	_, restoreErr := connection.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	if restoreErr == nil {
		var restored int
		restoreErr = connection.GetContext(ctx, &restored, `PRAGMA foreign_keys`)
		if restoreErr == nil && restored != 1 {
			restoreErr = fmt.Errorf("foreign-key enforcement was not restored after migration")
		}
	}
	return errors.Join(migrationErr, restoreErr)
}

func withMigrationConnectionTx(
	ctx context.Context,
	connection *sqlx.Conn,
	run func(*sqlx.Tx) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := connection.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	if err := run(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, context.Canceled) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, context.Canceled) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return tx.Commit()
}

type migrationQueryer interface {
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
}

func verifyForeignKeyGraph(ctx context.Context, queryer migrationQueryer) error {
	rows, err := queryer.QueryxContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign-key validation failed after migration")
	}
	return rows.Err()
}

func executeMigration(
	ctx context.Context,
	tx *sqlx.Tx,
	migration migration,
	applicationVersion string,
	createLedger bool,
) error {
	if createLedger {
		if _, err := tx.ExecContext(ctx, migrationLedgerSQL); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (
			version, name, checksum, applied_at_ms, app_version
		) VALUES (?, ?, ?, ?, ?)
	`,
		migration.version,
		migration.name,
		migration.checksum,
		time.Now().UTC().UnixMilli(),
		applicationVersion,
	)
	return err
}
