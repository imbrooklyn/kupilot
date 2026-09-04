package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	loadPrivacySQL = `
		SELECT role, policy_version, origin_hash, prometheus_origin_hash,
			loki_origin_hash, categories_json, decision, decided_at_ms, schema_version
		FROM privacy_consents
		WHERE role = ?
	`
	savePrivacySQL = `
		INSERT INTO privacy_consents (
			role, policy_version, origin_hash, prometheus_origin_hash,
			loki_origin_hash, categories_json, decision, decided_at_ms, schema_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (role) DO UPDATE SET
			policy_version = excluded.policy_version,
			origin_hash = excluded.origin_hash,
			prometheus_origin_hash = excluded.prometheus_origin_hash,
			loki_origin_hash = excluded.loki_origin_hash,
			categories_json = excluded.categories_json,
			decision = excluded.decision,
			decided_at_ms = excluded.decided_at_ms,
			schema_version = excluded.schema_version
	`
)

type privacyRow struct {
	Role                 string `db:"role"`
	PolicyVersion        string `db:"policy_version"`
	OriginHash           string `db:"origin_hash"`
	PrometheusOriginHash string `db:"prometheus_origin_hash"`
	LokiOriginHash       string `db:"loki_origin_hash"`
	CategoriesJSON       string `db:"categories_json"`
	Decision             string `db:"decision"`
	DecidedAtMS          int64  `db:"decided_at_ms"`
	SchemaVersion        int    `db:"schema_version"`
}

// PrivacyRepository persists only the exact application-owned consent tuple.
type PrivacyRepository struct {
	db   *DB
	role domain.ModelRole
}

func NewPrivacyRepository(db *DB) *PrivacyRepository {
	return &PrivacyRepository{db: db, role: domain.ModelRoleAgent}
}

// NewRolePrivacyRepository binds one repository instance to one exact model role.
func NewRolePrivacyRepository(db *DB, role domain.ModelRole) *PrivacyRepository {
	return &PrivacyRepository{db: db, role: role}
}

func (repository *PrivacyRepository) LoadPrivacy(ctx context.Context) (application.PrivacyRecord, bool, error) {
	if err := repositoryContext(ctx, repository.db, "load_privacy_consent"); err != nil {
		return application.PrivacyRecord{}, false, err
	}
	var row privacyRow
	if !repository.role.Valid() {
		return application.PrivacyRecord{}, false, application.ErrPrivacyRecord
	}
	if err := repository.db.handle.GetContext(ctx, &row, loadPrivacySQL, repository.role); errors.Is(err, sql.ErrNoRows) {
		return application.PrivacyRecord{}, false, nil
	} else if err != nil {
		return application.PrivacyRecord{}, false, repositoryFailure(
			repository.db, "privacy_consent_read_failed", "load_privacy_consent",
			"Kupilot could not read model-transfer consent.", err,
		)
	}
	var categories []application.ModelDataCategory
	if err := json.Unmarshal([]byte(row.CategoriesJSON), &categories); err != nil {
		return application.PrivacyRecord{}, false, repositoryFailure(
			repository.db, "privacy_consent_row_invalid", "load_privacy_consent",
			"Kupilot could not read model-transfer consent safely.", err,
		)
	}
	record := application.PrivacyRecord{
		Role: domain.ModelRole(row.Role), PolicyVersion: row.PolicyVersion, OriginHash: row.OriginHash,
		PrometheusOriginHash: row.PrometheusOriginHash, LokiOriginHash: row.LokiOriginHash, Categories: categories,
		Decision: application.PrivacyDecision(row.Decision), DecidedAt: time.UnixMilli(row.DecidedAtMS).UTC(),
		SchemaVersion: row.SchemaVersion,
	}
	if record.Validate() != nil || record.Role != repository.role {
		return application.PrivacyRecord{}, false, repositoryFailure(
			repository.db, "privacy_consent_row_invalid", "load_privacy_consent",
			"Kupilot could not read model-transfer consent safely.", application.ErrPrivacyRecord,
		)
	}
	return record, true, nil
}

func (repository *PrivacyRepository) SavePrivacy(ctx context.Context, record application.PrivacyRecord) error {
	if err := repositoryContext(ctx, repository.db, "save_privacy_consent"); err != nil {
		return err
	}
	if record.Validate() != nil || !repository.role.Valid() || record.Role != repository.role {
		return application.ErrPrivacyRecord
	}
	categories, err := json.Marshal(record.Categories)
	if err != nil {
		return application.ErrPrivacyRecord
	}
	result, err := repository.db.handle.ExecContext(
		ctx, savePrivacySQL, record.Role, record.PolicyVersion, record.OriginHash,
		record.PrometheusOriginHash, record.LokiOriginHash, string(categories),
		record.Decision, record.DecidedAt.UTC().UnixMilli(), record.SchemaVersion,
	)
	if err != nil {
		return repositoryFailure(
			repository.db, "privacy_consent_write_failed", "save_privacy_consent",
			"Kupilot could not store model-transfer consent.", err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return repositoryFailure(
			repository.db, "privacy_consent_write_failed", "save_privacy_consent",
			"Kupilot could not store model-transfer consent.", err,
		)
	}
	return nil
}

var _ application.PrivacyStore = (*PrivacyRepository)(nil)
