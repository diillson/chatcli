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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storageFixture lays out a state root with one item per rule and one
// protected neighbor per store, returning the root and the workspace
// directory a live checkpoint points at.
func storageFixture(t *testing.T, now time.Time) (root, liveWorkspace string) {
	t.Helper()
	root = t.TempDir()
	liveWorkspace = t.TempDir()
	// On macOS every t.TempDir() is under /var/folders, which the orphan
	// rule treats as throwaway; scope the rule to a directory of our own.
	throwaway := filepath.Join(root, "throwaway")
	require.NoError(t, os.MkdirAll(throwaway, 0o700))
	prev := tempRoots
	tempRoots = func() []string { return []string{throwaway} }
	t.Cleanup(func() { tempRoots = prev })
	old := now.Add(-100 * 24 * time.Hour)
	fresh := now.Add(-time.Hour)
	write := func(rel, content string, mod time.Time) {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		require.NoError(t, os.Chtimes(p, mod, mod))
	}
	snapshot := func(requests int) string {
		return `{"model_usage":{"openai:gpt-4o":{"requests":` + strconv.Itoa(requests) + `}}}`
	}
	// costs: one past TTL, four in a burst (three tiny, one a real session), a
	// stray temp file, the daily ledger.
	write("costs/20260101-120000-1.json", snapshot(1), old)
	for i, req := range []int{1, 1, 2, 9} {
		write("costs/20260908-121448-"+strconv.Itoa(i)+".json", snapshot(req), fresh)
	}
	write("costs/20260908-121448-99.json.tmp", "x", old)
	write("costs/daily-spend.json", "{}", old)
	// checkpoints: temp worktree, vanished worktree, live and fresh, live but
	// stale, and a directory that is not a checkpoint at all.
	repo := func(name, worktree string, mod time.Time) {
		write("checkpoints/"+name+"/config", "[core]\n\tworktree = "+worktree+"\n", mod)
		write("checkpoints/"+name+"/HEAD", "ref: refs/heads/main\n", mod)
		require.NoError(t, os.Chtimes(filepath.Join(root, "checkpoints", name), mod, mod))
	}
	repo("aaaa", filepath.Join(throwaway, "chatcli-test-ws"), fresh)
	repo("bbbb", filepath.Join(root, "gone-workspace"), fresh)
	repo("cccc", liveWorkspace, fresh)
	repo("dddd", liveWorkspace, old)
	write("checkpoints/notarepo/README", "x", old)
	// sessions: machine sessions expire, named ones never.
	write("sessions/autosave-old.json", "{}", old)
	write("sessions/mcp-fresh.json", "{}", fresh)
	write("sessions/my-named-old.json", "{}", old)
	// transcripts, pending memory, distilled memory, task graph runs.
	write("transcripts/old.jsonl", "{}", old)
	write("transcripts/fresh.jsonl", "{}", fresh)
	write("memory/pending/seg-old.json", "{}", old)
	write("memory/facts.json", "{}", old)
	write("taskgraph/tg-old/state.json", "{}", now.Add(-45*24*time.Hour))
	write("taskgraph/tg-active/state.json", "{}", now.Add(-45*24*time.Hour))
	write("taskgraph/tg-fresh/state.json", "{}", fresh)
	write("app.log", strings.Repeat("x", 2048), fresh)
	write("skills/one.md", "x", old)
	return root, liveWorkspace
}

func storeByName(res StorageResult, name string) StorageStore {
	for _, s := range res.Stores {
		if s.Name == name {
			return s
		}
	}
	return StorageStore{}
}

