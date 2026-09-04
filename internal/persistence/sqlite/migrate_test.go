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
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/imbrooklyn/kupilot/internal/domain"
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

func TestReleasedMigrationMatrixPreservesV01V02V03Data(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	db := sqlx.NewDb(raw, driverName)
	databaseClosed := false
	defer func() {
		if !databaseClosed {
			_ = db.Close()
		}
	}()
	migrations, err := loadMigrations()
	if err != nil || len(migrations) != 9 {
		t.Fatalf("loadMigrations() = %d/%v", len(migrations), err)
	}
	releasedChecksums := []string{
		"5ebc0b465e47bed2a1023197ad60178f2c6101fd7e3dda77b241d7b2caab6759",
		"6306bff703fc8b01f84bcbe36cbcf0c655a893e19d13b234209fcd0366d679bf",
		"b2301209285dbebfcdf9c08fa1e13383869bee6216eb42f924954e42904a4227",
		"7f5f1f6f3826e33fae784ff7c944f07250db666209100db40d2c2a646e737b9d",
	}
	for index, checksum := range releasedChecksums {
		if migrations[index].checksum != checksum {
			t.Fatalf("released migration %d checksum changed", index+1)
		}
	}
	for index := 0; index < 2; index++ {
		if err := applyMigration(context.Background(), db, migrations[index], "0.1.0", index == 0); err != nil {
			t.Fatalf("apply v0.1 migration %d error = %v", index+1, err)
		}
	}

	sessionID := "00000000-0000-7000-8000-000000009901"
	messageID := "00000000-0000-7000-8000-000000009902"
	runID := "00000000-0000-7000-8000-000000009903"
	modelRequestID := "00000000-0000-7000-8000-000000009904"
	invocationID := "00000000-0000-7000-8000-000000009905"
	evidenceID := "00000000-0000-7000-8000-000000009906"
	diagnosisID := "00000000-0000-7000-8000-000000009907"
	auditID := "00000000-0000-7000-8000-000000009908"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions (id, title, status, privacy_mode, last_context, last_namespace, version, created_at_ms, updated_at_ms) VALUES (?, 'Released Session', 'active', 'standard', 'selected', 'team-a', 1, 1000, 1010)`, []any{sessionID}},
		{`INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms) VALUES (?, ?, 'user', 'Safe released question', 'plain', 'committed', ?, 1001)`, []any{messageID, sessionID, domain.MessageContentHash("Safe released question")}},
		{`INSERT INTO agent_runs (id, session_id, request_message_id, status, scope_context, scope_namespace, scope_generation, prompt_version, tool_catalog_version, step_count, tool_call_count, model_request_count, started_at_ms, finished_at_ms) VALUES (?, ?, ?, 'completed', 'selected', 'team-a', 7, 'prompt-v1', 'tools-v1', 1, 1, 1, 1001, 1009)`, []any{runID, sessionID, messageID}},
		{`UPDATE messages SET run_id = ? WHERE id = ?`, []any{runID, messageID}},
		{`INSERT INTO model_requests (id, run_id, sequence, provider_kind, model, status, prompt_version, prompt_fingerprint, started_at_ms, finished_at_ms) VALUES (?, ?, 1, 'openai_compatible', 'released-model', 'succeeded', 'prompt-v1', ?, 1002, 1003)`, []any{modelRequestID, runID, strings.Repeat("d", 64)}},
		{`INSERT INTO tool_invocations (id, run_id, sequence, tool_name, tool_version, purpose, arguments_json, arguments_digest, status, result_summary, returned_bytes, evidence_count, started_at_ms, finished_at_ms) VALUES (?, ?, 1, 'get_resource', 'tools-v1', 'Inspect one Pod.', '{}', ?, 'succeeded', 'One safe observation.', 1, 1, 1003, 1004)`, []any{invocationID, runID, strings.Repeat("e", 64)}},
		{`INSERT INTO evidence_items (id, run_id, invocation_id, category, resource_ref_json, fact, source_path, severity, resource_version, redaction_count, truncated, fingerprint, observed_at_ms) VALUES (?, ?, ?, 'resource_status', ?, 'The Pod phase is Pending.', 'status.phase', 'info', '12', 0, 0, ?, 1004)`, []any{evidenceID, runID, invocationID, `{"api_version":"v1","kind":"Pod","namespace":"team-a","name":"sample-pod"}`, strings.Repeat("f", 64)}},
		{`INSERT INTO diagnoses (id, run_id, confirmed_json, hypotheses_json, missing_json, actions_json, answer_markdown, validation_warnings_json, observed_from_ms, observed_to_ms, created_at_ms) VALUES (?, ?, '[]', '[]', '[]', '[]', 'Safe released diagnosis.', '[]', 1004, 1004, 1005)`, []any{diagnosisID, runID}},
		{`INSERT INTO audit_events (id, session_id, run_id, event_type, actor, outcome, scope_context, scope_namespace, scope_generation, details_json, correlation_id, occurred_at_ms) VALUES (?, ?, ?, 'run_completed', 'system', 'success', 'selected', 'team-a', 7, '{}', 'released-matrix', 1006)`, []any{auditID, sessionID, runID}},
		{`INSERT INTO settings (key, value_json, schema_version, updated_at_ms) VALUES ('retention.operational_detail_days', '{"integer_value":30}', 1, 1007)`, nil},
		{`INSERT INTO privacy_consents (singleton_id, policy_version, origin_hash, categories_json, decision, decided_at_ms, schema_version) VALUES (1, 'privacy-policy-v1', ?, '["user_question"]', 'accepted', 1008, 1)`, []any{strings.Repeat("a", 64)}},
	}
	for index, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("v0.1 fixture statement %d error = %v", index, err)
		}
	}

	if err := applyMigration(context.Background(), db, migrations[2], "0.2.0", false); err != nil {
		t.Fatalf("apply v0.2 migration error = %v", err)
	}
	approvalID := "00000000-0000-7000-8000-000000009909"
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO approvals (
			id, run_id, session_id, operation, operation_schema_version, policy_version,
			scope_context, scope_namespace, scope_generation,
			target_api_version, target_kind, target_namespace, deployment_name, deployment_uid,
			template_fingerprint, deployment_generation, reason_summary, risk_summary,
			operation_digest, nonce_hash, status, state_reason,
			requested_at_ms, expires_at_ms, state_changed_at_ms
		) VALUES (
			?, ?, ?, 'restart_deployment', 'restart_deployment/v1', 'restart-deployment-approval/v1',
			'selected', 'team-a', 7,
			'apps/v1', 'Deployment', 'team-a', 'sample-deployment', 'deployment-uid',
			?, 3, 'Restart one exact Deployment.',
			'Restarting the Deployment replaces Pods and may temporarily reduce availability.',
			?, ?, 'pending', '', 1100, 61100, 1100
		)
	`, approvalID, runID, sessionID, strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)); err != nil {
		t.Fatalf("v0.2 approval fixture error = %v", err)
	}

	if err := applyMigration(context.Background(), db, migrations[3], "0.3.0", false); err != nil {
		t.Fatalf("apply v0.3 migration error = %v", err)
	}
	if err := applyMigration(context.Background(), db, migrations[4], "0.4.0", false); err != nil {
		t.Fatalf("apply v0.4 migration error = %v", err)
	}
	if err := applyMigration(context.Background(), db, migrations[5], "0.5.0", false); err != nil {
		t.Fatalf("apply v0.5 model-context migration error = %v", err)
	}
	if err := applyMigration(context.Background(), db, migrations[6], "0.5.0", false); err != nil {
		t.Fatalf("apply v0.5 action-permission migration error = %v", err)
	}
	if err := applyMigration(context.Background(), db, migrations[7], "0.5.0", false); err != nil {
		t.Fatalf("apply v0.5 resource-Evidence migration error = %v", err)
	}
	if err := applyMigration(context.Background(), db, migrations[8], "0.5.0", false); err != nil {
		t.Fatalf("apply v0.5 observability provenance migration error = %v", err)
	}
	wantRows := map[string]int{
		"sessions": 1, "messages": 1, "agent_runs": 1, "model_requests": 1,
		"tool_invocations": 1, "evidence_items": 1, "diagnoses": 1, "audit_events": 1,
		"settings": 1, "privacy_consents": 1, "approvals": 0, "approval_decisions": 0,
		"legacy_restart_approvals": 1, "legacy_restart_approval_decisions": 0,
		"action_reviews": 0, "schema_migrations": 9, "session_context_summaries": 0,
	}
	for table, want := range wantRows {
		var got int
		if err := db.GetContext(context.Background(), &got, "SELECT count(rowid) FROM "+table); err != nil {
			t.Fatalf("count %s error = %v", table, err)
		}
		if got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	var migratedEvidence struct {
		ResourceTypeJSON sql.NullString `db:"resource_type_json"`
		PolicyVersion    sql.NullString `db:"resource_policy_version"`
		PolicyGeneration sql.NullInt64  `db:"policy_generation"`
		Partial          int64          `db:"partial"`
	}
	if err := db.GetContext(context.Background(), &migratedEvidence, `
		SELECT resource_type_json, resource_policy_version, policy_generation, partial
		FROM evidence_items WHERE id = ?
	`, evidenceID); err != nil || migratedEvidence.ResourceTypeJSON.Valid || migratedEvidence.PolicyVersion.Valid ||
		migratedEvidence.PolicyGeneration.Valid || migratedEvidence.Partial != 0 {
		t.Fatalf("legacy Evidence provenance defaults = %#v/%v", migratedEvidence, err)
	}
	var retainedMessageID string
	if err := db.GetContext(context.Background(), &retainedMessageID, `
		SELECT retained_request_message_id FROM agent_runs WHERE id = ?
	`, runID); err != nil || retainedMessageID != messageID {
		t.Fatalf("retained request Message = %q/%v", retainedMessageID, err)
	}
	var approvalState string
	if err := db.GetContext(context.Background(), &approvalState, `SELECT status FROM legacy_restart_approvals WHERE id = ?`, approvalID); err != nil || approvalState != "cancelled" {
		t.Fatalf("migrated approval state = %q/%v", approvalState, err)
	}
	var migratedConsent struct {
		Role                 string `db:"role"`
		PolicyVersion        string `db:"policy_version"`
		PrometheusOriginHash string `db:"prometheus_origin_hash"`
		LokiOriginHash       string `db:"loki_origin_hash"`
		CategoriesJSON       string `db:"categories_json"`
		Decision             string `db:"decision"`
		SchemaVersion        int    `db:"schema_version"`
	}
	if err := db.GetContext(context.Background(), &migratedConsent, `
		SELECT role, policy_version, prometheus_origin_hash, loki_origin_hash, categories_json, decision, schema_version
		FROM privacy_consents
	`); err != nil || migratedConsent.Role != "agent" || migratedConsent.PolicyVersion != "privacy-policy-v1" ||
		migratedConsent.PrometheusOriginHash != "" || migratedConsent.LokiOriginHash != "" ||
		migratedConsent.CategoriesJSON != `["user_question","safe_conversation_context","resource_names_and_references","projected_kubernetes_status","projected_kubernetes_events","projected_kubernetes_metrics"]` ||
		migratedConsent.Decision != "pending" || migratedConsent.SchemaVersion != 3 {
		t.Fatalf("migrated consent = %#v/%v", migratedConsent, err)
	}
	var migratedModelRequest struct {
		ProfileName      string `db:"profile_name"`
		ModelRole        string `db:"model_role"`
		Invocation       string `db:"invocation"`
		ReservedCostUnit int    `db:"reserved_cost_units"`
	}
	if err := db.GetContext(context.Background(), &migratedModelRequest, `
		SELECT profile_name, model_role, invocation, reserved_cost_units
		FROM model_requests WHERE id = ?
	`, modelRequestID); err != nil || migratedModelRequest.ProfileName != "agent" ||
		migratedModelRequest.ModelRole != "agent" || migratedModelRequest.Invocation != "agent" ||
		migratedModelRequest.ReservedCostUnit != 1 {
		t.Fatalf("migrated model request = %#v/%v", migratedModelRequest, err)
	}
	wantVersions := []string{"0.1.0", "0.1.0", "0.2.0", "0.3.0", "0.4.0", "0.5.0", "0.5.0", "0.5.0", "0.5.0"}
	assertMigrationApplicationVersions(t, db, wantVersions)
	assertNoMigrationForeignKeyViolation(t, db)

	if err := db.Close(); err != nil {
		t.Fatalf("released matrix Close() error = %v", err)
	}
	databaseClosed = true
	reopened, err := Open(context.Background(), OpenOptions{
		StateDir: stateDir, ApplicationVersion: "0.4.0", CorrelationID: "released-migration-reopen",
	})
	if err != nil {
		t.Fatalf("Open(released matrix) error = %v", err)
	}
	defer reopened.Close()
	assertInitialSchema(t, reopened.handle.DB)
	var reopenedRows int
	if err := reopened.handle.GetContext(context.Background(), &reopenedRows, `SELECT count(id) FROM legacy_restart_approvals WHERE id = ?`, approvalID); err != nil || reopenedRows != 1 {
		t.Fatalf("reopened approval rows = %d/%v", reopenedRows, err)
	}
}

