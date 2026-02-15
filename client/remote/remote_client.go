/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package remote

import (
	"context"
	"fmt"

	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Client implements llm/client.LLMClient by delegating to a remote ChatCLI gRPC server.
type Client struct {
	conn           *grpc.ClientConn
	grpcClient     pb.ChatCLIServiceClient
	token          string            // server auth token
	clientAPIKey   string            // client's own LLM provider API key (forwarded to server)
	overProvider   string            // client-requested provider override (sent in every request)
	overModel      string            // client-requested model override (sent in every request)
	providerConfig map[string]string // provider-specific config (StackSpot realm/agent_id, Ollama base_url, etc.)
	model          string            // effective model name (for display)
	provider       string            // effective provider name (for display)
	logger         *zap.Logger
}

// Config holds remote client configuration.
type Config struct {
	Address        string            // server address (host:port)
	Token          string            // auth token for gRPC server authentication
	TLS            bool              // use TLS
	CertFile       string            // CA certificate file for TLS (optional)
	ClientAPIKey   string            // optional: client's own LLM API key/OAuth token (forwarded to server)
	Provider       string            // optional: override server's default provider (e.g., "GOOGLEAI")
	Model          string            // optional: override server's default model (e.g., "gemini-2.0-flash")
	ProviderConfig map[string]string // optional: provider-specific config (StackSpot, Ollama, etc.)
}

// NewClient creates a new remote gRPC client that implements LLMClient.
func NewClient(cfg Config, logger *zap.Logger) (*Client, error) {
	var dialOpts []grpc.DialOption

	if cfg.TLS {
		if cfg.CertFile != "" {
			creds, err := credentials.NewClientTLSFromFile(cfg.CertFile, "")
			if err != nil {
				return nil, fmt.Errorf("failed to load TLS certificate: %w", err)
			}
			dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
		} else {
			dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
		}
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(cfg.Address, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", cfg.Address, err)
	}

	grpcClient := pb.NewChatCLIServiceClient(conn)

	c := &Client{
		conn:           conn,
		grpcClient:     grpcClient,
		token:          cfg.Token,
		clientAPIKey:   cfg.ClientAPIKey,
		overProvider:   cfg.Provider,
		overModel:      cfg.Model,
		providerConfig: cfg.ProviderConfig,
		logger:         logger,
	}

	// Fetch server info to get the default model/provider
	ctx := c.withAuth(context.Background())
	info, err := grpcClient.GetServerInfo(ctx, &pb.GetServerInfoRequest{})
	if err != nil {
		logger.Warn("Failed to fetch server info, using defaults", zap.Error(err))
		c.model = "remote"
		c.provider = "remote"
	} else {
		c.model = info.Model
		c.provider = info.Provider
		logger.Info("Connected to remote ChatCLI server",
			zap.String("version", info.Version),
			zap.String("provider", info.Provider),
			zap.String("model", info.Model),
		)
	}

	// Client-side overrides take precedence over server defaults
	if c.overProvider != "" {
		c.provider = c.overProvider
	}
	if c.overModel != "" {
		c.model = c.overModel
	}

	return c, nil
}

// withAuth adds the authorization metadata to the context.
func (c *Client) withAuth(ctx context.Context) context.Context {
	if c.token == "" {
		return ctx
	}
	md := metadata.Pairs("authorization", "Bearer "+c.token)
	return metadata.NewOutgoingContext(ctx, md)
}

// GetModelName returns the remote server's model name.
// This satisfies the LLMClient interface.
func (c *Client) GetModelName() string {
	return c.model
}

// SendPrompt sends a prompt to the remote server and returns the response.
// This satisfies the LLMClient interface.
func (c *Client) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	ctx = c.withAuth(ctx)

	protoHistory := make([]*pb.ChatMessage, 0, len(history))
	for _, msg := range history {
		protoHistory = append(protoHistory, &pb.ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	resp, err := c.grpcClient.SendPrompt(ctx, &pb.SendPromptRequest{
		Prompt:         prompt,
		History:        protoHistory,
		MaxTokens:      int32(maxTokens),
		Provider:       c.overProvider,
		Model:          c.overModel,
		ClientApiKey:   c.clientAPIKey,
		ProviderConfig: c.providerConfig,
	})
	if err != nil {
		return "", fmt.Errorf("remote SendPrompt failed: %w", err)
	}

	// Update model/provider from server response when not explicitly set by the client.
	// This allows the display to show the actual model being used on the server.
	if resp.Model != "" && c.overModel == "" {
		c.model = resp.Model
	}
	if resp.Provider != "" && c.overProvider == "" {
		c.provider = resp.Provider
	}

	return resp.Response, nil
}

// GetProvider returns the remote server's provider.
func (c *Client) GetProvider() string {
	return c.provider
}

// ListSessions lists sessions from the remote server.
func (c *Client) ListSessions(ctx context.Context) ([]string, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ListSessions(ctx, &pb.ListSessionsRequest{})
	if err != nil {
		return nil, fmt.Errorf("remote ListSessions failed: %w", err)
	}
	return resp.Sessions, nil
}

// LoadSession loads a session from the remote server.
func (c *Client) LoadSession(ctx context.Context, name string) ([]models.Message, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.LoadSession(ctx, &pb.LoadSessionRequest{Name: name})
	if err != nil {
		return nil, fmt.Errorf("remote LoadSession failed: %w", err)
	}

	messages := make([]models.Message, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		messages = append(messages, models.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	return messages, nil
}

// SaveSession saves a session to the remote server.
func (c *Client) SaveSession(ctx context.Context, name string, history []models.Message) error {
	ctx = c.withAuth(ctx)
	protoMsgs := make([]*pb.ChatMessage, 0, len(history))
	for _, msg := range history {
		protoMsgs = append(protoMsgs, &pb.ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	_, err := c.grpcClient.SaveSession(ctx, &pb.SaveSessionRequest{
		Name:     name,
		Messages: protoMsgs,
	})
	if err != nil {
		return fmt.Errorf("remote SaveSession failed: %w", err)
	}
	return nil
}

// Health checks if the remote server is healthy.
func (c *Client) Health(ctx context.Context) (bool, string, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		return false, "", fmt.Errorf("health check failed: %w", err)
	}
	return resp.Status == pb.HealthResponse_SERVING, resp.Version, nil
}

// ServerInfo holds server metadata returned by GetServerInfo.
type ServerInfo struct {
	Version            string
	Provider           string
	Model              string
	AvailableProviders []string
	WatcherActive      bool
	WatcherTarget      string
}

// GetServerInfo fetches server metadata including watcher status.
func (c *Client) GetServerInfo(ctx context.Context) (*ServerInfo, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.GetServerInfo(ctx, &pb.GetServerInfoRequest{})
	if err != nil {
		return nil, fmt.Errorf("GetServerInfo failed: %w", err)
	}
	return &ServerInfo{
		Version:            resp.Version,
		Provider:           resp.Provider,
		Model:              resp.Model,
		AvailableProviders: resp.AvailableProviders,
		WatcherActive:      resp.WatcherActive,
		WatcherTarget:      resp.WatcherTarget,
	}, nil
}

// WatcherStatus holds the K8s watcher status from the server.
type WatcherStatus struct {
	Active        bool
	Deployment    string
	Namespace     string
	StatusSummary string
	AlertCount    int
	SnapshotCount int
	PodCount      int
}

// GetWatcherStatus fetches the K8s watcher status from the server.
func (c *Client) GetWatcherStatus(ctx context.Context) (*WatcherStatus, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.GetWatcherStatus(ctx, &pb.GetWatcherStatusRequest{})
	if err != nil {
		return nil, fmt.Errorf("GetWatcherStatus failed: %w", err)
	}
	return &WatcherStatus{
		Active:        resp.Active,
		Deployment:    resp.Deployment,
		Namespace:     resp.Namespace,
		StatusSummary: resp.StatusSummary,
		AlertCount:    int(resp.AlertCount),
		SnapshotCount: int(resp.SnapshotCount),
		PodCount:      int(resp.PodCount),
	}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
