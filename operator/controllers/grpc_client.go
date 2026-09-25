package controllers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// ConnectionOpts configures TLS and authentication for the gRPC connection.
type ConnectionOpts struct {
	// TLSEnabled enables TLS transport.
	TLSEnabled bool

	// CACert is the optional CA certificate bytes for verifying the server certificate.
	// If empty and TLSEnabled is true, the system certificate pool is used.
	CACert []byte

	// Token is the Bearer token for authentication.
	Token string

	// TokenSource, when set, yields the bearer credential per call (minted
	// JWTs that renew themselves). It takes precedence over Token.
	TokenSource TokenSource

	// ClientCertificate is the client certificate the operator presents
	// (mutual TLS), parsed from the Instance's Secret. It takes precedence
	// over CHATCLI_GRPC_TLS_CERT/KEY, which stay as the operator-wide
	// fallback. A pointer so the struct stays comparable.
	ClientCertificate *tls.Certificate
}

// ServerClient wraps the gRPC connection to the ChatCLI server.
type ServerClient struct {
	mu     sync.RWMutex
	conn   *grpc.ClientConn
	client pb.ChatCLIServiceClient
	token  string
	source TokenSource
	logger *zap.Logger
}

// NewServerClient creates a new ServerClient (not yet connected).
func NewServerClient(logger *zap.Logger) *ServerClient {
	return &ServerClient{logger: logger}
}

// Connect establishes a gRPC connection to the server at the given address.
func (sc *ServerClient) Connect(address string, opts ConnectionOpts) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn != nil {
		// Best-effort teardown of the previous connection before dialing a
		// new one; a close error on a stale conn is not actionable here.
		_ = sc.conn.Close()
	}

	var dialOpts []grpc.DialOption

	// Security (H7): TLS always required. Upgraded to TLS 1.3.
	// Use CHATCLI_GRPC_TLS_CERT/KEY for mTLS client certificates.
	// Use CHATCLI_GRPC_TLS_CA for custom CA certificate.
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	// Load custom CA certificate if provided
	if len(opts.CACert) > 0 {
		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(opts.CACert) {
			return fmt.Errorf("failed to parse CA certificate")
		}
		tlsCfg.RootCAs = certPool
	} else if caCertPath := os.Getenv("CHATCLI_GRPC_TLS_CA"); caCertPath != "" {
		// Operator-config path: canonicalize and require absolute so a
		// relative value cannot resolve against an unexpected working dir.
		caCertPath = filepath.Clean(caCertPath)
		if !filepath.IsAbs(caCertPath) {
			return fmt.Errorf("CHATCLI_GRPC_TLS_CA must be an absolute path, got %q", caCertPath)
		}
		caCert, err := os.ReadFile(caCertPath) // #nosec G304 G703 -- operator-supplied absolute path, cleaned above
		if err != nil {
			return fmt.Errorf("failed to read CA cert from %s: %w", caCertPath, err)
		}
		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(caCert) {
			return fmt.Errorf("failed to parse CA certificate from %s", caCertPath)
		}
		tlsCfg.RootCAs = certPool
	}

	// mTLS: per-Instance client certificate from the CR, else the
	// operator-wide one from the environment.
	if opts.ClientCertificate != nil {
		tlsCfg.Certificates = []tls.Certificate{*opts.ClientCertificate}
	} else if certPath := os.Getenv("CHATCLI_GRPC_TLS_CERT"); certPath != "" {
		keyPath := os.Getenv("CHATCLI_GRPC_TLS_KEY")
		if keyPath == "" {
			return fmt.Errorf("CHATCLI_GRPC_TLS_CERT set but CHATCLI_GRPC_TLS_KEY is missing")
		}
		clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return fmt.Errorf("failed to load client TLS cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{clientCert}
	}

	if opts.TLSEnabled {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		// Security (H7): TLS always enforced — insecure connections are not permitted
		sc.logger.Info("TLS enforced with system CAs (explicit TLS flag not set, but TLS is mandatory)")
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	}

	// Keepalive: detect dead connections without flooding the server.
	// Server EnforcementPolicy.MinTime is 20s, so Time must be >= 20s.
	dialOpts = append(dialOpts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                30 * time.Second, // ping every 30s if no activity
		Timeout:             5 * time.Second,  // wait 5s for pong before considering dead
		PermitWithoutStream: true,             // ping even without active RPCs
	}))

	// Client-side round-robin load balancing across resolved pod addresses
	dialOpts = append(dialOpts, grpc.WithDefaultServiceConfig(`{"loadBalancingConfig": [{"round_robin":{}}]}`))

	conn, err := grpc.NewClient(address, dialOpts...)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", address, err)
	}

	sc.conn = conn
	sc.client = pb.NewChatCLIServiceClient(conn)
	sc.token = opts.Token
	sc.source = opts.TokenSource

	sc.logger.Info("Connected to ChatCLI server",
		zap.String("address", address),
		zap.Bool("tls", opts.TLSEnabled),
		zap.Bool("auth", opts.Token != "" || opts.TokenSource != nil),
		zap.Bool("minted_jwt", opts.TokenSource != nil))
	return nil
}

