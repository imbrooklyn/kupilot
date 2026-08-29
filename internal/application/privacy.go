package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// PrivacyPolicyVersion changes whenever an eligible category or its meaning changes.
	PrivacyPolicyVersion       = "2026-08-10.v1"
	PrivacyRecordSchemaVersion = 1
	maxPrivacyPolicyBytes      = 64
)

var (
	ErrPrivacyConfiguration = errors.New("privacy policy configuration is invalid")
	ErrPrivacyRecord        = errors.New("privacy consent record is invalid")
	ErrPrivacyPersistence   = errors.New("privacy consent persistence is unavailable")
	ErrPrivacyReviewStale   = errors.New("privacy consent review is stale")
	ErrConsentRequired      = errors.New("current model-transfer consent is required")
)

// ModelDataCategory is one code-defined class eligible for model transfer.
type ModelDataCategory string

const (
	DataCategoryUserQuestion            ModelDataCategory = "user_question"
	DataCategorySafeConversationContext ModelDataCategory = "safe_conversation_context"
	DataCategoryResourceReferences      ModelDataCategory = "resource_names_and_references"
	DataCategoryProjectedStatus         ModelDataCategory = "projected_kubernetes_status"
	DataCategoryProjectedEvents         ModelDataCategory = "projected_kubernetes_events"
	DataCategoryRedactedContainerOutput ModelDataCategory = "redacted_container_output"
)

// PrivacyDecision is the durable authority state for one exact policy tuple.
type PrivacyDecision string

const (
	PrivacyDecisionPending  PrivacyDecision = "pending"
	PrivacyDecisionAccepted PrivacyDecision = "accepted"
	PrivacyDecisionRejected PrivacyDecision = "rejected"
	PrivacyDecisionRevoked  PrivacyDecision = "revoked"
)

// PrivacyAction is one typed user decision accepted by Application.
type PrivacyAction string

const (
	PrivacyActionAccept     PrivacyAction = "accept"
	PrivacyActionReject     PrivacyAction = "reject"
	PrivacyActionRevoke     PrivacyAction = "revoke"
	PrivacyActionToggleLogs PrivacyAction = "toggle_logs"
)

// PrivacyLogAuthorization is the Application-owned outcome consumed by the
// fixed log Tool policy adapter before any Kubernetes log read.
type PrivacyLogAuthorization string

const (
	PrivacyLogAllowed         PrivacyLogAuthorization = "allowed"
	PrivacyLogConsentRequired PrivacyLogAuthorization = "consent_required"
	PrivacyLogDenied          PrivacyLogAuthorization = "denied"
)

type privacyCategoryDefinition struct {
	ID          ModelDataCategory
	Description string
}

var privacyCategoryCatalog = [...]privacyCategoryDefinition{
	{DataCategoryUserQuestion, "Your question after local text cleanup, sensitive-value handling, and size limits."},
	{DataCategorySafeConversationContext, "Relevant conversation context from this diagnostic run, including safe cluster-read results and supporting observations."},
	{DataCategoryResourceReferences, "The active Context and Namespace, plus permitted resource names and references used by this diagnostic run."},
	{DataCategoryProjectedStatus, "Permitted Kubernetes status, conditions, counts, times, and resource relationships."},
	{DataCategoryProjectedEvents, "Recent permitted Kubernetes Event reasons and messages after local safety checks."},
	{DataCategoryRedactedContainerOutput, "Recent current or previous container output after local cleanup, size limits, and sensitive-value filtering."},
}

var neverEligibleModelData = [...]string{
	"Kubeconfig contents and Kubernetes credentials",
	"Tokens, certificates, private keys, and model API keys",
	"Kubernetes Secret objects or Secret data",
	"Raw Kubernetes objects, full YAML, and unrestricted fields",
	"Raw Events and raw or unbounded container output",
	"Assembled raw prompts, protocol bodies, headers, and vendor errors",
}

// PrivacyCategoryReview is code-authored display data, not a policy input.
type PrivacyCategoryReview struct {
	ID          ModelDataCategory
	Description string
	Enabled     bool
}

// PrivacyReview is the exact Application projection shown before a decision.
// Revision binds the visible origin, policy version, and enabled categories.
type PrivacyReview struct {
	Origin        string
	PolicyVersion string
	Revision      string
	Decision      PrivacyDecision
	LogsEnabled   bool
	Categories    []PrivacyCategoryReview
	NeverEligible []string
}

