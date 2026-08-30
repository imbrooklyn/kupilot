package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	putSettingSQL = `
		INSERT INTO settings (key, value_json, schema_version, updated_at_ms)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			value_json = excluded.value_json,
			schema_version = excluded.schema_version,
			updated_at_ms = excluded.updated_at_ms
		WHERE settings.updated_at_ms < excluded.updated_at_ms
	`
	getSettingSQL = `
		SELECT key, value_json, schema_version, updated_at_ms
		FROM settings
		WHERE key = ?
	`
	deleteSettingSQL = `
		DELETE FROM settings
		WHERE key = ?
	`
)

var _ auditcontract.SettingStore = (*SettingsRepository)(nil)

type settingRow struct {
	Key           string `db:"key"`
	ValueJSON     string `db:"value_json"`
	SchemaVersion int    `db:"schema_version"`
	UpdatedAtMS   int64  `db:"updated_at_ms"`
}

// SettingsRepository persists only code-allowlisted typed non-secret settings.
type SettingsRepository struct {
	db *DB
}

// NewSettingsRepository binds typed setting operations to one validated database.
func NewSettingsRepository(db *DB) *SettingsRepository {
	return &SettingsRepository{db: db}
}

// Put inserts or advances one setting using a strictly newer injected timestamp.
func (repository *SettingsRepository) Put(ctx context.Context, setting domain.Setting) error {
	if err := repositoryContext(ctx, repository.db, "put_setting"); err != nil {
		return err
	}
	if setting.Validate() != nil {
		return auditcontract.ErrInvalidRepositoryRequest
	}
	result, err := repository.db.handle.ExecContext(
		ctx,
		putSettingSQL,
		setting.Key,
		strconv.FormatInt(setting.IntegerValue, 10),
		setting.SchemaVersion,
		setting.UpdatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return repositoryFailure(repository.db, "setting_put_failed", "put_setting", "Kupilot could not store the setting.", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return repositoryFailure(repository.db, "setting_put_failed", "put_setting", "Kupilot could not store the setting.", err)
	}
	if affected != 1 {
		return auditcontract.ErrSettingConflict
	}
	return nil
}

// Get returns one exact allowlisted setting after strict type mapping.
func (repository *SettingsRepository) Get(ctx context.Context, key domain.SettingKey) (domain.Setting, error) {
	if err := repositoryContext(ctx, repository.db, "get_setting"); err != nil {
		return domain.Setting{}, err
	}
	if !key.Valid() {
		return domain.Setting{}, auditcontract.ErrInvalidRepositoryRequest
	}
	var row settingRow
	if err := repository.db.handle.GetContext(ctx, &row, getSettingSQL, key); errors.Is(err, sql.ErrNoRows) {
		return domain.Setting{}, auditcontract.ErrSettingNotFound
	} else if err != nil {
		return domain.Setting{}, repositoryFailure(repository.db, "setting_read_failed", "get_setting", "Kupilot could not read the setting.", err)
	}
	setting, err := row.domainSetting()
	if err != nil || setting.Key != key {
		return domain.Setting{}, repositoryFailure(repository.db, "setting_row_invalid", "get_setting", "Kupilot could not read the setting safely.", err)
	}
	return setting, nil
}

// Delete removes one exact allowlisted setting.
func (repository *SettingsRepository) Delete(ctx context.Context, key domain.SettingKey) error {
	if err := repositoryContext(ctx, repository.db, "delete_setting"); err != nil {
		return err
	}
	if !key.Valid() {
		return auditcontract.ErrInvalidRepositoryRequest
	}
	result, err := repository.db.handle.ExecContext(ctx, deleteSettingSQL, key)
	if err != nil {
		return repositoryFailure(repository.db, "setting_delete_failed", "delete_setting", "Kupilot could not delete the setting.", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return repositoryFailure(repository.db, "setting_delete_failed", "delete_setting", "Kupilot could not delete the setting.", err)
	}
	if affected != 1 {
		return auditcontract.ErrSettingNotFound
	}
	return nil
}

func (row settingRow) domainSetting() (domain.Setting, error) {
	integerValue, err := strconv.ParseInt(row.ValueJSON, 10, 64)
	if err != nil {
		return domain.Setting{}, err
	}
	setting := domain.Setting{
		Key:           domain.SettingKey(row.Key),
		IntegerValue:  integerValue,
		SchemaVersion: row.SchemaVersion,
		UpdatedAt:     time.UnixMilli(row.UpdatedAtMS).UTC(),
	}
	if err := setting.Validate(); err != nil {
		return domain.Setting{}, err
	}
	return setting, nil
}