func TestRunStorage_DryRunListsEveryRuleWithoutRemoving(t *testing.T) {
	now := time.Now()
	root, _ := storageFixture(t, now)
	res, err := RunStorage(context.Background(), StorageOptions{
		Root: root, TTL: 90 * 24 * time.Hour, Now: now, Burst: true, SkipTaskGraphRun: "tg-active",
	})
	require.NoError(t, err)
	assert.False(t, res.Applied)

	costs := storeByName(res, StoreCosts)
	assert.Equal(t, 5, costs.Prunable, "1 past TTL + 3 burst + 1 stray; the 9-request session and the ledger stay")
	assert.Equal(t, map[string]int{ReasonTTL: 1, ReasonBurst: 3, ReasonStray: 1}, costs.Reasons)

	ck := storeByName(res, StoreCheckpoints)
	assert.Equal(t, 3, ck.Prunable, "temp worktree, vanished worktree, stale live worktree")
	assert.Equal(t, map[string]int{ReasonOrphan: 2, ReasonTTL: 1}, ck.Reasons)

	sessions := storeByName(res, StoreSessions)
	assert.Equal(t, 1, sessions.Prunable, "only the old machine session")
	assert.Equal(t, 1, storeByName(res, StoreTranscripts).Prunable)
	assert.Equal(t, 1, storeByName(res, StorePending).Prunable)
	assert.Equal(t, 1, storeByName(res, StoreTaskGraph).Prunable, "the active run and the fresh run stay")

	mem := storeByName(res, StoreMemory)
	assert.True(t, mem.Protected)
	assert.Equal(t, 1, mem.Files, "pending is its own store, facts.json is memory")
	assert.Zero(t, mem.Prunable)
	assert.True(t, storeByName(res, StoreSkills).Protected)
	assert.True(t, storeByName(res, StoreCCR).OnApplyOnly)
	assert.Equal(t, int64(2048), storeByName(res, StoreLogs).Bytes)
	assert.Equal(t, 12, res.Prunable)
	assert.Zero(t, res.Removed)

	// Nothing moved.
	_, err = os.Stat(filepath.Join(root, "costs", "20260101-120000-1.json"))
	assert.NoError(t, err, "dry run must not remove")
	_, err = os.Stat(filepath.Join(root, "checkpoints", "aaaa"))
	assert.NoError(t, err)
}

func TestRunStorage_ApplyRemovesCandidatesAndKeepsProtected(t *testing.T) {
	now := time.Now()
	root, _ := storageFixture(t, now)
	res, err := RunStorage(context.Background(), StorageOptions{
		Root: root, TTL: 90 * 24 * time.Hour, Now: now, Burst: true, SkipTaskGraphRun: "tg-active", Apply: true,
	})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Equal(t, 12, res.Removed)
	assert.Positive(t, res.BytesFreed)

	gone := []string{
		"costs/20260101-120000-1.json", "costs/20260908-121448-0.json", "costs/20260908-121448-99.json.tmp",
		"checkpoints/aaaa", "checkpoints/bbbb", "checkpoints/dddd",
		"sessions/autosave-old.json", "transcripts/old.jsonl", "memory/pending/seg-old.json", "taskgraph/tg-old",
	}
	for _, rel := range gone {
		_, err := os.Stat(filepath.Join(root, rel))
		assert.True(t, os.IsNotExist(err), "%s should be gone", rel)
	}
	kept := []string{
		"costs/20260908-121448-3.json", "costs/daily-spend.json",
		"checkpoints/cccc", "checkpoints/notarepo",
		"sessions/mcp-fresh.json", "sessions/my-named-old.json", "transcripts/fresh.jsonl",
		"memory/facts.json", "taskgraph/tg-active", "taskgraph/tg-fresh", "app.log", "skills/one.md",
	}
	for _, rel := range kept {
		_, err := os.Stat(filepath.Join(root, rel))
		assert.NoError(t, err, "%s must survive", rel)
	}

	// A second apply finds nothing.
	again, err := RunStorage(context.Background(), StorageOptions{Root: root, TTL: 90 * 24 * time.Hour, Now: now, Burst: true, Apply: true, SkipTaskGraphRun: "tg-active"})
	require.NoError(t, err)
	assert.Zero(t, again.Removed)
}

func TestRunStorage_TTLZeroKeepsAgeButNotOrphans(t *testing.T) {
	now := time.Now()
	root, _ := storageFixture(t, now)
	res, err := RunStorage(context.Background(), StorageOptions{Root: root, TTL: 0, Now: now})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{ReasonOrphan: 2}, storeByName(res, StoreCheckpoints).Reasons)
	assert.Zero(t, storeByName(res, StoreSessions).Prunable)
	assert.Zero(t, storeByName(res, StoreTranscripts).Prunable)
	assert.Equal(t, map[string]int{ReasonStray: 1}, storeByName(res, StoreCosts).Reasons, "no burst rule without Burst, no TTL at zero")
}

