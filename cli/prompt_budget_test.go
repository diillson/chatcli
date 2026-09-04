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

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestPrefixBudget_SizingAndDegradeBookkeeping(t *testing.T) {
	t.Setenv("CHATCLI_CONTEXT_WINDOW", "200000")
	cli := newTenantTestCLI(t)
	b := cli.newPrefixBudget("CLAUDEAI", "claude-sonnet-5")
	if b.Window != 200000 || b.MaxChars != 400000 { // 200k × 0.5 × 4 chars
		t.Fatalf("budget = %+v", b)
	}
	if b.remaining() != 400000-prefixVolatileReserve {
		t.Fatalf("remaining keeps the volatile reserve, got %d", b.remaining())
	}
	b.spend(390000)
	if b.remaining() != 4000 || b.allow(24000) != 4000 || b.allow(1000) != 1000 {
		t.Fatalf("allow must cap by what remains: remaining=%d", b.remaining())
	}
	b.spend(100000)
	if b.remaining() != 0 {
		t.Fatal("remaining never goes negative")
	}
	b.noteDegraded("skills")
	b.noteDegraded("skills")
	b.noteDegraded("attached")
	if got := strings.Join(b.Degraded(), ","); got != "skills,attached" {
		t.Fatalf("degraded = %s", got)
	}
	t.Setenv("CHATCLI_CONTEXT_WINDOW", "4000")
	if small := cli.newPrefixBudget("X", "y"); small.MaxChars != prefixFloorChars {
		t.Fatalf("tiny windows keep the floor, got %d", small.MaxChars)
	}
}

func TestBuildPromptMessagesBudgeted_FoldsPastBudget(t *testing.T) {
	m, err := ctxmgr.NewManagerWithBasePath(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	m.AttachEmbeddingProvider(nil)
	big := strings.Repeat("content line that fills the attachment with prose\n", 400) // ~20 KB
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	fc, err := m.CreateContext(t.Context(), "big-ctx", "", []string{filepath.Join(dir, "notes.md")}, ctxmgr.ModeFull, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AttachContext("s", fc.ID, 1); err != nil {
		t.Fatal(err)
	}
	opts := ctxmgr.FormatOptions{IncludeMetadata: true, Role: "system"}
	full, folded, err := m.BuildPromptMessagesBudgeted("s", opts, 0)
	if err != nil || len(full) != 1 || len(folded) != 0 || len(full[0].Content) < len(big) {
		t.Fatalf("unbounded must inline: msgs=%d folded=%v err=%v", len(full), folded, err)
	}
	small, folded, err := m.BuildPromptMessagesBudgeted("s", opts, 2000)
	if err != nil || len(small) != 1 || len(folded) != 1 || folded[0] != "big-ctx" {
		t.Fatalf("over budget must fold: folded=%v err=%v", folded, err)
	}
	if len(small[0].Content) > 2200 || !strings.Contains(small[0].Content, "notes.md") || !strings.Contains(small[0].Content, "--rag") {
		t.Fatalf("folded card must be small, list files and teach the pull path: %q", small[0].Content)
	}
}

func TestProjectedContextPct_UnclampedAndAddsChatPrefix(t *testing.T) {
	cli := newTenantTestCLI(t)
	cli.Provider, cli.Model = "CLAUDEAI", "claude-sonnet-5"
	window := 1000
	cli.history = []models.Message{{Role: "user", Content: strings.Repeat("x", 3000)}, {Role: "assistant", Content: strings.Repeat("y", 1000)}}
	cli.promptBreakdowns.recordDegraded("chat", nil, []promptSection{{Name: "mode", Chars: 2000}})
	pct, ok := cli.projectedContextPct(window)
	if !ok {
		t.Fatal("projection must be available with history")
	}
	// (3000+1000+2000 chars) / 4 = 1500 tokens on a 1000 window = 150%,
	// plus the answer reserve (max_tokens capped at 25% of the window).
	want := 150 + float64(answerReserveTokens(cli.getMaxTokensForCurrentLLM(), window))/float64(window)*100
	if pct < want-1 || pct > want+1 {
		t.Fatalf("projected pct = %.1f, want ≈%.0f", pct, want)
	}
	if roundPct(pct) != int(want+0.5) || clampPct(pct) != 100 {
		t.Fatal("roundPct must not clamp, clampPct must")
	}
	// Agent mode: the system message lives in the history — no prefix added.
	cli.history = append([]models.Message{{Role: "system", Content: strings.Repeat("s", 4000)}}, cli.history...)
	pct2, _ := cli.projectedContextPct(window)
	if want2 := want + 50; pct2 < want2-1 || pct2 > want2+1 {
		t.Fatalf("with a system message the chat prefix is not added twice: %.1f (want ≈%.0f)", pct2, want2)
	}
	if _, ok := cli.projectedContextPct(0); ok {
		t.Fatal("no window, no projection")
	}
}

func TestClearConversation_CheckpointsAndKeepsSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cli := newTenantTestCLI(t)
	cli.currentSessionName = "work"
	cli.history = []models.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}
	cli.clearConversation(t.Context())
	if len(cli.history) != 0 || cli.currentSessionName != "work" {
		t.Fatalf("history must be empty and the session kept: %d msgs, session=%q", len(cli.history), cli.currentSessionName)
	}
	if len(cli.checkpoints) == 0 || len(cli.checkpoints[len(cli.checkpoints)-1].History) != 2 {
		t.Fatal("clear must checkpoint the conversation for /rewind")
	}
	cli.clearConversation(t.Context()) // empty: no new checkpoint
	if len(cli.checkpoints) != 1 {
		t.Fatalf("clearing an empty conversation must not checkpoint again: %d", len(cli.checkpoints))
	}
}
