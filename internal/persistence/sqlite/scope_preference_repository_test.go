package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func TestScopePreferenceRepositoryRoundTripsAndUpdatesOneTypedContext(t *testing.T) {
	stateDir := testStateDir(t)
	db := openTestDB(t, context.Background(), stateDir, "scope-preference-round-trip")
	repository := NewScopePreferenceRepository(db)

	if preference, found, err := repository.LoadLastContext(context.Background()); err != nil || found || preference != (application.ScopePreference{}) {
		t.Fatalf("LoadLastContext(missing) = %#v, %v, %v", preference, found, err)
	}

	updatedAt := time.UnixMilli(81_000).UTC()
	first := application.ScopePreference{Context: "development", UpdatedAt: updatedAt}
	if err := repository.SaveLastContext(context.Background(), first); err != nil {
		t.Fatalf("SaveLastContext(first) error = %v", err)
	}
	got, found, err := repository.LoadLastContext(context.Background())
	if err != nil || !found || got != first {
		t.Fatalf("LoadLastContext(first) = %#v, %v, %v, want %#v", got, found, err, first)
	}

	second := application.ScopePreference{Context: "production", UpdatedAt: updatedAt}
	if err := repository.SaveLastContext(context.Background(), second); err != nil {
		t.Fatalf("SaveLastContext(same timestamp update) error = %v", err)
	}
	got, found, err = repository.LoadLastContext(context.Background())
	if err != nil || !found || got != second {
		t.Fatalf("LoadLastContext(second) = %#v, %v, %v, want %#v", got, found, err, second)
	}

	var stored struct {
		ValueJSON     string `db:"value_json"`
		SchemaVersion int    `db:"schema_version"`
	}
	if err := db.handle.GetContext(
		context.Background(),
		&stored,
		`SELECT value_json, schema_version FROM settings WHERE key = ?`,
		scopePreferenceKey,
	); err != nil {
		t.Fatalf("stored preference query error = %v", err)
	}
	if stored.ValueJSON != `{"context":"production"}` || stored.SchemaVersion != scopePreferenceSchemaVersion {
		t.Fatalf("stored preference = %#v", stored)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close(first process) error = %v", err)
	}
	reopened := openTestDB(t, context.Background(), stateDir, "scope-preference-reopen")
	got, found, err = NewScopePreferenceRepository(reopened).LoadLastContext(context.Background())
	if err != nil || !found || got != second {
		t.Fatalf("LoadLastContext(after reopen) = %#v, %v, %v, want %#v", got, found, err, second)
	}
}

func TestScopePreferenceRepositoryRejectsInvalidStaleAndCorruptValuesWithoutDisclosure(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "scope-preference-policy")
	repository := NewScopePreferenceRepository(db)
	updatedAt := time.UnixMilli(82_000).UTC()

	for _, preference := range []application.ScopePreference{
		{UpdatedAt: updatedAt},
		{Context: "development"},
		{Context: "development", UpdatedAt: updatedAt.Add(time.Nanosecond)},
		{Context: strings.Repeat("x", 254), UpdatedAt: updatedAt},
	} {
		if err := repository.SaveLastContext(context.Background(), preference); !errors.Is(err, application.ErrInvalidScopePreference) {
			t.Errorf("SaveLastContext(%#v) error = %v, want ErrInvalidScopePreference", preference, err)
		}
	}
	var count int
	if err := db.handle.GetContext(context.Background(), &count, `SELECT count(key) FROM settings WHERE key = ?`, scopePreferenceKey); err != nil {
		t.Fatalf("preference count query error = %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid preferences wrote %d rows, want zero", count)
	}

	valid := application.ScopePreference{Context: "development", UpdatedAt: updatedAt}
	if err := repository.SaveLastContext(context.Background(), valid); err != nil {
		t.Fatalf("SaveLastContext(valid) error = %v", err)
	}
	stale := application.ScopePreference{Context: "stale-context", UpdatedAt: updatedAt.Add(-time.Millisecond)}
	err := repository.SaveLastContext(context.Background(), stale)
	assertStorageError(t, err, ClassPersistenceUnavailable, "scope_preference_write_conflict")
	got, found, err := repository.LoadLastContext(context.Background())
	if err != nil || !found || got != valid {
		t.Fatalf("preference after stale write = %#v, %v, %v, want %#v", got, found, err, valid)
	}

	rowCanary := "scope-preference-corrupt-canary"
	corrupt := `{"context":"development","unexpected":"` + rowCanary + `"}`
	if _, err := db.handle.ExecContext(
		context.Background(),
		`UPDATE settings SET value_json = ? WHERE key = ?`,
		corrupt,
		scopePreferenceKey,
	); err != nil {
		t.Fatalf("corrupt preference setup error = %v", err)
	}
	_, _, err = repository.LoadLastContext(context.Background())
	assertStorageError(t, err, ClassPersistenceUnavailable, "scope_preference_row_invalid")
	if strings.Contains(err.Error(), rowCanary) {
		t.Fatal("scope preference error disclosed stored JSON")
	}
}

func TestScopePreferenceRepositoryHonorsCancellation(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "scope-preference-cancel")
	repository := NewScopePreferenceRepository(db)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	preference := application.ScopePreference{Context: "development", UpdatedAt: time.UnixMilli(83_000).UTC()}

	if err := repository.SaveLastContext(cancelled, preference); !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveLastContext(cancelled) error = %v, want context.Canceled", err)
	}
	if _, _, err := repository.LoadLastContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadLastContext(cancelled) error = %v, want context.Canceled", err)
	}
	var count int
	if err := db.handle.GetContext(context.Background(), &count, `SELECT count(key) FROM settings WHERE key = ?`, scopePreferenceKey); err != nil {
		t.Fatalf("preference count query error = %v", err)
	}
	if count != 0 {
		t.Fatalf("cancelled preference write count = %d, want zero", count)
	}
}
