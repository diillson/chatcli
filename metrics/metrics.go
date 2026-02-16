/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

const (
	// Namespace is the Prometheus namespace for all ChatCLI metrics.
	Namespace = "chatcli"
)

// Registry is the custom Prometheus registry for ChatCLI.
// Using a custom registry avoids polluting the global default and gives full
// control over which collectors are active.
var Registry = prometheus.NewRegistry()

func init() {
	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// Server serves the /metrics HTTP endpoint for Prometheus scraping.
type Server struct {
	httpServer *http.Server
	logger     *zap.Logger
}

// NewServer creates a metrics HTTP server on the given port.
// Pass port=0 to disable.
func NewServer(port int, logger *zap.Logger) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	return &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// Start begins serving metrics. Non-blocking — runs in background goroutine.
func (s *Server) Start() {
	go func() {
		s.logger.Info("Metrics HTTP server starting", zap.String("addr", s.httpServer.Addr))
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("Metrics HTTP server error", zap.Error(err))
		}
	}()
}

// Stop gracefully shuts down the metrics server.
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.logger.Error("Metrics server shutdown error", zap.Error(err))
	}
}
