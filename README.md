# sqlq

A thin wrapper over [sqlx](https://github.com/jmoiron/sqlx) with two things:

- typed queries: `db.Get[T]` and `db.Select[T]` (generic methods, Go 1.27)
  return a value instead of filling a destination variable;
- a `Tracer` hook around every operation for metrics, logging or profiling.
  You pick the implementation when you open the pool: an adapter from this
  repository, your own, or none at all.

It works on top of any `database/sql` driver, including driver-level wrappers.
The driver sees connections and wire calls; sqlq sees logical operations,
including scan errors, `sql.ErrNoRows` and transaction boundaries.

Requires Go 1.27.

## Usage

```go
db, err := sqlq.Open("postgres", dsn,
	sqlq.WithTracer(slogq.New(logger, slogq.SlowerThan(200*time.Millisecond))),
)

const sqlGetUser = `-- name: GetUser
SELECT id, name FROM users WHERE id = $1`

u, err := db.Get[User](ctx, sqlGetUser, id)
if sqlq.IsEmpty(err) {
	return ErrNotFound
}

users, err := db.Select[User](ctx, sqlListUsers) // [] on no rows, never nil

tx, err := db.BeginTxx(ctx, nil)
if err != nil {
	return err
}
defer tx.Rollback()
n, err := tx.Get[int](ctx, sqlLockCounter, id)
if err != nil {
	return err
}
if _, err := tx.ExecContext(ctx, sqlSetCounter, id, n+1); err != nil {
	return err
}
return tx.Commit()
```

`Get` and `Select` are methods of both `DB` and `Tx`. Go has no generic
interface methods, so a helper that runs inside a transaction takes `*sqlq.Tx`
and one that does not takes `*sqlq.DB`. The rest of `DB` and `Tx` keeps the
sqlx method names (`ExecContext`, `BeginTxx`), so moving from `*sqlx.DB` and
`*sqlx.Tx` is mostly a change of field types. They do not embed sqlx: an
embedded method would skip the tracers silently. `Unwrap()` gives you sqlx for
anything sqlq does not wrap (named queries, `sqlx.In`, prepared statements);
those calls are not traced. `sqlq.New(*sqlx.DB, ...)` wraps a pool you have
already opened.

`T` in `Get` is the row type itself: `Get[User]`, not `Get[*User]`. Pointers to
scalars (`Get[*string]` for a nullable column) are fine.

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

- `op.Name` comes from a leading `-- name: X` comment in the query (the sqlc
  convention, also visible in `pg_stat_activity`). Without the comment it is
  the operation kind. Use `WithNamer` to replace this. The name is meant as a
  metric label, so a namer must return a bounded set of values and never the
  query text.
- `sqlq.Status(ctx, err)` sorts an outcome into one of `ok`, `empty`,
  `canceled`, `timeout` or `error`. Use it so that every tracer labels
  outcomes the same way.
- `op.Args` may contain personal data. The bundled adapters never log or
  export them.
- Only the first `Commit` or `Rollback` of a transaction is traced. The usual
  `defer tx.Rollback()` after a commit does not produce a false error.
- Without tracers there is no per-operation overhead.

### slogq

`go.revika.io/sqlq/slogq` uses only the standard library. Errors and
timeouts are logged at Error, and successful or empty operations slower than
`SlowerThan` at Warn. Fast empty results and canceled operations are not
logged.

### promq

`go.revika.io/sqlq/promq` is a separate module, so the core does not
depend on Prometheus. It adds a histogram
`sqlq_duration_seconds{name, kind, status}`:

```go
m := promq.New(promq.WithConstLabels(prometheus.Labels{"db": "main"}))
db, err := sqlq.Open("postgres", dsn, sqlq.WithTracer(m))
if err != nil {
	return err
}
reg.MustRegister(m)
reg.MustRegister(collectors.NewDBStatsCollector(db.Unwrap().DB, "main")) // pool stats
```

`promq.New` registers nothing by itself. Two pools in one registry need
different const labels.

## License

Apache-2.0
