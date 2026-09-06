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

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// PrivacyPolicyVersion changes whenever an eligible category or its meaning changes.
	PrivacyPolicyVersion       = "2026-09-05.v4"
	PrivacyRecordSchemaVersion = 3
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
	DataCategoryProjectedMetrics        ModelDataCategory = "projected_kubernetes_metrics"
	DataCategoryPrometheusResults       ModelDataCategory = "projected_prometheus_results"
	DataCategoryRedactedLokiOutput      ModelDataCategory = "redacted_loki_output"
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
	{DataCategorySafeConversationContext, "Retained safe user questions, final validated assistant answers, and a bounded untrusted summary from this exact Session."},
	{DataCategoryResourceReferences, "The active Context and Namespace, plus permitted resource names and references used by this diagnostic run."},
	{DataCategoryProjectedStatus, "Permitted Kubernetes status, conditions, counts, times, and resource relationships."},
	{DataCategoryProjectedEvents, "Recent permitted Kubernetes Event reasons and messages after local safety checks."},
	{DataCategoryRedactedContainerOutput, "Bounded container logs, remote-command output, container-file content, or diagnostic-Pod output after local cleanup, size limits, and sensitive-value filtering."},
	{DataCategoryProjectedMetrics, "Current Kubernetes Metrics API CPU and memory snapshots normalized to integer quantities."},
	{DataCategoryPrometheusResults, "Bounded samples from explicitly enabled code-owned Prometheus query templates."},
	{DataCategoryRedactedLokiOutput, "Bounded Loki log excerpts after local multiline, terminal, and sensitive-value filtering."},
}

var neverEligibleModelData = [...]string{
	"Kubeconfig contents and Kubernetes credentials",
	"Tokens, certificates, private keys, and model API keys",
	"Kubernetes Secret objects or Secret data",
	"Raw Kubernetes objects, full YAML, and unrestricted fields",
	"Raw Events and raw or unbounded container output",
	"Local process output, including bounded or redacted command output",
	"Assembled raw prompts, protocol bodies, headers, and vendor errors",
	"Raw PromQL, raw LogQL, arbitrary observability URLs or headers, and observability credentials",
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
	Role          domain.ModelRole
	Origin        string
	PolicyVersion string
	Revision      string
	Decision      PrivacyDecision
	LogsEnabled   bool
	Categories    []PrivacyCategoryReview
	NeverEligible []string
	DataSources   []PrivacyDataSourceReview
}

type PrivacyDataSourceReview struct {
	Kind       domain.DataSourceKind
	Origin     string
	OriginHash string
	Enabled    bool
}

// Validate rejects forged or widened privacy display data.
func (review PrivacyReview) Validate() error {
	if !review.Role.Valid() || !validPrivacyOrigin(review.Origin) || !validPrivacyPolicyVersion(review.PolicyVersion) ||
		!validPrivacyDigest(review.Revision) || !review.Decision.valid() ||
		len(review.Categories) != len(privacyCategoryCatalog) || len(review.NeverEligible) != len(neverEligibleModelData) || len(review.DataSources) != 2 {
		return ErrPrivacyRecord
	}
	for index, definition := range privacyCategoryCatalog {
		category := review.Categories[index]
		wantEnabled := reviewCategoryEnabled(definition.ID, review.LogsEnabled, review.DataSources[0].Enabled, review.DataSources[1].Enabled)
		if category.ID != definition.ID || category.Description != definition.Description || category.Enabled != wantEnabled {
			return ErrPrivacyRecord
		}
	}
	for index, value := range neverEligibleModelData {
		if review.NeverEligible[index] != value {
			return ErrPrivacyRecord
		}
	}
	if review.DataSources[0].Kind != domain.DataSourcePrometheus || review.DataSources[1].Kind != domain.DataSourceLoki {
		return ErrPrivacyRecord
	}
	for _, source := range review.DataSources {
		if source.Enabled != (source.Origin != "") || source.Enabled && (!validPrivacyOrigin(source.Origin) || source.OriginHash != privacyOriginHash(source.Origin)) || !source.Enabled && source.OriginHash != "" {
			return ErrPrivacyRecord
		}
	}
	wantRevision := privacyRevision(review.PolicyVersion, review.Role, privacyOriginHash(review.Origin), enabledPrivacyCategories(review.LogsEnabled, review.DataSources[0].Enabled, review.DataSources[1].Enabled), review.DataSources[0].OriginHash, review.DataSources[1].OriginHash)
	if review.Revision != wantRevision {
		return ErrPrivacyRecord
	}
	return nil
}

