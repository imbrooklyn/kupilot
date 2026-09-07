package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	sessioncontract "github.com/imbrooklyn/kupilot/internal/session"
)

func TestNonInteractiveExactSessionDeletionRequiresTwoPhaseDigest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 7, 4, 0, 0, 0, time.UTC)
	temporaryRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: filepath.Join(temporaryRoot, "state"), ApplicationVersion: "test", CorrelationID: "session-delete-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repository := sqlite.NewSessionRepository(database)
	session := domain.Session{
		ID: "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10", Title: "Deletion fixture",
		Status: domain.SessionStatusActive, PrivacyMode: domain.PrivacyModeStandard, Version: 1,
		CreatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := repository.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	manager, err := application.NewSessionManager(repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	var direct bytes.Buffer
	err = runSessionsDelete(ctx, manager, &cli.SessionDeleteOptions{SessionID: string(session.ID), Limit: 50}, sessionCommandIO{
		output: &direct, now: func() time.Time { return now }, isTTY: false,
	})
	if err == nil || !strings.Contains(err.Error(), "requires --dry-run") {
		t.Fatalf("direct non-interactive deletion error = %v", err)
	}
	if _, err := repository.GetByID(ctx, session.ID); err != nil {
		t.Fatalf("direct refusal changed the Session: %v", err)
	}

	var preview bytes.Buffer
	if err := runSessionsDelete(ctx, manager, &cli.SessionDeleteOptions{
		SessionID: string(session.ID), Limit: 50, DryRun: true,
	}, sessionCommandIO{output: &preview, now: func() time.Time { return now }, isTTY: false}); err != nil {
		t.Fatalf("dry-run error = %v", err)
	}
	digest := deletionDigestFromOutput(t, preview.String())
	if !strings.Contains(preview.String(), "No data was deleted.") {
		t.Fatalf("dry-run did not state its zero-write result: %q", preview.String())
	}
	if _, err := repository.GetByID(ctx, session.ID); err != nil {
		t.Fatalf("dry-run changed the Session: %v", err)
	}

	var committed bytes.Buffer
	if err := runSessionsDelete(ctx, manager, &cli.SessionDeleteOptions{
		SessionID: string(session.ID), Limit: 50, Confirm: digest,
	}, sessionCommandIO{output: &committed, now: func() time.Time { return now }, isTTY: false}); err != nil {
		t.Fatalf("digest confirmation error = %v", err)
	}
	if !strings.Contains(committed.String(), "Deleted: 1") {
		t.Fatalf("commit output = %q", committed.String())
	}
	if _, err := repository.GetByID(ctx, session.ID); !errors.Is(err, sessioncontract.ErrSessionNotFound) {
		t.Fatalf("committed Session remains: %v", err)
	}
}

func TestBatchSessionDeletionUsesFrozenAbsoluteCutoffDigestAndTTYCountPhrase(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 7, 4, 0, 0, 0, time.UTC)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.ResolvePaths(config.PathInput{KupilotHome: filepath.Join(root, "home")})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.EnsureHome(ctx, paths); err != nil {
		t.Fatal(err)
	}
	oldID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a12")
	recentID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a13")
	createSessions := func(records ...domain.Session) {
		t.Helper()
		database, openErr := sqlite.Open(ctx, sqlite.OpenOptions{
			StateDir: paths.StateDir, ApplicationVersion: "test", CorrelationID: "batch-delete-seed",
		})
		if openErr != nil {
			t.Fatal(openErr)
		}
		repository := sqlite.NewSessionRepository(database)
		for _, record := range records {
			if createErr := repository.Create(ctx, record); createErr != nil {
				_ = database.Close()
				t.Fatal(createErr)
			}
		}
		if closeErr := database.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	newSession := func(id domain.SessionID, lastActive time.Time) domain.Session {
		return domain.Session{
			ID: id, Title: "Batch deletion fixture", Status: domain.SessionStatusActive,
			PrivacyMode: domain.PrivacyModeStandard, Version: 1,
			CreatedAt: lastActive.Add(-time.Hour), LastActivityAt: lastActive, UpdatedAt: lastActive,
		}
	}
	createSessions(newSession(oldID, now.Add(-48*time.Hour)), newSession(recentID, now.Add(-time.Hour)))

	var preview bytes.Buffer
	previewIntent := cli.StartIntent{Kind: cli.IntentSessionsDelete, SessionDelete: &cli.SessionDeleteOptions{
		Before: "1d", Limit: application.SessionDeleteDefaultLimit, DryRun: true,
	}}
	if err := runLocalSessionCommand(ctx, previewIntent, buildinfo.Info{Version: "test"}, paths, sessionCommandIO{
		output: &preview, now: func() time.Time { return now }, isTTY: false,
	}); err != nil {
		t.Fatalf("relative batch dry-run error = %v", err)
	}
	digest := deletionDigestFromOutput(t, preview.String())
	const absoluteCutoff = "2026-09-06T04:00:00Z"
	for _, want := range []string{"Frozen cutoff UTC: " + absoluteCutoff, "selected: 1", "No data was deleted."} {
		if !strings.Contains(preview.String(), want) {
			t.Fatalf("batch dry-run missing %q: %q", want, preview.String())
		}
	}

	var refused bytes.Buffer
	err = runLocalSessionCommand(ctx, cli.StartIntent{Kind: cli.IntentSessionsDelete, SessionDelete: &cli.SessionDeleteOptions{
		Before: "1d", Limit: application.SessionDeleteDefaultLimit, Confirm: digest,
	}}, buildinfo.Info{Version: "test"}, paths, sessionCommandIO{
		output: &refused, now: func() time.Time { return now.Add(5 * time.Minute) }, isTTY: false,
	})
	if err == nil || !strings.Contains(err.Error(), "absolute RFC3339 cutoff") {
		t.Fatalf("relative automated confirmation error = %v", err)
	}

	var tampered bytes.Buffer
	err = runLocalSessionCommand(ctx, cli.StartIntent{Kind: cli.IntentSessionsDelete, SessionDelete: &cli.SessionDeleteOptions{
		Before: absoluteCutoff, Limit: application.SessionDeleteDefaultLimit, Confirm: strings.Repeat("0", 64),
	}}, buildinfo.Info{Version: "test"}, paths, sessionCommandIO{
		output: &tampered, now: func() time.Time { return now.Add(5 * time.Minute) }, isTTY: false,
	})
	if !errors.Is(err, application.ErrSessionDeletionStale) {
		t.Fatalf("tampered batch digest error = %v", err)
	}

	var committed bytes.Buffer
	if err := runLocalSessionCommand(ctx, cli.StartIntent{Kind: cli.IntentSessionsDelete, SessionDelete: &cli.SessionDeleteOptions{
		Before: absoluteCutoff, Limit: application.SessionDeleteDefaultLimit, Confirm: digest,
	}}, buildinfo.Info{Version: "test"}, paths, sessionCommandIO{
		output: &committed, now: func() time.Time { return now.Add(5 * time.Minute) }, isTTY: false,
	}); err != nil {
		t.Fatalf("absolute batch confirmation error = %v", err)
	}
	if !strings.Contains(committed.String(), "Deleted: 1") {
		t.Fatalf("batch commit output = %q", committed.String())
	}

	ttyID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a14")
	createSessions(newSession(ttyID, now.Add(-72*time.Hour)))
	var ttyOutput bytes.Buffer
	if err := runLocalSessionCommand(ctx, cli.StartIntent{Kind: cli.IntentSessionsDelete, SessionDelete: &cli.SessionDeleteOptions{
		Before: "1d", Limit: application.SessionDeleteDefaultLimit,
	}}, buildinfo.Info{Version: "test"}, paths, sessionCommandIO{
		input: strings.NewReader("DELETE 1 SESSIONS\n"), output: &ttyOutput,
		now: func() time.Time { return now.Add(10 * time.Minute) }, isTTY: true,
	}); err != nil {
		t.Fatalf("TTY batch confirmation error = %v", err)
	}
	if !strings.Contains(ttyOutput.String(), "Type exactly: DELETE 1 SESSIONS") || !strings.Contains(ttyOutput.String(), "Deleted: 1") {
		t.Fatalf("TTY batch output = %q", ttyOutput.String())
	}

	database, err := sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: paths.StateDir, ApplicationVersion: "test", CorrelationID: "batch-delete-verify",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := sqlite.NewSessionRepository(database)
	for _, removed := range []domain.SessionID{oldID, ttyID} {
		if _, err := repository.GetByID(ctx, removed); !errors.Is(err, sessioncontract.ErrSessionNotFound) {
			t.Fatalf("deleted Session %s remains: %v", removed, err)
		}
	}
	if _, err := repository.GetByID(ctx, recentID); err != nil {
		t.Fatalf("recent Session was not retained: %v", err)
	}
}

func TestCLIDoctorJSONIsTypedRedactedAndDoesNotAdvanceLastActive(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 7, 4, 0, 0, 0, time.UTC)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := config.ResolvePaths(config.PathInput{KupilotHome: filepath.Join(root, "home")})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.EnsureHome(ctx, paths); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		config.AgentAPIKeyEnvironmentVariable,
		config.ModelAPIKeyEnvironmentVariable,
		config.ApprovalReviewerAPIKeyEnvironmentVariable,
		config.PrometheusAPIKeyEnvironmentVariable,
		config.LokiAPIKeyEnvironmentVariable,
	} {
		value, found := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if found {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	credentialCanary := strings.Repeat("doctor-key-", 5)
	if err := os.Setenv(config.AgentAPIKeyEnvironmentVariable, credentialCanary); err != nil {
		t.Fatal(err)
	}

	database, err := sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: paths.StateDir, ApplicationVersion: "test", CorrelationID: "doctor-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := sqlite.NewSessionRepository(database)
	session := domain.Session{
		ID: "0198a46e-7d2a-7d34-9b6f-2df5f45a2a11", Title: "doctor-title-canary",
		Status: domain.SessionStatusActive, PrivacyMode: domain.PrivacyModeStandard, Version: 1,
		CreatedAt: now.Add(-2 * time.Hour), LastActivityAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := repository.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	intent := cli.StartIntent{Kind: cli.IntentDoctor, Doctor: &cli.DoctorOptions{JSON: true}}
	if err := runLocalSessionCommand(ctx, intent, buildinfo.Info{Version: "v0.0.0-test"}, paths, sessionCommandIO{
		output: &output, now: func() time.Time { return now }, isTTY: false,
	}); err != nil {
		t.Fatalf("runLocalSessionCommand(doctor) error = %v", err)
	}
	var envelope cliDoctorEnvelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("decode doctor JSON: %v", err)
	}
	if envelope.SchemaVersion != "kupilot.cli-doctor/v1" || envelope.Doctor.SchemaVersion != application.DoctorSchemaVersion ||
		envelope.Doctor.Storage.SessionCount != 1 || envelope.Terminal.InteractiveInput ||
		envelope.Doctor.ModelCompatibility.RuntimeVersion != "v0.9.19" ||
		envelope.Doctor.ModelCompatibility.AdapterVersion != "v0.1.13" ||
		envelope.Doctor.ModelCompatibility.LiveConformance != "not_run" ||
		envelope.Doctor.ModelCompatibility.StreamContinuation != "protocol_continuation_unavailable" ||
		envelope.Terminal.NativeClipboard != "unsupported" || envelope.Terminal.OSC52 != "unsupported" ||
		envelope.Terminal.Notification != "disabled" || envelope.Terminal.AlternateScreen != "disabled" ||
		envelope.Terminal.Scrollback != "primary_screen_restored_committed" {
		t.Fatalf("doctor envelope = %#v", envelope)
	}
	for _, forbidden := range []string{credentialCanary, session.Title, paths.HomeDir, paths.StateDir, "api_key", "raw SQL"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("doctor output contains forbidden value %q", forbidden)
		}
	}

	database, err = sqlite.Open(ctx, sqlite.OpenOptions{
		StateDir: paths.StateDir, ApplicationVersion: "test", CorrelationID: "doctor-verify",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	retained, err := sqlite.NewSessionRepository(database).GetByID(ctx, session.ID)
	if err != nil || !retained.LastActivityAt.Equal(session.LastActivityAt) {
		t.Fatalf("doctor changed Last active: %#v, %v", retained, err)
	}
}

func deletionDigestFromOutput(t *testing.T, output string) string {
	t.Helper()
	const marker = "Selection digest: "
	start := strings.Index(output, marker)
	if start < 0 {
		t.Fatalf("selection digest missing from %q", output)
	}
	value := output[start+len(marker):]
	if end := strings.IndexByte(value, '\n'); end >= 0 {
		value = value[:end]
	}
	if len(value) != application.MaxSessionDeletionDigestBytes {
		t.Fatalf("selection digest = %q", value)
	}
	return value
}
