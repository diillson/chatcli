package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ServerClient wraps the gRPC connection to the ChatCLI server.
type ServerClient struct {
	mu     sync.RWMutex
	conn   *grpc.ClientConn
	client pb.ChatCLIServiceClient
	logger *zap.Logger
}

// NewServerClient creates a new ServerClient (not yet connected).
func NewServerClient(logger *zap.Logger) *ServerClient {
	return &ServerClient{logger: logger}
}

// Connect establishes a gRPC connection to the server at the given address.
func (sc *ServerClient) Connect(address string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn != nil {
		sc.conn.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", address, err)
	}

	sc.conn = conn
	sc.client = pb.NewChatCLIServiceClient(conn)
	sc.logger.Info("Connected to ChatCLI server", zap.String("address", address))
	return nil
}

// GetAlerts calls the GetAlerts RPC.
func (sc *ServerClient) GetAlerts(ctx context.Context) (*pb.GetAlertsResponse, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}

	return sc.client.GetAlerts(ctx, &pb.GetAlertsRequest{})
}

// AnalyzeIssue calls the AnalyzeIssue RPC.
func (sc *ServerClient) AnalyzeIssue(ctx context.Context, req *pb.AnalyzeIssueRequest) (*pb.AnalyzeIssueResponse, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.client == nil {
		return nil, fmt.Errorf("not connected to server")
	}

	return sc.client.AnalyzeIssue(ctx, req)
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
		return err
	}
	return nil
}
