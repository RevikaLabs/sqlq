// Package sqlq is a thin wrapper over sqlx: typed db.Get[T] and
// db.Select[T] (generic methods, Go 1.27) instead of destination
// variables, and a Tracer hook around every
// operation for metrics, logging or profiling. Tracer implementations are
// pluggable at Open time; sqlq ships the interface, the slogq and promq
// adapters are optional.
//
// DB and Tx keep the sqlx method names and signatures (ExecContext,
// BeginTxx), so moving from *sqlx.DB / *sqlx.Tx is a change of field types.
// They do not embed sqlx: an embedded method would bypass the tracers
// silently. Unwrap gives explicit access to sqlx for anything not wrapped.
package sqlq

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
)

type config struct {
	tracers []Tracer
	namer   func(query string) string
}

// Option configures a DB.
type Option func(*config)

// WithTracer adds a tracer. Repeat it to add several; see Tracer for the
// call order.
func WithTracer(t Tracer) Option {
	return func(c *config) {
		if t == nil {
			panic("sqlq: WithTracer(nil)")
		}
		c.tracers = append(c.tracers, t)
	}
}

// WithNamer replaces CommentName as the source of Op.Name. An empty result
// falls back to Kind.String(). Op.Name is used as a metric label: the namer
// must return a bounded set of values, never the query text itself.
func WithNamer(namer func(query string) string) Option {
	return func(c *config) {
		if namer == nil {
			panic("sqlq: WithNamer(nil)")
		}
		c.namer = namer
	}
}

// DB is a pool handle; safe for concurrent use.
type DB struct {
	x   *sqlx.DB
	cfg *config
}

// Open opens a pool like sqlx.Open: it does not connect, use PingContext.
func Open(driverName, dsn string, opts ...Option) (*DB, error) {
	x, err := sqlx.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	return New(x, opts...), nil
}

// New wraps an existing sqlx pool.
func New(x *sqlx.DB, opts ...Option) *DB {
	if x == nil {
		panic("sqlq: New(nil)")
	}
	cfg := &config{namer: CommentName}
	for _, o := range opts {
		o(cfg)
	}
	return &DB{x: x, cfg: cfg}
}

// ExecContext executes a query without returning rows.
func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.cfg.exec(ctx, db.x, false, query, args)
}

// BeginTxx starts a transaction. ctx is kept for tracing Commit and
// Rollback, which take no context.
func (db *DB) BeginTxx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	var x *sqlx.Tx
	// traced is the ctx after Tracer.Start: commit and rollback spans
	// nest under the begin span.
	var traced context.Context
	err := db.cfg.run(ctx, KindBegin, "", nil, false, func(ctx context.Context, _ *Op) error {
		traced = ctx
		var err error
		x, err = db.x.BeginTxx(ctx, opts)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Tx{x: x, cfg: db.cfg, ctx: traced}, nil
}

func (db *DB) PingContext(ctx context.Context) error { return db.x.PingContext(ctx) }
func (db *DB) Close() error                          { return db.x.Close() }
func (db *DB) Stats() sql.DBStats                    { return db.x.Stats() }

// Unwrap returns the underlying pool. Operations on it are not traced.
func (db *DB) Unwrap() *sqlx.DB { return db.x }

// Tx is a transaction. Only the first Commit or Rollback is traced: the
// usual "defer tx.Rollback()" after a successful Commit returns
// sql.ErrTxDone as before, but is not reported to tracers.
type Tx struct {
	x    *sqlx.Tx
	cfg  *config
	ctx  context.Context
	done atomic.Bool
}

// ExecContext executes a query without returning rows.
func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.cfg.exec(ctx, tx.x, true, query, args)
}

func (tx *Tx) Commit() error   { return tx.finish(KindCommit, tx.x.Commit) }
func (tx *Tx) Rollback() error { return tx.finish(KindRollback, tx.x.Rollback) }

func (tx *Tx) finish(kind Kind, fn func() error) error {
	if !tx.done.CompareAndSwap(false, true) {
		return fn()
	}
	return tx.cfg.run(tx.ctx, kind, "", nil, true, func(context.Context, *Op) error { return fn() })
}

// Unwrap returns the underlying transaction. Operations on it are not traced.
func (tx *Tx) Unwrap() *sqlx.Tx { return tx.x }

func (c *config) exec(ctx context.Context, e sqlx.ExecerContext, inTx bool, query string, args []any) (sql.Result, error) {
	var res sql.Result
	err := c.run(ctx, KindExec, query, args, inTx, func(ctx context.Context, op *Op) error {
		var err error
		res, err = e.ExecContext(ctx, query, args...)
		if err == nil && op != nil {
			op.Rows, _ = res.RowsAffected()
		}
		return err
	})
	return res, err
}
