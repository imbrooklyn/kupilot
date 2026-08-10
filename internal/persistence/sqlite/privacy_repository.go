package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
)

const (
	loadPrivacySQL = `
		SELECT policy_version, origin_hash, categories_json, decision, decided_at_ms, schema_version
		FROM privacy_consents
		WHERE singleton_id = 1
	`
	savePrivacySQL = `
		INSERT INTO privacy_consents (
			singleton_id, policy_version, origin_hash, categories_json,
			decision, decided_at_ms, schema_version
		) VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (singleton_id) DO UPDATE SET
			policy_version = excluded.policy_version,
			origin_hash = excluded.origin_hash,
			categories_json = excluded.categories_json,
			decision = excluded.decision,
			decided_at_ms = excluded.decided_at_ms,
			schema_version = excluded.schema_version
	`
)

type privacyRow struct {
	PolicyVersion  string `db:"policy_version"`
	OriginHash     string `db:"origin_hash"`
	CategoriesJSON string `db:"categories_json"`
	Decision       string `db:"decision"`
	DecidedAtMS    int64  `db:"decided_at_ms"`
	SchemaVersion  int    `db:"schema_version"`
}

// PrivacyRepository persists only the exact application-owned consent tuple.
type PrivacyRepository struct {
	db *DB
}

func NewPrivacyRepository(db *DB) *PrivacyRepository { return &PrivacyRepository{db: db} }

func (repository *PrivacyRepository) LoadPrivacy(ctx context.Context) (application.PrivacyRecord, bool, error) {
	if err := repositoryContext(ctx, repository.db, "load_privacy_consent"); err != nil {
		return application.PrivacyRecord{}, false, err
	}
	var row privacyRow
	if err := repository.db.handle.GetContext(ctx, &row, loadPrivacySQL); errors.Is(err, sql.ErrNoRows) {
		return application.PrivacyRecord{}, false, nil
	} else if err != nil {
		return application.PrivacyRecord{}, false, repositoryFailure(
			repository.db, "privacy_consent_read_failed", "load_privacy_consent",
			"KuPilot could not read model-transfer consent.", err,
		)
	}
	var categories []application.ModelDataCategory
	if err := json.Unmarshal([]byte(row.CategoriesJSON), &categories); err != nil {
		return application.PrivacyRecord{}, false, repositoryFailure(
			repository.db, "privacy_consent_row_invalid", "load_privacy_consent",
			"KuPilot could not read model-transfer consent safely.", err,
		)
	}
	record := application.PrivacyRecord{
		PolicyVersion: row.PolicyVersion, OriginHash: row.OriginHash, Categories: categories,
		Decision: application.PrivacyDecision(row.Decision), DecidedAt: time.UnixMilli(row.DecidedAtMS).UTC(),
		SchemaVersion: row.SchemaVersion,
	}
	if record.Validate() != nil {
		return application.PrivacyRecord{}, false, repositoryFailure(
			repository.db, "privacy_consent_row_invalid", "load_privacy_consent",
			"KuPilot could not read model-transfer consent safely.", application.ErrPrivacyRecord,
		)
	}
	return record, true, nil
}

func (repository *PrivacyRepository) SavePrivacy(ctx context.Context, record application.PrivacyRecord) error {
	if err := repositoryContext(ctx, repository.db, "save_privacy_consent"); err != nil {
		return err
	}
	if record.Validate() != nil {
		return application.ErrPrivacyRecord
	}
	categories, err := json.Marshal(record.Categories)
	if err != nil {
		return application.ErrPrivacyRecord
	}
	result, err := repository.db.handle.ExecContext(
		ctx, savePrivacySQL, record.PolicyVersion, record.OriginHash, string(categories),
		record.Decision, record.DecidedAt.UTC().UnixMilli(), record.SchemaVersion,
	)
	if err != nil {
		return repositoryFailure(
			repository.db, "privacy_consent_write_failed", "save_privacy_consent",
			"KuPilot could not store model-transfer consent.", err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return repositoryFailure(
			repository.db, "privacy_consent_write_failed", "save_privacy_consent",
			"KuPilot could not store model-transfer consent.", err,
		)
	}
	return nil
}

var _ application.PrivacyStore = (*PrivacyRepository)(nil)
