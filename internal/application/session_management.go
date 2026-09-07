package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	SessionListDefaultLimit       = 20
	SessionListMaximumLimit       = 100
	SessionDeleteDefaultLimit     = 50
	SessionDeleteMaximumLimit     = 100
	SessionDeleteMaximumAgeDays   = 3650
	SessionDeletionPlanSchema     = "kupilot.session-deletion-plan/v1"
	SessionListCursorSchema       = "kupilot.session-list-cursor/v1"
	MaxSessionListCursorBytes     = 256
	MaxSessionDeletionDigestBytes = 64
)

var (
	ErrInvalidSessionManagementRequest = errors.New("Session management request is invalid")
	ErrSessionDeletionProtected        = errors.New("the selected Session is protected from deletion")
	ErrSessionDeletionLimit            = errors.New("the Session deletion selection exceeds its fixed limit")
	ErrSessionDeletionStale            = errors.New("the Session deletion selection changed before commit")
	ErrSessionProcessActive            = errors.New("another Kupilot process may be active")
)

// SessionProtectionReason is a fixed content-free explanation for why a
// Session cannot enter a destructive selection.
type SessionProtectionReason string

const (
	SessionProtectionNone            SessionProtectionReason = ""
	SessionProtectionCurrent         SessionProtectionReason = "current"
	SessionProtectionActiveRun       SessionProtectionReason = "active_run"
	SessionProtectionActiveAuthority SessionProtectionReason = "active_authority"
	SessionProtectionCorruptActivity SessionProtectionReason = "corrupt_activity"
	SessionProtectionFutureActivity  SessionProtectionReason = "future_activity"
	SessionProtectionUnknownState    SessionProtectionReason = "unknown_state"
)

func (reason SessionProtectionReason) valid() bool {
	switch reason {
	case SessionProtectionNone, SessionProtectionCurrent, SessionProtectionActiveRun,
		SessionProtectionActiveAuthority, SessionProtectionCorruptActivity,
		SessionProtectionFutureActivity, SessionProtectionUnknownState:
		return true
	default:
		return false
	}
}

// SessionMetadataRecord is the narrow storage projection used by Application.
// Integer timestamps remain untrusted until Application validates them against
// the frozen query time.
type SessionMetadataRecord struct {
	ID                   domain.SessionID
	Title                string
	Status               domain.SessionStatus
	PrivacyMode          domain.PrivacyMode
	Version              int64
	CreatedAtUnixMillis  int64
	LastActiveUnixMillis int64
	UpdatedAtUnixMillis  int64
	CommittedMessages    int64
	ActiveRuns           int64
	ActiveAuthorities    int64
}

func (record SessionMetadataRecord) basicValid() bool {
	return record.ID.Valid() && utf8.ValidString(record.Title) && len(record.Title) <= 512 &&
		(record.Status == domain.SessionStatusActive || record.Status == domain.SessionStatusArchived) &&
		(record.PrivacyMode == domain.PrivacyModeStandard || record.PrivacyMode == domain.PrivacyModeMinimal) &&
		record.Version >= 1 && record.CommittedMessages >= 0 && record.ActiveRuns >= 0 && record.ActiveAuthorities >= 0
}

// SessionListStoreRequest is the exact descending keyset query implemented by
// the persistence adapter.
type SessionListStoreRequest struct {
	Limit                  int
	BeforeLastActiveMillis *int64
	BeforeID               domain.SessionID
}

func (request SessionListStoreRequest) validate() bool {
	if request.Limit < 1 || request.Limit > SessionListMaximumLimit+1 {
		return false
	}
	return (request.BeforeLastActiveMillis == nil) == (request.BeforeID == "") &&
		(request.BeforeID == "" || request.BeforeID.Valid())
}

// SessionListStorePage contains at most one look-ahead row.
type SessionListStorePage struct {
	Sessions []SessionMetadataRecord
}

// SessionDeletionKind separates one exact graph from one inactive batch.
type SessionDeletionKind string

const (
	SessionDeletionExact  SessionDeletionKind = "exact"
	SessionDeletionBefore SessionDeletionKind = "before"
)

func (kind SessionDeletionKind) valid() bool {
	return kind == SessionDeletionExact || kind == SessionDeletionBefore
}

