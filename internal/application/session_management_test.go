package application

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

type sessionManagementStoreFake struct {
	records       []SessionMetadataRecord
	schema        int64
	commitCalls   int
	previewCalls  int
	listCalls     int
	mutateCommit  bool
	commitFailure error
}

func (store *sessionManagementStoreFake) ListSessionMetadata(_ context.Context, request SessionListStoreRequest) (SessionListStorePage, error) {
	store.listCalls++
	rows := append([]SessionMetadataRecord(nil), store.records...)
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].LastActiveUnixMillis > rows[j].LastActiveUnixMillis ||
			rows[i].LastActiveUnixMillis == rows[j].LastActiveUnixMillis && rows[i].ID > rows[j].ID
	})
	if request.BeforeLastActiveMillis != nil {
		filtered := rows[:0]
		for _, row := range rows {
			if row.LastActiveUnixMillis < *request.BeforeLastActiveMillis ||
				row.LastActiveUnixMillis == *request.BeforeLastActiveMillis && row.ID < request.BeforeID {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	if len(rows) > request.Limit {
		rows = rows[:request.Limit]
	}
	return SessionListStorePage{Sessions: rows}, nil
}

func (store *sessionManagementStoreFake) PreviewSessionDeletion(_ context.Context, request SessionDeletionSelectionRequest) (SessionDeletionSnapshot, error) {
	store.previewCalls++
	snapshot := SessionDeletionSnapshot{Request: request, SchemaRevision: store.schema, Remaining: len(store.records)}
	for _, row := range store.records {
		matched := request.Kind == SessionDeletionExact && row.ID == request.SessionID ||
			request.Kind == SessionDeletionBefore && row.LastActiveUnixMillis < request.Cutoff.UnixMilli()
		if !matched {
			continue
		}
		snapshot.Matched++
		eligible := row.ActiveRuns == 0 && row.ActiveAuthorities == 0 && row.LastActiveUnixMillis >= row.CreatedAtUnixMillis &&
			row.UpdatedAtUnixMillis >= row.LastActiveUnixMillis && row.LastActiveUnixMillis <= request.FrozenNow.UnixMilli() &&
			(request.Kind != SessionDeletionBefore || row.ID != request.CurrentSessionID)
		if !eligible {
			snapshot.Protected++
			continue
		}
		snapshot.Eligible++
		snapshot.Selected = append(snapshot.Selected, row)
	}
	sort.Slice(snapshot.Selected, func(i, j int) bool {
		return snapshot.Selected[i].LastActiveUnixMillis < snapshot.Selected[j].LastActiveUnixMillis ||
			snapshot.Selected[i].LastActiveUnixMillis == snapshot.Selected[j].LastActiveUnixMillis && snapshot.Selected[i].ID < snapshot.Selected[j].ID
	})
	snapshot.OverLimit = snapshot.Eligible > request.Limit
	if len(snapshot.Selected) > request.Limit {
		snapshot.Selected = snapshot.Selected[:request.Limit]
	}
	if !snapshot.OverLimit {
		snapshot.Remaining -= len(snapshot.Selected)
	}
	return snapshot, nil
}

func (store *sessionManagementStoreFake) CommitSessionDeletion(_ context.Context, expected SessionDeletionSnapshot) (int, error) {
	store.commitCalls++
	if store.commitFailure != nil {
		return 0, store.commitFailure
	}
	if store.mutateCommit {
		return 0, ErrSessionDeletionStale
	}
	selected := make(map[domain.SessionID]struct{}, len(expected.Selected))
	for _, row := range expected.Selected {
		selected[row.ID] = struct{}{}
	}
	kept := store.records[:0]
	for _, row := range store.records {
		if _, remove := selected[row.ID]; !remove {
			kept = append(kept, row)
		}
	}
	store.records = kept
	return len(store.records), nil
}

func (store *sessionManagementStoreFake) SessionStorageHealth(_ context.Context, now time.Time) (SessionStorageHealth, error) {
	result := SessionStorageHealth{SchemaRevision: store.schema, SessionCount: len(store.records)}
	for _, row := range store.records {
		if row.LastActiveUnixMillis < row.CreatedAtUnixMillis || row.UpdatedAtUnixMillis < row.LastActiveUnixMillis || row.LastActiveUnixMillis > now.UnixMilli() {
			result.ProtectedActivity++
		}
		if row.LastActiveUnixMillis > now.UnixMilli() {
			result.FutureActivity++
		}
		result.PendingRecoveryRuns += int(row.ActiveRuns)
	}
	return result, nil
}

type deletionIsolationFake struct {
	acquires int
	releases int
	fail     bool
}

func (isolation *deletionIsolationFake) AcquireExclusive(context.Context) (SessionDeletionLease, error) {
	isolation.acquires++
	if isolation.fail {
		return nil, ErrSessionProcessActive
	}
	return deletionLeaseFake{release: &isolation.releases}, nil
}

type deletionLeaseFake struct{ release *int }

func (lease deletionLeaseFake) Release() error { (*lease.release)++; return nil }

func TestParseSessionDeletionCutoffFreezesExactDurationAndRejectsAmbiguity(t *testing.T) {
	now := time.Date(2026, 9, 7, 2, 30, 0, 123000000, time.UTC)
	accepted := map[string]time.Time{
		"1d":                        now.UTC().Add(-24 * time.Hour).Truncate(time.Millisecond),
		"2w":                        now.UTC().Add(-14 * 24 * time.Hour).Truncate(time.Millisecond),
		"2026-09-01T00:00:00Z":      time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		"2026-09-01T08:00:00+08:00": time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	for input, want := range accepted {
		got, err := ParseSessionDeletionCutoff(input, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseSessionDeletionCutoff(%q) = %s, %v; want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0d", "-1d", "1.5d", "01d", "1m", "1y", "tomorrow", "2026-09-01", "3651d", " 1d"} {
		if _, err := ParseSessionDeletionCutoff(input, now); !errors.Is(err, ErrInvalidSessionManagementRequest) {
			t.Errorf("ParseSessionDeletionCutoff(%q) error = %v", input, err)
		}
	}
}

func TestSessionListUsesStableLastActivityCursorAndProtectsUnsafeTimestamps(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	rows := []SessionMetadataRecord{
		testSessionMetadata(1, now.Add(-time.Hour)), testSessionMetadata(2, now.Add(-time.Hour)),
		testSessionMetadata(3, now.Add(time.Hour)),
	}
	store := &sessionManagementStoreFake{records: rows, schema: 15}
	manager, _ := NewSessionManager(store, func() time.Time { return now })
	first, err := manager.List(context.Background(), SessionListRequest{Limit: 2, FrozenNow: now})
	if err != nil || len(first.Sessions) != 2 || first.NextCursor == "" || !first.Sessions[0].Protected || first.Sessions[0].ProtectionReason != SessionProtectionFutureActivity {
		t.Fatalf("first page/error = %#v/%v", first, err)
	}
	second, err := manager.List(context.Background(), SessionListRequest{Limit: 2, Cursor: first.NextCursor, FrozenNow: now})
	if err != nil || len(second.Sessions) != 1 || second.Sessions[0].ID != rows[0].ID {
		t.Fatalf("second page/error = %#v/%v", second, err)
	}
	if store.listCalls != 2 {
		t.Fatalf("list calls = %d", store.listCalls)
	}
}

func TestCorruptLastActivityRemainsVisibleButProtected(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	record := testSessionMetadata(1, now.Add(-time.Hour))
	record.LastActiveUnixMillis = -1
	store := &sessionManagementStoreFake{records: []SessionMetadataRecord{record}, schema: 16}
	manager, _ := NewSessionManager(store, func() time.Time { return now })
	page, err := manager.List(context.Background(), SessionListRequest{Limit: 1, FrozenNow: now})
	if err != nil || len(page.Sessions) != 1 || !page.Sessions[0].Protected ||
		page.Sessions[0].ProtectionReason != SessionProtectionCorruptActivity || page.Sessions[0].DeletionEligible {
		t.Fatalf("corrupt activity projection = %#v, %v", page, err)
	}
	candidate := UISessionCandidate{
		ID: record.ID, Title: record.Title, LastActivityAtUnixMillis: record.LastActiveUnixMillis,
		PrivacyMode: record.PrivacyMode, Status: record.Status, Management: true,
		Protected: true, ProtectionReason: SessionProtectionCorruptActivity,
	}
	if !validSessionCandidates([]UISessionCandidate{candidate}) {
		t.Fatal("protected corrupt activity was rejected from bounded Session management")
	}
	candidate.Protected = false
	candidate.ProtectionReason = SessionProtectionNone
	candidate.DeletionEligible = true
	if validSessionCandidates([]UISessionCandidate{candidate}) {
		t.Fatal("negative activity was accepted as deletion-eligible")
	}
}

func TestDeletionDigestBindsOrderedSnapshotAndBatchIsolation(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	store := &sessionManagementStoreFake{records: []SessionMetadataRecord{
		testSessionMetadata(1, now.Add(-72*time.Hour)), testSessionMetadata(2, now.Add(-48*time.Hour)),
		testSessionMetadata(3, now.Add(-time.Hour)),
	}, schema: 15}
	isolation := new(deletionIsolationFake)
	manager, _ := NewIsolatedSessionManager(store, func() time.Time { return now }, isolation)
	plan, err := manager.PreviewDeletion(context.Background(), SessionDeletionSelectionRequest{
		Kind: SessionDeletionBefore, Cutoff: now.Add(-24 * time.Hour), FrozenNow: now, Limit: 2,
	})
	if err != nil || plan.Validate() != nil || len(plan.Sessions) != 2 || plan.Sessions[0].ID != store.records[0].ID {
		t.Fatalf("PreviewDeletion() = %#v, %v", plan, err)
	}
	laterSnapshot := plan.Snapshot
	laterSnapshot.Request.FrozenNow = now.Add(5 * time.Minute)
	if got := sessionDeletionDigest(laterSnapshot); got != plan.Digest {
		t.Fatalf("absolute-cutoff digest changed only because confirmation time advanced: %q != %q", got, plan.Digest)
	}
	otherCurrentID := store.records[2].ID
	mutations := map[string]func(*SessionDeletionSnapshot){
		"current Session exclusion": func(snapshot *SessionDeletionSnapshot) { snapshot.Request.CurrentSessionID = otherCurrentID },
		"request limit":             func(snapshot *SessionDeletionSnapshot) { snapshot.Request.Limit++ },
		"schema revision":           func(snapshot *SessionDeletionSnapshot) { snapshot.SchemaRevision++ },
		"aggregate protection": func(snapshot *SessionDeletionSnapshot) {
			snapshot.Matched++
			snapshot.Protected++
		},
		"remaining count": func(snapshot *SessionDeletionSnapshot) { snapshot.Remaining++ },
	}
	for name, mutate := range mutations {
		t.Run("digest binds "+name, func(t *testing.T) {
			changed := plan.Snapshot
			mutate(&changed)
			if !changed.validate() {
				t.Fatal("test mutation produced an invalid snapshot")
			}
			if got := sessionDeletionDigest(changed); got == plan.Digest {
				t.Fatalf("digest did not bind %s", name)
			}
		})
	}
	overLimit := plan.Snapshot
	overLimit.Request.Limit = 1
	overLimit.OverLimit = true
	overLimit.Selected = append([]SessionMetadataRecord(nil), overLimit.Selected[:1]...)
	if !overLimit.validate() || sessionDeletionDigest(overLimit) == plan.Digest {
		t.Fatal("digest did not bind the over-limit selection state")
	}
	tampered := plan
	replacement := "0"
	if tampered.Digest[0] == '0' {
		replacement = "1"
	}
	tampered.Digest = replacement + tampered.Digest[1:]
	if _, err := manager.CommitDeletion(context.Background(), tampered, tampered.Digest); !errors.Is(err, ErrSessionDeletionStale) {
		t.Fatalf("tampered digest error = %v", err)
	}
	reordered := plan
	reordered.Snapshot.Selected = append([]SessionMetadataRecord(nil), plan.Snapshot.Selected...)
	reordered.Snapshot.Selected[0], reordered.Snapshot.Selected[1] = reordered.Snapshot.Selected[1], reordered.Snapshot.Selected[0]
	if reordered.Validate() == nil {
		t.Fatal("reordered plan validated")
	}
	committed, err := manager.CommitDeletion(context.Background(), plan, plan.Digest)
	if err != nil || committed.Deleted != 2 || committed.Remaining != 1 || isolation.acquires != 1 || isolation.releases != 1 {
		t.Fatalf("CommitDeletion() = %#v, %v; isolation=%d/%d", committed, err, isolation.acquires, isolation.releases)
	}
}

func TestDeletionFailureAndStaleSelectionWriteNothing(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	row := testSessionMetadata(1, now.Add(-time.Hour))
	store := &sessionManagementStoreFake{records: []SessionMetadataRecord{row}, schema: 15}
	manager, _ := NewSessionManager(store, func() time.Time { return now })
	plan, err := manager.PreviewDeletion(context.Background(), SessionDeletionSelectionRequest{
		Kind: SessionDeletionExact, SessionID: row.ID, FrozenNow: now, Limit: 1, CurrentSessionID: row.ID,
	})
	if err != nil {
		t.Fatalf("PreviewDeletion() error = %v", err)
	}
	store.mutateCommit = true
	if _, err := manager.CommitDeletion(context.Background(), plan, plan.Digest); !errors.Is(err, ErrSessionDeletionStale) || len(store.records) != 1 {
		t.Fatalf("stale commit error/records = %v/%d", err, len(store.records))
	}
	store.mutateCommit = false
	store.commitFailure = errors.New("synthetic persistence failure")
	if _, err := manager.CommitDeletion(context.Background(), plan, plan.Digest); err == nil || len(store.records) != 1 {
		t.Fatalf("failed commit error/records = %v/%d", err, len(store.records))
	}
	if !reflect.DeepEqual(store.records[0], row) {
		t.Fatal("failed deletion changed the Session")
	}
}

func testSessionMetadata(suffix int, active time.Time) SessionMetadataRecord {
	id := domain.SessionID("00000000-0000-7000-8000-00000000000" + string(rune('0'+suffix)))
	created := active.Add(-time.Hour).UnixMilli()
	return SessionMetadataRecord{
		ID: id, Title: "Safe Session", Status: domain.SessionStatusActive, PrivacyMode: domain.PrivacyModeStandard,
		Version: 1, CreatedAtUnixMillis: created, LastActiveUnixMillis: active.UnixMilli(), UpdatedAtUnixMillis: active.UnixMilli(),
		CommittedMessages: 1,
	}
}
