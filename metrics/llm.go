/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import "github.com/prometheus/client_golang/prometheus"

// LLMMetrics holds Prometheus metrics for LLM provider interactions.
type LLMMetrics struct {
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
	TokensUsed      *prometheus.CounterVec
	ErrorsTotal     *prometheus.CounterVec
}

// NewLLMMetrics creates and registers LLM metrics.
func NewLLMMetrics() *LLMMetrics {
	m := &LLMMetrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "llm",
			Name:      "requests_total",
			Help:      "Total number of LLM requests by provider, model, and status.",
		}, []string{"provider", "model", "status"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "llm",
			Name:      "request_duration_seconds",
			Help:      "Histogram of LLM request latencies in seconds.",
			Buckets:   []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120},
		}, []string{"provider", "model"}),

		TokensUsed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "llm",
			Name:      "tokens_used_total",
			Help:      "Total tokens consumed by LLM calls (prompt + completion).",
		}, []string{"provider", "model", "type"}),

		ErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "llm",
			Name:      "errors_total",
			Help:      "Total LLM errors by provider, model, and error type.",
		}, []string{"provider", "model", "error_type"}),
	}

	Registry.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.TokensUsed,
		m.ErrorsTotal,
	)

	return m
}
