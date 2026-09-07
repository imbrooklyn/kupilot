package tui

import (
	"context"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type tuiSessionManagementStore struct {
	record application.SessionMetadataRecord
}

func (store tuiSessionManagementStore) ListSessionMetadata(context.Context, application.SessionListStoreRequest) (application.SessionListStorePage, error) {
	return application.SessionListStorePage{Sessions: []application.SessionMetadataRecord{store.record}}, nil
}

func (store tuiSessionManagementStore) PreviewSessionDeletion(_ context.Context, request application.SessionDeletionSelectionRequest) (application.SessionDeletionSnapshot, error) {
	selected := []application.SessionMetadataRecord(nil)
	matched := 0
	if request.Kind == application.SessionDeletionExact && request.SessionID == store.record.ID {
		matched = 1
		selected = append(selected, store.record)
	} else if request.Kind == application.SessionDeletionBefore && store.record.LastActiveUnixMillis < request.Cutoff.UnixMilli() {
		matched = 1
		if store.record.ID != request.CurrentSessionID {
			selected = append(selected, store.record)
		}
	}
	protected := matched - len(selected)
	return application.SessionDeletionSnapshot{
		Request: request, Matched: matched, Eligible: len(selected), Protected: protected,
		Remaining: 1 - len(selected), SchemaRevision: 16, Selected: selected,
	}, nil
}

func (tuiSessionManagementStore) CommitSessionDeletion(context.Context, application.SessionDeletionSnapshot) (int, error) {
	return 0, nil
}

func (tuiSessionManagementStore) SessionStorageHealth(context.Context, time.Time) (application.SessionStorageHealth, error) {
	return application.SessionStorageHealth{SchemaRevision: 16, SessionCount: 1}, nil
}

func testSessionDeletionReview(t *testing.T, id domain.SessionID, title string, current bool) application.SessionDeletionReview {
	t.Helper()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	record := application.SessionMetadataRecord{
		ID: id, Title: title, Status: domain.SessionStatusActive, PrivacyMode: domain.PrivacyModeStandard,
		Version: 1, CreatedAtUnixMillis: now.Add(-2 * time.Hour).UnixMilli(),
		LastActiveUnixMillis: now.Add(-time.Hour).UnixMilli(), UpdatedAtUnixMillis: now.Add(-time.Hour).UnixMilli(),
	}
	manager, err := application.NewSessionManager(tuiSessionManagementStore{record: record}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewSessionManager() error = %v", err)
	}
	currentID := domain.SessionID("")
	if current {
		currentID = id
	}
	plan, err := manager.PreviewDeletion(context.Background(), application.SessionDeletionSelectionRequest{
		Kind: application.SessionDeletionExact, SessionID: id, FrozenNow: now, Limit: 1, CurrentSessionID: currentID,
	})
	if err != nil {
		t.Fatalf("PreviewDeletion() error = %v", err)
	}
	return application.SessionDeletionReview{Plan: plan, Current: current}
}

func testSessionDeletionResult(id domain.SessionID, current bool) *application.SessionDeletionResult {
	return &application.SessionDeletionResult{
		Kind: application.SessionDeletionExact, SessionID: id, SessionIDs: []domain.SessionID{id},
		WasCurrent: current, Deleted: 1,
	}
}

func testBatchSessionDeletionReview(t *testing.T, id domain.SessionID, title string) application.SessionDeletionReview {
	t.Helper()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	record := application.SessionMetadataRecord{
		ID: id, Title: title, Status: domain.SessionStatusActive, PrivacyMode: domain.PrivacyModeStandard,
		Version: 1, CreatedAtUnixMillis: now.Add(-72 * time.Hour).UnixMilli(),
		LastActiveUnixMillis: now.Add(-48 * time.Hour).UnixMilli(), UpdatedAtUnixMillis: now.Add(-48 * time.Hour).UnixMilli(),
	}
	manager, err := application.NewSessionManager(tuiSessionManagementStore{record: record}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewSessionManager() error = %v", err)
	}
	plan, err := manager.PreviewDeletion(context.Background(), application.SessionDeletionSelectionRequest{
		Kind: application.SessionDeletionBefore, Cutoff: now.Add(-24 * time.Hour), FrozenNow: now,
		Limit: application.SessionDeleteDefaultLimit,
	})
	if err != nil {
		t.Fatalf("PreviewDeletion() error = %v", err)
	}
	return application.SessionDeletionReview{Plan: plan}
}