// SessionDeletionSelectionRequest freezes every selector used by persistence.
type SessionDeletionSelectionRequest struct {
	Kind             SessionDeletionKind
	SessionID        domain.SessionID
	Cutoff           time.Time
	FrozenNow        time.Time
	Limit            int
	CurrentSessionID domain.SessionID
}

func (request SessionDeletionSelectionRequest) validate() bool {
	if !request.Kind.valid() || !validCoordinatorTime(request.FrozenNow) || request.Limit < 1 || request.Limit > SessionDeleteMaximumLimit {
		return false
	}
	if request.CurrentSessionID != "" && !request.CurrentSessionID.Valid() {
		return false
	}
	if request.Kind == SessionDeletionExact {
		return request.SessionID.Valid() && request.Cutoff.IsZero()
	}
	return request.SessionID == "" && validCoordinatorTime(request.Cutoff)
}

// SessionDeletionSnapshot is the complete content-free selection observed by
// one preview query. Selected is ordered oldest first.
type SessionDeletionSnapshot struct {
	Request        SessionDeletionSelectionRequest
	Matched        int
	Eligible       int
	Protected      int
	Remaining      int
	OverLimit      bool
	SchemaRevision int64
	Selected       []SessionMetadataRecord
}

func (snapshot SessionDeletionSnapshot) validate() bool {
	if !snapshot.Request.validate() || snapshot.Matched < 0 || snapshot.Eligible < 0 || snapshot.Protected < 0 ||
		snapshot.Remaining < 0 || snapshot.SchemaRevision < 1 || snapshot.Matched != snapshot.Eligible+snapshot.Protected ||
		len(snapshot.Selected) > snapshot.Request.Limit || snapshot.Eligible < len(snapshot.Selected) ||
		snapshot.OverLimit != (snapshot.Eligible > snapshot.Request.Limit) {
		return false
	}
	var previous SessionMetadataRecord
	for index, record := range snapshot.Selected {
		if !record.basicValid() || record.ActiveRuns != 0 || record.ActiveAuthorities != 0 ||
			record.LastActiveUnixMillis < record.CreatedAtUnixMillis ||
			record.LastActiveUnixMillis > snapshot.Request.FrozenNow.UTC().UnixMilli() ||
			snapshot.Request.Kind == SessionDeletionBefore && record.ID == snapshot.Request.CurrentSessionID {
			return false
		}
		if snapshot.Request.Kind == SessionDeletionExact && (len(snapshot.Selected) != 1 || record.ID != snapshot.Request.SessionID) ||
			snapshot.Request.Kind == SessionDeletionBefore && record.LastActiveUnixMillis >= snapshot.Request.Cutoff.UTC().UnixMilli() {
			return false
		}
		if index > 0 && (record.LastActiveUnixMillis < previous.LastActiveUnixMillis ||
			record.LastActiveUnixMillis == previous.LastActiveUnixMillis && record.ID <= previous.ID) {
			return false
		}
		previous = record
	}
	return true
}

// SessionManagementPersistence is the consumer-owned bounded Session metadata
// and deletion port. It exposes no SQL, transaction, DB handle, or generic CRUD.
type SessionManagementPersistence interface {
	ListSessionMetadata(context.Context, SessionListStoreRequest) (SessionListStorePage, error)
	PreviewSessionDeletion(context.Context, SessionDeletionSelectionRequest) (SessionDeletionSnapshot, error)
	CommitSessionDeletion(context.Context, SessionDeletionSnapshot) (remaining int, err error)
	SessionStorageHealth(context.Context, time.Time) (SessionStorageHealth, error)
}

// SessionDeletionIsolation proves that one batch commit is isolated from any
// other active Kupilot process. It carries no Session data or durable plan.
type SessionDeletionIsolation interface {
	AcquireExclusive(context.Context) (SessionDeletionLease, error)
}

// SessionDeletionLease is released after the one transaction attempt.
type SessionDeletionLease interface {
	Release() error
}

// SessionMetadata is the validated public-safe Application projection.
type SessionMetadata struct {
	ID               domain.SessionID        `json:"id"`
	Title            string                  `json:"title"`
	Status           domain.SessionStatus    `json:"status"`
	PrivacyMode      domain.PrivacyMode      `json:"privacy_mode"`
	LastActivityAt   time.Time               `json:"last_activity_at"`
	Current          bool                    `json:"current"`
	Resumable        bool                    `json:"resumable"`
	Protected        bool                    `json:"protected"`
	ProtectionReason SessionProtectionReason `json:"protection_reason,omitempty"`
	DeletionEligible bool                    `json:"deletion_eligible"`
}

