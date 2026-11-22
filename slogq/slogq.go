// Package slogq is a sqlq.Tracer that logs failed and slow operations with
// log/slog. Query arguments are never logged: they may carry personal data.
//
// Logged: status error and timeout at Error, ok and empty operations at or
// above the SlowerThan threshold at Warn. Fast empty results (sql.ErrNoRows
// is a normal outcome of Get) and canceled operations (the caller went away)
// are not logged.
package slogq

import (
	"context"
	"log/slog"
	"time"

	"go.revika.io/sqlq"
)

// Option configures a Tracer.
type Option func(*Tracer)

// SlowerThan logs ok and empty operations that took at least d. Zero (the
// default) disables slow-query logging.
func SlowerThan(d time.Duration) Option {
	return func(t *Tracer) { t.slow = d }
}

// Tracer logs operations; create it with New.
type Tracer struct {
	log  *slog.Logger
	slow time.Duration
}

// New returns a tracer writing to log.
func New(log *slog.Logger, opts ...Option) *Tracer {
	if log == nil {
		panic("slogq: New(nil)")
	}
	t := &Tracer{log: log}
	for _, o := range opts {
		o(t)
	}
	return t
}

func (t *Tracer) Start(ctx context.Context, _ *sqlq.Op) context.Context { return ctx }

func (t *Tracer) End(ctx context.Context, op *sqlq.Op, err error) {
	status := sqlq.Status(ctx, err)
	var level slog.Level
	switch {
	case status == sqlq.StatusError, status == sqlq.StatusTimeout:
		level = slog.LevelError
	case (status == sqlq.StatusOK || status == sqlq.StatusEmpty) && t.slow > 0 && op.Duration >= t.slow:
		level = slog.LevelWarn
	default:
		return
	}
	attrs := []slog.Attr{
		slog.String("name", op.Name),
		slog.String("kind", op.Kind.String()),
		slog.String("status", status),
		slog.Duration("duration", op.Duration),
		slog.Int64("rows", op.Rows),
		slog.Bool("in_tx", op.InTx),
	}
	if op.SQL != "" {
		attrs = append(attrs, slog.String("sql", op.SQL))
	}
	if err != nil {
		attrs = append(attrs, slog.Any("err", err))
	}
	t.log.LogAttrs(ctx, level, "sqlq: "+op.Kind.String(), attrs...)
}
