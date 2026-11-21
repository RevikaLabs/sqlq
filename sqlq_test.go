package sqlq_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"

	"github.com/RevikaLabs/sqlq"
)

// recorder logs Start/End calls of one tracer as "start:<name>" and
// "end:<name>:<status>[:rows]".
type recorder struct {
	id    string
	calls *[]string
}

func (r recorder) Start(ctx context.Context, op *sqlq.Op) context.Context {
	*r.calls = append(*r.calls, r.id+"start:"+op.Name)
	return ctx
}

func (r recorder) End(ctx context.Context, op *sqlq.Op, err error) {
	*r.calls = append(*r.calls, fmt.Sprintf("%send:%s:%s:%d", r.id, op.Name, sqlq.Status(ctx, err), op.Rows))
}

func newDB(t *testing.T, opts ...sqlq.Option) (*sqlq.DB, sqlmock.Sqlmock) {
	t.Helper()
	raw, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		raw.Close()
	})
	return sqlq.New(sqlx.NewDb(raw, "sqlmock"), opts...), mock
}

type user struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

func TestGet(t *testing.T) {
	var calls []string
	db, mock := newDB(t, sqlq.WithTracer(recorder{calls: &calls}))
	mock.ExpectQuery("SELECT").WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "ann"))

	u, err := db.Get[user](t.Context(), "-- name: GetUser :one\nSELECT id, name FROM users WHERE id = $1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if u != (user{ID: 1, Name: "ann"}) {
		t.Fatalf("got %+v", u)
	}
	assertCalls(t, calls, "start:GetUser", "end:GetUser:ok:1")
}

func TestGet_Empty(t *testing.T) {
	var calls []string
	db, mock := newDB(t, sqlq.WithTracer(recorder{calls: &calls}))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id", "name"}))

	_, err := db.Get[user](t.Context(), "SELECT id, name FROM users")
	if !sqlq.IsEmpty(err) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
	assertCalls(t, calls, "start:get", "end:get:empty:0")
}

func TestGet_PointerToRowStruct(t *testing.T) {
	db, _ := newDB(t)
	_, err := db.Get[*user](t.Context(), "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "use Get[sqlq_test.user]") {
		t.Fatalf("err = %v", err)
	}
}

func TestGet_NullableScalars(t *testing.T) {
	db, mock := newDB(t)
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"s"}).AddRow(nil))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"t"}).AddRow(nil))

	s, err := db.Get[*string](t.Context(), "SELECT s")
	if err != nil || s != nil {
		t.Fatalf("*string: %v, %v", s, err)
	}
	ts, err := db.Get[*time.Time](t.Context(), "SELECT t")
	if err != nil || ts != nil {
		t.Fatalf("*time.Time: %v, %v", ts, err)
	}
}

func TestSelect(t *testing.T) {
	var calls []string
	db, mock := newDB(t, sqlq.WithTracer(recorder{calls: &calls}))
	mock.ExpectQuery("SELECT").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "ann").AddRow(2, "bob"))

	users, err := db.Select[user](t.Context(), "-- name: ListUsers :many\nSELECT id, name FROM users")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0] != (user{ID: 1, Name: "ann"}) || users[1] != (user{ID: 2, Name: "bob"}) {
		t.Fatalf("got %+v", users)
	}
	assertCalls(t, calls, "start:ListUsers", "end:ListUsers:ok:2")
}

func TestSelect_EmptyIsNonNil(t *testing.T) {
	var calls []string
	db, mock := newDB(t, sqlq.WithTracer(recorder{calls: &calls}))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id", "name"}))

	users, err := db.Select[user](t.Context(), "SELECT id, name FROM users")
	if err != nil {
		t.Fatal(err)
	}
	if users == nil || len(users) != 0 {
		t.Fatalf("got %#v, want empty non-nil", users)
	}
	assertCalls(t, calls, "start:select", "end:select:ok:0")
}

func TestExec_RowsAffected(t *testing.T) {
	var calls []string
	db, mock := newDB(t, sqlq.WithTracer(recorder{calls: &calls}))
	mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 3))

	if _, err := db.ExecContext(t.Context(), "-- name: Touch\nUPDATE users SET name = name"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, calls, "start:Touch", "end:Touch:ok:3")
}

func TestTx_RollbackAfterCommitNotTraced(t *testing.T) {
	var calls []string
	db, mock := newDB(t, sqlq.WithTracer(recorder{calls: &calls}))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := func() error {
		tx, err := db.BeginTxx(t.Context(), nil)
		if err != nil {
			return err
		}
		defer func() {
			if err := tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
				t.Errorf("rollback after commit: %v, want sql.ErrTxDone", err)
			}
		}()
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO users DEFAULT VALUES"); err != nil {
			return err
		}
		return tx.Commit()
	}()
	if err != nil {
		t.Fatal(err)
	}
	assertCalls(t, calls,
		"start:begin", "end:begin:ok:0",
		"start:exec", "end:exec:ok:1",
		"start:commit", "end:commit:ok:0")
}

