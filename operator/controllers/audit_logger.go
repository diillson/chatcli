/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// OperatorAuditEntry represents a structured audit log entry for operator actions.
type OperatorAuditEntry struct {
	Timestamp         string            `json:"timestamp"`
	Actor             string            `json:"actor"`
	ActorType         string            `json:"actor_type,omitempty"` // "controller", "api", "webhook"
	Action            string            `json:"action"`
	Resource          string            `json:"resource"`
	ResourceKind      string            `json:"resource_kind,omitempty"`
	ResourceNamespace string            `json:"resource_namespace,omitempty"`
	Result            string            `json:"result"` // "success", "error", "denied", "expired"
	Details           map[string]string `json:"details,omitempty"`
	CorrelationID     string            `json:"correlation_id,omitempty"`
	Severity          string            `json:"severity,omitempty"` // "info", "warn", "critical"
}

// OperatorAuditLogger provides structured audit logging for the K8s operator.
type OperatorAuditLogger struct {
	mu         sync.Mutex
	zapLogger  *zap.Logger
	fileWriter io.WriteCloser
	encoder    *json.Encoder
}

// NewOperatorAuditLogger creates an audit logger. If CHATCLI_AUDIT_LOG_PATH env is set,
// entries are also written to that file in JSON-lines format.
func NewOperatorAuditLogger(zapLogger *zap.Logger) *OperatorAuditLogger {
	al := &OperatorAuditLogger{
		zapLogger: zapLogger.Named("audit"),
	}

	if path := os.Getenv("CHATCLI_AUDIT_LOG_PATH"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			zapLogger.Error("failed to open operator audit log file", zap.String("path", path), zap.Error(err))
		} else {
			al.fileWriter = f
			al.encoder = json.NewEncoder(f)
		}
	}

	return al
}

// Log writes an audit entry.
func (al *OperatorAuditLogger) Log(entry OperatorAuditEntry) {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	al.zapLogger.Info("audit",
		zap.String("actor", entry.Actor),
		zap.String("action", entry.Action),
		zap.String("resource", entry.Resource),
		zap.String("resource_kind", entry.ResourceKind),
		zap.String("resource_namespace", entry.ResourceNamespace),
		zap.String("result", entry.Result),
		zap.String("severity", entry.Severity),
	)

	if al.encoder != nil {
		al.mu.Lock()
		_ = al.encoder.Encode(entry)
		al.mu.Unlock()
	}
}

// LogApproval records an approval decision.
func (al *OperatorAuditLogger) LogApproval(name, namespace, decision, actor string, details map[string]string) {
	al.Log(OperatorAuditEntry{
		Actor:             actor,
		ActorType:         "api",
		Action:            "approval_" + decision,
		Resource:          name,
		ResourceKind:      "ApprovalRequest",
		ResourceNamespace: namespace,
		Result:            decision,
		Details:           details,
		Severity:          "info",
	})
}

// LogRemediation records a remediation execution.
func (al *OperatorAuditLogger) LogRemediation(name, namespace, action, result string, details map[string]string) {
	severity := "info"
	if result != "success" {
		severity = "warn"
	}
	al.Log(OperatorAuditEntry{
		Actor:             "remediation-controller",
		ActorType:         "controller",
		Action:            "remediation_" + action,
		Resource:          name,
		ResourceKind:      "RemediationPlan",
		ResourceNamespace: namespace,
		Result:            result,
		Details:           details,
		Severity:          severity,
	})
}

// LogRBACChange records an RBAC-related operation.
func (al *OperatorAuditLogger) LogRBACChange(kind, name, namespace, action, actor string) {
	al.Log(OperatorAuditEntry{
		Actor:             actor,
		ActorType:         "controller",
		Action:            "rbac_" + action,
		Resource:          name,
		ResourceKind:      kind,
		ResourceNamespace: namespace,
		Result:            "success",
		Severity:          "critical",
	})
}

// LogAPIAccess records an API access event.
func (al *OperatorAuditLogger) LogAPIAccess(method, path, clientIP, role, result string) {
	al.Log(OperatorAuditEntry{
		Actor:     clientIP,
		ActorType: "api",
		Action:    method + " " + path,
		Resource:  path,
		Result:    result,
		Details:   map[string]string{"role": role},
		Severity:  "info",
	})
}

// Close shuts down the file writer.
func (al *OperatorAuditLogger) Close() {
	if al.fileWriter != nil {
		al.fileWriter.Close()
	}
}
