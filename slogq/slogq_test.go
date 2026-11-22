package slogq_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.revika.io/sqlq"
	"go.revika.io/sqlq/slogq"
)

func TestEnd(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		dur  time.Duration
		err  error
		want string // substring of the log line, "" for no line
	}{
		{"ok fast", t.Context(), time.Millisecond, nil, ""},
		{"ok slow", t.Context(), time.Second, nil, "level=WARN"},
		{"error", t.Context(), time.Millisecond, errors.New("boom"), "level=ERROR"},
		{"empty fast", t.Context(), time.Millisecond, sql.ErrNoRows, ""},
		{"empty slow", t.Context(), time.Second, sql.ErrNoRows, "level=WARN"},
		{"canceled", canceled, time.Second, errors.New("pq: canceling statement"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tr := slogq.New(slog.New(slog.NewTextHandler(&buf, nil)), slogq.SlowerThan(100*time.Millisecond))
			op := &sqlq.Op{
				Kind:     sqlq.KindGet,
				Name:     "GetUser",
				SQL:      "SELECT name FROM users WHERE email = $1",
				Args:     []any{"secret@example.com"},
				Duration: tt.dur,
			}
			tr.End(tt.ctx, op, tt.err)

			out := buf.String()
			if tt.want == "" {
				if out != "" {
					t.Fatalf("unexpected log: %s", out)
				}
				return
			}
			if !strings.Contains(out, tt.want) || !strings.Contains(out, "name=GetUser") {
				t.Fatalf("log %q, want %q", out, tt.want)
			}
			if strings.Contains(out, "secret@example.com") {
				t.Fatalf("args leaked into log: %s", out)
			}
		})
	}
}
