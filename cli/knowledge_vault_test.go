/*
 * ChatCLI - Tests for the Obsidian vault projection (knowledge_vault.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/pkg/knowledge"
	"github.com/diillson/chatcli/pkg/vault"
	"go.uber.org/zap"
)

var wikilinkTargetRe = regexp.MustCompile(`\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]`)

func syncFixtureVault(t *testing.T) (*ChatCLI, string, vault.Report) {
	cli, dir, rep, _ := syncFixtureVaultG(t)
	return cli, dir, rep
}

func syncFixtureVaultG(t *testing.T) (*ChatCLI, string, vault.Report, *knowledge.Graph) {
	t.Helper()
	cli := richGraphFixture(t)
	m := cli.memoryStore.Manager()
	m.Profile.Update(map[string]string{"location": "Sample City", "salario": "123", "sensitive_mark": "location"})
	if err := m.WriteDailyNote("Paired on the Orbital gateway rollout."); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "vault")
	t.Setenv("CHATCLI_MEMORY_VAULT_DIR", dir)
	s := newVaultSyncer(cli.graphSources(), cli.vaultDir(), zap.NewNop())
	rep, err := s.run(false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return cli, dir, rep, cli.knowledgeGraph()
}

func TestVault_EndToEnd(t *testing.T) {
	_, dir, rep, g := syncFixtureVaultG(t)
	if rep.Written < 10 {
		t.Fatalf("vault too small: %+v", rep)
	}
	files := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Home.md", "Projects/orbital.md", "Topics/telemetry security.md", "Profile/Sample User.md"} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing note %s (have %d notes)", want, len(files))
		}
	}
	var sawDoc, sawFact, sawJournal, sawSession, sawCard bool
	for rel, body := range files {
		switch {
		case strings.HasPrefix(rel, "Contexts/orbital-docs/") && strings.HasSuffix(rel, "gateway.md"):
			sawDoc = true
			if !strings.Contains(body, "Terminates mTLS") || !strings.Contains(body, "links to →") {
				t.Errorf("document note lacks content or links:\n%s", body)
			}
		case strings.HasPrefix(rel, "Facts/"):
			sawFact = true
			if !strings.Contains(body, "chatcli_id: fact:") {
				t.Errorf("fact note lacks front matter:\n%s", body)
			}
		case strings.HasPrefix(rel, "Journal/Daily/"):
			sawJournal = true
			if !strings.Contains(body, "Paired on the Orbital gateway rollout") {
				t.Errorf("daily note lacks its text:\n%s", body)
			}
		case strings.HasPrefix(rel, "Sessions/"):
			sawSession = true
			if strings.Contains(body, "hello") {
				t.Errorf("session transcript leaked into the vault:\n%s", body)
			}
		case strings.HasPrefix(rel, "Board/"):
			sawCard = true
		}
		// Every path wikilink lands on a note; a bare [[name]] the graph
		// can resolve must have been rewritten to its path.
		for _, m := range wikilinkTargetRe.FindAllStringSubmatch(body, -1) {
			if strings.Contains(m[1], "/") {
				if _, ok := files[m[1]+".md"]; !ok {
					t.Errorf("%s: dangling [[%s]]", rel, m[1])
				}
				continue
			}
			if _, ok := g.Resolve(m[1]); ok {
				t.Errorf("%s: resolvable [[%s]] left as a bare name", rel, m[1])
			}
		}
	}
	if !sawDoc || !sawFact || !sawJournal || !sawSession || !sawCard {
		t.Fatalf("kinds missing: doc=%v fact=%v journal=%v session=%v card=%v", sawDoc, sawFact, sawJournal, sawSession, sawCard)
	}
	profile := files["Profile/Sample User.md"]
	if strings.Contains(profile, "Sample City") || strings.Contains(profile, "123") || !strings.Contains(profile, vaultSensitiveMask) {
		t.Fatalf("sensitive profile values not masked:\n%s", profile)
	}
	if !strings.Contains(profile, "SRE") {
		t.Fatalf("non-sensitive profile values must stay:\n%s", profile)
	}
	for rel, body := range files {
		if strings.HasPrefix(rel, "Facts/architecture/") && !strings.Contains(body, "  - grpc") {
			t.Errorf("fact tags missing from front matter:\n%s", body)
		}
	}
	if gw := files["Contexts/orbital-docs/gateway.md"]; !strings.Contains(gw, "[[Contexts/orbital-docs/setup|Setup]]") {
		t.Errorf("in-document [[Setup]] not rewritten to its note:\n%s", gw)
	}
}

func TestVault_SecondSyncWritesNothing(t *testing.T) {
	cli, _, _ := syncFixtureVault(t)
	s := newVaultSyncer(cli.graphSources(), cli.vaultDir(), zap.NewNop())
	rep, err := s.run(false)
	if err != nil || rep.Written != 0 || rep.Removed != 0 {
		t.Fatalf("idempotent sync rewrote notes: %+v %v", rep, err)
	}
}

func TestVault_MemoryChangeFlowsIn(t *testing.T) {
	cli, dir, _ := syncFixtureVault(t)
	cli.memoryStore.Manager().Topics.RecordWithSummary(map[string]string{"collector fleet": "how collectors are rolled out"})
	s := newVaultSyncer(cli.graphSources(), cli.vaultDir(), zap.NewNop())
	rep, err := s.run(false)
	if err != nil || rep.Written == 0 {
		t.Fatalf("change not synced: %+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Topics", "collector fleet.md")); err != nil {
		t.Fatalf("new topic note missing: %v", err)
	}
}

func TestVaultSyncer_DebouncesAndStops(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	t.Setenv("CHATCLI_MEMORY_VAULT_DIR", filepath.Join(t.TempDir(), "v"))
	t.Setenv("CHATCLI_MEMORY_VAULT", "")
	cli.wireVault()
	s := cli.vaultSync
	if s == nil {
		t.Fatal("syncer not wired")
	}
	t.Cleanup(s.shutdown)
	for i := 0; i < 5; i++ {
		cli.memoryStore.Manager().Facts.AddFact("burst fact number "+string(rune('a'+i)), "general", nil)
	}
	s.mu.Lock()
	armed := s.timer != nil && !s.since.IsZero()
	s.mu.Unlock()
	if !armed {
		t.Fatal("memory change did not arm the vault debounce")
	}
	s.shutdown()
	s.schedule() // after stop: ignored
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if !stopped {
		t.Fatal("shutdown did not stop the syncer")
	}
}

func TestVaultSyncer_MaxDelayBoundsDebounce(t *testing.T) {
	s := newVaultSyncer(nil, "", nil)
	s.since = time.Now().Add(-vaultMaxDelay + time.Second)
	s.schedule()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer == nil {
		t.Fatal("timer not armed")
	}
	s.stopped = true
	s.timer.Stop()
}

func TestVault_DisabledByEnv(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_VAULT", "off")
	cli := newTestCLIWithMemory(t)
	cli.wireVault()
	if cli.vaultSync != nil {
		t.Fatal("vault wired although disabled")
	}
	if vaultEnabled() {
		t.Fatal("vaultEnabled ignores off")
	}
}

func TestVaultDir_DefaultNextToMemory(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_VAULT_DIR", "")
	cli := newTestCLIWithMemory(t)
	want := filepath.Join(filepath.Dir(cli.memoryStore.Manager().MemoryDir()), "vault")
	if got := cli.vaultDir(); got != want {
		t.Fatalf("vaultDir = %q, want %q", got, want)
	}
}

func TestDocVaultPathAndCommonDir(t *testing.T) {
	if got := commonSourceDir([]string{"/home/u/repo/docs/a.md", "/home/u/repo/docs/guide/b.md"}); got != "/home/u/repo/docs" {
		t.Fatalf("commonSourceDir = %q", got)
	}
	if got := commonSourceDir([]string{"a.md", "b.md"}); got != "" {
		t.Fatalf("flat corpus common dir = %q", got)
	}
	if got := docVaultPath("/home/u/repo/docs/guide/b.md", "/home/u/repo/docs"); got != "guide/b.md" {
		t.Fatalf("docVaultPath = %q", got)
	}
	if got := docVaultPath(`C:\repo\src\main.go`, "C:/repo"); got != "src/main.go.md" {
		t.Fatalf("windows/non-md docVaultPath = %q", got)
	}
	if got := docVaultPath("../../etc/passwd", ""); strings.Contains(got, "..") {
		t.Fatalf("traversal kept: %q", got)
	}
}

func TestPlainTitle(t *testing.T) {
	for in, want := range map[string]string{
		"prefers tests in [[Orbital]]":   "prefers tests in Orbital",
		"see [[Setup|the setup]] now":    "see the setup now",
		"[[Guide#Install]] and [[ação]]": "Guide and ação",
		"unterminated [[x":               "unterminated [[x",
		"  plain  ":                      "plain",
	} {
		if got := plainTitle(in); got != want {
			t.Errorf("plainTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVault_PerTenantIsolation(t *testing.T) {
	cli := newTenantTestCLI(t)
	cli.memoryStore = workspace.NewMemoryStore(cli.stateRoot, zap.NewNop())
	t.Setenv("CHATCLI_MEMORY_VAULT", "")
	shared := filepath.Join(t.TempDir(), "shared-override")
	t.Setenv("CHATCLI_MEMORY_VAULT_DIR", shared)
	cli.wireVault()
	t.Cleanup(cli.shutdownVaultSyncers)
	cli.memoryStore.Manager().Facts.AddFact("base secret alpha", "general", nil)

	ctx := context.Background()
	leave := cli.enterTenant(ctx, "telegram:1")
	cli.memoryStore.Manager().Facts.AddFact("tenant one secret bravo", "general", nil)
	ts := cli.tenants.items["telegram:1"]
	if ts == nil || ts.vaultSync == nil {
		t.Fatal("tenant vault syncer not wired")
	}
	if !strings.HasPrefix(ts.vaultSync.dir, ts.root) || strings.HasPrefix(ts.vaultSync.dir, shared) {
		t.Fatalf("tenant vault must live in its own root, got %s (root %s)", ts.vaultSync.dir, ts.root)
	}
	if cli.currentVaultSyncer() != ts.vaultSync || cli.vaultDir() != ts.vaultSync.dir {
		t.Fatal("/memory vault must address the active tenant's vault")
	}
	leave()
	if cli.currentVaultSyncer() != cli.vaultSync || cli.vaultDir() != shared {
		t.Fatal("base vault must be restored on leave")
	}

	// The tenant's background sync runs while the turn goroutine swaps
	// tenants: it must read only its bound stores (go test -race).
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := ts.vaultSync.run(false); err != nil {
			t.Errorf("tenant sync: %v", err)
		}
	}()
	for i := 0; i < 20; i++ {
		cli.enterTenant(ctx, "telegram:2")()
	}
	<-done
	if _, err := cli.vaultSync.run(false); err != nil {
		t.Fatal(err)
	}

	read := func(dir string) string {
		var all strings.Builder
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(p, ".md") {
				b, _ := os.ReadFile(p)
				all.Write(b)
			}
			return nil
		})
		return all.String()
	}
	tenantVault, baseVault := read(ts.vaultSync.dir), read(shared)
	if !strings.Contains(tenantVault, "bravo") || strings.Contains(tenantVault, "alpha") {
		t.Fatalf("tenant vault content wrong (bravo=%v alpha=%v)", strings.Contains(tenantVault, "bravo"), strings.Contains(tenantVault, "alpha"))
	}
	if !strings.Contains(baseVault, "alpha") || strings.Contains(baseVault, "bravo") {
		t.Fatal("base vault leaked tenant data or lost its own")
	}
}

func TestVault_PathCollisionsKeepLinksExact(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skip(err)
	}
	cli.contextHandler = ch
	m := cli.memoryStore.Manager()
	m.Profile.Update(map[string]string{"name": "Pat Example", "sensitive_mark": "name", "skills": "Rust"})
	m.Profile.Update(map[string]string{"sensitive_mark": "skills"})
	docs := t.TempDir()
	// A root document named like its context: both map to Contexts/guide/guide.md.
	writeDoc(t, docs, "guide.md", "# guide\n\nRoot page.\n")
	writeDoc(t, docs, "run.sh", "#!/bin/sh\nif [[ -f guide ]]; then echo ok; fi\n")
	if _, err := ch.GetManager().CreateContext(context.Background(), "guide", "", []string{docs}, ctxmgr.ModeFull, nil, true); err != nil {
		t.Fatal(err)
	}
	g := cli.buildKnowledgeGraph()
	src := cli.graphSources()
	notes := src.buildVaultNotes(g)
	paths := map[string]string{}
	seen := map[string]bool{}
	for _, n := range notes {
		low := strings.ToLower(n.Path)
		if seen[low] {
			t.Fatalf("two notes share a path: %s", n.Path)
		}
		seen[low] = true
		body := n.Body
		if body == "" && n.BodyFunc != nil {
			body = n.BodyFunc()
		}
		paths[n.Path] = body
	}
	for p, body := range paths {
		for _, mm := range wikilinkTargetRe.FindAllStringSubmatch(body, -1) {
			if strings.Contains(mm[1], "/") {
				if _, ok := paths[mm[1]+".md"]; !ok {
					t.Errorf("%s: [[%s]] lands on no note", p, mm[1])
				}
			}
		}
		if strings.HasSuffix(p, "run.sh.md") && !strings.Contains(body, "[[ -f guide ]]") {
			t.Errorf("shell code rewritten inside the vault:\n%s", body)
		}
		if strings.Contains(body, "Pat Example") || strings.Contains(p, "Pat Example") {
			t.Errorf("private name leaked in %s", p)
		}
		if strings.HasPrefix(p, "Profile/") && strings.Contains(body, "  - rust") {
			t.Errorf("private skills leaked as tags:\n%s", body)
		}
	}
	if _, ok := paths["Profile/user.md"]; !ok {
		t.Errorf("private-name profile must be Profile/user.md, have %v", keys(paths))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
