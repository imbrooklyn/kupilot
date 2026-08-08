package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestWithTxCommitsAndRollsBack(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "transaction-contract")
	if _, err := db.handle.ExecContext(context.Background(), `
		CREATE TABLE transaction_items (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL
		) STRICT
	`); err != nil {
		t.Fatalf("create transaction table error = %v", err)
	}

	if err := withTx(context.Background(), db.handle, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`INSERT INTO transaction_items (id, name) VALUES (?, ?)`,
			1,
			"committed",
		)
		return err
	}); err != nil {
		t.Fatalf("committed withTx() error = %v", err)
	}

	forced := errors.New("forced transaction failure")
	err := withTx(context.Background(), db.handle, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO transaction_items (id, name) VALUES (?, ?)`,
			2,
			"rolled-back",
		); err != nil {
			return err
		}
		return forced
	})
	if !errors.Is(err, forced) {
		t.Fatalf("rolled-back withTx() error = %v, want forced error", err)
	}

	var count int
	if err := db.handle.GetContext(context.Background(), &count, `SELECT count(id) FROM transaction_items`); err != nil {
		t.Fatalf("count query error = %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
}

func TestWithTxHonorsCancelledContextBeforeCallback(t *testing.T) {
	db := openTestDB(t, context.Background(), testStateDir(t), "transaction-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := withTx(ctx, db.handle, func(*sqlx.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withTx() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("transaction callback ran after context cancellation")
	}
}