func TestTx_GetSelect(t *testing.T) {
	var inTx []bool
	db, mock := newDB(t, sqlq.WithTracer(sqlq.EndFunc(func(_ context.Context, op *sqlq.Op, _ error) {
		inTx = append(inTx, op.InTx)
	})))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "ann"))
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(2, "bob"))
	mock.ExpectRollback()

	tx, err := db.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := tx.Get[user](t.Context(), "SELECT id, name FROM users LIMIT 1")
	if err != nil || u.Name != "ann" {
		t.Fatalf("Get: %+v, %v", u, err)
	}
	users, err := tx.Select[user](t.Context(), "SELECT id, name FROM users")
	if err != nil || len(users) != 1 || users[0].Name != "bob" {
		t.Fatalf("Select: %+v, %v", users, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// begin, get, select, rollback
	if fmt.Sprint(inTx) != "[false true true true]" {
		t.Fatalf("InTx = %v", inTx)
	}
}

type spanKey struct{}

// spanTracer simulates a span tracer: Start(begin) attaches a span, and
// End of commit reports whether that span is still visible — the commit
// context must be the one returned by Start(begin).
type spanTracer struct{ commitNested *bool }

func (spanTracer) Start(ctx context.Context, op *sqlq.Op) context.Context {
	if op.Kind == sqlq.KindBegin {
		return context.WithValue(ctx, spanKey{}, true)
	}
	return ctx
}

func (s spanTracer) End(ctx context.Context, op *sqlq.Op, _ error) {
	if op.Kind == sqlq.KindCommit {
		*s.commitNested = ctx.Value(spanKey{}) != nil
	}
}

func TestTx_CommitNestsUnderBegin(t *testing.T) {
	commitNested := false
	db, mock := newDB(t, sqlq.WithTracer(spanTracer{commitNested: &commitNested}))
	mock.ExpectBegin()
	mock.ExpectCommit()

	tx, err := db.BeginTxx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !commitNested {
		t.Fatal("commit was traced without the begin span context")
	}
}

func TestTracerOrder(t *testing.T) {
	var calls []string
	db, mock := newDB(t,
		sqlq.WithTracer(recorder{id: "a.", calls: &calls}),
		sqlq.WithTracer(recorder{id: "b.", calls: &calls}))
	mock.ExpectExec("DELETE").WillReturnError(errors.New("boom"))

	if _, err := db.ExecContext(t.Context(), "DELETE FROM users"); err == nil {
		t.Fatal("want error")
	}
	assertCalls(t, calls, "a.start:exec", "b.start:exec", "b.end:exec:error:0", "a.end:exec:error:0")
}

func TestWithNamer(t *testing.T) {
	var calls []string
	db, mock := newDB(t,
		sqlq.WithNamer(func(string) string { return "custom" }),
		sqlq.WithTracer(recorder{calls: &calls}))
	mock.ExpectExec("DELETE").WillReturnResult(sqlmock.NewResult(0, 0))

	if _, err := db.ExecContext(t.Context(), "-- name: Ignored\nDELETE FROM users"); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, calls, "start:custom", "end:custom:ok:0")
}

func TestStatus(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancel2 := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel2()
	driverErr := errors.New("pq: canceling statement due to user request")

	tests := []struct {
		ctx  context.Context
		err  error
		want string
	}{
		{t.Context(), nil, sqlq.StatusOK},
		{t.Context(), fmt.Errorf("wrap: %w", sql.ErrNoRows), sqlq.StatusEmpty},
		{t.Context(), driverErr, sqlq.StatusError},
		{canceled, driverErr, sqlq.StatusCanceled},
		{expired, driverErr, sqlq.StatusTimeout},
		{t.Context(), context.DeadlineExceeded, sqlq.StatusTimeout},
		{canceled, sql.ErrNoRows, sqlq.StatusCanceled},
		{canceled, fmt.Errorf("wrap: %w", context.DeadlineExceeded), sqlq.StatusCanceled},
	}
	for _, tt := range tests {
		if got := sqlq.Status(tt.ctx, tt.err); got != tt.want {
			t.Errorf("Status(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}

func TestCommentName(t *testing.T) {
	tests := map[string]string{
		"-- name: GetUser :one\nSELECT 1": "GetUser",
		"\n  --name:ListUsers\nSELECT 1":  "ListUsers",
		"SELECT 1 -- name: X":             "",
		"-- just a comment\nSELECT 1":     "",
		"-- name:\nSELECT 1":              "",
	}
	for q, want := range tests {
		if got := sqlq.CommentName(q); got != want {
			t.Errorf("CommentName(%q) = %q, want %q", q, got, want)
		}
	}
}

func assertCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("calls:\n got %v\nwant %v", got, want)
	}
}