// Validate rejects forged or widened privacy display data.
func (review PrivacyReview) Validate() error {
	if !validPrivacyOrigin(review.Origin) || !validPrivacyPolicyVersion(review.PolicyVersion) ||
		!validPrivacyDigest(review.Revision) || !review.Decision.valid() ||
		len(review.Categories) != len(privacyCategoryCatalog) || len(review.NeverEligible) != len(neverEligibleModelData) {
		return ErrPrivacyRecord
	}
	for index, definition := range privacyCategoryCatalog {
		category := review.Categories[index]
		wantEnabled := definition.ID != DataCategoryRedactedContainerOutput || review.LogsEnabled
		if category.ID != definition.ID || category.Description != definition.Description || category.Enabled != wantEnabled {
			return ErrPrivacyRecord
		}
	}
	for index, value := range neverEligibleModelData {
		if review.NeverEligible[index] != value {
			return ErrPrivacyRecord
		}
	}
	wantRevision := privacyRevision(review.PolicyVersion, privacyOriginHash(review.Origin), enabledPrivacyCategories(review.LogsEnabled))
	if review.Revision != wantRevision {
		return ErrPrivacyRecord
	}
	return nil
}

// PrivacyRecord is the complete durable allowlist. It intentionally contains
// an origin hash rather than the configured origin itself.
type PrivacyRecord struct {
	PolicyVersion string
	OriginHash    string
	Categories    []ModelDataCategory
	Decision      PrivacyDecision
	DecidedAt     time.Time
	SchemaVersion int
}

// Validate checks the storage-neutral consent tuple and fixed category set.
func (record PrivacyRecord) Validate() error {
	if !validPrivacyPolicyVersion(record.PolicyVersion) || !validPrivacyDigest(record.OriginHash) ||
		!record.Decision.valid() || !validCoordinatorTime(record.DecidedAt) ||
		record.SchemaVersion != PrivacyRecordSchemaVersion {
		return ErrPrivacyRecord
	}
	if _, ok := privacyLogsSetting(record.Categories); !ok {
		return ErrPrivacyRecord
	}
	return nil
}

func (decision PrivacyDecision) valid() bool {
	return decision == PrivacyDecisionPending || decision == PrivacyDecisionAccepted ||
		decision == PrivacyDecisionRejected || decision == PrivacyDecisionRevoked
}

// PrivacyStore persists the one process-global model-transfer decision tuple.
type PrivacyStore interface {
	LoadPrivacy(context.Context) (PrivacyRecord, bool, error)
	SavePrivacy(context.Context, PrivacyRecord) error
}

// PrivacyManagerConfig contains only code-owned policy and a narrow store.
type PrivacyManagerConfig struct {
	Store         PrivacyStore
	Origin        string
	PolicyVersion string
	LogsEnabled   bool
	Now           func() time.Time
}

// PrivacyManager owns consent lifecycle and the current log category. It owns
// no goroutine and performs no I/O while holding its state mutex.
type PrivacyManager struct {
	store         PrivacyStore
	origin        string
	originHash    string
	policyVersion string
	now           func() time.Time

	loadMu      sync.Mutex
	writeMu     sync.Mutex
	mu          sync.RWMutex
	loaded      bool
	logsEnabled bool
	record      *PrivacyRecord
}

// NewPrivacyManager constructs the policy without loading durable state.
func NewPrivacyManager(config PrivacyManagerConfig) (*PrivacyManager, error) {
	version := config.PolicyVersion
	if version == "" {
		version = PrivacyPolicyVersion
	}
	if config.Store == nil || config.Now == nil || !validCoordinatorTime(config.Now()) ||
		config.Origin != "" && !validPrivacyOrigin(config.Origin) || !validPrivacyPolicyVersion(version) {
		return nil, ErrPrivacyConfiguration
	}
	return &PrivacyManager{
		store: config.Store, origin: config.Origin, originHash: privacyOriginHash(config.Origin),
		policyVersion: version, now: config.Now, logsEnabled: config.LogsEnabled,
	}, nil
}

// ReconfigureOrigin replaces the sole canonical model origin and invalidates
// all in-memory authority when the binding changes. Durable records remain
// historical and are matched again only by their exact origin hash.
func (manager *PrivacyManager) ReconfigureOrigin(origin string) error {
	if manager == nil || !validPrivacyOrigin(origin) {
		return ErrPrivacyConfiguration
	}
	manager.writeMu.Lock()
	defer manager.writeMu.Unlock()
	manager.loadMu.Lock()
	defer manager.loadMu.Unlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.origin == origin {
		return nil
	}
	manager.origin = origin
	manager.originHash = privacyOriginHash(origin)
	manager.loaded = false
	manager.record = nil
	return nil
}