func TestRunStorage_OnlyFilterAndUnknownStore(t *testing.T) {
	now := time.Now()
	root, _ := storageFixture(t, now)
	res, err := RunStorage(context.Background(), StorageOptions{Root: root, TTL: 90 * 24 * time.Hour, Now: now, Only: StoreSessions})
	require.NoError(t, err)
	require.Len(t, res.Stores, 1)
	assert.Equal(t, StoreSessions, res.Stores[0].Name)

	_, err = RunStorage(context.Background(), StorageOptions{Root: root, Only: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

func TestRenderStorage_ShowsEveryStoreAndTheHint(t *testing.T) {
	now := time.Now()
	root, _ := storageFixture(t, now)
	res, err := RunStorage(context.Background(), StorageOptions{Root: root, TTL: 90 * 24 * time.Hour, Now: now, Burst: true})
	require.NoError(t, err)

	var sb strings.Builder
	RenderStorage(&sb, res, false)
	out := sb.String()
	for _, name := range StorageStoreNames() {
		assert.Contains(t, out, name)
	}
	assert.Contains(t, out, "--apply", "the inventory tells how to remove")
	assert.Contains(t, out, i18n.T("storage.protected"))

	sb.Reset()
	res.Applied = true
	RenderStorage(&sb, res, true)
	assert.Contains(t, sb.String(), i18n.T("storage.col.removed"))
}

func TestParseStoragePruneArgs(t *testing.T) {
	only, apply, ok := parseStoragePruneArgs(nil)
	assert.True(t, ok)
	assert.Equal(t, "", only)
	assert.False(t, apply, "dry run is the default")

	only, apply, ok = parseStoragePruneArgs([]string{"--apply", "costs"})
	assert.True(t, ok)
	assert.Equal(t, StoreCosts, only)
	assert.True(t, apply)

	_, _, ok = parseStoragePruneArgs([]string{"costs", "sessions"})
	assert.False(t, ok, "one store at a time")
	_, _, ok = parseStoragePruneArgs([]string{"bogus"})
	assert.False(t, ok)
}

// The boot pass removes orphaned checkpoints on its own; the age rule
// follows the session TTL like every other store.
func TestRetentionPass_RemovesOrphanedCheckpoints(t *testing.T) {
	now := time.Now()
	root, _ := storageFixture(t, now)
	t.Setenv("CHATCLI_SESSION_TTL", "90")
	c := &ChatCLI{stateRoot: root}
	rep := c.runRetentionPass()
	assert.Equal(t, 3, rep.Checkpoints)
	_, err := os.Stat(filepath.Join(root, "checkpoints", "cccc"))
	assert.NoError(t, err, "the live, fresh checkpoint stays")
	_, err = os.Stat(filepath.Join(root, "checkpoints", "aaaa"))
	assert.True(t, os.IsNotExist(err))
}

func TestStorageSuggestions_OfferSubcommandsStoresAndFlags(t *testing.T) {
	c := &ChatCLI{}
	first := c.getStorageSuggestions(docAt("/storage "))
	texts := suggestTexts(first)
	assert.Contains(t, texts, "prune")
	assert.Contains(t, texts, StoreCosts)

	afterPrune := suggestTexts(c.getStorageSuggestions(docAt("/storage prune ")))
	assert.Contains(t, afterPrune, "--apply")
	assert.Contains(t, afterPrune, StoreCheckpoints)
	assert.NotContains(t, afterPrune, StoreSkills, "prune offers only stores that have a rule")

	// One store at most, each flag once, and the two modes exclude each other.
	afterStore := suggestTexts(c.getStorageSuggestions(docAt("/storage prune costs ")))
	assert.Equal(t, []string{"--apply", "--dry-run"}, afterStore)
	afterFlag := suggestTexts(c.getStorageSuggestions(docAt("/storage prune --apply ")))
	assert.NotContains(t, afterFlag, "--apply")
	assert.NotContains(t, afterFlag, "--dry-run")
	assert.Contains(t, afterFlag, StoreCosts)
	assert.Empty(t, c.getStorageSuggestions(docAt("/storage prune costs --apply ")))
	assert.Equal(t, []string{StoreCosts}, suggestTexts(c.getStorageSuggestions(docAt("/storage prune co"))), "a half-typed store still completes")
	assert.Empty(t, c.getStorageSuggestions(docAt("/storage costs ")), "a bare store takes nothing after it")
}
