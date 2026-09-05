package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestPrivacyConsentLifecycleBindsOriginCategoriesAndPolicy(t *testing.T) {
	store := new(privacyTestStore)
	now := privacyTestClock()
	manager := newPrivacyTestManager(t, store, "https://model.example", PrivacyPolicyVersion, now)
	review, err := manager.Review(context.Background())
	if err != nil || review.Validate() != nil || review.Decision != PrivacyDecisionPending || review.LogsEnabled {
		t.Fatalf("initial Review() = %#v, %v", review, err)
	}
	if allowed, err := manager.AuthorizeModel(context.Background()); err != nil || allowed {
		t.Fatalf("pre-consent AuthorizeModel() = %v, %v", allowed, err)
	}
	if manager.AuthorizeLogs(context.Background()) != PrivacyLogDenied {
		t.Fatal("disabled logs were not denied before any Tool action")
	}

	accepted, err := manager.Decide(context.Background(), PrivacyActionAccept, review.Revision, nil)
	if err != nil || accepted.Decision != PrivacyDecisionAccepted {
		t.Fatalf("Accept() = %#v, %v", accepted, err)
	}
	if allowed, err := manager.AuthorizeModel(context.Background()); err != nil || !allowed {
		t.Fatalf("accepted AuthorizeModel() = %v, %v", allowed, err)
	}
	record := store.snapshot()
	if record.OriginHash == "" || strings.Contains(record.OriginHash, "model.example") ||
		len(record.Categories) != len(enabledPrivacyCategories(false, false, false)) {
		t.Fatalf("stored consent = %#v", record)
	}

	restarted := newPrivacyTestManager(t, store, "https://model.example", PrivacyPolicyVersion, now)
	if allowed, err := restarted.AuthorizeModel(context.Background()); err != nil || !allowed {
		t.Fatalf("restart AuthorizeModel() = %v, %v", allowed, err)
	}
	review, _ = restarted.Review(context.Background())
	enableLogs := true
	pending, err := restarted.Decide(context.Background(), PrivacyActionToggleLogs, review.Revision, &enableLogs)
	if err != nil || pending.Decision != PrivacyDecisionPending || !pending.LogsEnabled {
		t.Fatalf("ToggleLogs() = %#v, %v", pending, err)
	}
	if allowed, _ := restarted.AuthorizeModel(context.Background()); allowed ||
		restarted.AuthorizeLogs(context.Background()) != PrivacyLogConsentRequired {
		t.Fatal("category change inherited stale consent")
	}
	accepted, err = restarted.Decide(context.Background(), PrivacyActionAccept, pending.Revision, nil)
	if err != nil || !accepted.LogsEnabled || restarted.AuthorizeLogs(context.Background()) != PrivacyLogAllowed {
		t.Fatalf("log-category Accept() = %#v, %v", accepted, err)
	}

	changedOrigin := newPrivacyTestManager(t, store, "https://other.example", PrivacyPolicyVersion, now)
	if allowed, _ := changedOrigin.AuthorizeModel(context.Background()); allowed {
		t.Fatal("changed origin reused consent")
	}
	changedPolicy := newPrivacyTestManager(t, store, "https://model.example", "2026-09-05.v3", now)
	if allowed, _ := changedPolicy.AuthorizeModel(context.Background()); allowed {
		t.Fatal("changed policy version reused consent")
	}
}