// Review returns the exact current display contract after loading persisted state.
func (manager *PrivacyManager) Review(ctx context.Context) (PrivacyReview, error) {
	if err := manager.ensureLoaded(ctx); err != nil {
		return PrivacyReview{}, err
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.reviewLocked(), nil
}

// AuthorizeModel performs the current exact-tuple check used before durable run
// start and again immediately before each transport request.
func (manager *PrivacyManager) AuthorizeModel(ctx context.Context) (bool, error) {
	if err := manager.ensureLoaded(ctx); err != nil {
		return false, err
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.acceptedLocked(), nil
}

// AuthorizeLogs fails closed before either fixed container-log Tool can read.
func (manager *PrivacyManager) AuthorizeLogs(ctx context.Context) PrivacyLogAuthorization {
	if err := manager.ensureLoaded(ctx); err != nil {
		return PrivacyLogConsentRequired
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.logsEnabled {
		return PrivacyLogDenied
	}
	if !manager.acceptedLocked() {
		return PrivacyLogConsentRequired
	}
	return PrivacyLogAllowed
}

// Decide persists one exact, revision-bound user action. A category toggle
// always returns to pending consent and therefore cannot inherit authority.
func (manager *PrivacyManager) Decide(
	ctx context.Context,
	action PrivacyAction,
	revision string,
	logsEnabled *bool,
) (PrivacyReview, error) {
	if err := manager.ensureLoaded(ctx); err != nil {
		return PrivacyReview{}, err
	}
	if ctx == nil || ctx.Err() != nil {
		if ctx == nil {
			return PrivacyReview{}, context.Canceled
		}
		return PrivacyReview{}, ctx.Err()
	}
	manager.writeMu.Lock()
	defer manager.writeMu.Unlock()

	manager.mu.RLock()
	current := manager.reviewLocked()
	manager.mu.RUnlock()
	if revision != current.Revision {
		return PrivacyReview{}, ErrPrivacyReviewStale
	}

	decision := PrivacyDecisionPending
	nextLogs := current.LogsEnabled
	switch action {
	case PrivacyActionAccept:
		if logsEnabled != nil {
			return PrivacyReview{}, ErrPrivacyRecord
		}
		decision = PrivacyDecisionAccepted
	case PrivacyActionReject:
		if logsEnabled != nil {
			return PrivacyReview{}, ErrPrivacyRecord
		}
		decision = PrivacyDecisionRejected
	case PrivacyActionRevoke:
		if logsEnabled != nil {
			return PrivacyReview{}, ErrPrivacyRecord
		}
		decision = PrivacyDecisionRevoked
	case PrivacyActionToggleLogs:
		if logsEnabled == nil || *logsEnabled == current.LogsEnabled {
			return PrivacyReview{}, ErrPrivacyRecord
		}
		nextLogs = *logsEnabled
	default:
		return PrivacyReview{}, ErrPrivacyRecord
	}
	decidedAt := manager.now().UTC()
	record := PrivacyRecord{
		PolicyVersion: manager.policyVersion, OriginHash: manager.originHash,
		Categories: enabledPrivacyCategories(nextLogs), Decision: decision,
		DecidedAt: decidedAt, SchemaVersion: PrivacyRecordSchemaVersion,
	}
	if record.Validate() != nil {
		return PrivacyReview{}, ErrPrivacyRecord
	}
	if err := manager.store.SavePrivacy(ctx, record); err != nil {
		return PrivacyReview{}, fmt.Errorf("%w: %w", ErrPrivacyPersistence, err)
	}
	manager.mu.Lock()
	manager.logsEnabled = nextLogs
	copy := clonePrivacyRecord(record)
	manager.record = &copy
	result := manager.reviewLocked()
	manager.mu.Unlock()
	return result, nil
}

// FailClosed removes in-memory authority after a privacy persistence failure.
func (manager *PrivacyManager) FailClosed() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	manager.loaded = true
	manager.record = nil
	manager.mu.Unlock()
}

func (manager *PrivacyManager) ensureLoaded(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrPrivacyConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manager.mu.RLock()
	originConfigured := manager.origin != ""
	manager.mu.RUnlock()
	if !originConfigured {
		return ErrModelUnconfigured
	}
	manager.mu.RLock()
	loaded := manager.loaded
	manager.mu.RUnlock()
	if loaded {
		return nil
	}
	manager.loadMu.Lock()
	defer manager.loadMu.Unlock()
	manager.mu.RLock()
	loaded = manager.loaded
	manager.mu.RUnlock()
	if loaded {
		return nil
	}
	record, found, err := manager.store.LoadPrivacy(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPrivacyPersistence, err)
	}
	if found && record.Validate() != nil {
		return ErrPrivacyPersistence
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if found {
		copy := clonePrivacyRecord(record)
		manager.record = &copy
		if record.PolicyVersion == manager.policyVersion && record.OriginHash == manager.originHash {
			if enabled, ok := privacyLogsSetting(record.Categories); ok {
				manager.logsEnabled = enabled
			}
		}
	}
	manager.loaded = true
	return nil
}

func (manager *PrivacyManager) reviewLocked() PrivacyReview {
	decision := PrivacyDecisionPending
	if manager.acceptedLocked() {
		decision = PrivacyDecisionAccepted
	} else if manager.recordMatchesLocked() {
		decision = manager.record.Decision
	}
	categories := make([]PrivacyCategoryReview, len(privacyCategoryCatalog))
	for index, definition := range privacyCategoryCatalog {
		categories[index] = PrivacyCategoryReview{
			ID: definition.ID, Description: definition.Description,
			Enabled: definition.ID != DataCategoryRedactedContainerOutput || manager.logsEnabled,
		}
	}
	never := append([]string(nil), neverEligibleModelData[:]...)
	enabled := enabledPrivacyCategories(manager.logsEnabled)
	return PrivacyReview{
		Origin: manager.origin, PolicyVersion: manager.policyVersion,
		Revision: privacyRevision(manager.policyVersion, manager.originHash, enabled),
		Decision: decision, LogsEnabled: manager.logsEnabled,
		Categories: categories, NeverEligible: never,
	}
}

func (manager *PrivacyManager) acceptedLocked() bool {
	return manager.recordMatchesLocked() && manager.record.Decision == PrivacyDecisionAccepted
}

func (manager *PrivacyManager) recordMatchesLocked() bool {
	if manager.record == nil || manager.record.PolicyVersion != manager.policyVersion ||
		manager.record.OriginHash != manager.originHash {
		return false
	}
	want := enabledPrivacyCategories(manager.logsEnabled)
	return equalPrivacyCategories(manager.record.Categories, want)
}

func enabledPrivacyCategories(logsEnabled bool) []ModelDataCategory {
	result := make([]ModelDataCategory, 0, len(privacyCategoryCatalog))
	for _, definition := range privacyCategoryCatalog {
		if definition.ID == DataCategoryRedactedContainerOutput && !logsEnabled {
			continue
		}
		result = append(result, definition.ID)
	}
	return result
}

func privacyLogsSetting(categories []ModelDataCategory) (bool, bool) {
	withoutLogs := enabledPrivacyCategories(false)
	if equalPrivacyCategories(categories, withoutLogs) {
		return false, true
	}
	withLogs := enabledPrivacyCategories(true)
	if equalPrivacyCategories(categories, withLogs) {
		return true, true
	}
	return false, false
}

func equalPrivacyCategories(left, right []ModelDataCategory) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func clonePrivacyRecord(record PrivacyRecord) PrivacyRecord {
	record.Categories = append([]ModelDataCategory(nil), record.Categories...)
	return record
}

func privacyOriginHash(origin string) string {
	digest := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(digest[:])
}

func privacyRevision(policyVersion, originHash string, categories []ModelDataCategory) string {
	values := make([]string, len(categories))
	for index, category := range categories {
		values[index] = string(category)
	}
	digest := sha256.Sum256([]byte(policyVersion + "\n" + originHash + "\n" + strings.Join(values, "\n")))
	return hex.EncodeToString(digest[:])
}

func validPrivacyDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
}

func validPrivacyPolicyVersion(value string) bool {
	if value == "" || len(value) > maxPrivacyPolicyBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return true
}

func validPrivacyOrigin(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") || value != parsed.Scheme+"://"+parsed.Host ||
		parsed.Hostname() != strings.ToLower(parsed.Hostname()) {
		return false
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		return host == "localhost" || ip != nil && ip.IsLoopback()
	}
	return true
}
