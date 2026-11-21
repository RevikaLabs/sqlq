package sqlq

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Kind is the type of a traced operation.
type Kind uint8

const (
	KindGet Kind = iota + 1
	KindSelect
	KindExec
	KindBegin
	KindCommit
	KindRollback
)

// String returns the lower-case kind name, suitable as a metric label.
func (k Kind) String() string {
	switch k {
	case KindGet:
		return "get"
	case KindSelect:
		return "select"
	case KindExec:
		return "exec"
	case KindBegin:
		return "begin"
	case KindCommit:
		return "commit"
	case KindRollback:
		return "rollback"
	}
	return "unknown"
}

// Op describes one database operation. The same *Op is passed to Start and
// End of every tracer; tracers must treat it as read-only and must not keep
// it after End.
type Op struct {
	Kind Kind
	// Name is a bounded identifier of the query, safe as a metric label:
	// the "-- name: X" comment of the query, the result of WithNamer, or
	// Kind.String() as the fallback.
	Name string
	// SQL is the query text; empty for begin, commit and rollback.
	SQL string
	// Args are the query arguments as passed by the caller. They may carry
	// personal data: do not log or export them by default.
	Args []any
	// InTx reports that the operation runs inside a transaction
	// (commit and rollback included).
	InTx bool
	// Start is set before the first Tracer.Start.
	Start time.Time
	// Duration and Rows are set before the first Tracer.End. Rows is
	// RowsAffected for exec, the number of rows for select, 1 or 0 for get;
	// 0 when unknown (the driver does not report RowsAffected).
	Duration time.Duration
	Rows     int64
}

// Tracer is a hook around every operation. Start runs before the operation;
// the context it returns is passed to the operation and to End, so a tracer
// can open a span or attach values. End runs after the operation, also when
// it panics. A tracer can observe the error but cannot change it. Every End
// receives the context returned by the last Start.
//
// With several tracers, Start is called in registration order and End in
// reverse order. Tracer is not related to OpenTelemetry's Tracer; an
// OpenTelemetry adapter is one possible implementation.
type Tracer interface {
	Start(ctx context.Context, op *Op) context.Context
	End(ctx context.Context, op *Op, err error)
}

// EndFunc adapts a function to a Tracer that only needs the result:
// metrics, logs, profiling.
type EndFunc func(ctx context.Context, op *Op, err error)

func (f EndFunc) Start(ctx context.Context, _ *Op) context.Context { return ctx }
func (f EndFunc) End(ctx context.Context, op *Op, err error)       { f(ctx, op, err) }

// Status values returned by Status.
const (
	StatusOK       = "ok"
	StatusEmpty    = "empty"
	StatusCanceled = "canceled"
	StatusTimeout  = "timeout"
	StatusError    = "error"
)

// Status classifies the outcome of an operation into a small fixed set, so
// every tracer labels outcomes the same way. ctx is the context passed to
// End. The context is checked before the error: a driver may report a
// canceled query with its own error (lib/pq returns SQLSTATE 57014), and a
// query canceled while reading rows may surface as sql.ErrNoRows.
func Status(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return StatusTimeout
	case errors.Is(ctx.Err(), context.Canceled):
		return StatusCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return StatusTimeout
	case errors.Is(err, context.Canceled):
		return StatusCanceled
	case errors.Is(err, sql.ErrNoRows):
		return StatusEmpty
	}
	return StatusError
}

// errPanicked is reported to End when the operation panics; the panic itself
// keeps propagating to the caller.
var errPanicked = errors.New("sqlq: operation panicked")

// run executes fn under the configured tracers. fn receives a nil op when
// there are no tracers: it must not touch it then (fast path, no allocation).
func (c *config) run(ctx context.Context, kind Kind, query string, args []any, inTx bool, fn func(ctx context.Context, op *Op) error) error {
	if len(c.tracers) == 0 {
		return fn(ctx, nil)
	}
	op := &Op{
		Kind:  kind,
		Name:  c.name(kind, query),
		SQL:   query,
		Args:  args,
		InTx:  inTx,
		Start: time.Now(),
	}
	// started counts tracers whose Start returned: on a panic in a later
	// Start only those get End, so no span is left open.
	started, completed := 0, false
	defer func() {
		if !completed {
			c.end(ctx, op, errPanicked, started)
		}
	}()
	for _, t := range c.tracers {
		ctx = t.Start(ctx, op)
		started++
	}
	err := fn(ctx, op)
	completed = true
	c.end(ctx, op, err, started)
	return err
}

func (c *config) end(ctx context.Context, op *Op, err error, started int) {
	op.Duration = time.Since(op.Start)
	for i := started - 1; i >= 0; i-- {
		c.tracers[i].End(ctx, op, err)
	}
}

func (c *config) name(kind Kind, query string) string {
	if query != "" {
		if n := c.namer(query); n != "" {
			return n
		}
	}
	return kind.String()
}

// CommentName is the default namer: it returns X from a leading
// "-- name: X" comment (the sqlc convention, e.g. "-- name: GetUser :one"),
// or "" when the query has none.
func CommentName(query string) string {
	line, _, _ := strings.Cut(strings.TrimLeft(query, " \t\r\n"), "\n")
	rest, ok := strings.CutPrefix(line, "--")
	if !ok {
		return ""
	}
	rest, ok = strings.CutPrefix(strings.TrimSpace(rest), "name:")
	if !ok {
		return ""
	}
	if f := strings.Fields(rest); len(f) > 0 {
		return f[0]
	}
	return ""
}
