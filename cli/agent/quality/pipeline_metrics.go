/*
 * ChatCLI - Pipeline observability metrics.
 *
 * Mirrors the lessonq metrics pattern: singleton, registered once
 * against the shared chatcli metrics.Registry. Exposed via /metrics
 * and ingested by the project's standard dashboards.
 */
package quality

import (
	"sync"

	chatclimetrics "github.com/diillson/chatcli/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// PipelineMetrics groups the Prometheus collectors for pipeline
// observability.
type PipelineMetrics struct {
	// DispatchTotal counts pipeline Run() invocations by outcome.
	// outcome ∈ {ok, error, draining, closed, pre_short_circuit}.
	DispatchTotal *prometheus.CounterVec

	// HookDuration histograms hook execution latency.
	HookDuration *prometheus.HistogramVec // labels: hook, phase

	// HookErrors counts hook errors by reason.
	// reason ∈ {returned_error, timeout, panic, circuit_open}.
	HookErrors *prometheus.CounterVec // labels: hook, reason

	// HookCircuitState is the current breaker state per hook.
	// 0 = closed, 1 = open, 2 = half_open.
	HookCircuitState *prometheus.GaugeVec

	// Generation tracks the latest snapshot generation so operators
	// can correlate config/hook changes with metric spikes.
	Generation prometheus.Gauge
}

var (
	pipelineMetricsOnce sync.Once
	pipelineMetricsInst *PipelineMetrics
)

// getPipelineMetrics returns the process-wide singleton.
func getPipelineMetrics() *PipelineMetrics {
	pipelineMetricsOnce.Do(func() {
		pipelineMetricsInst = newPipelineMetrics()
	})
	return pipelineMetricsInst
}

func newPipelineMetrics() *PipelineMetrics {
	m := &PipelineMetrics{
		DispatchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "quality_pipeline",
			Name:      "dispatch_total",
			Help:      "Pipeline Run() invocations, labeled by outcome.",
		}, []string{"outcome"}),

		HookDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "quality_pipeline",
			Name:      "hook_duration_seconds",
			Help:      "Histogram of per-hook execution latency.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 15, 60},
		}, []string{"hook", "phase"}),

		HookErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "quality_pipeline",
			Name:      "hook_errors_total",
			Help:      "Hook errors classified by reason.",
		}, []string{"hook", "reason"}),

		HookCircuitState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "quality_pipeline",
			Name:      "hook_circuit_state",
			Help:      "Per-hook circuit-breaker state (0=closed, 1=open, 2=half_open).",
		}, []string{"hook"}),

		Generation: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: chatclimetrics.Namespace,
			Subsystem: "quality_pipeline",
			Name:      "generation",
			Help:      "Current snapshot generation (increments on each hook or config change).",
		}),
	}

	chatclimetrics.Registry.MustRegister(
		m.DispatchTotal,
		m.HookDuration,
		m.HookErrors,
		m.HookCircuitState,
		m.Generation,
	)
	return m
}