func projectSessionMetadata(record SessionMetadataRecord, current domain.SessionID, now time.Time) (SessionMetadata, bool) {
	if !record.basicValid() || !validCoordinatorTime(now) {
		return SessionMetadata{}, false
	}
	result := SessionMetadata{
		ID: record.ID, Title: record.Title, Status: record.Status, PrivacyMode: record.PrivacyMode,
		LastActivityAt: time.UnixMilli(record.LastActiveUnixMillis).UTC(), Current: record.ID == current,
		Resumable: record.Status == domain.SessionStatusActive && record.PrivacyMode == domain.PrivacyModeStandard && record.CommittedMessages > 0,
	}
	switch {
	case record.LastActiveUnixMillis < 0 || record.CreatedAtUnixMillis < 0 ||
		record.UpdatedAtUnixMillis < 0 || record.LastActiveUnixMillis < record.CreatedAtUnixMillis ||
		record.UpdatedAtUnixMillis < record.LastActiveUnixMillis:
		result.Protected, result.ProtectionReason = true, SessionProtectionCorruptActivity
	case record.LastActiveUnixMillis > now.UTC().UnixMilli():
		result.Protected, result.ProtectionReason = true, SessionProtectionFutureActivity
	case result.Current:
		result.Protected, result.ProtectionReason = true, SessionProtectionCurrent
	case record.ActiveRuns > 0:
		result.Protected, result.ProtectionReason = true, SessionProtectionActiveRun
	case record.ActiveAuthorities > 0:
		result.Protected, result.ProtectionReason = true, SessionProtectionActiveAuthority
	}
	result.DeletionEligible = !result.Protected
	return result, true
}

// SessionListCursor is the public bounded descending keyset cursor.
type SessionListCursor struct {
	LastActivityAt time.Time
	ID             domain.SessionID
}

// SessionListRequest freezes list paging and future-time protection.
type SessionListRequest struct {
	Limit            int
	Cursor           string
	CurrentSessionID domain.SessionID
	FrozenNow        time.Time
}

// SessionListResult is one bounded safe metadata page.
type SessionListResult struct {
	SchemaVersion string            `json:"schema_version"`
	Sessions      []SessionMetadata `json:"sessions"`
	NextCursor    string            `json:"next_cursor,omitempty"`
}

// SessionManager owns local Session discovery and deletion plan authority.
type SessionManager struct {
	store     SessionManagementPersistence
	now       func() time.Time
	isolation SessionDeletionIsolation
}

// NewIsolatedSessionManager additionally requires an exclusive process lease
// for every batch commit. Exact single-Session deletion does not use it.
func NewIsolatedSessionManager(store SessionManagementPersistence, now func() time.Time, isolation SessionDeletionIsolation) (*SessionManager, error) {
	manager, err := NewSessionManager(store, now)
	if err != nil || isolation == nil {
		return nil, ErrCoordinatorDependency
	}
	manager.isolation = isolation
	return manager, nil
}

// NewSessionManager constructs the Application use case without performing I/O.
func NewSessionManager(store SessionManagementPersistence, now func() time.Time) (*SessionManager, error) {
	if store == nil || now == nil || !validCoordinatorTime(now()) {
		return nil, ErrCoordinatorDependency
	}
	return &SessionManager{store: store, now: now}, nil
}

