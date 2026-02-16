package metrics

import "github.com/prometheus/client_golang/prometheus"

// newTestRegistry creates a fresh Prometheus registry for test isolation.
func newTestRegistry() *prometheus.Registry {
	return prometheus.NewRegistry()
}

// newGRPCMetricsOn creates gRPC metrics registered on the given registry.
func newGRPCMetricsOn(reg *prometheus.Registry) *GRPCMetrics {
	m := &GRPCMetrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "grpc", Name: "requests_total",
			Help: "test",
		}, []string{"method", "code"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Subsystem: "grpc", Name: "request_duration_seconds",
			Help: "test", Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		InFlightRequests: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "grpc", Name: "in_flight_requests",
			Help: "test",
		}, []string{"method"}),
		StreamMsgsSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "grpc", Name: "stream_messages_sent_total",
			Help: "test",
		}, []string{"method"}),
		StreamMsgsReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "grpc", Name: "stream_messages_received_total",
			Help: "test",
		}, []string{"method"}),
	}
	reg.MustRegister(m.RequestsTotal, m.RequestDuration, m.InFlightRequests, m.StreamMsgsSent, m.StreamMsgsReceived)
	return m
}

// newLLMMetricsOn creates LLM metrics registered on the given registry.
func newLLMMetricsOn(reg *prometheus.Registry) *LLMMetrics {
	m := &LLMMetrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "llm", Name: "requests_total",
			Help: "test",
		}, []string{"provider", "model", "status"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Subsystem: "llm", Name: "request_duration_seconds",
			Help: "test", Buckets: prometheus.DefBuckets,
		}, []string{"provider", "model"}),
		TokensUsed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "llm", Name: "tokens_used_total",
			Help: "test",
		}, []string{"provider", "model", "type"}),
		ErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "llm", Name: "errors_total",
			Help: "test",
		}, []string{"provider", "model", "error_type"}),
	}
	reg.MustRegister(m.RequestsTotal, m.RequestDuration, m.TokensUsed, m.ErrorsTotal)
	return m
}

// newWatcherMetricsOn creates watcher metrics registered on the given registry.
func newWatcherMetricsOn(reg *prometheus.Registry) *WatcherMetrics {
	m := &WatcherMetrics{
		CollectionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "collection_duration_seconds",
			Help: "test", Buckets: prometheus.DefBuckets,
		}, []string{"target"}),
		CollectionErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "collection_errors_total",
			Help: "test",
		}, []string{"target"}),
		AlertsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "alerts_total",
			Help: "test",
		}, []string{"target", "severity", "type"}),
		TargetsMonitored: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "targets_monitored",
			Help: "test",
		}),
		PodsReady: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "pods_ready",
			Help: "test",
		}, []string{"namespace", "deployment"}),
		PodsDesired: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "pods_desired",
			Help: "test",
		}, []string{"namespace", "deployment"}),
		SnapshotsStored: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "snapshots_stored",
			Help: "test",
		}, []string{"target"}),
		PodRestarts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace, Subsystem: "watcher", Name: "pod_restarts_total",
			Help: "test",
		}, []string{"target"}),
	}
	reg.MustRegister(m.CollectionDuration, m.CollectionErrors, m.AlertsTotal,
		m.TargetsMonitored, m.PodsReady, m.PodsDesired, m.SnapshotsStored, m.PodRestarts)
	return m
}
