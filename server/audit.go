/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/auditchain"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// AuditEntry is a structured audit log event for security-relevant operations.
type AuditEntry struct {
	Timestamp string `json:"timestamp"`
	Kind      string `json:"kind,omitempty"` // "grpc" (LLM entries in the same trail carry "llm")
	RequestID string `json:"request_id"`

	// Who did what, to what, from where. These four answer the question an
	// audit trail exists for, without the reader having to map a gRPC
	// method path back to an operation or a peer string back to a caller.
	Action string `json:"action,omitempty"` // short RPC name, e.g. "DeleteSession"
	Actor  string `json:"actor,omitempty"`  // "user:<subject>", or "anonymous"
	Role   string `json:"role,omitempty"`   // the actor's resolved role
	IP     string `json:"ip,omitempty"`     // caller host, without the ephemeral port

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
	mu        sync.Mutex
	zapLogger *zap.Logger
	chain     *auditchain.Writer
}

// NewAuditLogger creates an audit logger. If CHATCLI_AUDIT_LOG_PATH is set,
// audit entries are also written to that file in JSON-lines format.
func NewAuditLogger(logger *zap.Logger) *AuditLogger {
	al := &AuditLogger{
		zapLogger: logger.Named("audit"),
	}

	if path := os.Getenv("CHATCLI_AUDIT_LOG_PATH"); path != "" {
		cleanPath := filepath.Clean(path)
		if !filepath.IsAbs(cleanPath) {
			logger.Error("audit log path must be absolute; file audit disabled", zap.String("path", path))
			return al
		}
		// The same hash-chained, file-locked trail the LLM auditor writes:
		// gRPC entries interleave with LLM entries in one verifiable chain.
		w, err := auditchain.Open(cleanPath, auditchain.Options{})
		if err != nil {
			logger.Error("failed to open audit log file", zap.String("path", path), zap.Error(err))
		} else {
			al.chain = w
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
		zap.String("action", entry.Action),
		zap.String("actor", entry.Actor),
		zap.String("role", entry.Role),
		zap.String("ip", entry.IP),
		zap.String("client_id", entry.ClientID),
		zap.String("method", entry.Method),
		zap.String("resource", entry.Resource),
		zap.String("result", entry.Result),
		zap.String("duration", entry.Duration),
	)

	if al.chain != nil {
		if entry.Kind == "" {
			entry.Kind = "grpc"
		}
		al.mu.Lock()
		if err := al.chain.Append(entry); err != nil {
			al.zapLogger.Warn("audit log write failed", zap.Error(err))
		}
		al.mu.Unlock()
	}
}

// Close shuts down the audit file writer.
func (al *AuditLogger) Close() {
	if al.chain != nil {
		if err := al.chain.Close(); err != nil {
			al.zapLogger.Error("failed to close audit log file", zap.Error(err))
		}
	}
}

// UnaryInterceptor returns a gRPC interceptor that logs all unary RPCs for audit.
func (al *AuditLogger) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		requestID := uuid.New().String()

		// Inject request ID into context metadata for downstream handlers
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", requestID)
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))

		// The holder auth fills on the way in, so this outer layer can name
		// the caller on the way out instead of logging "anonymous" for
		// every authenticated call.
		ctx, outcome := withAuthOutcome(ctx)

		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		entry := auditEntryFor(ctx, outcome, requestID, info.FullMethod, duration)
		entry.Resource = extractResourceFromRequest(info.FullMethod, req)
		applyAuditResult(&entry, outcome, err)

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

// auditEntryFor builds the identity-bearing part of an audit entry.
func auditEntryFor(ctx context.Context, outcome *authOutcome, requestID, fullMethod string, duration time.Duration) AuditEntry {
	user, _ := outcome.snapshot()
	if user == nil {
		// A handler invoked without the wrapping interceptor (tests, or a
		// future chain that reorders) still audits with whatever identity
		// the context carries.
		user = UserFromContext(ctx)
	}

	entry := AuditEntry{
		RequestID:  requestID,
		Action:     shortMethodName(fullMethod),
		Actor:      "anonymous",
		ClientID:   "anonymous",
		ClientAddr: extractPeerAddr(ctx),
		IP:         extractPeerAddress(ctx),
		Method:     fullMethod,
		Duration:   duration.String(),
	}
	if user != nil && user.Subject != "" {
		entry.Actor = "user:" + user.Subject
		entry.ClientID = user.Subject
		entry.Role = string(user.Role)
	}
	return entry
}

// applyAuditResult records the outcome, distinguishing a call authentication
// refused from one the handler failed. Both are errors on the wire; only one
// of them is a security event.
func applyAuditResult(entry *AuditEntry, outcome *authOutcome, err error) {
	_, denied := outcome.snapshot()
	switch {
	case denied:
		entry.Result = "denied"
		entry.Details = map[string]string{"error": "authentication failed"}
	case err != nil:
		entry.Result = "error"
		entry.Details = map[string]string{"error": sanitizeErrorForAudit(err.Error())}
	default:
		entry.Result = "success"
	}
}

// shortMethodName reduces "/chatcli.v1.ChatCLIService/SendPrompt" to
// "SendPrompt".
func shortMethodName(fullMethod string) string {
	parts := strings.Split(fullMethod, "/")
	return parts[len(parts)-1]
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

// StreamInterceptor returns a gRPC stream interceptor that audits streaming
// RPCs.
//
// Without it the audit trail simply had no streaming half: StreamPrompt and
// InteractiveSession — the two RPCs that carry the actual conversation —
// left no line at all, while the unary calls around them did. An audit
// trail with a hole that shape reads as evidence of absence.
func (al *AuditLogger) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		requestID := uuid.New().String()

		ctx, outcome := withAuthOutcome(ss.Context())
		wrapped := &auditServerStream{ServerStream: ss, ctx: ctx}

		start := time.Now()
		err := handler(srv, wrapped)
		duration := time.Since(start)

		entry := auditEntryFor(ctx, outcome, requestID, info.FullMethod, duration)
		applyAuditResult(&entry, outcome, err)
		entry.Details = mergeAuditDetails(entry.Details, map[string]string{
			"stream":         "true",
			"messages_recvd": strconv.Itoa(wrapped.received()),
		})

		if isSecuritySensitive(info.FullMethod) {
			al.Log(entry)
		} else {
			al.zapLogger.Debug("rpc stream",
				zap.String("request_id", requestID),
				zap.String("method", info.FullMethod),
				zap.String("duration", duration.String()),
			)
		}

		return err
	}
}

// mergeAuditDetails adds fields to an entry's details without dropping
// whatever the result already recorded there.
func mergeAuditDetails(base, extra map[string]string) map[string]string {
	if base == nil {
		base = make(map[string]string, len(extra))
	}
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// auditServerStream carries the audit context inward and counts the
// messages the stream received, so the trail records the shape of a stream
// and not merely that one happened.
type auditServerStream struct {
	grpc.ServerStream
	ctx   context.Context
	mu    sync.Mutex
	count int
}

func (a *auditServerStream) Context() context.Context { return a.ctx }

func (a *auditServerStream) RecvMsg(m interface{}) error {
	err := a.ServerStream.RecvMsg(m)
	if err == nil {
		a.mu.Lock()
		a.count++
		a.mu.Unlock()
	}
	return err
}

func (a *auditServerStream) received() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.count
}
