package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
)

const (
	scopePreferenceKey           = "scope.last_context"
	scopePreferenceSchemaVersion = 1
	maxScopePreferenceJSONBytes  = 512
	putScopePreferenceSQL        = `
		INSERT INTO settings (key, value_json, schema_version, updated_at_ms)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			value_json = excluded.value_json,
			schema_version = excluded.schema_version,
			updated_at_ms = excluded.updated_at_ms
		WHERE settings.updated_at_ms <= excluded.updated_at_ms
	`
	getScopePreferenceSQL = `
		SELECT key, value_json, schema_version, updated_at_ms
		FROM settings
		WHERE key = ?
	`
)

var _ application.ScopePreferenceStore = (*ScopePreferenceRepository)(nil)

type scopePreferenceJSON struct {
	Context string `json:"context"`
}

// ScopePreferenceRepository stores only the code-owned last-Context setting.
type ScopePreferenceRepository struct {
	db *DB
}

// NewScopePreferenceRepository binds the typed preference to one database.
func NewScopePreferenceRepository(db *DB) *ScopePreferenceRepository {
	return &ScopePreferenceRepository{db: db}
}

// LoadLastContext reads and strictly validates the one admitted preference.
func (repository *ScopePreferenceRepository) LoadLastContext(
	ctx context.Context,
) (application.ScopePreference, bool, error) {
	if err := repositoryContext(ctx, repository.db, "read_scope_preference"); err != nil {
		return application.ScopePreference{}, false, err
	}
	var row settingRow
	err := repository.db.handle.GetContext(ctx, &row, getScopePreferenceSQL, scopePreferenceKey)
	if errors.Is(err, sql.ErrNoRows) {
		return application.ScopePreference{}, false, nil
	}
	if err != nil {
		return application.ScopePreference{}, false, repositoryFailure(
			repository.db,
			"scope_preference_read_failed",
			"read_scope_preference",
			"Kupilot could not read the Kubernetes Context preference.",
			err,
		)
	}
	preference, err := decodeScopePreference(row)
	if err != nil {
		return application.ScopePreference{}, false, repositoryFailure(
			repository.db,
			"scope_preference_row_invalid",
			"read_scope_preference",
			"Kupilot could not read the Kubernetes Context preference safely.",
			err,
		)
	}
	return preference, true, nil
}

// SaveLastContext stores one successfully activated Context using injected time.
func (repository *ScopePreferenceRepository) SaveLastContext(
	ctx context.Context,
	preference application.ScopePreference,
) error {
	if err := repositoryContext(ctx, repository.db, "write_scope_preference"); err != nil {
		return err
	}
	if preference.Validate() != nil {
		return application.ErrInvalidScopePreference
	}
	encoded, err := json.Marshal(scopePreferenceJSON{Context: preference.Context})
	if err != nil || len(encoded) == 0 || len(encoded) > maxScopePreferenceJSONBytes {
		return application.ErrInvalidScopePreference
	}
	result, err := repository.db.handle.ExecContext(
		ctx,
		putScopePreferenceSQL,
		scopePreferenceKey,
		string(encoded),
		scopePreferenceSchemaVersion,
		preference.UpdatedAt.UnixMilli(),
	)
	if err != nil {
		return repositoryFailure(
			repository.db,
			"scope_preference_write_failed",
			"write_scope_preference",
			"Kupilot could not store the Kubernetes Context preference.",
			err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return repositoryFailure(
			repository.db,
			"scope_preference_write_conflict",
			"write_scope_preference",
			"Kupilot could not store the Kubernetes Context preference.",
			err,
		)
	}
	return nil
}

func decodeScopePreference(row settingRow) (application.ScopePreference, error) {
	if row.Key != scopePreferenceKey || row.SchemaVersion != scopePreferenceSchemaVersion ||
		len(row.ValueJSON) == 0 || len(row.ValueJSON) > maxScopePreferenceJSONBytes {
		return application.ScopePreference{}, application.ErrInvalidScopePreference
	}
	decoder := json.NewDecoder(bytes.NewBufferString(row.ValueJSON))
	decoder.DisallowUnknownFields()
	var stored scopePreferenceJSON
	if err := decoder.Decode(&stored); err != nil {
		return application.ScopePreference{}, application.ErrInvalidScopePreference
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return application.ScopePreference{}, application.ErrInvalidScopePreference
	}
	preference := application.ScopePreference{
		Context:   stored.Context,
		UpdatedAt: time.UnixMilli(row.UpdatedAtMS).UTC(),
	}
	if preference.Validate() != nil {
		return application.ScopePreference{}, application.ErrInvalidScopePreference
	}
	return preference, nil
}
