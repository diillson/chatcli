/*
 * ChatCLI - Scheduler: Prometheus metrics.
 *
 * Metrics are registered against the default Prometheus registry and
 * exposed by the /metrics endpoint in server mode. The scheduler does
 * not start its own HTTP server — the chatcli metrics package handles
 * that.
 *
 * Naming convention follows Prometheus recommendations:
 *   chatcli_scheduler_<subject>_<unit>_<op>
 */
package scheduler

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics is the scheduler's Prometheus surface. Use GetMetrics() to
// obtain the package singleton — the counters are process-wide so a
// single Scheduler per process is the normal case.
type Metrics struct {
	JobsCreated       *prometheus.CounterVec   // label: owner_kind, action_type
	JobsFired         *prometheus.CounterVec   // label: outcome, action_type
	WaitChecks        *prometheus.CounterVec   // label: condition_type, satisfied
	WaitDuration      *prometheus.HistogramVec // label: condition_type
	ActionDuration    *prometheus.HistogramVec // label: action_type, outcome
	QueueDepth        prometheus.Gauge
	ActiveJobs        prometheus.Gauge
	BreakerState      *prometheus.GaugeVec   // label: kind, key
	RetryCount        *prometheus.CounterVec // label: attempt (bucketed)
	EnqueueErrors     *prometheus.CounterVec // label: reason (rate_limited, full, invalid)
	WALSegments       prometheus.Gauge
	AuditWrites       prometheus.Counter
	DaemonConnections prometheus.Gauge
}

var (
	metricsOnce     sync.Once
	metricsInstance *Metrics
)

// GetMetrics returns the package-wide singleton, constructing it on
// first call. Subsequent calls return the same instance so registering
// twice doesn't panic with Prometheus's duplicate-metric error.
func GetMetrics() *Metrics {
	metricsOnce.Do(func() {
		metricsInstance = newMetrics()
	})
	return metricsInstance
}

func newMetrics() *Metrics {
	return &Metrics{
		JobsCreated: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "chatcli_scheduler_jobs_created_total",
			Help: "Total scheduler jobs created, labeled by owner and action type.",
		}, []string{"owner_kind", "action_type"}),

		JobsFired: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "chatcli_scheduler_jobs_fired_total",
			Help: "Total scheduler job fires, labeled by outcome and action type.",
		}, []string{"outcome", "action_type"}),

		WaitChecks: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "chatcli_scheduler_wait_checks_total",
			Help: "Total wait-condition evaluations, labeled by condition type and whether it was satisfied.",
		}, []string{"condition_type", "satisfied"}),

		WaitDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "chatcli_scheduler_wait_duration_seconds",
			Help:    "End-to-end duration of a wait loop (from StatusWaiting entry to satisfaction or timeout).",
			Buckets: prometheus.ExponentialBuckets(0.5, 2, 16), // .5s .. ~4h
		}, []string{"condition_type"}),

		ActionDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "chatcli_scheduler_action_duration_seconds",
			Help:    "Duration of a single action execution.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 14), // 50ms .. ~14m
		}, []string{"action_type", "outcome"}),

		QueueDepth: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "chatcli_scheduler_queue_depth",
			Help: "Number of jobs currently in the in-memory scheduling index.",
		}),

		ActiveJobs: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "chatcli_scheduler_active_jobs",
			Help: "Number of jobs currently in a non-terminal status.",
		}),

		BreakerState: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "chatcli_scheduler_breaker_state",
			Help: "Circuit breaker state (0=closed, 1=open, 2=half_open).",
		}, []string{"kind", "key"}),

		RetryCount: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "chatcli_scheduler_retries_total",
			Help: "Total retry attempts, labeled by bucket (e.g. attempt=1, 2, 3+).",
		}, []string{"attempt"}),

		EnqueueErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "chatcli_scheduler_enqueue_errors_total",
			Help: "Total enqueue rejections, labeled by reason.",
		}, []string{"reason"}),

		WALSegments: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "chatcli_scheduler_wal_segments",
			Help: "Number of live .wal records on disk.",
		}),

		AuditWrites: promauto.NewCounter(prometheus.CounterOpts{
			Name: "chatcli_scheduler_audit_writes_total",
			Help: "Total audit-log entries written.",
		}),

		DaemonConnections: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "chatcli_scheduler_daemon_connections",
			Help: "Number of IPC clients currently connected to the daemon.",
		}),
	}
}

// retryBucket maps an attempt number to a coarse label so we don't
// blow up cardinality (attempt=1 / 2 / 3 / 4+).
func retryBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n == 2:
		return "2"
	case n == 3:
		return "3"
	default:
		return "4+"
	}
}
