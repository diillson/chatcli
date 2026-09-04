/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/pkg/auditchain"
	"go.uber.org/zap"
)

func TestAuditLogger_JoinsTheSharedChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("CHATCLI_AUDIT_LOG_PATH", path)
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "")
	// An LLM auditor (another process in production) already writes here.
	llm, err := auditchain.Open(path, auditchain.Options{Seal: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	_ = llm.Append(map[string]string{"kind": "llm", "phase": "send"})

	al := NewAuditLogger(zap.NewNop())
	if al.chain == nil {
		t.Fatal("audit file must be open")
	}
	al.Log(AuditEntry{RequestID: "r1", ClientID: "c1", Method: "/chat", Result: "success"})
	_ = llm.Append(map[string]string{"kind": "llm", "phase": "recv"})
	al.Log(AuditEntry{RequestID: "r2", ClientID: "c1", Method: "/chat", Result: "denied"})
	al.Close()
	_ = llm.Close()

	rep, err := auditchain.Verify(path, nil)
	if err != nil || !rep.Intact() || rep.Chained != 4 {
		t.Fatalf("gRPC and LLM entries must form one chain: %+v err=%v", rep, err)
	}
}

func TestAuditLogger_RejectsRelativePath(t *testing.T) {
	t.Setenv("CHATCLI_AUDIT_LOG_PATH", "relative/audit.jsonl")
	al := NewAuditLogger(zap.NewNop())
	if al.chain != nil {
		t.Fatal("a relative audit path must not be opened")
	}
	al.Log(AuditEntry{RequestID: "r1"}) // zap only, no panic
	al.Close()
}
