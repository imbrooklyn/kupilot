package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func TestPrivacyRepositoryPersistsOnlyBoundedConsentTuple(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	stateDir := filepath.Join(root, "state")
	db := openTestDB(t, context.Background(), stateDir, "privacy-consent")
	repository := NewPrivacyRepository(db)
	originCanary := "https://privacy-origin-canary.example"
	now := func() time.Time { return time.UnixMilli(20_000).UTC() }
	manager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
		Store: repository, Origin: originCanary, Now: now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	review, err := manager.Review(context.Background())
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if _, err := manager.Decide(context.Background(), application.PrivacyActionAccept, review.Revision, nil); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	record, found, err := repository.LoadPrivacy(context.Background())
	if err != nil || !found || record.Validate() != nil || record.Decision != application.PrivacyDecisionAccepted {
		t.Fatalf("LoadPrivacy() = %#v/%v/%v", record, found, err)
	}
	if strings.Contains(record.OriginHash, originCanary) {
		t.Fatal("consent record stored the raw origin")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", entry.Name(), err)
		}
		if strings.Contains(string(content), originCanary) {
			t.Fatalf("SQLite file %s contains the raw origin canary", entry.Name())
		}
	}
}

func TestPrivacyRepositoryRejectsCancellationAndCorruptRows(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "privacy-corrupt")
	repository := NewPrivacyRepository(db)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := repository.LoadPrivacy(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled LoadPrivacy() error = %v", err)
	}
	if err := repository.SavePrivacy(cancelled, application.PrivacyRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled SavePrivacy() error = %v", err)
	}
	if _, err := db.handle.ExecContext(context.Background(), `
		INSERT INTO privacy_consents (
			singleton_id, policy_version, origin_hash, categories_json,
			decision, decided_at_ms, schema_version
		) VALUES (1, '2026-08-10.v1', ?, '["unknown"]', 'accepted', 1, 1)
	`, strings.Repeat("0", 64)); err != nil {
		t.Fatalf("corrupt row setup error = %v", err)
	}
	if _, found, err := repository.LoadPrivacy(context.Background()); err == nil || found {
		t.Fatalf("corrupt LoadPrivacy() found/error = %v/%v", found, err)
	}
}
