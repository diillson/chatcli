/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ServerMetrics holds Prometheus metrics for server-level information.
type ServerMetrics struct {
	Info   *prometheus.GaugeVec
	uptime prometheus.GaugeFunc
}

// NewServerMetrics creates and registers server metrics.
// startTime is the server boot time used to compute uptime.
func NewServerMetrics(version, provider, model string, startTime time.Time) *ServerMetrics {
	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Subsystem: "server",
		Name:      "info",
		Help:      "Server metadata (version, provider, model). Value is always 1.",
	}, []string{"version", "provider", "model"})

	info.WithLabelValues(version, provider, model).Set(1)

	uptime := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: Namespace,
		Subsystem: "server",
		Name:      "uptime_seconds",
		Help:      "Server uptime in seconds.",
	}, func() float64 {
		return time.Since(startTime).Seconds()
	})

	Registry.MustRegister(info, uptime)

	return &ServerMetrics{
		Info:   info,
		uptime: uptime,
	}
}
