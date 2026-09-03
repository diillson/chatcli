/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestSessionTTLDuration(t *testing.T) {
	t.Setenv("CHATCLI_SESSION_TTL", "")
	if got := sessionTTLDuration(); got != 90*24*time.Hour {
		t.Fatalf("default = %s", got)
	}
	t.Setenv("CHATCLI_SESSION_TTL", "30d")
	if got := sessionTTLDuration(); got != 30*24*time.Hour {
		t.Fatalf("30d = %s", got)
	}
	t.Setenv("CHATCLI_SESSION_TTL", "0")
	if got := sessionTTLDuration(); got != 0 {
		t.Fatalf("0 must disable, got %s", got)
	}
}

func TestRunRetentionPass_SweepsStaleParksAndCosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CHATCLI_PARK_DIR", filepath.Join(home, "parks"))
	t.Setenv("CHATCLI_SESSION_TTL", "")
	if err := os.MkdirAll(filepath.Join(home, "parks"), 0o700); err != nil {
		t.Fatal(err)
	}

	stale := &park.Snapshot{Token: park.NewToken(), CreatedAt: time.Now().Add(-100 * 24 * time.Hour),
		History: []models.Message{{Role: "user", Content: "old"}}}
	fresh := &park.Snapshot{Token: park.NewToken(), History: []models.Message{{Role: "user", Content: "new"}}}
	if err := stale.Save(); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	if err := fresh.Save(); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	costDir := costStoreDir()
	if err := os.MkdirAll(costDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldCost := filepath.Join(costDir, "old.json")
	newCost := filepath.Join(costDir, "new.json")
	_ = os.WriteFile(oldCost, []byte("{}"), 0o600)
	_ = os.WriteFile(newCost, []byte("{}"), 0o600)
	past := time.Now().Add(-100 * 24 * time.Hour)
	_ = os.Chtimes(oldCost, past, past)

	cli := &ChatCLI{logger: zap.NewNop()}
	rep := cli.runRetentionPass()
	if rep.Parks != 1 || rep.Costs != 1 {
		t.Fatalf("report = %+v, want 1 park and 1 cost snapshot removed", rep)
	}
	if _, err := park.Load(fresh.Token); err != nil {
		t.Fatalf("fresh park must survive: %v", err)
	}
	if _, err := park.Load(stale.Token); err == nil {
		t.Fatal("stale park must be gone")
	}
	if _, err := os.Stat(newCost); err != nil {
		t.Fatal("fresh cost snapshot must survive")
	}

	// TTL 0 keeps everything.
	t.Setenv("CHATCLI_SESSION_TTL", "0")
	_ = os.WriteFile(oldCost, []byte("{}"), 0o600)
	_ = os.Chtimes(oldCost, past, past)
	if rep := cli.runRetentionPass(); rep.Parks != 0 || rep.Costs != 0 {
		t.Fatalf("ttl 0 must sweep nothing, got %+v", rep)
	}
	cli.showConfigRetention() // renders without panicking
}