// withAuth injects the Bearer credential into the gRPC context metadata:
// the per-call source when configured (a minted JWT that renews itself),
// else the static token. A source that fails leaves the call
// unauthenticated so the server's refusal, not a silent retry loop, is what
// surfaces; the failure is logged with its cause.
func (sc *ServerClient) withAuth(ctx context.Context) context.Context {
	token := sc.token
	if sc.source != nil {
		minted, err := sc.source.Token(ctx)
		if err != nil {
			sc.logger.Error("operator credential unavailable; call goes out unauthenticated", zap.Error(err))
			return ctx
		}
		token = minted
	}
	if token == "" {
		return ctx
	}
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewOutgoingContext(ctx, md)
}

// GetAlerts calls the GetAlerts RPC.
func (sc *ServerClient) GetAlerts(ctx context.Context) (*pb.GetAlertsResponse, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}

	return sc.client.GetAlerts(sc.withAuth(ctx), &pb.GetAlertsRequest{})
}

// StreamAlerts opens the StreamAlerts RPC. The stream lives as long as ctx;
// cancel it to close the stream. With includeCurrent the server opens with
// the alerts active right now before pushing new ones.
func (sc *ServerClient) StreamAlerts(ctx context.Context, includeCurrent bool) (pb.ChatCLIService_StreamAlertsClient, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}

	return sc.client.StreamAlerts(sc.withAuth(ctx), &pb.StreamAlertsRequest{IncludeCurrent: includeCurrent})
}

// AnalyzeIssue calls the AnalyzeIssue RPC.
func (sc *ServerClient) AnalyzeIssue(ctx context.Context, req *pb.AnalyzeIssueRequest) (*pb.AnalyzeIssueResponse, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}

	return sc.client.AnalyzeIssue(sc.withAuth(ctx), req)
}

// Health calls the Health RPC (unauthenticated on the server).
func (sc *ServerClient) Health(ctx context.Context) (*pb.HealthResponse, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}
	return sc.client.Health(sc.withAuth(ctx), &pb.HealthRequest{})
}

// GetServerInfo calls the GetServerInfo RPC.
func (sc *ServerClient) GetServerInfo(ctx context.Context) (*pb.GetServerInfoResponse, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}
	return sc.client.GetServerInfo(sc.withAuth(ctx), &pb.GetServerInfoRequest{})
}

// AgenticStepCaller abstracts the AgenticStep RPC for testing.
type AgenticStepCaller interface {
	AgenticStep(ctx context.Context, req *pb.AgenticStepRequest) (*pb.AgenticStepResponse, error)
	IsConnected() bool
}

// AgenticStep calls the AgenticStep RPC for one turn of the agentic remediation loop.
func (sc *ServerClient) AgenticStep(ctx context.Context, req *pb.AgenticStepRequest) (*pb.AgenticStepResponse, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}

	return sc.client.AgenticStep(sc.withAuth(ctx), req)
}

// IsConnected returns true if the client has an active connection.
func (sc *ServerClient) IsConnected() bool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.conn != nil
}

// Close closes the gRPC connection.
func (sc *ServerClient) Close() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn != nil {
		err := sc.conn.Close()
		sc.conn = nil
		sc.client = nil
		sc.token = ""
		return err
	}
	return nil
}
