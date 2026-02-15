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
	"syscall"

	"github.com/diillson/chatcli/llm/manager"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
)

// Config holds server configuration.
type Config struct {
	Port        int
	Token       string // auth token (empty = no auth)
	TLSCertFile string
	TLSKeyFile  string
	Provider    string
	Model       string
}

// Server wraps the gRPC server and its dependencies.
type Server struct {
	config     Config
	grpcServer *grpc.Server
	handler    *Handler
	logger     *zap.Logger
}

// New creates a new ChatCLI gRPC server.
func New(cfg Config, llmMgr manager.LLMManager, sessionStore SessionStore, logger *zap.Logger) *Server {
	authInterceptor := NewTokenAuthInterceptor(cfg.Token, logger)

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			recoveryUnaryInterceptor(logger),
			loggingUnaryInterceptor(logger),
			authInterceptor.Unary(),
		),
		grpc.ChainStreamInterceptor(
			recoveryStreamInterceptor(logger),
			loggingStreamInterceptor(logger),
			authInterceptor.Stream(),
		),
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

	pb.RegisterChatCLIServiceServer(grpcServer, handler)
	reflection.Register(grpcServer) // enable grpc reflection for debugging

	return &Server{
		config:     cfg,
		grpcServer: grpcServer,
		handler:    handler,
		logger:     logger,
	}
}

// Start begins listening and serving gRPC requests.
// It blocks until the server is stopped via signal or Stop().
func (s *Server) Start() error {
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
		s.grpcServer.GracefulStop()
	}()

	s.logger.Info("ChatCLI gRPC server starting",
		zap.Int("port", s.config.Port),
		zap.String("provider", s.config.Provider),
		zap.String("model", s.config.Model),
		zap.Bool("auth_enabled", s.config.Token != ""),
		zap.Bool("tls_enabled", s.config.TLSCertFile != ""),
	)

	fmt.Printf("🚀 ChatCLI server listening on %s\n", addr)
	if s.config.Token != "" {
		fmt.Println("🔒 Authentication enabled (Bearer token required)")
	}

	if err := s.grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("gRPC server failed: %w", err)
	}

	s.logger.Info("ChatCLI gRPC server stopped")
	return nil
}

// Stop gracefully stops the server.
func (s *Server) Stop() {
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
