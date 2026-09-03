/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func TestAtRest_CoversMemoryContextsCCRAndCosts(t *testing.T) {
	t.Setenv(atrest.EnvKey, "coverage-key")
	root := t.TempDir()

	// Memory JSON stores are sealed and reload.
	mem := memory.NewManager(filepath.Join(root, "memory"), memory.DefaultConfig(), zap.NewNop())
	mem.Facts.AddFact("the deploy freeze ends on friday", "project", nil)
	mem.Profile.Update(map[string]string{"name": "Dev"})
	raw, _ := os.ReadFile(filepath.Join(root, "memory", "memory_index.json"))
	if !atrest.IsEncrypted(raw) || strings.Contains(string(raw), "freeze") {
		t.Fatal("facts store must be sealed on disk")
	}
	again := memory.NewManager(filepath.Join(root, "memory"), memory.DefaultConfig(), zap.NewNop())
	if again.Facts.Count() != 1 || again.Profile.Get().Name != "Dev" {
		t.Fatal("sealed memory stores must reload")
	}

	// Knowledge contexts (NewStorage roots itself at $HOME/.chatcli/contexts).
	t.Setenv("HOME", root)
	st, err := ctxmgr.NewStorage(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	fc := &ctxmgr.FileContext{ID: "c1", Name: "kb", Mode: ctxmgr.ModeKnowledge, Files: []utils.FileInfo{{Path: "a.md", Content: "secret corpus", Size: 13}}}
	if err := st.SaveContext(fc); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(root, ".chatcli", "contexts", "c1.json"))
	if !atrest.IsEncrypted(raw) {
		t.Fatal("context files must be sealed")
	}
	if loaded, err := st.LoadContext("c1"); err != nil || loaded.Files[0].Content != "secret corpus" {
		t.Fatalf("sealed context must load: %v", err)
	}
	export := filepath.Join(root, "kb-export.json")
	if err := st.ExportContext(fc, export); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(export); atrest.IsEncrypted(raw) {
		t.Fatal("exports stay plaintext")
	}

	// CCR archive.
	disk, err := compress.NewDiskStore(filepath.Join(root, "ccr"), 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	key, err := disk.Put("archived tool output")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "ccr"))
	if len(entries) == 0 {
		t.Fatal("ccr file expected")
	}
	raw, _ = os.ReadFile(filepath.Join(root, "ccr", entries[0].Name()))
	if !atrest.IsEncrypted(raw) {
		t.Fatal("ccr entries must be sealed")
	}
	fresh, _ := compress.NewDiskStore(filepath.Join(root, "ccr"), 1<<20, 0)
	if got, ok, err := fresh.Get(key); err != nil || !ok || got != "archived tool output" {
		t.Fatalf("sealed ccr entry must read back: %v %v %q", err, ok, got)
	}

	// Cost snapshots.
	ct := NewCostTrackerAt(filepath.Join(root, "costs"))
	ct.RecordRealUsage("openai", "gpt-5.6-terra", &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100, IsReal: true})
	if err := ct.SaveSession(); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(filepath.Join(root, "costs"))
	raw, _ = os.ReadFile(filepath.Join(root, "costs", files[0].Name()))
	if !atrest.IsEncrypted(raw) {
		t.Fatal("cost snapshots must be sealed")
	}
	if snap, err := loadCostSnapshotFrom(filepath.Join(root, "costs", files[0].Name())); err != nil || snap.TotalTokens == 0 {
		t.Fatalf("sealed cost snapshot must load: %v", err)
	}
}

// loadCostSnapshotFrom reads one snapshot file through the at-rest opener.
func loadCostSnapshotFrom(path string) (*SessionCostData, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if b, err = atrest.Open(b); err != nil {
		return nil, err
	}
	var data SessionCostData
	return &data, json.Unmarshal(b, &data)
}

func TestAuditChain_VerifiesAndDetectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(AuditLogPathEnv, path)
	cli := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker()}
	cli.initLLMAudit("test")
	if cli.llmAudit == nil {
		t.Fatal("audit must be on")
	}
	for i := 0; i < 3; i++ {
		cli.llmAudit.record(client.RequestAuditEvent{Phase: "send", Provider: "openai", Model: "gpt-5.6-terra", Status: "ok"})
	}
	cli.llmAudit.close()
	rep, err := VerifyAuditChain(path)
	if err != nil || !rep.Intact() || rep.Chained != 3 || rep.Entries != 3 {
		t.Fatalf("intact chain expected: %v %+v", err, rep)
	}
	// A restarted process continues the chain.
	cli.initLLMAudit("test")
	cli.llmAudit.record(client.RequestAuditEvent{Phase: "send", Provider: "openai", Model: "gpt-5.6-terra", Status: "ok"})
	cli.llmAudit.close()
	if rep, _ := VerifyAuditChain(path); !rep.Intact() || rep.Chained != 4 {
		t.Fatalf("restart must continue the chain: %+v", rep)
	}
	// Tamper with the second line.
	lines := strings.Split(strings.TrimSpace(readFileString(t, path)), "\n")
	lines[1] = strings.Replace(lines[1], `"status":"ok"`, `"status":"edited"`, 1)
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	if rep, _ := VerifyAuditChain(path); rep.Intact() || rep.BrokenAt != 2 {
		t.Fatalf("tampering must break the chain at line 2: %+v", rep)
	}
	// Dropping a line breaks the link too.
	_ = os.WriteFile(path, []byte(lines[0]+"\n"+lines[2]+"\n"), 0o600)
	if rep, _ := VerifyAuditChain(path); rep.Intact() || rep.BrokenAt != 2 {
		t.Fatalf("a removed line must break the link: %+v", rep)
	}
	cli.configSecurityVerifyAudit(nil) // prints, no panic
}

func TestResealWalk_SealsPlaintextStores(t *testing.T) {
	root := t.TempDir()
	t.Setenv(atrest.EnvKey, "")
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root, costTracker: NewCostTracker()}
	for _, rel := range []string{"sessions/a.json", "memory/memory_index.json", "contexts/c.json", "ccr/ab.ccr", "costs/s.json"} {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte(`{"plain":true}`), 0o600)
	}
	_ = os.MkdirAll(filepath.Join(root, "transcripts"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "transcripts", "t.jsonl"), []byte("{\"kind\":\"msg\"}\n"), 0o600)
	cli.configSecurityReseal() // no key: message only
	t.Setenv(atrest.EnvKey, "walk-key")
	rep := cli.resealAtRestStores()
	if rep.Rewritten != 5 || len(rep.Errors) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "memory", "memory_index.json"))
	if !atrest.IsEncrypted(raw) {
		t.Fatal("plaintext store must be sealed by the walk")
	}
	cli.routeConfigSecurity([]string{"reseal"})
	cli.routeConfigSecurity([]string{"verify-audit", filepath.Join(root, "nope")})
}

func readFileString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
