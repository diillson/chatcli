/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/models"
)

func TestTenantPaths_ParkAndTaskGraphFollowTheTenantRoot(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	t.Cleanup(func() { park.SetBaseDir("") })
	global := t.TempDir()
	cli := &ChatCLI{stateRoot: global}
	if cli.tenantRootActive() {
		t.Fatal("global root is not a tenant root")
	}
	cli.applyTenantPaths()
	if dir, _ := park.Dir(); dir != filepath.Clean(os.Getenv("CHATCLI_PARK_DIR")) {
		t.Fatalf("global set keeps the default park resolution: %s", dir)
	}
	tenant := filepath.Join(global, "tenants", "acme")
	cli.stateRoot = tenant
	if !cli.tenantRootActive() {
		t.Fatal("tenant root must be detected")
	}
	cli.applyTenantPaths()
	dir, err := park.Dir()
	if err != nil || dir != filepath.Join(tenant, "parked") {
		t.Fatalf("park dir must follow the tenant: %s %v", dir, err)
	}
	if base, err := cli.taskGraphBaseDir(); err != nil || base != filepath.Join(tenant, "taskgraph") {
		t.Fatalf("taskgraph base must follow the tenant: %s %v", base, err)
	}
	if a := newTaskGraphAdapter(cli, nil); a.baseDirFn == nil {
		t.Fatal("adapter must resolve through the CLI")
	}
	cli.stateRoot = global
	cli.applyTenantPaths()
	if base, _ := cli.taskGraphBaseDir(); strings.Contains(base, "tenants") {
		t.Fatal("back on the global set, taskgraph uses the default base")
	}
}

func TestDailyBudget_AccruesPersistsAndGates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHATCLI_DAILY_BUDGET_USD", "1.00")
	t.Setenv("CHATCLI_BUDGET_HARD_STOP", "true")
	t.Setenv("CHATCLI_SESSION_BUDGET_USD", "")
	ct := NewCostTrackerAt(dir)
	if spent, limit := ct.DailyBudget(); spent != 0 || limit != 1 {
		t.Fatalf("fresh: %v %v", spent, limit)
	}
	// 100K prompt tokens on a priced model → well under the cap.
	ct.RecordRealUsage("openai", "gpt-5.6-terra", &models.UsageInfo{PromptTokens: 100_000, CompletionTokens: 1000, TotalTokens: 101_000, IsReal: true})
	spent, _ := ct.DailyBudget()
	if spent <= 0 || ct.CheckBudget() == BudgetExceeded || ct.BudgetBlocked() {
		t.Fatalf("spend must accrue without exceeding: %v level=%v", spent, ct.CheckBudget())
	}
	ct.FlushDailySpend()
	// A second session the same day starts from the persisted spend.
	next := NewCostTrackerAt(dir)
	if s2, _ := next.DailyBudget(); s2 < spent-1e-9 {
		t.Fatalf("persisted daily spend must reload: %v < %v", s2, spent)
	}
	// Push the day over the cap: hard stop engages even though the session
	// budget is unset.
	next.RecordRealUsage("openai", "gpt-5.6-terra", &models.UsageInfo{PromptTokens: 5_000_000, CompletionTokens: 10, TotalTokens: 5_000_010, IsReal: true})
	if next.CheckBudget() != BudgetExceeded || !next.BudgetBlocked() {
		t.Fatalf("daily cap must gate: level=%v blocked=%v", next.CheckBudget(), next.BudgetBlocked())
	}
	// Reload without a daily budget: gate lifts.
	t.Setenv("CHATCLI_DAILY_BUDGET_USD", "")
	next.ReloadBudget()
	if next.BudgetBlocked() {
		t.Fatal("no daily budget → no daily gate")
	}
	// Yesterday's file is ignored.
	_ = os.WriteFile(filepath.Join(dir, dailySpendFile), []byte(`{"date":"`+time.Now().AddDate(0, 0, -1).Format("2006-01-02")+`","spent_usd":50}`), 0o600)
	t.Setenv("CHATCLI_DAILY_BUDGET_USD", "1.00")
	if s3, _ := NewCostTrackerAt(dir).DailyBudget(); s3 != 0 {
		t.Fatalf("an older day must not carry over: %v", s3)
	}
	var nilTracker *CostTracker
	nilTracker.FlushDailySpend()
}