func TestPrivacyDecisionsRejectStaleCancelledAndFailedWrites(t *testing.T) {
	store := new(privacyTestStore)
	manager := newPrivacyTestManager(t, store, "https://model.example", PrivacyPolicyVersion, privacyTestClock())
	review, _ := manager.Review(context.Background())
	if _, err := manager.Decide(context.Background(), PrivacyActionAccept, strings.Repeat("0", 64), nil); !errors.Is(err, ErrPrivacyReviewStale) || store.saveCalls != 0 {
		t.Fatalf("stale decision error/calls = %v/%d", err, store.saveCalls)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Decide(cancelled, PrivacyActionAccept, review.Revision, nil); !errors.Is(err, context.Canceled) || store.saveCalls != 0 {
		t.Fatalf("cancelled decision error/calls = %v/%d", err, store.saveCalls)
	}
	store.saveErr = errors.New("synthetic privacy store failure")
	if _, err := manager.Decide(context.Background(), PrivacyActionAccept, review.Revision, nil); !errors.Is(err, ErrPrivacyPersistence) {
		t.Fatalf("failed decision error = %v", err)
	}
	manager.FailClosed()
	if allowed, err := manager.AuthorizeModel(context.Background()); err != nil || allowed {
		t.Fatalf("fail-closed AuthorizeModel() = %v, %v", allowed, err)
	}
}

func TestPrivacyManagerDefersStoreIOUntilModelOriginIsConfigured(t *testing.T) {
	store := new(privacyTestStore)
	manager := newPrivacyTestManager(t, store, "", PrivacyPolicyVersion, privacyTestClock())
	if _, err := manager.Review(context.Background()); !errors.Is(err, ErrModelUnconfigured) {
		t.Fatalf("unconfigured Review() error = %v", err)
	}
	if allowed, err := manager.AuthorizeModel(context.Background()); allowed || !errors.Is(err, ErrModelUnconfigured) {
		t.Fatalf("unconfigured AuthorizeModel() = %v, %v", allowed, err)
	}
	if store.loadCalls != 0 || store.saveCalls != 0 {
		t.Fatalf("unconfigured privacy store calls = load %d save %d", store.loadCalls, store.saveCalls)
	}
	if err := manager.ReconfigureOrigin("https://model.example"); err != nil {
		t.Fatalf("ReconfigureOrigin() error = %v", err)
	}
	review, err := manager.Review(context.Background())
	if err != nil || review.Decision != PrivacyDecisionPending || store.loadCalls != 1 || store.saveCalls != 0 {
		t.Fatalf("configured Review() = %#v, %v; load %d save %d", review, err, store.loadCalls, store.saveCalls)
	}
}

func TestPrivacyConsentIsIndependentForEachModelRoleAtTheSameOrigin(t *testing.T) {
	t.Parallel()

	now := privacyTestClock()
	agentStore := new(privacyTestStore)
	reviewerStore := new(privacyTestStore)
	agentManager, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: agentStore, Role: domain.ModelRoleAgent, Origin: "https://model.example", Now: now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager(agent) error = %v", err)
	}
	reviewerManager, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: reviewerStore, Role: domain.ModelRoleApprovalReviewer, Origin: "https://model.example", Now: now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager(reviewer) error = %v", err)
	}
	agentReview, err := agentManager.Review(context.Background())
	if err != nil {
		t.Fatalf("Review(agent) error = %v", err)
	}
	reviewerReview, err := reviewerManager.Review(context.Background())
	if err != nil {
		t.Fatalf("Review(reviewer) error = %v", err)
	}
	if agentReview.Role != domain.ModelRoleAgent || reviewerReview.Role != domain.ModelRoleApprovalReviewer ||
		agentReview.Revision == reviewerReview.Revision {
		t.Fatalf("role reviews = agent %#v, reviewer %#v", agentReview, reviewerReview)
	}
	if _, err := agentManager.Decide(context.Background(), PrivacyActionAccept, agentReview.Revision, nil); err != nil {
		t.Fatalf("Decide(agent) error = %v", err)
	}
	if allowed, err := agentManager.AuthorizeModel(context.Background()); err != nil || !allowed {
		t.Fatalf("AuthorizeModel(agent) = %v/%v", allowed, err)
	}
	if allowed, err := reviewerManager.AuthorizeModel(context.Background()); err != nil || allowed {
		t.Fatalf("AuthorizeModel(reviewer) = %v/%v", allowed, err)
	}
	if agentStore.snapshot().Role != domain.ModelRoleAgent || reviewerStore.saveCalls != 0 {
		t.Fatalf("role stores = agent %#v, reviewer saves %d", agentStore.snapshot(), reviewerStore.saveCalls)
	}
}