// PrivacyRecord is the complete durable allowlist. It intentionally contains
// an origin hash rather than the configured origin itself.
type PrivacyRecord struct {
	Role                 domain.ModelRole
	PolicyVersion        string
	OriginHash           string
	PrometheusOriginHash string
	LokiOriginHash       string
	Categories           []ModelDataCategory
	Decision             PrivacyDecision
	DecidedAt            time.Time
	SchemaVersion        int
}

// Validate checks the storage-neutral consent tuple and fixed category set.
func (record PrivacyRecord) Validate() error {
	if !record.Role.Valid() || !validPrivacyPolicyVersion(record.PolicyVersion) || !validPrivacyDigest(record.OriginHash) ||
		!record.Decision.valid() || !validCoordinatorTime(record.DecidedAt) ||
		record.SchemaVersion != PrivacyRecordSchemaVersion {
		return ErrPrivacyRecord
	}
	if record.PrometheusOriginHash != "" && !validPrivacyDigest(record.PrometheusOriginHash) || record.LokiOriginHash != "" && !validPrivacyDigest(record.LokiOriginHash) ||
		!validPrivacyCategorySequence(record.Categories, record.PrometheusOriginHash, record.LokiOriginHash) {
		return ErrPrivacyRecord
	}
	return nil
}

func (decision PrivacyDecision) valid() bool {
	return decision == PrivacyDecisionPending || decision == PrivacyDecisionAccepted ||
		decision == PrivacyDecisionRejected || decision == PrivacyDecisionRevoked
}

// PrivacyStore persists one role-bound model-transfer decision tuple.
type PrivacyStore interface {
	LoadPrivacy(context.Context) (PrivacyRecord, bool, error)
	SavePrivacy(context.Context, PrivacyRecord) error
}

// PrivacyManagerConfig contains only code-owned policy and a narrow store.
type PrivacyManagerConfig struct {
	Store            PrivacyStore
	Role             domain.ModelRole
	Origin           string
	PolicyVersion    string
	LogsEnabled      bool
	PrometheusOrigin string
	LokiOrigin       string
	Now              func() time.Time
}

// PrivacyManager owns consent lifecycle and the current log category. It owns
// no goroutine and performs no I/O while holding its state mutex.
type PrivacyManager struct {
	store                PrivacyStore
	role                 domain.ModelRole
	origin               string
	originHash           string
	prometheusOrigin     string
	prometheusOriginHash string
	lokiOrigin           string
	lokiOriginHash       string
	policyVersion        string
	now                  func() time.Time

	loadMu      sync.Mutex
	writeMu     sync.Mutex
	mu          sync.RWMutex
	loaded      bool
	logsEnabled bool
	record      *PrivacyRecord
	revision    uint64
}

// PrivacyBindingSnapshot is a content-free, no-I/O status projection.
type PrivacyBindingSnapshot struct {
	Role       domain.ModelRole
	OriginHash string
	Loaded     bool
	Accepted   bool
	Revision   uint64
}