func assertMigrationApplicationVersions(t *testing.T, db *sqlx.DB, wantVersions []string) {
	t.Helper()
	rows, err := db.QueryxContext(context.Background(), `SELECT version, app_version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("migration application query error = %v", err)
	}
	defer rows.Close()
	versionIndex := 0
	for rows.Next() {
		var version int
		var applicationVersion string
		if err := rows.Scan(&version, &applicationVersion); err != nil {
			t.Fatalf("migration application scan error = %v", err)
		}
		if versionIndex >= len(wantVersions) || version != versionIndex+1 || applicationVersion != wantVersions[versionIndex] {
			t.Fatalf("migration application row = %d/%q", version, applicationVersion)
		}
		versionIndex++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("migration application rows error = %v", err)
	}
	if versionIndex != len(wantVersions) {
		t.Fatalf("migration application row count = %d, want %d", versionIndex, len(wantVersions))
	}
}

func assertNoMigrationForeignKeyViolation(t *testing.T, db *sqlx.DB) {
	t.Helper()
	rows, err := db.QueryxContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check error = %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("released migration matrix has a foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_check rows error = %v", err)
	}
}

func TestApprovalRuntimeMigrationRejectsUnexpectedReleasedRowsWithoutDataLoss(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	db := sqlx.NewDb(raw, driverName)
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := loadMigrations()
	if err != nil || len(migrations) != 9 {
		t.Fatalf("loadMigrations() = %d/%v", len(migrations), err)
	}
	for index := 0; index < 2; index++ {
		if err := applyMigration(context.Background(), db, migrations[index], "upgrade-fixture", index == 0); err != nil {
			t.Fatalf("applyMigration(%d) error = %v", index+1, err)
		}
	}
	sessionID := "00000000-0000-7000-8000-000000009701"
	messageID := "00000000-0000-7000-8000-000000009702"
	runID := "00000000-0000-7000-8000-000000009703"
	approvalID := "00000000-0000-7000-8000-000000009704"
	now := time.UnixMilli(1_000).UTC()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions (id, title, status, privacy_mode, version, created_at_ms, updated_at_ms) VALUES (?, ?, 'active', 'standard', 1, ?, ?)`, []any{sessionID, "Upgrade fixture", now.UnixMilli(), now.UnixMilli()}},
		{`INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms) VALUES (?, ?, 'user', 'Safe fixture', 'plain', 'committed', ?, ?)`, []any{messageID, sessionID, strings.Repeat("1", 64), now.UnixMilli()}},
		{`INSERT INTO agent_runs (id, session_id, request_message_id, status, scope_context, scope_namespace, scope_generation, prompt_version, tool_catalog_version, started_at_ms) VALUES (?, ?, ?, 'running', 'test-context', 'test-namespace', 1, 'prompt-v1', 'tools-v1', ?)`, []any{runID, sessionID, messageID, now.UnixMilli()}},
		{`INSERT INTO approvals (id, run_id, session_id, operation, scope_context, scope_namespace, scope_generation, target_ref_json, canonical_parameters_json, operation_digest, human_summary, risk_summary, status, policy_version, requested_at_ms, expires_at_ms) VALUES (?, ?, ?, 'restart_deployment', 'test-context', 'test-namespace', 1, ?, '{}', ?, 'Safe legacy summary', 'Safe legacy risk', 'pending', 'policy-v1', ?, ?)`, []any{approvalID, runID, sessionID, `{"api_version":"apps/v1","kind":"Deployment","namespace":"test-namespace","name":"sample"}`, strings.Repeat("2", 64), now.UnixMilli(), now.Add(time.Minute).UnixMilli()}},
	}
	for index, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("upgrade fixture statement %d error = %v", index, err)
		}
	}
	if err := applyMigration(context.Background(), db, migrations[2], "upgrade-fixture", false); err == nil {
		t.Fatal("approval runtime migration error = nil")
	}
	var preserved int
	if err := db.GetContext(context.Background(), &preserved, `SELECT count(id) FROM approvals WHERE id = ?`, approvalID); err != nil {
		t.Fatalf("released approval query error = %v", err)
	}
	var migrationRows int
	if err := db.GetContext(context.Background(), &migrationRows, `SELECT count(version) FROM schema_migrations WHERE version = 3`); err != nil {
		t.Fatalf("migration record query error = %v", err)
	}
	var decisionTableRows int
	if err := db.GetContext(context.Background(), &decisionTableRows, `SELECT count(name) FROM sqlite_schema WHERE type = 'table' AND name = 'approval_decisions'`); err != nil {
		t.Fatalf("runtime table query error = %v", err)
	}
	if preserved != 1 || migrationRows != 0 || decisionTableRows != 0 {
		t.Fatalf("preserved/migration/runtime table = %d/%d/%d, want 1/0/0", preserved, migrationRows, decisionTableRows)
	}
}

