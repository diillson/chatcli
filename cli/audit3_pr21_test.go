/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestCostTrackerReset_ClearsEveryAggregate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	t.Cleanup(ct.FlushDailySpend)
	ct.RecordUsage("OPENAI", "gpt-5.6", 1000, 100)
	ct.mu.Lock()
	ct.cacheStorageUSD, ct.embeddingCostUSD, ct.compactionCostUSD, ct.memoryCostUSD = 1, 1, 1, 1
	ct.cacheResources, ct.memoryCalls = 3, 2
	ct.mu.Unlock()
	ct.RecordContextEdits(2, 500)
	ct.Reset()
	if ct.TotalCost() != 0 {
		t.Fatalf("total after reset = %v", ct.TotalCost())
	}
	snap := ct.Snapshot()
	if snap.CacheStorageCostUSD != 0 || snap.EmbeddingCostUSD != 0 || snap.MemoryCostUSD != 0 || snap.CacheResources != 0 {
		t.Fatalf("aggregates survived the reset: %+v", snap)
	}
	if e, _, _ := ct.ContextEditStats(); e != 0 {
		t.Fatal("context edit stats survived the reset")
	}
}

func TestBudgetMessage_DailyOnlyAndDailyTrippedFirst(t *testing.T) {
	ct := NewCostTracker()
	ct.mu.Lock()
	ct.budgetLimitUSD, ct.dailyLimitUSD, ct.budgetWarningPct = 0, 1.0, 0.8
	ct.dailySpentUSD = 0.9
	msg := ct.budgetMessageLocked()
	ct.mu.Unlock()
	if msg == "" || !strings.Contains(msg, "0.9") {
		t.Fatalf("a daily-only budget must announce its own warning: %q", msg)
	}
	ct.mu.Lock()
	ct.dailySpentUSD = 1.2
	exceeded := ct.budgetMessageLocked()
	// Both limits set, only the daily one tripped: the daily message wins.
	ct.budgetLimitUSD, ct.totalCostUSD = 100, 1.2
	both := ct.budgetMessageLocked()
	// Session limit set and daily far from its threshold: session message.
	ct.dailySpentUSD, ct.totalCostUSD = 0.1, 95
	session := ct.budgetMessageLocked()
	ct.mu.Unlock()
	if !strings.Contains(exceeded, "1.2") || !strings.Contains(both, "1.2") || !strings.Contains(both, "1.00") {
		t.Fatalf("daily exceeded: %q / %q", exceeded, both)
	}
	if !strings.Contains(session, "100") {
		t.Fatalf("session message when the daily limit is quiet: %q", session)
	}
}

func TestCompactSummarizerClient_FollowsTheEnvValue(t *testing.T) {
	c := newRPCChatCLI(t, &rpcChatFakeClient{reply: "ok"})
	t.Setenv(CompactModelEnv, "")
	if c.compactSummarizerClient() != nil {
		t.Fatal("no handle → nil")
	}
	// An unusable handle is not pinned: the next call re-resolves.
	t.Setenv(CompactModelEnv, "nope:not-a-model")
	_ = c.compactSummarizerClient()
	if c.compactSummarizerHandle != "" {
		t.Fatal("a failed resolution must not be cached")
	}
	t.Setenv(CompactModelEnv, "")
	if c.compactSummarizerClient() != nil {
		t.Fatal("cleared handle → nil again")
	}
}

func TestInitLLMAudit_LockedManagedPathFailsClosed(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed.env")
	unwritable := filepath.Join(dir, "missing-dir", "sub", "audit.jsonl")
	if err := os.WriteFile(managed, []byte("!CHATCLI_AUDIT_LOG_PATH="+unwritable+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_MANAGED_CONFIG", managed)
	config.ApplyManaged()
	t.Setenv(AuditLogPathEnv, unwritable)
	_ = os.MkdirAll(filepath.Dir(unwritable), 0o700)
	_ = os.Chmod(filepath.Dir(filepath.Dir(unwritable)), 0o500)
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(filepath.Dir(unwritable)), 0o700) })
	c := &ChatCLI{logger: zap.NewNop()}
	// Relative path under a locked policy: refused.
	t.Setenv(AuditLogPathEnv, "relative/audit.jsonl")
	if err := c.initLLMAuditErr("test"); !auditPathLocked() || !errors.Is(err, errAuditLocked) {
		t.Fatalf("locked relative path must fail closed: locked=%v err=%v", auditPathLocked(), err)
	}
	// Unlocked policy: a bad path only disables auditing.
	if err := os.WriteFile(managed, []byte("CHATCLI_AUDIT_LOG_PATH="+unwritable+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.ApplyManaged()
	if err := c.initLLMAuditErr("test"); err != nil {
		t.Fatalf("unlocked bad path must not block the session: %v", err)
	}
	_ = models.Message{}
}