// List returns one page without changing Session activity.
func (manager *SessionManager) List(ctx context.Context, request SessionListRequest) (SessionListResult, error) {
	if manager == nil || ctx == nil || manager.store == nil {
		return SessionListResult{}, ErrInvalidSessionManagementRequest
	}
	if request.Limit == 0 {
		request.Limit = SessionListDefaultLimit
	}
	if request.FrozenNow.IsZero() {
		request.FrozenNow = manager.now()
	}
	if request.Limit < 1 || request.Limit > SessionListMaximumLimit || !validCoordinatorTime(request.FrozenNow) ||
		request.CurrentSessionID != "" && !request.CurrentSessionID.Valid() {
		return SessionListResult{}, ErrInvalidSessionManagementRequest
	}
	storeRequest := SessionListStoreRequest{Limit: request.Limit + 1}
	if request.Cursor != "" {
		cursor, err := DecodeSessionListCursor(request.Cursor)
		if err != nil {
			return SessionListResult{}, err
		}
		millis := cursor.LastActivityAt.UTC().UnixMilli()
		storeRequest.BeforeLastActiveMillis = &millis
		storeRequest.BeforeID = cursor.ID
	}
	page, err := manager.store.ListSessionMetadata(ctx, storeRequest)
	if err != nil {
		return SessionListResult{}, err
	}
	if len(page.Sessions) > request.Limit+1 {
		return SessionListResult{}, ErrPersistenceUnavailable
	}
	hasNext := len(page.Sessions) > request.Limit
	rows := page.Sessions
	if hasNext {
		rows = rows[:request.Limit]
	}
	result := SessionListResult{SchemaVersion: "kupilot.sessions-list/v1", Sessions: make([]SessionMetadata, 0, len(rows))}
	for _, row := range rows {
		item, ok := projectSessionMetadata(row, request.CurrentSessionID, request.FrozenNow)
		if !ok {
			return SessionListResult{}, ErrPersistenceUnavailable
		}
		result.Sessions = append(result.Sessions, item)
	}
	if hasNext && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.NextCursor = EncodeSessionListCursor(SessionListCursor{
			LastActivityAt: time.UnixMilli(last.LastActiveUnixMillis).UTC(), ID: last.ID,
		})
	}
	return result, nil
}

