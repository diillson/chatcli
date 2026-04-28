/*
 * ChatCLI - Tests for the Anthropic cache breakpoint planner
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func block(text string, marked bool) map[string]interface{} {
	b := map[string]interface{}{"type": "text", "text": text}
	if marked {
		b["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	return b
}

func countMarkers(blocks []map[string]interface{}) int {
	n := 0
	for _, b := range blocks {
		if _, ok := b["cache_control"]; ok {
			n++
		}
	}
	return n
}

func TestCoalesceUnderLimitIsNoop(t *testing.T) {
	in := []map[string]interface{}{
		block("a", true),
		block("b", true),
		block("c", false),
		block("d", true),
	}
	before := CacheBlocksCoalescedTotal()
	out := coalesceCacheControl(in, 4)
	if countMarkers(out) != 3 {
		t.Fatalf("expected 3 markers preserved, got %d", countMarkers(out))
	}
	if got := CacheBlocksCoalescedTotal() - before; got != 0 {
		t.Fatalf("expected 0 coalesced, got %d", got)
	}
}

func TestCoalesceDropsEarliestMarkers(t *testing.T) {
	in := []map[string]interface{}{
		block("p0", true), // earliest — should be dropped
		block("p1", true), // earliest — should be dropped
		block("p2", true),
		block("p3", true),
		block("p4", true),
		block("p5", true), // latest — kept
	}
	before := CacheBlocksCoalescedTotal()
	out := coalesceCacheControl(in, 4)
	if countMarkers(out) != 4 {
		t.Fatalf("expected 4 markers after coalesce, got %d", countMarkers(out))
	}
	if _, ok := out[0]["cache_control"]; ok {
		t.Fatal("expected earliest marker (p0) to be dropped")
	}
	if _, ok := out[1]["cache_control"]; ok {
		t.Fatal("expected second-earliest marker (p1) to be dropped")
	}
	if _, ok := out[5]["cache_control"]; !ok {
		t.Fatal("expected latest marker (p5) to be kept")
	}
	if got := CacheBlocksCoalescedTotal() - before; got != 2 {
		t.Fatalf("expected counter to advance by 2, got %d", got)
	}
}

func TestCoalesceRespectsBudgetForToolReserve(t *testing.T) {
	in := []map[string]interface{}{
		block("a", true),
		block("b", true),
		block("c", true),
		block("d", true),
	}
	out := coalesceCacheControl(in, 3) // tool path reserves 1 marker for tools
	if countMarkers(out) != 3 {
		t.Fatalf("expected 3 markers (budget=3), got %d", countMarkers(out))
	}
	if _, ok := out[0]["cache_control"]; ok {
		t.Fatal("expected earliest marker to be dropped first")
	}
}

func TestCoalescePreservesBlockContent(t *testing.T) {
	in := []map[string]interface{}{
		block("alpha", true),
		block("beta", true),
		block("gamma", true),
		block("delta", true),
		block("epsilon", true),
	}
	out := coalesceCacheControl(in, 4)
	if len(out) != 5 {
		t.Fatalf("expected 5 blocks preserved, got %d", len(out))
	}
	expected := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	for i, want := range expected {
		if out[i]["text"] != want {
			t.Fatalf("block %d: expected text %q, got %v", i, want, out[i]["text"])
		}
	}
}

func countBodyMarkers(reqBody map[string]interface{}) int {
	total := 0
	count := func(v interface{}) {
		switch s := v.(type) {
		case []map[string]interface{}:
			for _, m := range s {
				if _, ok := m["cache_control"]; ok {
					total++
				}
			}
		case []interface{}:
			for _, item := range s {
				if m, ok := item.(map[string]interface{}); ok {
					if _, ok := m["cache_control"]; ok {
						total++
					}
				}
			}
		}
	}
	if v, ok := reqBody["system"]; ok {
		count(v)
	}
	if v, ok := reqBody["tools"]; ok {
		count(v)
	}
	if msgs, ok := reqBody["messages"]; ok {
		switch m := msgs.(type) {
		case []map[string]interface{}:
			for _, msg := range m {
				count(msg["content"])
			}
		case []interface{}:
			for _, msg := range m {
				if mm, ok := msg.(map[string]interface{}); ok {
					count(mm["content"])
				}
			}
		}
	}
	return total
}

// TestEnforceBudgetUnderCapIsNoop: assembled body within budget should
// pass through untouched.
func TestEnforceBudgetUnderCapIsNoop(t *testing.T) {
	reqBody := map[string]interface{}{
		"system": []map[string]interface{}{
			block("a", true),
			block("b", true),
			block("c", false),
		},
	}
	before := CacheBlocksCoalescedTotal()
	enforceCacheControlBudget(reqBody, 4)
	if got := countBodyMarkers(reqBody); got != 2 {
		t.Fatalf("expected 2 markers preserved, got %d", got)
	}
	if got := CacheBlocksCoalescedTotal() - before; got != 0 {
		t.Fatalf("expected counter unchanged, got delta=%d", got)
	}
}

// TestEnforceBudgetTrimsToCap: a body that overshoots the cap (e.g.
// because two upstream code paths each stamped markers without
// coordinating) is trimmed to the cap, dropping earliest first.
func TestEnforceBudgetTrimsToCap(t *testing.T) {
	reqBody := map[string]interface{}{
		"system": []map[string]interface{}{
			block("sys-old-1", true),
			block("sys-old-2", true),
			block("sys-new-1", true),
			block("sys-new-2", true),
			block("sys-new-3", true),
			block("sys-new-4", true),
		},
	}
	before := CacheBlocksCoalescedTotal()
	enforceCacheControlBudget(reqBody, 4)
	if got := countBodyMarkers(reqBody); got != 4 {
		t.Fatalf("expected 4 markers after enforcement, got %d", got)
	}
	if got := CacheBlocksCoalescedTotal() - before; got != 2 {
		t.Fatalf("expected counter delta=2, got %d", got)
	}
	sys := reqBody["system"].([]map[string]interface{})
	if _, ok := sys[0]["cache_control"]; ok {
		t.Fatal("earliest marker (sys-old-1) should be dropped")
	}
	if _, ok := sys[1]["cache_control"]; ok {
		t.Fatal("second-earliest marker (sys-old-2) should be dropped")
	}
	if _, ok := sys[5]["cache_control"]; !ok {
		t.Fatal("latest marker (sys-new-4) should survive")
	}
}

// TestEnforceBudgetCountsToolsAndMessages: markers in `tools` and
// per-message content blocks count toward the same global cap.
func TestEnforceBudgetCountsToolsAndMessages(t *testing.T) {
	toolDef := map[string]interface{}{
		"name":          "read",
		"cache_control": map[string]string{"type": "ephemeral"},
	}
	contentBlock := map[string]interface{}{
		"type":          "tool_result",
		"cache_control": map[string]string{"type": "ephemeral"},
	}
	reqBody := map[string]interface{}{
		"system": []map[string]interface{}{
			block("s1", true),
			block("s2", true),
			block("s3", true),
		},
		"tools": []map[string]interface{}{toolDef},
		"messages": []map[string]interface{}{
			{"role": "user", "content": []map[string]interface{}{contentBlock}},
		},
	}
	enforceCacheControlBudget(reqBody, 4)
	if got := countBodyMarkers(reqBody); got != 4 {
		t.Fatalf("expected 4 markers, got %d", got)
	}
	if _, ok := toolDef["cache_control"]; !ok {
		t.Fatal("tool def marker (later in scan order) should be preserved")
	}
	if _, ok := contentBlock["cache_control"]; !ok {
		t.Fatal("message-content marker (latest in scan order) should be preserved")
	}
}

// TestEnforceBudgetHandlesOAuthInterfaceShape: the OAuth chat path
// returns `system` as []interface{} (mixed oauthTextBlock entries and
// structured blocks). The boundary check must walk that shape.
func TestEnforceBudgetHandlesOAuthInterfaceShape(t *testing.T) {
	reqBody := map[string]interface{}{
		"system": []interface{}{
			map[string]interface{}{"type": "text", "text": "base"}, // unmarked
			map[string]interface{}{
				"type": "text", "text": "p1",
				"cache_control": map[string]string{"type": "ephemeral"},
			},
			map[string]interface{}{
				"type": "text", "text": "p2",
				"cache_control": map[string]string{"type": "ephemeral"},
			},
			map[string]interface{}{
				"type": "text", "text": "p3",
				"cache_control": map[string]string{"type": "ephemeral"},
			},
			map[string]interface{}{
				"type": "text", "text": "p4",
				"cache_control": map[string]string{"type": "ephemeral"},
			},
			map[string]interface{}{
				"type": "text", "text": "p5",
				"cache_control": map[string]string{"type": "ephemeral"},
			},
		},
	}
	enforceCacheControlBudget(reqBody, 4)
	if got := countBodyMarkers(reqBody); got != 4 {
		t.Fatalf("expected 4 markers, got %d", got)
	}
}

// TestModeTransitionWorstCase reproduces the user-reported scenario:
// a /coder turn left a system message in cli.history with 4 SystemParts,
// the user attaches several /context blobs in chat mode bringing the
// chat-built system message to 6 SystemParts, and the chat path then
// builds a request with both system messages plus a future-proofing
// marker on the last tool definition. The end-to-end pipeline (per-
// section coalesce + boundary enforcement) must keep the wire body at
// or below the Anthropic cap of 4 markers.
func TestModeTransitionWorstCase(t *testing.T) {
	c := newTestClient()

	coderSystem := models.Message{
		Role: "system",
		SystemParts: []models.ContentBlock{
			{Type: "text", Text: "core", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "tools", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "workspace", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "skills+orchestrator", CacheControl: &models.CacheControl{Type: "ephemeral"}},
		},
	}
	chatSystem := models.Message{
		Role: "system",
		SystemParts: []models.ContentBlock{
			{Type: "text", Text: "mode+lang", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "workspace", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "ctx-attached-1", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "ctx-attached-2", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "ctx-attached-3", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			{Type: "text", Text: "ctx-attached-4", CacheControl: &models.CacheControl{Type: "ephemeral"}},
		},
	}
	history := []models.Message{
		chatSystem, // chat-built system message comes first
		coderSystem,
		{Role: "user", Content: "previous coder turn"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "now in chat mode"},
	}

	_, systemObj := c.buildMessagesAndSystem("now in chat mode", history)
	reqBody := map[string]interface{}{
		"system": systemObj,
	}
	enforceCacheControlBudget(reqBody, anthropicMaxCacheBreakpoints)

	if got := countBodyMarkers(reqBody); got > anthropicMaxCacheBreakpoints {
		t.Fatalf("post-transition body has %d markers, exceeds Anthropic cap of %d",
			got, anthropicMaxCacheBreakpoints)
	}
}
