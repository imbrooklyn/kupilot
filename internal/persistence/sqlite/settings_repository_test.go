package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	auditcontract "github.com/imbrooklyn/kupilot/internal/audit"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestSettingsRepositoryRoundTripsUpdatesAndDeletesAllowlistedValue(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "settings-round-trip")
	repository := NewSettingsRepository(db)
	setting := testRetentionSetting(30, time.UnixMilli(500).UTC())
	if err := repository.Put(context.Background(), setting); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, err := repository.Get(context.Background(), setting.Key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !reflect.DeepEqual(got, setting) {
		t.Fatalf("Get() = %#v, want %#v", got, setting)
	}

	updated := testRetentionSetting(45, time.UnixMilli(501).UTC())
	if err := repository.Put(context.Background(), updated); err != nil {
		t.Fatalf("Put(update) error = %v", err)
	}
	if err := repository.Put(context.Background(), setting); !errors.Is(err, auditcontract.ErrSettingConflict) {
		t.Fatalf("Put(stale) error = %v, want ErrSettingConflict", err)
	}
	got, err = repository.Get(context.Background(), setting.Key)
	if err != nil || !reflect.DeepEqual(got, updated) {
		t.Fatalf("Get(after stale) = %#v, %v, want %#v", got, err, updated)
	}

	if err := repository.Delete(context.Background(), setting.Key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repository.Get(context.Background(), setting.Key); !errors.Is(err, auditcontract.ErrSettingNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrSettingNotFound", err)
	}
	if err := repository.Delete(context.Background(), setting.Key); !errors.Is(err, auditcontract.ErrSettingNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrSettingNotFound", err)
	}
	for index, days := range []int64{0, 3650} {
		boundary := testRetentionSetting(days, time.UnixMilli(int64(502+index)).UTC())
		if err := repository.Put(context.Background(), boundary); err != nil {
			t.Fatalf("Put(boundary %d) error = %v", days, err)
		}
		got, err := repository.Get(context.Background(), boundary.Key)
		if err != nil || !reflect.DeepEqual(got, boundary) {
			t.Fatalf("Get(boundary %d) = %#v, %v, want %#v", days, got, err, boundary)
		}
	}
}

func TestSettingsRepositoryRejectsUnknownSensitiveOversizedAndInvalidValues(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "settings-policy")
	repository := NewSettingsRepository(db)
	now := time.UnixMilli(510).UTC()
	tests := []domain.Setting{
		{Key: "retention.unknown_days", IntegerValue: 30, SchemaVersion: 1, UpdatedAt: now},
		{Key: "model_api_key", IntegerValue: 30, SchemaVersion: 1, UpdatedAt: now},
		{Key: domain.SettingKey(strings.Repeat("x", 65)), IntegerValue: 30, SchemaVersion: 1, UpdatedAt: now},
		{Key: domain.SettingOperationalDetailRetentionDays, IntegerValue: -1, SchemaVersion: 1, UpdatedAt: now},
		{Key: domain.SettingOperationalDetailRetentionDays, IntegerValue: 3651, SchemaVersion: 1, UpdatedAt: now},
		{Key: domain.SettingOperationalDetailRetentionDays, IntegerValue: 30, SchemaVersion: 2, UpdatedAt: now},
		{Key: domain.SettingOperationalDetailRetentionDays, IntegerValue: 30, SchemaVersion: 1, UpdatedAt: now.Add(time.Nanosecond)},
	}
	for _, setting := range tests {
		if err := repository.Put(context.Background(), setting); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
			t.Errorf("Put(%q, %d) error = %v, want ErrInvalidRepositoryRequest", setting.Key, setting.IntegerValue, err)
		}
	}

	var count int
	if err := db.handle.GetContext(context.Background(), &count, `SELECT count(key) FROM settings`); err != nil {
		t.Fatalf("settings count error = %v", err)
	}
	if count != 0 {
		t.Fatalf("settings row count = %d, want 0", count)
	}
	if _, err := repository.Get(context.Background(), "retention.operational_detail_days' OR 1=1 --"); !errors.Is(err, auditcontract.ErrInvalidRepositoryRequest) {
		t.Fatalf("Get(injection-shaped key) error = %v, want ErrInvalidRepositoryRequest", err)
	}
}

func TestSettingsRepositoryRejectsCorruptRowsWithoutDisclosure(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "settings-strict")
	repository := NewSettingsRepository(db)
	setting := testRetentionSetting(30, time.UnixMilli(520).UTC())
	if err := repository.Put(context.Background(), setting); err != nil {
		t.Fatalf("Put(setup) error = %v", err)
	}
	rowCanary := "settings-corrupt-value-canary"
	corrupt := `{"days":30,"note":"` + rowCanary + `"}`
	if _, err := db.handle.ExecContext(context.Background(), `UPDATE settings SET value_json = ? WHERE key = ?`, corrupt, setting.Key); err != nil {
		t.Fatalf("corrupt setting setup error = %v", err)
	}
	_, err := repository.Get(context.Background(), setting.Key)
	assertStorageError(t, err, ClassPersistenceUnavailable, "setting_row_invalid")
	if strings.Contains(err.Error(), rowCanary) {
		t.Fatal("setting row error disclosed stored JSON")
	}
}

func TestSettingsRepositoryHonorsCancellation(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "settings-cancel")
	repository := NewSettingsRepository(db)
	setting := testRetentionSetting(30, time.UnixMilli(530).UTC())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := repository.Put(cancelled, setting); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := repository.Get(cancelled, setting.Key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get(cancelled) error = %v, want context.Canceled", err)
	}
	if err := repository.Delete(cancelled, setting.Key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete(cancelled) error = %v, want context.Canceled", err)
	}
}

func testRetentionSetting(days int64, updatedAt time.Time) domain.Setting {
	return domain.Setting{
		Key:           domain.SettingOperationalDetailRetentionDays,
		IntegerValue:  days,
		SchemaVersion: 1,
		UpdatedAt:     updatedAt,
	}
}