func TestMinimalRunIdentityMigrationPreservesReleasedSessionGraph(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	db := sqlx.NewDb(raw, driverName)
	migrations, err := loadMigrations()
	if err != nil || len(migrations) != 9 {
		t.Fatalf("loadMigrations() = %d/%v", len(migrations), err)
	}
	for index := 0; index < 3; index++ {
		if err := applyMigration(context.Background(), db, migrations[index], "released-v3", index == 0); err != nil {
			t.Fatalf("applyMigration(%d) error = %v", index+1, err)
		}
	}
	sessionID := "00000000-0000-7000-8000-000000009801"
	messageID := "00000000-0000-7000-8000-000000009802"
	runID := "00000000-0000-7000-8000-000000009803"
	auditID := "00000000-0000-7000-8000-000000009804"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions (id, title, status, privacy_mode, version, created_at_ms, updated_at_ms) VALUES (?, 'Released Session', 'active', 'standard', 1, 9800, 9800)`, []any{sessionID}},
		{`INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms) VALUES (?, ?, 'user', 'Safe released content', 'plain', 'committed', ?, 9801)`, []any{messageID, sessionID, domain.MessageContentHash("Safe released content")}},
		{`INSERT INTO agent_runs (id, session_id, request_message_id, status, scope_context, scope_namespace, scope_generation, prompt_version, tool_catalog_version, started_at_ms) VALUES (?, ?, ?, 'running', 'test-context', 'test-namespace', 1, 'prompt-v1', 'tools-v1', 9801)`, []any{runID, sessionID, messageID}},
		{`UPDATE messages SET run_id = ? WHERE id = ?`, []any{runID, messageID}},
		{`INSERT INTO audit_events (id, session_id, run_id, event_type, actor, outcome, scope_context, scope_namespace, scope_generation, details_json, occurred_at_ms) VALUES (?, ?, ?, 'run_started', 'user', 'success', 'test-context', 'test-namespace', 1, '{}', 9802)`, []any{auditID, sessionID, runID}},
	}
	for index, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
			t.Fatalf("released graph statement %d error = %v", index, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("released fixture Close() error = %v", err)
	}

	upgraded, err := Open(context.Background(), OpenOptions{
		StateDir: stateDir, ApplicationVersion: "upgrade-v4", CorrelationID: "minimal-run-upgrade",
	})
	if err != nil {
		t.Fatalf("Open(upgrade) error = %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	for table, want := range map[string]int{"sessions": 1, "messages": 1, "agent_runs": 1, "audit_events": 1} {
		var got int
		if err := upgraded.handle.GetContext(context.Background(), &got, "SELECT count(rowid) FROM "+table); err != nil {
			t.Fatalf("count %s error = %v", table, err)
		}
		if got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	var retainedMessageID string
	if err := upgraded.handle.GetContext(context.Background(), &retainedMessageID, `
		SELECT retained_request_message_id FROM agent_runs WHERE id = ?
	`, runID); err != nil || retainedMessageID != messageID {
		t.Fatalf("retained request Message = %q/%v", retainedMessageID, err)
	}
	var retainedForeignKeyCount int
	if err := upgraded.handle.GetContext(context.Background(), &retainedForeignKeyCount, `
		SELECT count(id)
		FROM pragma_foreign_key_list('agent_runs')
		WHERE "table" = 'messages' AND "from" = 'retained_request_message_id'
	`); err != nil || retainedForeignKeyCount != 1 {
		t.Fatalf("retained request foreign key count = %d/%v", retainedForeignKeyCount, err)
	}
	rows, err := upgraded.handle.QueryxContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check error = %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("upgraded Session graph has a foreign-key violation")
	}
	var migrationApplication string
	if err := upgraded.handle.GetContext(context.Background(), &migrationApplication, `
		SELECT app_version FROM schema_migrations WHERE version = 4
	`); err != nil || migrationApplication != "upgrade-v4" {
		t.Fatalf("migration 4 application = %q/%v", migrationApplication, err)
	}
}

func TestMinimalRunIdentityMigrationRollsBackForeignKeyFailure(t *testing.T) {
	stateDir := testStateDir(t)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	raw.SetMaxOpenConns(1)
	db := sqlx.NewDb(raw, driverName)
	migrations, err := loadMigrations()
	if err != nil || len(migrations) != 9 {
		t.Fatalf("loadMigrations() = %d/%v", len(migrations), err)
	}
	for index := 0; index < 3; index++ {
		if err := applyMigration(context.Background(), db, migrations[index], "released-v3", index == 0); err != nil {
			t.Fatalf("applyMigration(%d) error = %v", index+1, err)
		}
	}
	sessionID := "00000000-0000-7000-8000-000000009811"
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sessions (id, title, status, privacy_mode, version, created_at_ms, updated_at_ms)
		VALUES (?, 'Preserved Session', 'active', 'standard', 1, 9810, 9810)
	`, sessionID); err != nil {
		t.Fatalf("Session setup error = %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys error = %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO audit_events (
			id, session_id, run_id, event_type, actor, outcome, details_json, occurred_at_ms
		) VALUES (?, ?, ?, 'run_started', 'user', 'success', '{}', 9811)
	`, "00000000-0000-7000-8000-000000009812", sessionID, "00000000-0000-7000-8000-000000009899"); err != nil {
		t.Fatalf("orphan setup error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("released fixture Close() error = %v", err)
	}

	_, err = Open(context.Background(), OpenOptions{
		StateDir: stateDir, ApplicationVersion: "upgrade-v4", CorrelationID: "minimal-run-rollback",
	})
	assertStorageError(t, err, ClassPersistenceUnavailable, "storage_migration_failed")
	raw = openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
	defer raw.Close()
	var migrationCount int
	if err := raw.QueryRowContext(context.Background(), `SELECT count(version) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("migration count error = %v", err)
	}
	var retainedColumnCount int
	if err := raw.QueryRowContext(context.Background(), `
		SELECT count(name) FROM pragma_table_info('agent_runs') WHERE name = 'retained_request_message_id'
	`).Scan(&retainedColumnCount); err != nil {
		t.Fatalf("retained column query error = %v", err)
	}
	var sessionCount int
	if err := raw.QueryRowContext(context.Background(), `SELECT count(id) FROM sessions WHERE id = ?`, sessionID).Scan(&sessionCount); err != nil {
		t.Fatalf("Session count error = %v", err)
	}
	if migrationCount != 3 || retainedColumnCount != 0 || sessionCount != 1 {
		t.Fatalf("rollback migration/column/Session = %d/%d/%d, want 3/0/1", migrationCount, retainedColumnCount, sessionCount)
	}
}

