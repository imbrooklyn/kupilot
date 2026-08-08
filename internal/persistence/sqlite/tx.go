package sqlite

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
)

func withTx(ctx context.Context, db *sqlx.DB, run func(*sqlx.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	if err := run(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, context.Canceled) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, context.Canceled) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return tx.Commit()
}
