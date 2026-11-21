package sqlq

import (
	"context"
	"errors"
	"testing"
)

// A panic inside database/sql (e.g. in Scan) leaves the driver connection
// locked, so the panic path is tested on run directly.
func TestRun_EndOnPanic(t *testing.T) {
	var got error
	c := &config{namer: CommentName}
	WithTracer(EndFunc(func(_ context.Context, _ *Op, err error) { got = err }))(c)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = c.run(t.Context(), KindGet, "SELECT 1", nil, false, func(context.Context, *Op) error { panic("scan") })
	}()
	if !errors.Is(got, errPanicked) {
		t.Fatalf("End err = %v, want errPanicked", got)
	}
}

type startPanics struct{}

func (startPanics) Start(context.Context, *Op) context.Context { panic("start") }
func (startPanics) End(context.Context, *Op, error)            {}

func TestRun_PanicInStartEndsStartedTracers(t *testing.T) {
	var ended []string
	c := &config{namer: CommentName}
	WithTracer(EndFunc(func(context.Context, *Op, error) { ended = append(ended, "a") }))(c)
	WithTracer(startPanics{})(c)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = c.run(t.Context(), KindExec, "DELETE", nil, false, func(context.Context, *Op) error { return nil })
	}()
	if len(ended) != 1 {
		t.Fatalf("ended = %v, want [a]", ended)
	}
}
