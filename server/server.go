/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/fallback"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/metrics"
	"github.com/diillson/chatcli/pkg/persona"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
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
	rateLimiter   *PerClientRateLimiter // for cleanup on shutdown
	auditLogger   *AuditLogger          // for cleanup on shutdown
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

	// --- Security: Rate Limiting (H3) ---
	rateLimiterCfg := DefaultRateLimiterConfig()
	rateLimiter := NewPerClientRateLimiter(rateLimiterCfg, logger)

	// --- Security: Audit Logging (L1) ---
	auditLogger := NewAuditLogger(logger)

	// Prepend security interceptors: validation → rate limit → audit (before auth chain)
	unaryChain = append([]grpc.UnaryServerInterceptor{
		ValidationInterceptor(),
		rateLimiter.UnaryInterceptor(),
		auditLogger.UnaryInterceptor(),
	}, unaryChain...)
	streamChain = append([]grpc.StreamServerInterceptor{
		rateLimiter.StreamInterceptor(),
	}, streamChain...)

	// --- Security: Message Size Limits (H2) ---
	maxRecvMsgSize := envIntOrDefault("CHATCLI_MAX_RECV_MSG_SIZE", 50*1024*1024) // 50MB
	maxSendMsgSize := envIntOrDefault("CHATCLI_MAX_SEND_MSG_SIZE", 50*1024*1024) // 50MB
	maxConcurrentStreams := envIntOrDefault("CHATCLI_MAX_CONCURRENT_STREAMS", 100)

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryChain...),
		grpc.ChainStreamInterceptor(streamChain...),
		grpc.MaxRecvMsgSize(maxRecvMsgSize),
		grpc.MaxSendMsgSize(maxSendMsgSize),
		grpc.MaxConcurrentStreams(safeUint32(maxConcurrentStreams)),
		// Allow operator and remote clients to send keepalive pings frequently.
		// Without this the default MinTime is 5min, causing ENHANCE_YOUR_CALM
		// disconnects for clients pinging every 30s.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             20 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    60 * time.Second,
			Timeout: 10 * time.Second,
		}),
	}

	// TLS configuration
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			// Write to stderr directly before Fatal — zap may not flush in containers
			// with read-only filesystems or redirected stdout, causing silent exits.
			fmt.Fprintf(os.Stderr, "FATAL: TLS certificate load failed: %v (cert=%s, key=%s)\n",
				err, cfg.TLSCertFile, cfg.TLSKeyFile)
			logger.Fatal(i18n.T("server.tls.load_failed"),
				zap.Error(err),
				zap.String("cert", cfg.TLSCertFile),
				zap.String("key", cfg.TLSKeyFile),
			)
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13, // Security: Upgrade to TLS 1.3 (M7)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
		logger.Info(i18n.T("server.tls.enabled"), zap.String("cert", cfg.TLSCertFile))
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

	// Security (M9): gRPC reflection requires BOTH config flag AND env var.
	// This prevents accidental exposure in production.
	reflectionEnv := strings.EqualFold(os.Getenv("CHATCLI_GRPC_REFLECTION"), "true")
	if cfg.EnableReflection && reflectionEnv {
		reflection.Register(grpcServer)
		logger.Info(i18n.T("server.reflection.enabled"))
		logger.Warn("gRPC reflection is enabled — disable in production environments")
	} else if cfg.EnableReflection || reflectionEnv {
		logger.Info("gRPC reflection requires both --enable-reflection flag AND CHATCLI_GRPC_REFLECTION=true env var")
	}

	return &Server{
		config:        cfg,
		grpcServer:    grpcServer,
		handler:       handler,
		logger:        logger,
		metricsServer: metricsServer,
		rateLimiter:   rateLimiter,
		auditLogger:   auditLogger,
	}
}

// Start begins listening and serving gRPC requests.
// It blocks until the server is stopped via signal or Stop().
func (s *Server) Start() error {
	// Start metrics HTTP server before gRPC (non-blocking)
	if s.metricsServer != nil {
		s.metricsServer.Start()
	}

	// Security: Bind to localhost by default, require explicit CHATCLI_BIND_ADDRESS for 0.0.0.0 (L3)
	bindAddr := os.Getenv("CHATCLI_BIND_ADDRESS")
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, s.config.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("server.listen.failed", addr), err)
	}

	// Graceful shutdown on SIGINT/SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		s.logger.Info(i18n.T("server.shutdown.signal"), zap.String("signal", sig.String()))
		if s.metricsServer != nil {
			s.metricsServer.Stop()
		}
		s.grpcServer.GracefulStop()
	}()

	s.logger.Info(i18n.T("server.starting"),
		zap.Int("port", s.config.Port),
		zap.String("provider", s.config.Provider),
		zap.String("model", s.config.Model),
		zap.Bool("auth_enabled", s.config.Token != ""),
		zap.Bool("tls_enabled", s.config.TLSCertFile != ""),
		zap.Int("metrics_port", s.config.MetricsPort),
	)

	fmt.Println(i18n.T("server.listening", addr))
	if s.config.Token != "" {
		fmt.Println(i18n.T("server.auth_enabled"))
	}
	if s.config.MetricsPort > 0 {
		fmt.Println(i18n.T("server.metrics_enabled", s.config.MetricsPort))
	}

	if err := s.grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("server.grpc_failed"), err)
	}

	s.logger.Info(i18n.T("server.stopped"))
	return nil
}

// Stop gracefully stops the server and cleans up resources.
func (s *Server) Stop() {
	if s.metricsServer != nil {
		s.metricsServer.Stop()
	}
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}
	if s.auditLogger != nil {
		s.auditLogger.Close()
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

// SetPluginManager configures plugin management for remote discovery and execution.
func (s *Server) SetPluginManager(pm *plugins.Manager) {
	s.handler.SetPluginManager(pm)
}

// SetPersonaLoader configures the persona loader for remote agent/skill discovery.
func (s *Server) SetPersonaLoader(pl *persona.Loader) {
	s.handler.SetPersonaLoader(pl)
}

// SetFallbackChain configures the provider fallback chain for automatic failover.
func (s *Server) SetFallbackChain(chain *fallback.Chain) {
	s.handler.SetFallbackChain(chain)
}

// SetMCPManager configures the MCP manager for tool interoperability.
func (s *Server) SetMCPManager(mgr *mcp.Manager) {
	s.handler.SetMCPManager(mgr)
}

// safeUint32 converts an int to uint32 with bounds checking to prevent overflow.
func safeUint32(v int) uint32 {
	if v < 0 {
		return 0
	}
	if v > int(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(v)
}

// envIntOrDefault reads an integer from an environment variable, returning the default on failure.
func envIntOrDefault(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultVal
}
