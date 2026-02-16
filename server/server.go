/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/metrics"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
)

// Config holds server configuration.
type Config struct {
	Port             int
	Token            string // auth token (empty = no auth)
	TLSCertFile      string
	TLSKeyFile       string
	Provider         string
	Model            string
	EnableReflection bool // enable gRPC reflection (default: false, check CHATCLI_GRPC_REFLECTION env)
	MetricsPort      int  // Prometheus metrics HTTP port (0 = disabled, default: 9090)
}

// Server wraps the gRPC server and its dependencies.
type Server struct {
	config        Config
	grpcServer    *grpc.Server
	handler       *Handler
	logger        *zap.Logger
	metricsServer *metrics.Server
}

// New creates a new ChatCLI gRPC server.
func New(cfg Config, llmMgr manager.LLMManager, sessionStore SessionStore, logger *zap.Logger) *Server {
	authInterceptor := NewTokenAuthInterceptor(cfg.Token, logger)

	// Build interceptor chains — metrics first (outermost), then recovery, logging, auth
	unaryChain := []grpc.UnaryServerInterceptor{
		recoveryUnaryInterceptor(logger),
		loggingUnaryInterceptor(logger),
		authInterceptor.Unary(),
	}
	streamChain := []grpc.StreamServerInterceptor{
		recoveryStreamInterceptor(logger),
		loggingStreamInterceptor(logger),
		authInterceptor.Stream(),
	}

	// Metrics setup (gRPC interceptors + LLM/session/server metrics)
	var (
		grpcMetrics    *metrics.GRPCMetrics
		llmMetrics     *metrics.LLMMetrics
		sessionMetrics *metrics.SessionMetrics
		metricsServer  *metrics.Server
	)

	if cfg.MetricsPort > 0 {
		grpcMetrics = metrics.NewGRPCMetrics()
		llmMetrics = metrics.NewLLMMetrics()
		sessionMetrics = metrics.NewSessionMetrics()

		vi := version.GetCurrentVersion()
		metrics.NewServerMetrics(vi.Version, cfg.Provider, cfg.Model, time.Now())

		// Prepend metrics interceptors (outermost in chain)
		unaryChain = append([]grpc.UnaryServerInterceptor{grpcMetrics.UnaryInterceptor()}, unaryChain...)
		streamChain = append([]grpc.StreamServerInterceptor{grpcMetrics.StreamInterceptor()}, streamChain...)

		metricsServer = metrics.NewServer(cfg.MetricsPort, logger)
	}

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryChain...),
		grpc.ChainStreamInterceptor(streamChain...),
	}

	// TLS configuration
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			logger.Fatal("Failed to load TLS certificate", zap.Error(err))
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
		logger.Info("TLS enabled", zap.String("cert", cfg.TLSCertFile))
	}

	grpcServer := grpc.NewServer(opts...)
	handler := NewHandler(llmMgr, sessionStore, logger, cfg.Provider, cfg.Model)

	// Inject optional metrics recorders into handler
	if llmMetrics != nil {
		handler.llmMetrics = llmMetrics
	}
	if sessionMetrics != nil {
		handler.sessionMetrics = sessionMetrics
	}

	pb.RegisterChatCLIServiceServer(grpcServer, handler)

	// gRPC reflection is gated behind config or env var to avoid exposing the
	// full service schema in production. Enable via Config.EnableReflection
	// or CHATCLI_GRPC_REFLECTION=true.
	if cfg.EnableReflection || strings.EqualFold(os.Getenv("CHATCLI_GRPC_REFLECTION"), "true") {
		reflection.Register(grpcServer)
		logger.Info("gRPC reflection enabled")
	}

	return &Server{
		config:        cfg,
		grpcServer:    grpcServer,
		handler:       handler,
		logger:        logger,
		metricsServer: metricsServer,
	}
}

// Start begins listening and serving gRPC requests.
// It blocks until the server is stopped via signal or Stop().
func (s *Server) Start() error {
	// Start metrics HTTP server before gRPC (non-blocking)
	if s.metricsServer != nil {
		s.metricsServer.Start()
	}

	addr := fmt.Sprintf(":%d", s.config.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Graceful shutdown on SIGINT/SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		s.logger.Info("Received shutdown signal", zap.String("signal", sig.String()))
		if s.metricsServer != nil {
			s.metricsServer.Stop()
		}
		s.grpcServer.GracefulStop()
	}()

	s.logger.Info("ChatCLI gRPC server starting",
		zap.Int("port", s.config.Port),
		zap.String("provider", s.config.Provider),
		zap.String("model", s.config.Model),
		zap.Bool("auth_enabled", s.config.Token != ""),
		zap.Bool("tls_enabled", s.config.TLSCertFile != ""),
		zap.Int("metrics_port", s.config.MetricsPort),
	)

	fmt.Printf("🚀 ChatCLI server listening on %s\n", addr)
	if s.config.Token != "" {
		fmt.Println("🔒 Authentication enabled (Bearer token required)")
	}
	if s.config.MetricsPort > 0 {
		fmt.Printf("📊 Prometheus metrics on :%d/metrics\n", s.config.MetricsPort)
	}

	if err := s.grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("gRPC server failed: %w", err)
	}

	s.logger.Info("ChatCLI gRPC server stopped")
	return nil
}

// Stop gracefully stops the server.
func (s *Server) Stop() {
	if s.metricsServer != nil {
		s.metricsServer.Stop()
	}
	s.grpcServer.GracefulStop()
}

// SetWatcherContext configures K8s watcher context injection for all prompts.
func (s *Server) SetWatcherContext(fn func() string) {
	s.handler.SetWatcherContext(fn)
}

// SetWatcher configures full K8s watcher integration with context, status, and stats.
func (s *Server) SetWatcher(cfg WatcherConfig) {
	s.handler.SetWatcher(cfg)
}
