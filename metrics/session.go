/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import "github.com/prometheus/client_golang/prometheus"

// SessionMetrics holds Prometheus metrics for session management.
type SessionMetrics struct {
	ActiveSessions  prometheus.Gauge
	OperationsTotal *prometheus.CounterVec
}

// NewSessionMetrics creates and registers session metrics.
func NewSessionMetrics() *SessionMetrics {
	m := &SessionMetrics{
		ActiveSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "session",
			Name:      "active_total",
			Help:      "Number of currently active interactive sessions.",
		}),

		OperationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "session",
			Name:      "operations_total",
			Help:      "Total session operations by type (save, load, list, delete).",
		}, []string{"operation"}),
	}

	Registry.MustRegister(
		m.ActiveSessions,
		m.OperationsTotal,
	)

	return m
}
