/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import "github.com/prometheus/client_golang/prometheus"

// WatcherMetrics holds Prometheus metrics for the K8s watcher subsystem.
type WatcherMetrics struct {
	CollectionDuration *prometheus.HistogramVec
	CollectionErrors   *prometheus.CounterVec
	AlertsTotal        *prometheus.CounterVec
	TargetsMonitored   prometheus.Gauge
	PodsReady          *prometheus.GaugeVec
	PodsDesired        *prometheus.GaugeVec
	SnapshotsStored    *prometheus.GaugeVec
	PodRestarts        *prometheus.GaugeVec
}

// NewWatcherMetrics creates and registers watcher metrics.
func NewWatcherMetrics() *WatcherMetrics {
	m := &WatcherMetrics{
		CollectionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "collection_duration_seconds",
			Help:      "Histogram of K8s data collection cycle durations.",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30},
		}, []string{"target"}),

		CollectionErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "collection_errors_total",
			Help:      "Total collection cycle errors by target.",
		}, []string{"target"}),

		AlertsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "alerts_total",
			Help:      "Total alerts detected by severity and type.",
		}, []string{"target", "severity", "type"}),

		TargetsMonitored: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "targets_monitored",
			Help:      "Number of K8s targets currently being monitored.",
		}),

		PodsReady: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "pods_ready",
			Help:      "Number of ready pods per deployment.",
		}, []string{"namespace", "deployment"}),

		PodsDesired: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "pods_desired",
			Help:      "Number of desired pods per deployment.",
		}, []string{"namespace", "deployment"}),

		SnapshotsStored: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "snapshots_stored",
			Help:      "Number of snapshots stored per target.",
		}, []string{"target"}),

		PodRestarts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "watcher",
			Name:      "pod_restarts_total",
			Help:      "Total pod restart count per target (sum across all pods).",
		}, []string{"target"}),
	}

	Registry.MustRegister(
		m.CollectionDuration,
		m.CollectionErrors,
		m.AlertsTotal,
		m.TargetsMonitored,
		m.PodsReady,
		m.PodsDesired,
		m.SnapshotsStored,
		m.PodRestarts,
	)

	return m
}

// WatcherMetricsRecorder is the interface that the k8s package uses to record
// watcher metrics without importing the metrics package directly.
// This avoids circular dependencies between k8s/ and metrics/.
type WatcherMetricsRecorder interface {
	ObserveCollectionDuration(target string, seconds float64)
	IncrementCollectionErrors(target string)
	IncrementAlert(target, severity, alertType string)
	SetPodsReady(namespace, deployment string, count float64)
	SetPodsDesired(namespace, deployment string, count float64)
	SetSnapshotsStored(target string, count float64)
	SetPodRestarts(target string, count float64)
}

// Recorder returns a WatcherMetricsRecorder backed by this WatcherMetrics.
func (m *WatcherMetrics) Recorder() WatcherMetricsRecorder {
	return &watcherRecorder{m: m}
}

type watcherRecorder struct {
	m *WatcherMetrics
}

func (r *watcherRecorder) ObserveCollectionDuration(target string, seconds float64) {
	r.m.CollectionDuration.WithLabelValues(target).Observe(seconds)
}

func (r *watcherRecorder) IncrementCollectionErrors(target string) {
	r.m.CollectionErrors.WithLabelValues(target).Inc()
}

func (r *watcherRecorder) IncrementAlert(target, severity, alertType string) {
	r.m.AlertsTotal.WithLabelValues(target, severity, alertType).Inc()
}

func (r *watcherRecorder) SetPodsReady(namespace, deployment string, count float64) {
	r.m.PodsReady.WithLabelValues(namespace, deployment).Set(count)
}

func (r *watcherRecorder) SetPodsDesired(namespace, deployment string, count float64) {
	r.m.PodsDesired.WithLabelValues(namespace, deployment).Set(count)
}

func (r *watcherRecorder) SetSnapshotsStored(target string, count float64) {
	r.m.SnapshotsStored.WithLabelValues(target).Set(count)
}

func (r *watcherRecorder) SetPodRestarts(target string, count float64) {
	r.m.PodRestarts.WithLabelValues(target).Set(count)
}
