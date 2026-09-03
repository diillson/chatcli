/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

type usageAwareStub struct{ u *models.UsageInfo }

func (s *usageAwareStub) GetModelName() string { return "stub" }
func (s *usageAwareStub) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	return "", nil
}
func (s *usageAwareStub) LastUsage() *models.UsageInfo { return s.u }

func TestLLMAudit_SurfaceTenantAndTokensOnRecv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(AuditLogPathEnv, path)
	cli := newTenantTestCLI(t)
	cli.Client = &usageAwareStub{u: &models.UsageInfo{PromptTokens: 900, CompletionTokens: 40, CacheReadInputTokens: 600, CacheCreationInputTokens: 100, IsReal: true}}
	cli.initLLMAudit("repl")
	t.Cleanup(func() { cli.llmAudit.close() })
	cli.SetAuditSurface("gateway")
	client.LogRequestStart(zap.NewNop(), "CLAUDEAI", "claude-sonnet-5")
	client.LogRequestFinish(zap.NewNop(), "CLAUDEAI", "claude-sonnet-5", "success", 50*time.Millisecond)
	cli.llmAudit.close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var entries []llmAuditEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e llmAuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 2 || entries[0].Surface != "gateway" || entries[1].Surface != "gateway" {
		t.Fatalf("surface must follow SetAuditSurface: %+v", entries)
	}
	recv := entries[1]
	if recv.InputTokens != 900 || recv.OutputTokens != 40 || recv.CacheReadTokens != 600 || recv.CacheWriteTokens != 100 {
		t.Fatalf("recv line must carry the provider usage: %+v", recv)
	}
	if entries[0].InputTokens != 0 {
		t.Fatal("send lines carry no usage")
	}
}

func TestExternalMemoryStore_RedactsSecrets(t *testing.T) {
	cli := newTenantTestCLI(t)
	f := &fakeToolCaller{answers: map[string]string{}, fail: map[string]bool{}}
	cli.extToolCaller = f
	t.Setenv(MemoryProviderEnv, "mcp:memsvc")
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "")
	secret := "ghp_" + strings.Repeat("b", 36)
	cli.externalMemoryStore(context.Background(), "s", []models.Message{{Role: "user", Content: "token is " + secret}})
	waitCalls(t, f, "memsvc/memory_store", 1)
	msgs, _ := f.args[0]["messages"].([]map[string]string)
	if len(msgs) != 1 || strings.Contains(msgs[0]["content"], secret) {
		t.Fatalf("secrets must be redacted before leaving the process: %v", f.args[0])
	}
}

func TestEnterTenantChecked_RefusesWhenTheStoreSetCannotBeBuilt(t *testing.T) {
	cli := newTenantTestCLI(t)
	blocker := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli.stateRoot = blocker // MkdirAll under a file fails
	leave, err := cli.enterTenantChecked(context.Background(), "slack:x")
	if err == nil || leave != nil {
		t.Fatalf("a principal without a store set must be refused, got leave=%v err=%v", leave != nil, err)
	}
	if cli.activeTenant() != "" {
		t.Fatal("the shared set must stay installed")
	}
}

func TestRetentionPass_CoversTenantRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CHATCLI_SESSION_TTL", "1d")
	cli := newTenantTestCLI(t)
	cli.stateRoot = root
	tenant := filepath.Join(root, "tenants", "slack_a-deadbeef")
	costs := filepath.Join(tenant, "costs")
	if err := os.MkdirAll(costs, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(costs, "old-session.json")
	if err := os.WriteFile(old, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if roots := tenantRoots(root); len(roots) != 1 || roots[0] != tenant {
		t.Fatalf("tenantRoots = %v", roots)
	}
	rep := cli.runRetentionPass()
	if _, err := os.Stat(old); !os.IsNotExist(err) || rep.Costs < 1 {
		t.Fatalf("stale tenant cost snapshot must be pruned: rep=%+v err=%v", rep, err)
	}
}
