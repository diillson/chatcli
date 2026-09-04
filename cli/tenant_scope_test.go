/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestTenantRootFor_SafeAndDistinct(t *testing.T) {
	a := tenantRootFor("/base", "telegram:12345")
	b := tenantRootFor("/base", "telegram/12345")
	if a == b {
		t.Fatal("principals that sanitize alike must still get distinct roots")
	}
	if !strings.HasPrefix(a, filepath.Join("/base", "tenants", "telegram_12345-")) {
		t.Fatalf("root = %s", a)
	}
	if strings.ContainsAny(filepath.Base(a), ":/\\ ") {
		t.Fatalf("unsafe characters in %s", a)
	}
	long := tenantRootFor("/base", strings.Repeat("x", 200))
	// 48-char slug + "-" + 16-byte digest as hex (32 chars).
	if len(filepath.Base(long)) > 48+1+32 {
		t.Fatalf("slug not capped: %s", filepath.Base(long))
	}
}

func newTenantTestCLI(t *testing.T) *ChatCLI {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv(GatewayMaxTenantsEnv, "")
	t.Setenv("CHATCLI_SESSION_TRANSCRIPT", "false")
	sm, err := NewSessionManagerAt(filepath.Join(root, "sessions"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return &ChatCLI{
		logger:           zap.NewNop(),
		stateRoot:        root,
		sessionManager:   sm,
		historyCompactor: NewHistoryCompactor(zap.NewNop()),
		costTracker:      NewCostTracker(),
		history:          []models.Message{{Role: "user", Content: "base turn"}},
	}
}

func TestEnterTenant_SwapsStoresAndRestoresBase(t *testing.T) {
	cli := newTenantTestCLI(t)
	baseSM := cli.sessionManager
	ctx := context.Background()

	if leave := cli.enterTenant(ctx, defaultHubPrincipal); leave != nil {
		t.Fatal("shared principal must be a no-op")
	}
	leave := cli.enterTenant(ctx, "telegram:42")
	if leave == nil {
		t.Fatal("tenant must be entered")
	}
	if cli.sessionManager == baseSM || !strings.Contains(cli.sessionManager.sessionsDir, filepath.Join("tenants", "telegram_42-")) {
		t.Fatalf("tenant session store not installed: %s", cli.sessionManager.sessionsDir)
	}
	if len(cli.history) != 0 || cli.activeTenant() != "telegram:42" {
		t.Fatalf("tenant must start with its own empty history, got %d (active=%q)", len(cli.history), cli.activeTenant())
	}
	if cli.contextHandler == nil || cli.costTracker == baseTracker(t, cli) {
		t.Fatal("tenant contexts/costs not installed")
	}
	cli.history = append(cli.history, models.Message{Role: "user", Content: "tenant turn"})
	leave()

	if cli.sessionManager != baseSM || cli.activeTenant() != "" {
		t.Fatal("base stores must be restored on leave")
	}
	if len(cli.history) != 1 || cli.history[0].Content != "base turn" {
		t.Fatalf("base history must be restored, got %+v", cli.history)
	}

	// Re-entering resumes the tenant's own conversation.
	leave = cli.enterTenant(ctx, "telegram:42")
	if len(cli.history) != 1 || cli.history[0].Content != "tenant turn" {
		t.Fatalf("tenant history must persist across turns, got %+v", cli.history)
	}
	leave()
}

// baseTracker returns the pool's base cost tracker (nil when no pool).
func baseTracker(t *testing.T, cli *ChatCLI) *CostTracker {
	t.Helper()
	if cli.tenants == nil || cli.tenants.base == nil {
		return nil
	}
	return cli.tenants.base.costTracker
}

func TestEnterTenant_EvictsLeastRecentlyUsed(t *testing.T) {
	cli := newTenantTestCLI(t)
	t.Setenv(GatewayMaxTenantsEnv, "1")
	ctx := context.Background()
	cli.enterTenant(ctx, "slack:a")()
	cli.enterTenant(ctx, "slack:b")()
	if n := len(cli.tenants.items); n != 1 {
		t.Fatalf("cap 1 must keep one resident set, got %d", n)
	}
	if _, ok := cli.tenants.items["slack:b"]; !ok {
		t.Fatal("the most recent tenant must survive eviction")
	}
	// Evicted tenants are rebuilt from disk on return — its root exists.
	leave := cli.enterTenant(ctx, "slack:a")
	if !strings.Contains(cli.sessionManager.sessionsDir, "slack_a-") {
		t.Fatalf("rebuilt tenant store = %s", cli.sessionManager.sessionsDir)
	}
	leave()
}

func TestTenantRootFor_KeepsLegacyDigestRoots(t *testing.T) {
	base := t.TempDir()
	fresh := tenantRootFor(base, "slack:u1")
	if len(filepath.Base(fresh)) != len("slack_u1-")+32 {
		t.Fatalf("new roots carry the 16-byte digest: %s", filepath.Base(fresh))
	}
	// A root created by an earlier build (32-bit digest) keeps being used
	// so an upgrade never orphans a tenant's state.
	legacy := filepath.Join(base, "tenants", "slack_u1-"+filepath.Base(fresh)[len("slack_u1-"):len("slack_u1-")+8])
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := tenantRootFor(base, "slack:u1"); got != legacy {
		t.Fatalf("legacy root must win while it exists: %s", got)
	}
}
