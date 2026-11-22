// Package promq is a sqlq.Tracer that exports a Prometheus histogram of
// operation durations:
//
//	sqlq_duration_seconds{name, kind, status}
//
// name is sqlq.Op.Name, kind is sqlq.Kind, status is sqlq.Status. Metrics
// is a prometheus.Collector and is not registered anywhere by New:
//
//	m := promq.New(promq.WithConstLabels(prometheus.Labels{"db": "main"}))
//	reg.MustRegister(m)
//	db, err := sqlq.Open("postgres", dsn, sqlq.WithTracer(m))
//
// Two pools in one registry need different const labels. Connection pool
// metrics are covered by collectors.NewDBStatsCollector(db.Unwrap().DB, name).
// promq is a separate module so the sqlq core does not depend on Prometheus.
package promq

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"go.revika.io/sqlq"
)

type options struct {
	namespace   string
	constLabels prometheus.Labels
	buckets     []float64
}

// Option configures Metrics.
type Option func(*options)

// WithNamespace prefixes the metric name: <namespace>_sqlq_duration_seconds.
func WithNamespace(ns string) Option { return func(o *options) { o.namespace = ns } }

// WithConstLabels adds labels with fixed values, e.g. the pool name.
func WithConstLabels(l prometheus.Labels) Option { return func(o *options) { o.constLabels = l } }

// WithBuckets replaces prometheus.DefBuckets.
func WithBuckets(b []float64) Option { return func(o *options) { o.buckets = b } }

// Metrics is a sqlq.Tracer and a prometheus.Collector.
type Metrics struct {
	duration *prometheus.HistogramVec
}

var _ sqlq.Tracer = (*Metrics)(nil)

// New creates unregistered Metrics.
func New(opts ...Option) *Metrics {
	o := options{buckets: prometheus.DefBuckets}
	for _, opt := range opts {
		opt(&o)
	}
	return &Metrics{
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace:   o.namespace,
			Subsystem:   "sqlq",
			Name:        "duration_seconds",
			Help:        "Duration of database operations by query name, kind and status.",
			ConstLabels: o.constLabels,
			Buckets:     o.buckets,
		}, []string{"name", "kind", "status"}),
	}
}

func (m *Metrics) Start(ctx context.Context, _ *sqlq.Op) context.Context { return ctx }

func (m *Metrics) End(ctx context.Context, op *sqlq.Op, err error) {
	m.duration.WithLabelValues(op.Name, op.Kind.String(), sqlq.Status(ctx, err)).Observe(op.Duration.Seconds())
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) { m.duration.Describe(ch) }
func (m *Metrics) Collect(ch chan<- prometheus.Metric) { m.duration.Collect(ch) }
