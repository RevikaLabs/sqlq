# sqlq

Typed queries and tracing for [sqlx](https://github.com/jmoiron/sqlx):

- `db.Get[T]` / `db.Select[T]` — generic methods (Go 1.27) that return a value
  instead of filling a destination variable;
- a `Tracer` hook around every operation — metrics, logging or profiling,
  picked when the pool is opened: an adapter from this repository, your own,
  or none at all.

Works on top of any `database/sql` driver, including driver-level wrappers.
The driver sees connections and wire calls; sqlq sees logical operations,
including scan errors, `sql.ErrNoRows` and transaction boundaries.

## Install

```sh
go get go.revika.io/sqlq
```

```go
import "go.revika.io/sqlq"
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.revika.io/sqlq"
	"go.revika.io/sqlq/slogq"
)

type User struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	db, err := sqlq.Open("postgres", os.Getenv("DATABASE_URL"),
		sqlq.WithTracer(slogq.New(logger, slogq.SlowerThan(200*time.Millisecond))))
	if err != nil {
		logger.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()

	// Single row. A missing row gives sql.ErrNoRows — IsEmpty tells it apart
	// from real errors. The `-- name:` comment (sqlc convention, visible in
	// pg_stat_activity) names the operation for tracers.
	const sqlGetUser = `-- name: GetUser :one
SELECT id, name FROM users WHERE id = $1`

	u, err := db.Get[User](ctx, sqlGetUser, 42)
	if sqlq.IsEmpty(err) {
		logger.Info("user not found")
		os.Exit(1)
	}
	if err != nil {
		logger.Error("get user", "err", err)
		os.Exit(1)
	}
	fmt.Println(u.Name)

	// Many rows: [] on no rows, never nil.
	users, err := db.Select[User](ctx, `-- name: ListUsers :many
SELECT id, name FROM users ORDER BY id`)
	if err != nil {
		logger.Error("list users", "err", err)
		os.Exit(1)
	}
	fmt.Println(len(users), "users")

	// Transaction: Get/Select work on Tx, sqlx method names are kept.
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		logger.Error("begin tx", "err", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	n, err := tx.Get[int](ctx, `-- name: LockCounter :one
SELECT n FROM counters WHERE id = $1 FOR UPDATE`, 1)
	if err != nil {
		logger.Error("lock counter", "err", err)
		os.Exit(1)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE counters SET n = $1 WHERE id = $2`, n+1, 1); err != nil {
		logger.Error("update counter", "err", err)
		os.Exit(1)
	}
	if err := tx.Commit(); err != nil {
		logger.Error("commit", "err", err)
		os.Exit(1)
	}
}
```

## How it differs from sqlx

`Get` and `Select` are methods of both `*sqlq.DB` and `*sqlq.Tx`. Go has no
generic interface methods, so a helper that runs inside a transaction takes
`*sqlq.Tx` and one that does not takes `*sqlq.DB`. The rest of `DB` and `Tx`
keeps the sqlx method names (`ExecContext`, `BeginTxx`), so moving from
`*sqlx.DB` / `*sqlx.Tx` is mostly a change of field types. They do not embed
sqlx: an embedded method would skip the tracers silently. `Unwrap()` gives
you sqlx for anything sqlq does not wrap (named queries, `sqlx.In`, prepared
statements); those calls are not traced. `sqlq.New(*sqlx.DB, ...)` wraps a
pool you have already opened.

`T` in `Get` is the row type itself: `Get[User]`, not `Get[*User]`. Pointers
to scalars (`Get[*string]` for a nullable column) are fine.

## Tracers

```go
type Tracer interface {
	Start(ctx context.Context, op *Op) context.Context
	End(ctx context.Context, op *Op, err error)
}
```

`Start` runs before the operation, and the context it returns goes on to the
operation and to `End`, which is where you open a span. `End` gets the result,
`op.Duration` and `op.Rows`. A tracer can see the error but cannot change it.
If you only need the result, use `sqlq.EndFunc`:

```go
sqlq.WithTracer(sqlq.EndFunc(func(ctx context.Context, op *sqlq.Op, err error) {
	// op.Name, op.Kind, op.Duration, op.Rows, sqlq.Status(ctx, err)
}))
```

Things to know:

- `op.Name` comes from a leading `-- name: X` comment in the query. Without
  the comment it is the operation kind. Use `WithNamer` to replace this. The
  name is meant as a metric label, so a namer must return a bounded set of
  values and never the query text.
- `sqlq.Status(ctx, err)` sorts an outcome into one of `ok`, `empty`,
  `canceled`, `timeout` or `error`. Use it so that every tracer labels
  outcomes the same way.
- `op.Args` may contain personal data. The bundled adapters never log or
  export them.
- Only the first `Commit` or `Rollback` of a transaction is traced. The usual
  `defer tx.Rollback()` after a commit does not produce a false error.
- Without tracers there is no per-operation overhead.

## Modules

### slogq — logging, stdlib only

```go
import "go.revika.io/sqlq/slogq"
```

Errors and timeouts are logged at Error, and successful or empty operations
slower than `slogq.SlowerThan(200*time.Millisecond)` at Warn. Fast empty
results and canceled operations are not logged.

### promq — Prometheus metrics, separate module

```go
import (
	"github.com/prometheus/client_golang/prometheus"

	"go.revika.io/sqlq"
	"go.revika.io/sqlq/promq"
)
```

The core does not depend on Prometheus; `promq` is a separate module. It adds
a histogram `sqlq_duration_seconds{name, kind, status}`:

```go
package main

import (
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"go.revika.io/sqlq"
	"go.revika.io/sqlq/promq"
)

func main() {
	m := promq.New(promq.WithConstLabels(prometheus.Labels{"db": "main"}))
	db, err := sqlq.Open("postgres", os.Getenv("DATABASE_URL"), sqlq.WithTracer(m))
	if err != nil {
		os.Exit(1)
	}
	defer db.Close()

	reg := prometheus.DefaultRegisterer
	reg.MustRegister(m)
	reg.MustRegister(collectors.NewDBStatsCollector(db.Unwrap().DB, "main")) // pool stats
}
```

`promq.New` registers nothing by itself. Two pools in one registry need
different const labels.

## License

Apache-2.0
