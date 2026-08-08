package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	moderncsqlite "modernc.org/sqlite"
)

var cancelProbeSequence atomic.Uint64

func TestSelectedDriverAndSQLXContract(t *testing.T) {
	registerSQLXBindType()
	if got := sqlx.BindType(driverName); got != sqlx.QUESTION {
		t.Fatalf("sqlx bind type = %d, want QUESTION", got)
	}
	if got := sqlx.Rebind(sqlx.BindType(driverName), `VALUES (?, ?)`); got != `VALUES (?, ?)` {
		t.Fatalf("sqlx Rebind() = %q", got)
	}
	for _, registered := range sql.Drivers() {
		if registered == "sqlite3" {
			t.Fatal("unexpected second SQLite driver named sqlite3 is registered")
		}
	}

	db := openTestDB(t, context.Background(), testStateDir(t), "driver-contract")
	ctx := context.Background()
	var sqliteVersion string
	if err := db.handle.GetContext(ctx, &sqliteVersion, `SELECT sqlite_version()`); err != nil {
		t.Fatalf("SQLite version query error = %v", err)
	}
	if sqliteVersion != "3.53.3" {
		t.Fatalf("SQLite version = %q, want 3.53.3", sqliteVersion)
	}
	if _, err := db.handle.ExecContext(ctx, `
		CREATE TABLE driver_contract_items (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE
		) STRICT
	`); err != nil {
		t.Fatalf("create contract table error = %v", err)
	}
	type namedItem struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	namedQuery, namedArguments, err := sqlx.Named(
		`INSERT INTO driver_contract_items (id, name) VALUES (:id, :name)`,
		namedItem{ID: 2, Name: string(rune(0x03bb)) + "'); DROP TABLE sessions; --"},
	)
	if err != nil {
		t.Fatalf("sqlx.Named() error = %v", err)
	}
	if got := db.handle.Rebind(namedQuery); got != `INSERT INTO driver_contract_items (id, name) VALUES (?, ?)` {
		t.Fatalf("named Rebind() = %q", got)
	}
	if len(namedArguments) != 2 {
		t.Fatalf("named argument count = %d, want 2", len(namedArguments))
	}
	if _, err := db.handle.NamedExecContext(ctx,
		`INSERT INTO driver_contract_items (id, name) VALUES (:id, :name)`,
		namedItem{ID: 1, Name: "named-value"},
	); err != nil {
		t.Fatalf("NamedExecContext() error = %v", err)
	}
	if _, err := db.handle.ExecContext(ctx, db.handle.Rebind(namedQuery), namedArguments...); err != nil {
		t.Fatalf("rebound named ExecContext() error = %v", err)
	}
	var sessionsTableCount int
	if err := db.handle.GetContext(ctx, &sessionsTableCount, `
		SELECT count(name)
		FROM sqlite_schema
		WHERE type = 'table' AND name = 'sessions'
	`); err != nil {
		t.Fatalf("sessions table query error = %v", err)
	}
	if sessionsTableCount != 1 {
		t.Fatalf("sessions table count = %d, want 1", sessionsTableCount)
	}

	const workers = 8
	const writesPerWorker = 16
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for offset := 0; offset < writesPerWorker; offset++ {
				id := int64(10 + worker*writesPerWorker + offset)
				if _, err := db.handle.ExecContext(ctx,
					`INSERT INTO driver_contract_items (id, name) VALUES (?, ?)`,
					id,
					fmt.Sprintf("item-%d", id),
				); err != nil {
					errorsByWorker <- err
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Errorf("concurrent write error = %v", err)
	}
	var count int
	if err := db.handle.GetContext(ctx, &count, `SELECT count(id) FROM driver_contract_items`); err != nil {
		t.Fatalf("count query error = %v", err)
	}
	if want := 2 + workers*writesPerWorker; count != want {
		t.Fatalf("row count = %d, want %d", count, want)
	}
}

func TestSelectedDriverCancelsInFlightQuery(t *testing.T) {
	entered := make(chan struct{})
	functionName := fmt.Sprintf("kupilot_cancel_probe_%d", cancelProbeSequence.Add(1))
	if err := moderncsqlite.RegisterScalarFunction(
		functionName,
		0,
		func(*moderncsqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			close(entered)
			return int64(0), nil
		},
	); err != nil {
		t.Fatalf("RegisterScalarFunction() error = %v", err)
	}
	db := openTestDB(t, context.Background(), testStateDir(t), "driver-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var value int64
		result <- db.handle.GetContext(ctx, &value, `
			WITH RECURSIVE counter(value) AS (
				SELECT `+functionName+`()
				UNION ALL
				SELECT value + 1 FROM counter WHERE value < 1000000000
			)
			SELECT sum(value) FROM counter
		`) // #nosec G202 -- the identifier is test-generated.
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("query returned before the cancellation probe: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("query did not enter the cancellation probe")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("query error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled query did not return")
	}
}
