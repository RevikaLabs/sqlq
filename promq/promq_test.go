package promq_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"go.revika.io/sqlq"
	"go.revika.io/sqlq/promq"
)

func TestMetrics(t *testing.T) {
	m := promq.New(promq.WithNamespace("app"), promq.WithConstLabels(prometheus.Labels{"db": "main"}))
	ctx := t.Context()
	m.End(ctx, &sqlq.Op{Kind: sqlq.KindGet, Name: "GetUser", Duration: time.Millisecond}, nil)
	m.End(ctx, &sqlq.Op{Kind: sqlq.KindGet, Name: "GetUser", Duration: time.Millisecond}, sql.ErrNoRows)
	m.End(ctx, &sqlq.Op{Kind: sqlq.KindExec, Name: "exec", Duration: time.Millisecond}, errors.New("boom"))

	if n := testutil.CollectAndCount(m, "app_sqlq_duration_seconds"); n != 3 {
		t.Fatalf("series = %d, want 3", n)
	}
	problems, err := testutil.CollectAndLint(m)
	if err != nil || len(problems) > 0 {
		t.Fatalf("lint: %v %v", err, problems)
	}

	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(m)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, metric := range mfs[0].GetMetric() {
		var l []string
		for _, p := range metric.GetLabel() {
			l = append(l, p.GetName()+"="+p.GetValue())
		}
		labels = append(labels, strings.Join(l, ","))
	}
	want := []string{
		"db=main,kind=exec,name=exec,status=error",
		"db=main,kind=get,name=GetUser,status=empty",
		"db=main,kind=get,name=GetUser,status=ok",
	}
	if strings.Join(labels, " ") != strings.Join(want, " ") {
		t.Fatalf("labels:\n got %v\nwant %v", labels, want)
	}
}

func TestTwoPoolsOneRegistry(t *testing.T) {
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(promq.New(promq.WithConstLabels(prometheus.Labels{"db": "main"}))); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(promq.New(promq.WithConstLabels(prometheus.Labels{"db": "replica"}))); err != nil {
		t.Fatalf("second pool: %v", err)
	}
}