func TestPrivacyConsentBindsOptionalSourceOriginsAndCategories(t *testing.T) {
	store := new(privacyTestStore)
	manager, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: store, Role: domain.ModelRoleAgent, Origin: "https://model.example",
		PrometheusOrigin: "https://prometheus.example", LokiOrigin: "https://loki.example", LogsEnabled: true,
		Now: privacyTestClock(),
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	review, err := manager.Review(context.Background())
	if err != nil || review.Validate() != nil || len(review.DataSources) != 2 || !review.DataSources[0].Enabled || !review.DataSources[1].Enabled {
		t.Fatalf("Review() = %#v/%v", review, err)
	}
	prometheusHash := privacyOriginHash("https://prometheus.example")
	lokiHash := privacyOriginHash("https://loki.example")
	if manager.AuthorizeDataSource(context.Background(), domain.DataSourcePrometheus, prometheusHash) != PrivacyLogConsentRequired ||
		manager.AuthorizeDataSource(context.Background(), domain.DataSourceLoki, lokiHash) != PrivacyLogConsentRequired {
		t.Fatal("optional source transfer was allowed before exact consent")
	}
	if _, err := manager.Decide(context.Background(), PrivacyActionAccept, review.Revision, nil); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if manager.AuthorizeDataSource(context.Background(), domain.DataSourcePrometheus, prometheusHash) != PrivacyLogAllowed ||
		manager.AuthorizeDataSource(context.Background(), domain.DataSourceLoki, lokiHash) != PrivacyLogAllowed ||
		manager.AuthorizeDataSource(context.Background(), domain.DataSourcePrometheus, strings.Repeat("0", 64)) != PrivacyLogDenied {
		t.Fatal("optional source authorization did not bind exact origins")
	}
	record := store.snapshot()
	if record.PrometheusOriginHash != prometheusHash || record.LokiOriginHash != lokiHash || record.Validate() != nil ||
		!containsPrivacyCategory(record.Categories, DataCategoryPrometheusResults) || !containsPrivacyCategory(record.Categories, DataCategoryRedactedLokiOutput) {
		t.Fatalf("stored optional source consent = %#v", record)
	}

	changed, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: store, Role: domain.ModelRoleAgent, Origin: "https://model.example",
		PrometheusOrigin: "https://other-prometheus.example", LokiOrigin: "https://loki.example", LogsEnabled: true,
		Now: privacyTestClock(),
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager(changed) error = %v", err)
	}
	if changed.AuthorizeDataSource(context.Background(), domain.DataSourceLoki, lokiHash) != PrivacyLogConsentRequired {
		t.Fatal("an origin change reused the previous source consent tuple")
	}

	malformed := record
	malformed.PrometheusOriginHash = ""
	if malformed.Validate() == nil {
		t.Fatal("PrivacyRecord accepted a source category without its exact origin hash")
	}
}

func containsPrivacyCategory(categories []ModelDataCategory, want ModelDataCategory) bool {
	for _, category := range categories {
		if category == want {
			return true
		}
	}
	return false
}

func TestPrivacyManagerRejectsAConsentRecordFromAnotherRole(t *testing.T) {
	t.Parallel()

	store := &privacyTestStore{
		found: true,
		record: PrivacyRecord{
			Role: domain.ModelRoleAgent, PolicyVersion: PrivacyPolicyVersion,
			OriginHash: privacyOriginHash("https://model.example"), Categories: enabledPrivacyCategories(false),
			Decision: PrivacyDecisionAccepted, DecidedAt: time.UnixMilli(10_000).UTC(),
			SchemaVersion: PrivacyRecordSchemaVersion,
		},
	}
	manager, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: store, Role: domain.ModelRoleApprovalReviewer, Origin: "https://model.example", Now: privacyTestClock(),
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	if allowed, err := manager.AuthorizeModel(context.Background()); allowed || !errors.Is(err, ErrPrivacyPersistence) {
		t.Fatalf("AuthorizeModel() = %v/%v", allowed, err)
	}
	if store.loadCalls != 1 || store.saveCalls != 0 {
		t.Fatalf("misrouted role store calls = load %d save %d", store.loadCalls, store.saveCalls)
	}
}

type privacyTestStore struct {
	mu        sync.Mutex
	record    PrivacyRecord
	found     bool
	loadErr   error
	saveErr   error
	loadCalls int
	saveCalls int
}

func (store *privacyTestStore) LoadPrivacy(ctx context.Context) (PrivacyRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loadCalls++
	if err := ctx.Err(); err != nil {
		return PrivacyRecord{}, false, err
	}
	return clonePrivacyRecord(store.record), store.found, store.loadErr
}

func (store *privacyTestStore) SavePrivacy(ctx context.Context, record PrivacyRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	store.saveCalls++
	if store.saveErr != nil {
		return store.saveErr
	}
	store.record = clonePrivacyRecord(record)
	store.found = true
	return nil
}

func (store *privacyTestStore) snapshot() PrivacyRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	return clonePrivacyRecord(store.record)
}

func privacyTestClock() func() time.Time {
	var mu sync.Mutex
	next := time.UnixMilli(10_000).UTC()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		value := next
		next = next.Add(time.Millisecond)
		return value
	}
}

func newPrivacyTestManager(
	t *testing.T,
	store PrivacyStore,
	origin string,
	policyVersion string,
	now func() time.Time,
) *PrivacyManager {
	t.Helper()
	manager, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: store, Origin: origin, PolicyVersion: policyVersion, Now: now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	return manager
}