// EncodeSessionListCursor emits a bounded opaque content-free cursor.
func EncodeSessionListCursor(cursor SessionListCursor) string {
	if !cursor.ID.Valid() || !validCoordinatorTime(cursor.LastActivityAt) {
		return ""
	}
	raw := SessionListCursorSchema + "\x00" + strconv.FormatInt(cursor.LastActivityAt.UTC().UnixMilli(), 10) + "\x00" + string(cursor.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeSessionListCursor validates one exact schema-1 cursor.
func DecodeSessionListCursor(value string) (SessionListCursor, error) {
	if value == "" || len(value) > MaxSessionListCursorBytes {
		return SessionListCursor{}, ErrInvalidSessionManagementRequest
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) > MaxSessionListCursorBytes {
		return SessionListCursor{}, ErrInvalidSessionManagementRequest
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 || parts[0] != SessionListCursorSchema {
		return SessionListCursor{}, ErrInvalidSessionManagementRequest
	}
	millis, err := strconv.ParseInt(parts[1], 10, 64)
	id := domain.SessionID(parts[2])
	if err != nil || millis < 0 || strconv.FormatInt(millis, 10) != parts[1] || !id.Valid() {
		return SessionListCursor{}, ErrInvalidSessionManagementRequest
	}
	return SessionListCursor{LastActivityAt: time.UnixMilli(millis).UTC(), ID: id}, nil
}

// ParseSessionDeletionCutoff resolves one exact relative or RFC3339 cutoff
// against a single frozen instant.
func ParseSessionDeletionCutoff(value string, frozenNow time.Time) (time.Time, error) {
	if value == "" || value != strings.TrimSpace(value) || !validCoordinatorTime(frozenNow) {
		return time.Time{}, ErrInvalidSessionManagementRequest
	}
	if len(value) >= 2 && (value[len(value)-1] == 'd' || value[len(value)-1] == 'w') {
		digits := value[:len(value)-1]
		if digits == "" || digits[0] == '0' && len(digits) > 1 {
			return time.Time{}, ErrInvalidSessionManagementRequest
		}
		for _, current := range digits {
			if current < '0' || current > '9' {
				return time.Time{}, ErrInvalidSessionManagementRequest
			}
		}
		units, err := strconv.ParseInt(digits, 10, 32)
		multiplier := int64(1)
		if value[len(value)-1] == 'w' {
			multiplier = 7
		}
		days := units * multiplier
		if err != nil || units <= 0 || days <= 0 || days > SessionDeleteMaximumAgeDays {
			return time.Time{}, ErrInvalidSessionManagementRequest
		}
		return frozenNow.UTC().Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Millisecond), nil
	}
	cutoff, err := time.Parse(time.RFC3339, value)
	if err != nil || !strings.Contains(value, "T") || cutoff.IsZero() || cutoff.UnixMilli() < 0 ||
		cutoff.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, ErrInvalidSessionManagementRequest
	}
	return cutoff.UTC().Truncate(time.Millisecond), nil
}

// SessionDeletionPlan is the content-free, current-command deletion preview.
type SessionDeletionPlan struct {
	SchemaVersion string
	Snapshot      SessionDeletionSnapshot
	Sessions      []SessionMetadata
	Digest        string
	Oldest        *time.Time
	Newest        *time.Time
}

func (plan SessionDeletionPlan) Validate() error {
	if plan.SchemaVersion != SessionDeletionPlanSchema || !plan.Snapshot.validate() ||
		len(plan.Sessions) != len(plan.Snapshot.Selected) || len(plan.Digest) != MaxSessionDeletionDigestBytes ||
		plan.Digest != sessionDeletionDigest(plan.Snapshot) {
		return ErrInvalidSessionManagementRequest
	}
	if len(plan.Sessions) == 0 {
		if plan.Oldest != nil || plan.Newest != nil {
			return ErrInvalidSessionManagementRequest
		}
		return nil
	}
	if plan.Oldest == nil || plan.Newest == nil || plan.Oldest.After(*plan.Newest) {
		return ErrInvalidSessionManagementRequest
	}
	return nil
}

// PreviewDeletion freezes one exact or cutoff selection without writing.
func (manager *SessionManager) PreviewDeletion(ctx context.Context, request SessionDeletionSelectionRequest) (SessionDeletionPlan, error) {
	if manager == nil || ctx == nil || manager.store == nil {
		return SessionDeletionPlan{}, ErrInvalidSessionManagementRequest
	}
	if err := ctx.Err(); err != nil {
		return SessionDeletionPlan{}, err
	}
	if request.FrozenNow.IsZero() {
		request.FrozenNow = manager.now()
	}
	if request.Limit == 0 {
		request.Limit = SessionDeleteDefaultLimit
	}
	if !request.validate() {
		return SessionDeletionPlan{}, ErrInvalidSessionManagementRequest
	}
	snapshot, err := manager.store.PreviewSessionDeletion(ctx, request)
	if err != nil {
		return SessionDeletionPlan{}, err
	}
	if !snapshot.validate() {
		return SessionDeletionPlan{}, ErrPersistenceUnavailable
	}
	plan := SessionDeletionPlan{SchemaVersion: SessionDeletionPlanSchema, Snapshot: snapshot}
	for _, row := range snapshot.Selected {
		metadata, ok := projectSessionMetadata(row, "", request.FrozenNow)
		if !ok || !metadata.DeletionEligible {
			return SessionDeletionPlan{}, ErrPersistenceUnavailable
		}
		plan.Sessions = append(plan.Sessions, metadata)
	}
	if len(plan.Sessions) > 0 {
		oldest := plan.Sessions[0].LastActivityAt
		newest := plan.Sessions[len(plan.Sessions)-1].LastActivityAt
		plan.Oldest, plan.Newest = &oldest, &newest
	}
	plan.Digest = sessionDeletionDigest(snapshot)
	if plan.Validate() != nil {
		return SessionDeletionPlan{}, ErrPersistenceUnavailable
	}
	return plan, nil
}

// CommitDeletion verifies the plan digest before the repository rechecks the
// exact snapshot and commits its all-or-nothing cascade.
func (manager *SessionManager) CommitDeletion(ctx context.Context, plan SessionDeletionPlan, confirmation string) (SessionDeletionCommitResult, error) {
	if manager == nil || ctx == nil || manager.store == nil || plan.Validate() != nil || confirmation != plan.Digest {
		return SessionDeletionCommitResult{}, ErrSessionDeletionStale
	}
	if err := ctx.Err(); err != nil {
		return SessionDeletionCommitResult{}, err
	}
	if plan.Snapshot.OverLimit {
		return SessionDeletionCommitResult{}, ErrSessionDeletionLimit
	}
	if len(plan.Snapshot.Selected) == 0 {
		return SessionDeletionCommitResult{}, ErrSessionDeletionProtected
	}
	var lease SessionDeletionLease
	if plan.Snapshot.Request.Kind == SessionDeletionBefore {
		if manager.isolation == nil {
			return SessionDeletionCommitResult{}, ErrSessionProcessActive
		}
		var err error
		lease, err = manager.isolation.AcquireExclusive(ctx)
		if err != nil || lease == nil {
			return SessionDeletionCommitResult{}, ErrSessionProcessActive
		}
		defer lease.Release()
	}
	remaining, err := manager.store.CommitSessionDeletion(ctx, plan.Snapshot)
	if err != nil {
		return SessionDeletionCommitResult{}, err
	}
	return SessionDeletionCommitResult{
		Deleted: len(plan.Snapshot.Selected), Protected: plan.Snapshot.Protected, Remaining: remaining,
	}, nil
}

// SessionDeletionCommitResult contains only committed counts.
type SessionDeletionCommitResult struct {
	Deleted   int `json:"deleted"`
	Protected int `json:"protected"`
	Remaining int `json:"remaining"`
}

func sessionDeletionDigest(snapshot SessionDeletionSnapshot) string {
	if !snapshot.validate() {
		return ""
	}
	var canonical bytes.Buffer
	writeDeletionString(&canonical, SessionDeletionPlanSchema)
	writeDeletionString(&canonical, string(snapshot.Request.Kind))
	writeDeletionString(&canonical, string(snapshot.Request.SessionID))
	writeDeletionInt64(&canonical, snapshot.Request.Cutoff.UTC().UnixMilli())
	writeDeletionString(&canonical, string(snapshot.Request.CurrentSessionID))
	writeDeletionInt64(&canonical, int64(snapshot.Request.Limit))
	writeDeletionInt64(&canonical, snapshot.SchemaRevision)
	writeDeletionInt64(&canonical, int64(snapshot.Matched))
	writeDeletionInt64(&canonical, int64(snapshot.Eligible))
	writeDeletionInt64(&canonical, int64(snapshot.Protected))
	writeDeletionInt64(&canonical, int64(snapshot.Remaining))
	if snapshot.OverLimit {
		writeDeletionInt64(&canonical, 1)
	} else {
		writeDeletionInt64(&canonical, 0)
	}
	writeDeletionInt64(&canonical, int64(len(snapshot.Selected)))
	for _, record := range snapshot.Selected {
		writeDeletionString(&canonical, string(record.ID))
		writeDeletionInt64(&canonical, record.LastActiveUnixMillis)
		writeDeletionInt64(&canonical, record.Version)
	}
	sum := sha256.Sum256(canonical.Bytes())
	return hex.EncodeToString(sum[:])
}

func writeDeletionString(buffer *bytes.Buffer, value string) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(value)))
	_, _ = buffer.WriteString(value)
}