// NewPrivacyManager constructs the policy without loading durable state.
func NewPrivacyManager(config PrivacyManagerConfig) (*PrivacyManager, error) {
	version := config.PolicyVersion
	if version == "" {
		version = PrivacyPolicyVersion
	}
	role := config.Role
	if role == "" {
		role = domain.ModelRoleAgent
	}
	if config.Store == nil || config.Now == nil || !validCoordinatorTime(config.Now()) ||
		!role.Valid() || config.Origin != "" && !validPrivacyOrigin(config.Origin) || config.PrometheusOrigin != "" && !validPrivacyOrigin(config.PrometheusOrigin) ||
		config.LokiOrigin != "" && !validPrivacyOrigin(config.LokiOrigin) || !validPrivacyPolicyVersion(version) {
		return nil, ErrPrivacyConfiguration
	}
	return &PrivacyManager{
		store: config.Store, role: role, origin: config.Origin, originHash: privacyOriginHash(config.Origin),
		prometheusOrigin: config.PrometheusOrigin, prometheusOriginHash: optionalPrivacyOriginHash(config.PrometheusOrigin),
		lokiOrigin: config.LokiOrigin, lokiOriginHash: optionalPrivacyOriginHash(config.LokiOrigin),
		policyVersion: version, now: config.Now, logsEnabled: config.LogsEnabled, revision: 1,
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
	manager.advanceRevisionLocked()
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

// OriginHash returns the current content-free canonical origin binding without I/O.
func (manager *PrivacyManager) OriginHash() string {
	if manager == nil {
		return ""
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.originHash
}

// Snapshot reports current in-memory consent state without loading storage.
func (manager *PrivacyManager) Snapshot() PrivacyBindingSnapshot {
	if manager == nil {
		return PrivacyBindingSnapshot{}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return PrivacyBindingSnapshot{
		Role: manager.role, OriginHash: manager.originHash,
		Loaded: manager.loaded, Accepted: manager.loaded && manager.acceptedLocked(), Revision: manager.revision,
	}
}

// AuthorizeContainerOutput fails closed before any admitted container output,
// including logs, Pod Exec, file content, or diagnostic-Pod output, can cross
// the model boundary.
func (manager *PrivacyManager) AuthorizeContainerOutput(ctx context.Context) PrivacyLogAuthorization {
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

// AuthorizeLogs preserves the narrower log-facing contract while sharing the
// same consent-bound redacted-container-output category.
func (manager *PrivacyManager) AuthorizeLogs(ctx context.Context) PrivacyLogAuthorization {
	return manager.AuthorizeContainerOutput(ctx)
}

// AuthorizeDataSource binds an optional-source transfer to the exact accepted
// source origin and category. Loki additionally requires the log category.
func (manager *PrivacyManager) AuthorizeDataSource(ctx context.Context, kind domain.DataSourceKind, originHash string) PrivacyLogAuthorization {
	if err := manager.ensureLoaded(ctx); err != nil {
		return PrivacyLogConsentRequired
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.acceptedLocked() {
		return PrivacyLogConsentRequired
	}
	switch kind {
	case domain.DataSourcePrometheus:
		if manager.prometheusOriginHash == "" || originHash != manager.prometheusOriginHash {
			return PrivacyLogDenied
		}
	case domain.DataSourceLoki:
		if manager.lokiOriginHash == "" || originHash != manager.lokiOriginHash || !manager.logsEnabled {
			return PrivacyLogDenied
		}
	default:
		return PrivacyLogDenied
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
		Role: manager.role, PolicyVersion: manager.policyVersion, OriginHash: manager.originHash,
		PrometheusOriginHash: manager.prometheusOriginHash, LokiOriginHash: manager.lokiOriginHash,
		Categories: enabledPrivacyCategories(nextLogs, manager.prometheusOrigin != "", manager.lokiOrigin != ""), Decision: decision,
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
	manager.advanceRevisionLocked()
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
	manager.advanceRevisionLocked()
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
	if found && (record.Validate() != nil || record.Role != manager.role) {
		return ErrPrivacyPersistence
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if found {
		copy := clonePrivacyRecord(record)
		manager.record = &copy
		if record.PolicyVersion == manager.policyVersion && record.OriginHash == manager.originHash &&
			record.PrometheusOriginHash == manager.prometheusOriginHash && record.LokiOriginHash == manager.lokiOriginHash {
			if enabled, ok := privacyLogsSetting(record.Categories); ok {
				manager.logsEnabled = enabled
			}
		}
	}
	manager.loaded = true
	manager.advanceRevisionLocked()
	return nil
}

func (manager *PrivacyManager) advanceRevisionLocked() {
	if manager.revision < ^uint64(0) {
		manager.revision++
	}
}

func (manager *PrivacyManager) reviewLocked() PrivacyReview {
	decision := PrivacyDecisionPending
	if manager.acceptedLocked() {
		decision = PrivacyDecisionAccepted
	} else if manager.recordMatchesLocked() {
		decision = manager.record.Decision
	}
	prometheusEnabled, lokiEnabled := manager.prometheusOrigin != "", manager.lokiOrigin != ""
	categories := make([]PrivacyCategoryReview, len(privacyCategoryCatalog))
	for index, definition := range privacyCategoryCatalog {
		categories[index] = PrivacyCategoryReview{
			ID: definition.ID, Description: definition.Description,
			Enabled: reviewCategoryEnabled(definition.ID, manager.logsEnabled, prometheusEnabled, lokiEnabled),
		}
	}
	never := append([]string(nil), neverEligibleModelData[:]...)
	enabled := enabledPrivacyCategories(manager.logsEnabled, prometheusEnabled, lokiEnabled)
	sources := []PrivacyDataSourceReview{
		{Kind: domain.DataSourcePrometheus, Origin: manager.prometheusOrigin, OriginHash: manager.prometheusOriginHash, Enabled: prometheusEnabled},
		{Kind: domain.DataSourceLoki, Origin: manager.lokiOrigin, OriginHash: manager.lokiOriginHash, Enabled: lokiEnabled},
	}
	return PrivacyReview{
		Role: manager.role, Origin: manager.origin, PolicyVersion: manager.policyVersion,
		Revision: privacyRevision(manager.policyVersion, manager.role, manager.originHash, enabled, manager.prometheusOriginHash, manager.lokiOriginHash),
		Decision: decision, LogsEnabled: manager.logsEnabled,
		Categories: categories, NeverEligible: never, DataSources: sources,
	}
}

func (manager *PrivacyManager) acceptedLocked() bool {
	return manager.recordMatchesLocked() && manager.record.Decision == PrivacyDecisionAccepted
}

func (manager *PrivacyManager) recordMatchesLocked() bool {
	if manager.record == nil || manager.record.Role != manager.role || manager.record.PolicyVersion != manager.policyVersion ||
		manager.record.OriginHash != manager.originHash || manager.record.PrometheusOriginHash != manager.prometheusOriginHash || manager.record.LokiOriginHash != manager.lokiOriginHash {
		return false
	}
	want := enabledPrivacyCategories(manager.logsEnabled, manager.prometheusOrigin != "", manager.lokiOrigin != "")
	return equalPrivacyCategories(manager.record.Categories, want)
}

func enabledPrivacyCategories(logsEnabled bool, sourceEnabled ...bool) []ModelDataCategory {
	prometheusEnabled, lokiEnabled := false, false
	if len(sourceEnabled) > 0 {
		prometheusEnabled = sourceEnabled[0]
	}
	if len(sourceEnabled) > 1 {
		lokiEnabled = sourceEnabled[1]
	}
	result := make([]ModelDataCategory, 0, len(privacyCategoryCatalog))
	for _, definition := range privacyCategoryCatalog {
		if !reviewCategoryEnabled(definition.ID, logsEnabled, prometheusEnabled, lokiEnabled) {
			continue
		}
		result = append(result, definition.ID)
	}
	return result
}

func reviewCategoryEnabled(category ModelDataCategory, logs, prometheus, loki bool) bool {
	switch category {
	case DataCategoryRedactedContainerOutput:
		return logs
	case DataCategoryPrometheusResults:
		return prometheus
	case DataCategoryRedactedLokiOutput:
		return loki && logs
	default:
		return true
	}
}

func privacyLogsSetting(categories []ModelDataCategory) (bool, bool) {
	for _, prometheus := range []bool{false, true} {
		for _, loki := range []bool{false, true} {
			if equalPrivacyCategories(categories, enabledPrivacyCategories(false, prometheus, loki)) {
				return false, true
			}
			if equalPrivacyCategories(categories, enabledPrivacyCategories(true, prometheus, loki)) {
				return true, true
			}
		}
	}
	return false, false
}

func validPrivacyCategorySequence(categories []ModelDataCategory, prometheusOriginHash, lokiOriginHash string) bool {
	logs, ok := privacyLogsSetting(categories)
	return ok && equalPrivacyCategories(categories, enabledPrivacyCategories(logs, prometheusOriginHash != "", lokiOriginHash != ""))
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

func privacyRevision(policyVersion string, role domain.ModelRole, originHash string, categories []ModelDataCategory, sourceHashes ...string) string {
	values := make([]string, len(categories))
	for index, category := range categories {
		values[index] = string(category)
	}
	digest := sha256.Sum256([]byte(policyVersion + "\n" + string(role) + "\n" + originHash + "\n" + strings.Join(sourceHashes, "\n") + "\n" + strings.Join(values, "\n")))
	return hex.EncodeToString(digest[:])
}

func optionalPrivacyOriginHash(origin string) string {
	if origin == "" {
		return ""
	}
	return privacyOriginHash(origin)
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
