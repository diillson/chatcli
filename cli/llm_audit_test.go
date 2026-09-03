/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

func TestLLMAudit_WritesOneLinePerPhase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(AuditLogPathEnv, path)
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "")

	cli := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker()}
	cli.initLLMAudit("test")
	if cli.llmAudit == nil {
		t.Fatal("audit writer not installed")
	}
	t.Cleanup(cli.llmAudit.close)

	before := redactionsTotal.Load()
	_ = redactSecretsWithMode("OPENAI_API_KEY="+strings.Repeat("k", 24), contentRedactPermissive)
	if redactionsTotal.Load() != before+1 {
		t.Fatal("a rewrite must bump the redaction counter")
	}
	_ = redactSecretsWithMode("plain text", contentRedactPermissive)
	if redactionsTotal.Load() != before+1 {
		t.Fatal("an untouched text must not bump the counter")
	}

	client.LogRequestStart(zap.NewNop(), "CLAUDEAI", "claude-sonnet-5", zap.Int("payload_bytes", 4096), zap.Int("cache_markers", 3))
	client.LogRequestFinish(zap.NewNop(), "CLAUDEAI", "claude-sonnet-5", "success", 120*time.Millisecond, zap.Int("prompt_tokens", 900))

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	defer func() { _ = f.Close() }()
	var lines []llmAuditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e llmAuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 audit lines, got %d", len(lines))
	}
	send, recv := lines[0], lines[1]
	if send.Kind != "llm" || send.Phase != "send" || send.Provider != "CLAUDEAI" || send.Surface != "test" ||
		send.Fields["payload_bytes"] != "4096" || send.Fields["cache_markers"] != "3" || send.Session == "" {
		t.Fatalf("send line = %+v", send)
	}
	if recv.Phase != "recv" || recv.Status != "success" || recv.DurationMS != 120 || recv.Fields["prompt_tokens"] != "900" ||
		recv.Redactions < before+1 {
		t.Fatalf("recv line = %+v", recv)
	}
	if strings.Contains(send.Timestamp, " ") || !strings.HasSuffix(send.Timestamp, "Z") {
		t.Fatalf("timestamp must be RFC3339 UTC: %q", send.Timestamp)
	}
}

func TestLLMAudit_RelativePathOrUnsetDisables(t *testing.T) {
	t.Setenv(AuditLogPathEnv, "relative/audit.jsonl")
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.initLLMAudit("test")
	if cli.llmAudit != nil {
		t.Fatal("relative path must not enable auditing")
	}
	t.Setenv(AuditLogPathEnv, "")
	cli.initLLMAudit("test")
	if cli.llmAudit != nil {
		t.Fatal("unset path must not enable auditing")
	}
	cli.llmAudit.close() // nil-safe
}
