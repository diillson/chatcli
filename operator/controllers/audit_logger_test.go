/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewOperatorAuditLogger_RelativePathRejected(t *testing.T) {
	t.Setenv("CHATCLI_AUDIT_LOG_PATH", filepath.Join("relative", "audit.log"))

	core, logs := observer.New(zapcore.ErrorLevel)
	al := NewOperatorAuditLogger(zap.New(core))

	if al.fileWriter != nil || al.encoder != nil {
		t.Error("relative path must not open a file writer")
	}
	if logs.FilterMessageSnippet("must be an absolute path").Len() != 1 {
		t.Errorf("expected rejection to be logged, got %v", logs.All())
	}
	// The logger itself must stay usable for the zap sink.
	al.Log(OperatorAuditEntry{Action: "noop", Resource: "r", Result: "success"})
	al.Close()
}

func TestNewOperatorAuditLogger_AbsolutePathWritesEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	t.Setenv("CHATCLI_AUDIT_LOG_PATH", path)

	al := NewOperatorAuditLogger(zap.NewNop())
	if al.fileWriter == nil || al.encoder == nil {
		t.Fatal("absolute path must enable the file writer")
	}

	al.Log(OperatorAuditEntry{
		Actor:    "controller",
		Action:   "scale_deployment",
		Resource: "default/web",
		Result:   "success",
	})
	al.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	var entry OperatorAuditEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil {
		t.Fatalf("audit log is not JSON-lines: %v\n%s", err, data)
	}
	if entry.Action != "scale_deployment" || entry.Timestamp == "" {
		t.Errorf("unexpected entry: %+v", entry)
	}
}

func TestNewOperatorAuditLogger_OpenFailureIsNonFatal(t *testing.T) {
	// Absolute path inside a directory that does not exist: OpenFile
	// fails, but the constructor must still return a usable logger.
	t.Setenv("CHATCLI_AUDIT_LOG_PATH", filepath.Join(t.TempDir(), "missing", "audit.log"))

	core, logs := observer.New(zapcore.ErrorLevel)
	al := NewOperatorAuditLogger(zap.New(core))

	if al.fileWriter != nil {
		t.Error("open failure must leave the file writer unset")
	}
	if logs.FilterMessageSnippet("failed to open").Len() != 1 {
		t.Errorf("expected open failure to be logged, got %v", logs.All())
	}
}

func TestOperatorAuditLoggerClose_NoWriter(t *testing.T) {
	t.Setenv("CHATCLI_AUDIT_LOG_PATH", "")
	al := NewOperatorAuditLogger(zap.NewNop())
	// Must be a no-op, not a nil dereference.
	al.Close()
}

type failingWriteCloser struct{}

func (failingWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (failingWriteCloser) Close() error                { return fmt.Errorf("disk gone") }

func TestOperatorAuditLoggerClose_FlushErrorIsLogged(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	al := &OperatorAuditLogger{
		zapLogger:  zap.New(core),
		fileWriter: failingWriteCloser{},
	}

	al.Close()

	if logs.FilterMessageSnippet("failed to close operator audit log file").Len() != 1 {
		t.Errorf("expected close error to be logged, got %v", logs.All())
	}
}
