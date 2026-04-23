/*
 * ChatCLI - Lesson Queue: Prometheus metrics.
 *
 * Follows the existing pattern from chatcli's metrics/ package: a
 * struct with per-metric fields registered once against the shared
 * Registry. A singleton holder is used so both the Runner and the
 * ReflexionHook can emit without passing plumbing around.
 *
 * Registration is once-per-process, guarded by sync.Once. Re-creating
 * a Runner (tests, hot reload) reuses the same metric collectors —
 * registering the same CounterVec twice would panic otherwise.
 */
package lessonq

import (
	"sync"

	chatclimetrics "github.com/diillson/chatcli/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the Prometheus collectors for the lesson queue
// subsystem. All metrics use the "chatcli" namespace and "lessonq"
// subsystem so they group cleanly in dashboards.
type Metrics struct {
	// Enqueue counters, labeled by outcome ("accepted"|"rejected_full"|
	// "deduped"|"dropped_oldest").
	EnqueueTotal *prometheus.CounterVec

	// QueueDepth is the current in-memory queue length. Gauge.
	QueueDepth prometheus.Gauge

	// ProcessingDuration histograms time from dequeue to outcome.
	ProcessingDuration *prometheus.HistogramVec // labels: outcome

	// AttemptsTotal counts processing attempts, labeled by outcome.
	AttemptsTotal *prometheus.CounterVec

	// RetryTotal counts scheduled retries labeled by attempt number.
	RetryTotal *prometheus.CounterVec

	// DLQSize is the current DLQ length.
	DLQSize prometheus.Gauge

	// WALCorruption counts torn-write / bad-CRC records detected on
	// read. A non-zero value is worth paging on.
	WALCorruption prometheus.Counter

	// WALSegments gauges the number of active WAL segments on disk.
	WALSegments prometheus.Gauge

	// StaleDiscarded counts entries dropped at drain time because
	// they exceeded StaleAfter.
	StaleDiscarded prometheus.Counter

	// PersistFailures counts persist callback errors (separate from
	// LLM errors — we want to distinguish fs/store vs provider).
	PersistFailures prometheus.Counter
}

var (
	metricsOnce     sync.Once
	metricsInstance *Metrics
)

// GetMetrics returns the process-wide singleton, registering the
// collectors on first call. Safe for concurrent use.
func GetMetrics() *Metrics {
	metricsOnce.Do(func() {
		metricsInstance = newMetrics()
	})
	return metricsInstance
}

func newMetrics() *Metrics {
	m := &Metrics{
		EnqueueTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "enqueue_total",
			Help:      "Lesson-queue enqueue attempts by outcome.",
		}, []string{"outcome"}),

		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "queue_depth",
			Help:      "Current number of pending jobs in the lesson queue.",
		}),

		ProcessingDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "processing_duration_seconds",
			Help:      "Histogram of lesson job processing time (dequeue→outcome).",
			Buckets:   []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120},
		}, []string{"outcome"}),

		AttemptsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "attempts_total",
			Help:      "Lesson processing attempts classified by outcome.",
		}, []string{"outcome"}),

		RetryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "retry_total",
			Help:      "Retries scheduled, labeled by the attempt number that triggered the retry.",
		}, []string{"attempt"}),

		DLQSize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "dlq_size",
			Help:      "Number of jobs currently in the dead letter queue.",
		}),

		WALCorruption: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "wal_corruption_total",
			Help:      "WAL records rejected due to CRC mismatch or malformed headers.",
		}),

		WALSegments: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "wal_segments",
			Help:      "Number of active WAL segment files on disk.",
		}),

		StaleDiscarded: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "stale_discarded_total",
			Help:      "Jobs dropped at drain time because they exceeded StaleAfter.",
		}),

		PersistFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "lessonq",
			Name:      "persist_failures_total",
			Help:      "Persist callback errors (memory/store layer), separate from LLM errors.",
		}),
	}

	chatclimetrics.Registry.MustRegister(
		m.EnqueueTotal,
		m.QueueDepth,
		m.ProcessingDuration,
		m.AttemptsTotal,
		m.RetryTotal,
		m.DLQSize,
		m.WALCorruption,
		m.WALSegments,
		m.StaleDiscarded,
		m.PersistFailures,
	)
	return m
}