func TestMigrateV04RuntimeLimitsPreservesGraphAndRollsBackFailure(t *testing.T) {
	t.Run("upgrade preserves released rows and enforces role limits", func(t *testing.T) {
		stateDir := testStateDir(t)
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
		raw.SetMaxOpenConns(1)
		db := sqlx.NewDb(raw, driverName)
		t.Cleanup(func() { _ = db.Close() })
		migrations, err := loadMigrations()
		if err != nil || len(migrations) != 9 {
			t.Fatalf("loadMigrations() = %d/%v", len(migrations), err)
		}
		for index := 0; index < 4; index++ {
			if err := applyMigration(context.Background(), db, migrations[index], "released-v3", index == 0); err != nil {
				t.Fatalf("applyMigration(%d) error = %v", index+1, err)
			}
		}

		sessionID := "00000000-0000-7000-8000-000000009821"
		userMessageID := "00000000-0000-7000-8000-000000009822"
		runID := "00000000-0000-7000-8000-000000009823"
		assistantMessageID := "00000000-0000-7000-8000-000000009824"
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO sessions (id, title, status, privacy_mode, version, created_at_ms, updated_at_ms) VALUES (?, 'Released Session', 'active', 'standard', 1, 9820, 9820)`, []any{sessionID}},
			{`INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms) VALUES (?, ?, 'user', 'Released question', 'plain', 'committed', ?, 9821)`, []any{userMessageID, sessionID, domain.MessageContentHash("Released question")}},
			{`INSERT INTO agent_runs (id, session_id, request_message_id, retained_request_message_id, status, scope_context, scope_namespace, scope_generation, prompt_version, tool_catalog_version, started_at_ms, finished_at_ms) VALUES (?, ?, ?, ?, 'completed', 'test-context', 'test-namespace', 1, 'prompt-v1', 'tools-v1', 9821, 9822)`, []any{runID, sessionID, userMessageID, userMessageID}},
			{`UPDATE messages SET run_id = ? WHERE id = ?`, []any{runID, userMessageID}},
			{`INSERT INTO messages (id, session_id, run_id, role, content, content_format, status, content_hash, created_at_ms) VALUES (?, ?, ?, 'assistant', 'Released answer', 'markdown', 'committed', ?, 9822)`, []any{assistantMessageID, sessionID, runID, domain.MessageContentHash("Released answer")}},
		}
		for index, statement := range statements {
			if _, err := db.ExecContext(context.Background(), statement.query, statement.args...); err != nil {
				t.Fatalf("released fixture statement %d error = %v", index, err)
			}
		}

		if err := applyMigration(context.Background(), db, migrations[4], "0.4.0", false); err != nil {
			t.Fatalf("apply v0.4 runtime limits migration error = %v", err)
		}
		var messageCount int
		if err := db.GetContext(context.Background(), &messageCount, `SELECT count(id) FROM messages`); err != nil || messageCount != 2 {
			t.Fatalf("preserved Message count/error = %d/%v", messageCount, err)
		}
		var migrationCount int
		if err := db.GetContext(context.Background(), &migrationCount, `SELECT count(version) FROM schema_migrations`); err != nil || migrationCount != 5 {
			t.Fatalf("migration count/error = %d/%v", migrationCount, err)
		}
		var indexCount int
		if err := db.GetContext(context.Background(), &indexCount, `SELECT count(name) FROM sqlite_schema WHERE type = 'index' AND name = 'messages_session_created_idx'`); err != nil || indexCount != 1 {
			t.Fatalf("Message index count/error = %d/%v", indexCount, err)
		}
		assertNoMigrationForeignKeyViolation(t, db)
		if _, err := db.ExecContext(context.Background(), `
			UPDATE agent_runs
			SET step_count = 128, tool_call_count = 256, model_request_count = 64
			WHERE id = ?
		`, runID); err != nil {
			t.Fatalf("expanded runtime counter update error = %v", err)
		}
		if _, err := db.ExecContext(context.Background(), `UPDATE agent_runs SET step_count = 129 WHERE id = ?`, runID); err == nil {
			t.Fatal("runtime counter above the hard ceiling was accepted by SQLite")
		}

		longAnswer := strings.Repeat("a", 64*1024+1)
		if _, err := db.ExecContext(context.Background(), `
			INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms)
			VALUES (?, ?, 'assistant', ?, 'markdown', 'committed', ?, 9823)
		`, "00000000-0000-7000-8000-000000009825", sessionID, longAnswer, domain.MessageContentHash(longAnswer)); err != nil {
			t.Fatalf("assistant above the question limit error = %v", err)
		}
		if _, err := db.ExecContext(context.Background(), `
			INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms)
			VALUES (?, ?, 'user', ?, 'plain', 'committed', ?, 9824)
		`, "00000000-0000-7000-8000-000000009826", sessionID, longAnswer, domain.MessageContentHash(longAnswer)); err == nil {
			t.Fatal("user above the question limit was accepted by SQLite")
		}
	})

	t.Run("failure preserves the old table and migration ledger", func(t *testing.T) {
		stateDir := testStateDir(t)
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		raw := openRawDatabase(t, filepath.Join(stateDir, databaseFilename))
		raw.SetMaxOpenConns(1)
		db := sqlx.NewDb(raw, driverName)
		t.Cleanup(func() { _ = db.Close() })
		migrations, err := loadMigrations()
		if err != nil || len(migrations) != 9 {
			t.Fatalf("loadMigrations() = %d/%v", len(migrations), err)
		}
		for index := 0; index < 4; index++ {
			if err := applyMigration(context.Background(), db, migrations[index], "released-v3", index == 0); err != nil {
				t.Fatalf("applyMigration(%d) error = %v", index+1, err)
			}
		}
		sessionID := "00000000-0000-7000-8000-000000009831"
		messageID := "00000000-0000-7000-8000-000000009832"
		if _, err := db.ExecContext(context.Background(), `INSERT INTO sessions (id, title, status, privacy_mode, version, created_at_ms, updated_at_ms) VALUES (?, 'Preserved Session', 'active', 'standard', 1, 9830, 9830)`, sessionID); err != nil {
			t.Fatalf("Session setup error = %v", err)
		}
		if _, err := db.ExecContext(context.Background(), `INSERT INTO messages (id, session_id, role, content, content_format, status, content_hash, created_at_ms) VALUES (?, ?, 'user', 'Preserved question', 'plain', 'committed', ?, 9831)`, messageID, sessionID, domain.MessageContentHash("Preserved question")); err != nil {
			t.Fatalf("Message setup error = %v", err)
		}
		if _, err := db.ExecContext(context.Background(), `CREATE TABLE messages_v5 (id INTEGER PRIMARY KEY) STRICT`); err != nil {
			t.Fatalf("migration conflict setup error = %v", err)
		}

		if err := applyMigration(context.Background(), db, migrations[4], "0.4.0", false); err == nil {
			t.Fatal("v0.4 runtime limits migration error = nil")
		}
		var migrationCount int
		if err := db.GetContext(context.Background(), &migrationCount, `SELECT count(version) FROM schema_migrations`); err != nil || migrationCount != 4 {
			t.Fatalf("preserved migration count/error = %d/%v", migrationCount, err)
		}
		var messageCount int
		if err := db.GetContext(context.Background(), &messageCount, `SELECT count(id) FROM messages WHERE id = ?`, messageID); err != nil || messageCount != 1 {
			t.Fatalf("preserved Message count/error = %d/%v", messageCount, err)
		}
		var foreignKeys int
		if err := db.GetContext(context.Background(), &foreignKeys, `PRAGMA foreign_keys`); err != nil || foreignKeys != 1 {
			t.Fatalf("foreign-key state/error = %d/%v", foreignKeys, err)
		}
	})
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
	`, 10, "000010_future.sql", strings.Repeat("1", 64), 1, "future-version"); err != nil {
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

func TestActionAuthoritySchemaContainsNoRawParameterOrTransportColumns(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "action-schema-safety")
	rows, err := db.handle.QueryxContext(context.Background(), `
		SELECT schema_table.name AS table_name, table_column.name AS column_name
		FROM sqlite_schema AS schema_table
		JOIN pragma_table_info(schema_table.name) AS table_column
		WHERE schema_table.type = 'table'
		  AND schema_table.name IN ('approvals', 'approval_decisions', 'action_reviews')
		ORDER BY schema_table.name, table_column.cid
	`)
	if err != nil {
		t.Fatalf("action schema column query error = %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tableName string
		var columnName string
		if err := rows.Scan(&tableName, &columnName); err != nil {
			t.Fatalf("action schema column scan error = %v", err)
		}
		lower := strings.ToLower(columnName)
		for _, forbidden := range []string{
			"raw", "json", "payload", "body", "command", "executable", "argv", "environment",
		} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("forbidden action authority column %s.%s", tableName, columnName)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("action schema rows error = %v", err)
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
		"action_reviews",
		"agent_runs",
		"approval_decisions",
		"approvals",
		"audit_events",
		"diagnoses",
		"evidence_items",
		"legacy_restart_approval_decisions",
		"legacy_restart_approvals",
		"messages",
		"model_requests",
		"privacy_consents",
		"schema_migrations",
		"session_context_summaries",
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
	wantNames := []string{
		"000001_initial.sql",
		"000002_privacy_consent.sql",
		"000003_approval_runtime.sql",
		"000004_minimal_run_identity.sql",
		"000005_v04_runtime_limits.sql",
		"000006_session_model_context.sql",
		"000007_action_permission_foundation.sql",
		"000008_resource_evidence_provenance.sql",
		"000009_observability_provenance_and_consent.sql",
	}
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
