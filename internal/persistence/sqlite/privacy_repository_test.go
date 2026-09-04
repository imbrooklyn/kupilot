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
	"github.com/imbrooklyn/kupilot/internal/domain"
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
			role, policy_version, origin_hash, categories_json,
			decision, decided_at_ms, schema_version
		) VALUES ('agent', '2026-09-04.v2', ?, '["unknown"]', 'accepted', 1, 3)
	`, strings.Repeat("0", 64)); err != nil {
		t.Fatalf("corrupt row setup error = %v", err)
	}
	if _, found, err := repository.LoadPrivacy(context.Background()); err == nil || found {
		t.Fatalf("corrupt LoadPrivacy() found/error = %v/%v", found, err)
	}
}

func TestPrivacyRepositoriesRoundTripIndependentRoleRows(t *testing.T) {
	t.Parallel()

	database := openTestDB(t, context.Background(), testStateDir(t), "privacy-role-rows")
	agentRepository := NewRolePrivacyRepository(database, domain.ModelRoleAgent)
	reviewerRepository := NewRolePrivacyRepository(database, domain.ModelRoleApprovalReviewer)
	now := func() time.Time { return time.UnixMilli(25_000).UTC() }
	for _, fixture := range []struct {
		role       domain.ModelRole
		origin     string
		repository *PrivacyRepository
	}{
		{role: domain.ModelRoleAgent, origin: "https://agent.example", repository: agentRepository},
		{role: domain.ModelRoleApprovalReviewer, origin: "https://reviewer.example", repository: reviewerRepository},
	} {
		manager, err := application.NewPrivacyManager(application.PrivacyManagerConfig{
			Store: fixture.repository, Role: fixture.role, Origin: fixture.origin, Now: now,
		})
		if err != nil {
			t.Fatalf("NewPrivacyManager(%s) error = %v", fixture.role, err)
		}
		review, err := manager.Review(context.Background())
		if err != nil {
			t.Fatalf("Review(%s) error = %v", fixture.role, err)
		}
		if _, err := manager.Decide(context.Background(), application.PrivacyActionAccept, review.Revision, nil); err != nil {
			t.Fatalf("Decide(%s) error = %v", fixture.role, err)
		}
	}
	agentRecord, agentFound, err := agentRepository.LoadPrivacy(context.Background())
	if err != nil || !agentFound || agentRecord.Role != domain.ModelRoleAgent {
		t.Fatalf("agent LoadPrivacy() = %#v/%v/%v", agentRecord, agentFound, err)
	}
	reviewerRecord, reviewerFound, err := reviewerRepository.LoadPrivacy(context.Background())
	if err != nil || !reviewerFound || reviewerRecord.Role != domain.ModelRoleApprovalReviewer ||
		reviewerRecord.OriginHash == agentRecord.OriginHash {
		t.Fatalf("reviewer LoadPrivacy() = %#v/%v/%v", reviewerRecord, reviewerFound, err)
	}
	if err := agentRepository.SavePrivacy(context.Background(), reviewerRecord); !errors.Is(err, application.ErrPrivacyRecord) {
		t.Fatalf("agent repository accepted reviewer record: %v", err)
	}
	if err := reviewerRepository.SavePrivacy(context.Background(), agentRecord); !errors.Is(err, application.ErrPrivacyRecord) {
		t.Fatalf("reviewer repository accepted agent record: %v", err)
	}
	var rows int
	if err := database.handle.GetContext(context.Background(), &rows, `SELECT count(role) FROM privacy_consents`); err != nil || rows != 2 {
		t.Fatalf("privacy role rows = %d/%v", rows, err)
	}
}