func writeDeletionInt64(buffer *bytes.Buffer, value int64) {
	_ = binary.Write(buffer, binary.BigEndian, value)
}

// SessionStorageHealth is the bounded doctor projection returned by storage.
type SessionStorageHealth struct {
	SchemaRevision      int64 `json:"schema_revision"`
	SessionCount        int   `json:"session_count"`
	ProtectedActivity   int   `json:"protected_activity"`
	FutureActivity      int   `json:"future_activity"`
	PendingRecoveryRuns int   `json:"pending_recovery_runs"`
}

func (health SessionStorageHealth) valid() bool {
	return health.SchemaRevision >= 1 && health.SessionCount >= 0 && health.ProtectedActivity >= 0 &&
		health.FutureActivity >= 0 && health.PendingRecoveryRuns >= 0 &&
		health.ProtectedActivity >= health.FutureActivity
}

// StorageHealth reads bounded integrity counters without changing activity.
func (manager *SessionManager) StorageHealth(ctx context.Context, frozenNow time.Time) (SessionStorageHealth, error) {
	if manager == nil || ctx == nil || manager.store == nil {
		return SessionStorageHealth{}, ErrInvalidSessionManagementRequest
	}
	if frozenNow.IsZero() {
		frozenNow = manager.now()
	}
	if !validCoordinatorTime(frozenNow) {
		return SessionStorageHealth{}, ErrInvalidSessionManagementRequest
	}
	health, err := manager.store.SessionStorageHealth(ctx, frozenNow)
	if err != nil {
		return SessionStorageHealth{}, err
	}
	if !health.valid() {
		return SessionStorageHealth{}, ErrPersistenceUnavailable
	}
	return health, nil
}
