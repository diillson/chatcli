/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// AuditEntry is a structured audit log event for security-relevant operations.
type AuditEntry struct {
	Timestamp   string            `json:"timestamp"`
	RequestID   string            `json:"request_id"`
	ClientID    string            `json:"client_id"`
	ClientAddr  string            `json:"client_addr,omitempty"`
	Method      string            `json:"method"`
	Resource    string            `json:"resource,omitempty"`
	Result      string            `json:"result"` // "success", "error", "denied"
	Duration    string            `json:"duration,omitempty"`
	RequestSize int               `json:"request_size,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
}

// AuditLogger provides structured audit logging for gRPC server operations.
type AuditLogger struct {
	mu         sync.Mutex
	zapLogger  *zap.Logger
	fileWriter io.WriteCloser
	encoder    *json.Encoder
}

// NewAuditLogger creates an audit logger. If CHATCLI_AUDIT_LOG_PATH is set,
// audit entries are also written to that file in JSON-lines format.
func NewAuditLogger(logger *zap.Logger) *AuditLogger {
	al := &AuditLogger{
		zapLogger: logger.Named("audit"),
	}

	if path := os.Getenv("CHATCLI_AUDIT_LOG_PATH"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			logger.Error("failed to open audit log file", zap.String("path", path), zap.Error(err))
		} else {
			al.fileWriter = f
			al.encoder = json.NewEncoder(f)
		}
	}

	return al
}

// Log writes an audit entry to both the zap logger and the optional audit file.
func (al *AuditLogger) Log(entry AuditEntry) {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	al.zapLogger.Info("audit",
		zap.String("request_id", entry.RequestID),
		zap.String("client_id", entry.ClientID),
		zap.String("method", entry.Method),
		zap.String("resource", entry.Resource),
		zap.String("result", entry.Result),
		zap.String("duration", entry.Duration),
	)

	if al.encoder != nil {
		al.mu.Lock()
		_ = al.encoder.Encode(entry)
		al.mu.Unlock()
	}
}

// Close shuts down the audit file writer.
func (al *AuditLogger) Close() {
	if al.fileWriter != nil {
		al.fileWriter.Close()
	}
}

// UnaryInterceptor returns a gRPC interceptor that logs all unary RPCs for audit.
func (al *AuditLogger) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		requestID := uuid.New().String()

		// Inject request ID into context metadata for downstream handlers
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", requestID)
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))

		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		entry := AuditEntry{
			RequestID:  requestID,
			ClientID:   extractClientID(ctx),
			ClientAddr: extractPeerAddr(ctx),
			Method:     info.FullMethod,
			Resource:   extractResourceFromRequest(info.FullMethod, req),
			Duration:   duration.String(),
		}

		if err != nil {
			entry.Result = "error"
			entry.Details = map[string]string{"error": sanitizeErrorForAudit(err.Error())}
		} else {
			entry.Result = "success"
		}

		// Log security-sensitive operations at higher priority
		if isSecuritySensitive(info.FullMethod) {
			al.Log(entry)
		} else {
			// Still log but only to zap (not audit file) for non-sensitive operations
			al.zapLogger.Debug("rpc",
				zap.String("request_id", requestID),
				zap.String("method", info.FullMethod),
				zap.String("duration", duration.String()),
			)
		}

		return resp, err
	}
}

func extractClientID(ctx context.Context) string {
	if user := UserFromContext(ctx); user != nil {
		return user.Subject
	}
	return "anonymous"
}

func extractPeerAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		return p.Addr.String()
	}
	return ""
}

func extractResourceFromRequest(method string, req interface{}) string {
	parts := strings.Split(method, "/")
	shortMethod := parts[len(parts)-1]

	switch shortMethod {
	case "LoadSession", "SaveSession", "DeleteSession":
		// Extract session name without importing proto types directly
		if r, ok := req.(interface{ GetName() string }); ok {
			return r.GetName()
		}
	case "ExecuteRemotePlugin", "DownloadPlugin":
		if r, ok := req.(interface{ GetPluginName() string }); ok {
			return r.GetPluginName()
		}
	case "GetAgentDefinition":
		if r, ok := req.(interface{ GetName() string }); ok {
			return r.GetName()
		}
	}
	return ""
}

func isSecuritySensitive(method string) bool {
	sensitive := []string{
		"SaveSession", "DeleteSession", "LoadSession", "ListSessions",
		"ExecuteRemotePlugin", "DownloadPlugin",
		"SendPrompt", "StreamPrompt", "InteractiveSession",
		"AnalyzeIssue",
	}
	for _, s := range sensitive {
		if strings.HasSuffix(method, "/"+s) {
			return true
		}
	}
	return true // default: audit everything
}

// sanitizeErrorForAudit removes potentially sensitive data from error messages.
func sanitizeErrorForAudit(msg string) string {
	// Truncate long errors
	if len(msg) > 500 {
		return msg[:500] + "...[truncated]"
	}
	return msg
}
