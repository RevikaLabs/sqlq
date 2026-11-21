package sqlq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/jmoiron/sqlx"
)

// Get runs a single-row query and scans it into T: a struct with `db` tags
// or a scannable value (string, int64, time.Time, *string for a nullable
// column, an sql.Scanner). A missing row returns sql.ErrNoRows, see [IsEmpty].
//
// T must not be a pointer to a row struct: sqlx would scan it as a single
// column and fail with a confusing Scan error. Get reports that as an error
// naming the type to use instead.
func (db *DB) Get[T any](ctx context.Context, query string, args ...any) (T, error) {
	return get[T](ctx, db.x, db.cfg, false, query, args)
}

// Select runs a multi-row query and scans every row into the result. An
// empty result is an empty, non-nil slice, so it serializes as JSON [].
func (db *DB) Select[T any](ctx context.Context, query string, args ...any) ([]T, error) {
	return selectRows[T](ctx, db.x, db.cfg, false, query, args)
}

// Get is [DB.Get] inside the transaction.
func (tx *Tx) Get[T any](ctx context.Context, query string, args ...any) (T, error) {
	return get[T](ctx, tx.x, tx.cfg, true, query, args)
}

// Select is [DB.Select] inside the transaction.
func (tx *Tx) Select[T any](ctx context.Context, query string, args ...any) ([]T, error) {
	return selectRows[T](ctx, tx.x, tx.cfg, true, query, args)
}

// IsEmpty reports that err is "no rows" from Get.
func IsEmpty(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func get[T any](ctx context.Context, q sqlx.QueryerContext, cfg *config, inTx bool, query string, args []any) (T, error) {
	var dest T
	if err := checkRowType[T](); err != nil {
		return dest, err
	}
	err := cfg.run(ctx, KindGet, query, args, inTx, func(ctx context.Context, op *Op) error {
		if err := sqlx.GetContext(ctx, q, &dest, query, args...); err != nil {
			return err
		}
		if op != nil {
			op.Rows = 1
		}
		return nil
	})
	return dest, err
}

func selectRows[T any](ctx context.Context, q sqlx.QueryerContext, cfg *config, inTx bool, query string, args []any) ([]T, error) {
	dest := []T{}
	err := cfg.run(ctx, KindSelect, query, args, inTx, func(ctx context.Context, op *Op) error {
		if err := sqlx.SelectContext(ctx, q, &dest, query, args...); err != nil {
			return err
		}
		if op != nil {
			op.Rows = int64(len(dest))
		}
		return nil
	})
	return dest, err
}

var scannerType = reflect.TypeFor[sql.Scanner]()

// checkRowType rejects T = *S where S is a struct sqlx would map by fields.
// Pointers to scalars and to field-less structs (*time.Time) are valid
// nullable destinations and pass.
func checkRowType[T any]() error {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct || t.Implements(scannerType) {
		return nil
	}
	for f := range t.Elem().Fields() {
		if f.IsExported() {
			return fmt.Errorf("sqlq: Get[%s] takes a row struct, not a pointer to it: use Get[%s]", t, t.Elem())
		}
	}
	return nil
}
