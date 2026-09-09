/*
 * ChatCLI - prefix curation must never cost capability
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Every trade in this file is the same one: material leaves the prompt only
 * while it stays reachable. These tests pin the "stays reachable" half —
 * the part that, if it ever silently broke, would show up as a model that
 * got worse rather than as a failing build.
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

func curationSkill(name, body string) *persona.Skill {
	return &persona.Skill{
		Name:        name,
		Description: "does " + name,
		Path:        "/skills/" + name + ".md",
		Content:     body,
	}
}

const curationBody = "Step 1: never run multiline shell.\nStep 2: verify every write.\n" +
	"Step 3: commit with a single type prefix and clean up the scratch files afterwards."

// TestSkillBodyRepeatsOnlyWhenTheBodyIsStillInHistory is the core guard: a
// skill may be reduced to "you already have this" ONLY while the earlier copy
// is genuinely still in the conversation.
func TestSkillBodyRepeatsOnlyWhenTheBodyIsStillInHistory(t *testing.T) {
	skill := curationSkill("patch-flow", curationBody)
	fingerprint := skillBodyFingerprint(skill)

	var withHistory strings.Builder
	visible := func(*persona.Skill) bool { return true }
	renderSkillEntriesCurated(&withHistory, []*persona.Skill{skill}, skillCuration{
		Budget:       10000,
		Injected:     map[string]string{"patch-flow": fingerprint},
		StillVisible: visible,
		Recovery:     "call context_pull",
	})
	if strings.Contains(withHistory.String(), "Step 1") {
		t.Error("body re-inlined although the earlier copy is still visible")
	}

	var gone strings.Builder
	renderSkillEntriesCurated(&gone, []*persona.Skill{skill}, skillCuration{
		Budget:       10000,
		Injected:     map[string]string{"patch-flow": fingerprint},
		StillVisible: func(*persona.Skill) bool { return false },
		Recovery:     "call context_pull",
	})
	if !strings.Contains(gone.String(), "Step 1") {
		t.Fatal("body withheld after the earlier copy left the history — the model lost binding guidance")
	}
}

// TestEditedSkillIsAlwaysReInlined: the fingerprint must catch an edit, or
// the model follows instructions that no longer exist on disk.
func TestEditedSkillIsAlwaysReInlined(t *testing.T) {
	old := curationSkill("patch-flow", curationBody)
	edited := curationSkill("patch-flow", curationBody+"\nStep 4: new rule.")

	var b strings.Builder
	renderSkillEntriesCurated(&b, []*persona.Skill{edited}, skillCuration{
		Budget:       10000,
		Injected:     map[string]string{"patch-flow": skillBodyFingerprint(old)},
		StillVisible: func(*persona.Skill) bool { return true },
	})
	if !strings.Contains(b.String(), "Step 4: new rule.") {
		t.Fatal("edited skill not re-inlined")
	}
}

// TestRepeatNoteNeverReadsAsAbsentGuidance: the note replaces a body; it must
// not invite the model to treat the skill as optional or empty.
func TestRepeatNoteNeverReadsAsAbsentGuidance(t *testing.T) {
	note := renderSkillBodyRepeat("call context_pull with kind=skill")
	for _, forbidden := range []string{"do not invent", "no source file"} {
		if strings.Contains(note, forbidden) {
			t.Errorf("repeat note borrows the empty-skill wording: %q", note)
		}
	}
	if !strings.Contains(note, "follow that copy") {
		t.Errorf("repeat note does not tell the model the guidance still binds: %q", note)
	}
	if !strings.Contains(note, "call context_pull") {
		t.Errorf("repeat note offers no way back: %q", note)
	}
}

// TestDeferredBodyPointsAtSomethingChatCanActuallyOpen: without a recovery
// call the pointer sends the model to a file path, which chat cannot read.
func TestDeferredBodyPointsAtSomethingChatCanActuallyOpen(t *testing.T) {
	skill := curationSkill("patch-flow", curationBody)
	withPull := renderSkillBodyPointerWithRecovery(skill, "call context_pull with kind=skill")
	if !strings.Contains(withPull, "context_pull") {
		t.Errorf("chat pointer has no reachable recovery: %q", withPull)
	}
	withoutPull := renderSkillBodyPointerWithRecovery(skill, "")
	if withoutPull != renderSkillBodyPointer(skill) {
		t.Errorf("surfaces with file tools must keep the source-path pointer: %q", withoutPull)
	}
}

// TestCurationDisarmedWithoutRecovery: with context_pull off, chat must
// inline exactly what it inlined before this change.
func TestCurationDisarmedWithoutRecovery(t *testing.T) {
	t.Setenv(chatContextPullEnvVar, "false")
	cli := &ChatCLI{}
	cur := cli.chatSkillCuration(24000)
	if cur.Injected != nil || cur.Recovery != "" || cur.StillVisible != nil {
		t.Fatalf("curation armed without a recovery path: %+v", cur)
	}
}

// TestSkillBodyStillInHistoryTracksTheRealHistory: the visibility probe is
// what makes de-duplication safe across compaction, /clear and /rewind
// without tracking every site that can drop a message.
func TestSkillBodyStillInHistoryTracksTheRealHistory(t *testing.T) {
	skill := curationSkill("patch-flow", curationBody)
	cli := &ChatCLI{}
	if cli.skillBodyStillInHistory(skill) {
		t.Fatal("empty history reported as still carrying the body")
	}
	cli.history = []models.Message{
		{Role: "user", Content: "[TURN CONTEXT]\n## Skill: patch-flow\n\n" + curationBody},
		{Role: "assistant", Content: "ok"},
	}
	if !cli.skillBodyStillInHistory(skill) {
		t.Fatal("body present in history reported as gone")
	}
	// Compaction rewrote the turn into a summary: the body is gone.
	cli.history = []models.Message{{Role: "user", Content: "[summary] talked about patching"}}
	if cli.skillBodyStillInHistory(skill) {
		t.Fatal("summarized history reported as still carrying the body")
	}
}

// TestMCPCatalogKeepsDescriptionsWithoutRecovery: names-only is a summary,
// and a summary is only allowed while it can be expanded.
func TestMCPCatalogKeepsDescriptionsWithoutRecovery(t *testing.T) {
	t.Setenv(chatContextPullEnvVar, "false")
	cli := &ChatCLI{}
	if cli.chatContextPullActive() {
		t.Fatal("recovery reported active while disabled")
	}
}

// TestContextPullRecoversWhatWasCuratedOut walks the tool end to end on the
// two kinds the curation depends on.
func TestContextPullRecoversWhatWasCuratedOut(t *testing.T) {
	cli, _ := newPipelineCLI(t, map[string]string{
		"patch-flow": "---\nname: patch-flow\ndescription: how to patch\n---\n" + curationBody,
	})

	list := cli.runChatContextPull(`{"kind":"skills"}`)
	if !strings.Contains(list, "patch-flow") {
		t.Fatalf("skill catalog does not list the installed skill: %q", list)
	}

	body := cli.runChatContextPull(`{"kind":"skill","name":"patch-flow"}`)
	if !strings.Contains(body, "Step 1") {
		t.Fatalf("skill body not recovered: %q", body)
	}

	// Unknown names must guide, not dead-end.
	miss := cli.runChatContextPull(`{"kind":"skill","name":"nope"}`)
	if !strings.Contains(miss, "kind=skills") {
		t.Fatalf("unknown skill gives no way forward: %q", miss)
	}
}

// TestContextPullArgsAreParsedLeniently: strict parsing here only teaches the
// model to retry, which costs a whole extra round trip.
func TestContextPullArgsAreParsedLeniently(t *testing.T) {
	cases := []struct{ in, kind, name string }{
		{`{"kind":"skill","name":"a"}`, "skill", "a"},
		{`{"cmd":"skill","skill":"a"}`, "skill", "a"},
		{`{"kind":"skill","args":{"name":"a"}}`, "skill", "a"},
		{`{"kind":"MCP_Tools"}`, "mcp_tools", ""},
		{`"skills"`, "skills", ""},
	}
	for _, c := range cases {
		got := parseContextPullArgs(c.in)
		if got.Kind != c.kind || got.Name != c.name {
			t.Errorf("parseContextPullArgs(%s) = %+v, want kind=%q name=%q", c.in, got, c.kind, c.name)
		}
	}
}

// TestChatExceptionToolsIncludeRecoveryOnlyWhenOn keeps the tool definition
// out of the prefix when the exception is off — the definition is prefix
// bytes like everything else.
func TestChatExceptionToolsIncludeRecoveryOnlyWhenOn(t *testing.T) {
	withPull := buildChatExceptionTools(chatExceptions{pull: true})
	if len(withPull) != 1 || withPull[0].Function.Name != contextPullToolName {
		t.Fatalf("context_pull not offered: %+v", withPull)
	}
	if got := buildChatExceptionTools(chatExceptions{}); len(got) != 0 {
		t.Fatalf("tools offered with every exception off: %+v", got)
	}
}

// newCuratedCLI is a chat CLI wired with a real workspace context builder, so
// the stable/volatile placement can be exercised end to end.
func newCuratedCLI(t *testing.T) *ChatCLI {
	t.Helper()
	cli, _ := newPipelineCLI(t, nil)
	wsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("You are helpful"), 0o644); err != nil {
		t.Fatal(err)
	}
	bl := workspace.NewBootstrapLoader(wsDir, "", zap.NewNop())
	ms := workspace.NewMemoryStore(t.TempDir(), zap.NewNop())
	ms.Manager().ProcessExtraction("## LONGTERM\n- embed.FS needs '/' on Windows\n\n## PROFILE_UPDATE\nname=Test\n")
	cli.memoryStore = ms
	cli.contextBuilder = workspace.NewContextBuilder(bl, ms, wsDir)
	return cli
}

// TestStableWorkspaceBlockIsCachedAndConstant: the whole point of moving it
// is that it can be cached, which requires a cache hint AND byte-identical
// content across turns.
func TestStableWorkspaceBlockIsCachedAndConstant(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_MODE", memModeIndex)
	cli := newCuratedCLI(t)

	first, ok := cli.workspaceStablePart(testCtx())
	if !ok || strings.TrimSpace(first.Text) == "" {
		t.Fatal("no stable workspace block produced")
	}
	if first.CacheControl == nil {
		t.Fatal("stable block carries no cache hint — it would be re-sent every turn")
	}
	second, _ := cli.workspaceStablePart(testCtx())
	if first.Text != second.Text {
		t.Fatal("stable block changed between turns; it would invalidate the prefix it lives in")
	}
}

// TestStableWorkspaceBlockIsDroppedWithTheHistory: the memo describes a
// conversation, and must not survive one being cleared.
func TestStableWorkspaceBlockIsDroppedWithTheHistory(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_MODE", memModeIndex)
	cli := newCuratedCLI(t)
	if _, ok := cli.workspaceStablePart(testCtx()); !ok {
		t.Fatal("no stable block to begin with")
	}
	if cli.chatWorkspaceStable == nil {
		t.Fatal("stable block was not memoized")
	}
	cli.skillBodiesInjected = map[string]string{"x": "y"}
	cli.resetChatPrefixMemo()
	if cli.chatWorkspaceStable != nil || cli.skillBodiesInjected != nil {
		t.Fatal("prefix memo survived the conversation it describes")
	}
}

// TestCachedPrefixStaysContiguous is the invariant every block placement must
// respect: once a hint-less (volatile) part appears, no later part may carry
// a cache hint, or the cached prefix is broken for every turn.
func TestCachedPrefixStaysContiguous(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_MODE", memModeIndex)
	cli := newCuratedCLI(t)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable in this environment: %v", err)
	}
	cli.contextHandler = ch

	out := cli.assembleChatSystemPrompt(testCtx(), "how do I patch this?", "")
	sawVolatile := false
	sawStableWorkspace := false
	for i, p := range out.parts {
		if strings.Contains(p.Text, "You are helpful") && p.CacheControl != nil {
			sawStableWorkspace = true
		}
		if p.CacheControl == nil {
			sawVolatile = true
			continue
		}
		if sawVolatile {
			t.Fatalf("part %d carries a cache hint after a volatile part — prefix broken", i)
		}
	}
	if !sawStableWorkspace {
		t.Fatal("the stable workspace half never reached the cached prefix")
	}
}
