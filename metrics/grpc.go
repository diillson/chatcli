/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// GRPCMetrics holds Prometheus metrics for gRPC server instrumentation.
type GRPCMetrics struct {
	RequestsTotal      *prometheus.CounterVec
	RequestDuration    *prometheus.HistogramVec
	InFlightRequests   *prometheus.GaugeVec
	StreamMsgsSent     *prometheus.CounterVec
	StreamMsgsReceived *prometheus.CounterVec
}

// NewGRPCMetrics creates and registers gRPC metrics on the custom registry.
func NewGRPCMetrics() *GRPCMetrics {
	m := &GRPCMetrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "grpc",
			Name:      "requests_total",
			Help:      "Total number of gRPC requests by method and status code.",
		}, []string{"method", "code"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "grpc",
			Name:      "request_duration_seconds",
			Help:      "Histogram of gRPC request latencies in seconds.",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"method"}),

		InFlightRequests: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "grpc",
			Name:      "in_flight_requests",
			Help:      "Number of gRPC requests currently being processed.",
		}, []string{"method"}),

		StreamMsgsSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "grpc",
			Name:      "stream_messages_sent_total",
			Help:      "Total number of gRPC stream messages sent.",
		}, []string{"method"}),

		StreamMsgsReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "grpc",
			Name:      "stream_messages_received_total",
			Help:      "Total number of gRPC stream messages received.",
		}, []string{"method"}),
	}

	Registry.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.InFlightRequests,
		m.StreamMsgsSent,
		m.StreamMsgsReceived,
	)

	return m
}

// UnaryInterceptor returns a gRPC unary server interceptor that records metrics.
func (m *GRPCMetrics) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		method := info.FullMethod
		m.InFlightRequests.WithLabelValues(method).Inc()
		start := time.Now()

		resp, err := handler(ctx, req)

		duration := time.Since(start).Seconds()
		code := status.Code(err).String()

		m.InFlightRequests.WithLabelValues(method).Dec()
		m.RequestsTotal.WithLabelValues(method, code).Inc()
		m.RequestDuration.WithLabelValues(method).Observe(duration)

		return resp, err
	}
}

// StreamInterceptor returns a gRPC stream server interceptor that records metrics.
func (m *GRPCMetrics) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		method := info.FullMethod
		m.InFlightRequests.WithLabelValues(method).Inc()
		start := time.Now()

		wrapped := &instrumentedServerStream{
			ServerStream: ss,
			method:       method,
			metrics:      m,
		}

		err := handler(srv, wrapped)

		duration := time.Since(start).Seconds()
		code := status.Code(err).String()

		m.InFlightRequests.WithLabelValues(method).Dec()
		m.RequestsTotal.WithLabelValues(method, code).Inc()
		m.RequestDuration.WithLabelValues(method).Observe(duration)

		return err
	}
}

// instrumentedServerStream wraps grpc.ServerStream to count sent/received messages.
type instrumentedServerStream struct {
	grpc.ServerStream
	method  string
	metrics *GRPCMetrics
}

func (s *instrumentedServerStream) SendMsg(m interface{}) error {
	err := s.ServerStream.SendMsg(m)
	if err == nil {
		s.metrics.StreamMsgsSent.WithLabelValues(s.method).Inc()
	}
	return err
}

func (s *instrumentedServerStream) RecvMsg(m interface{}) error {
	err := s.ServerStream.RecvMsg(m)
	if err == nil {
		s.metrics.StreamMsgsReceived.WithLabelValues(s.method).Inc()
	}
	return err
}
