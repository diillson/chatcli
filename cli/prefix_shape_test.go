package cli

import (
	"strings"
	"testing"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func shapeHistory() []models.Message {
	return []models.Message{
		{Role: "system", SystemParts: []models.ContentBlock{{Type: "text", Text: "core"}, {Type: "text", Text: "tools"}}},
		{Role: "user", Content: "do it"},
		{Role: "assistant", ToolCalls: []models.ToolCall{{ID: "c1", Name: "read", Arguments: map[string]interface{}{"file": "a.go"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "big output"},
		{Role: "user", Content: "skills", Meta: &models.MessageMeta{SkillNames: "alpha"}},
	}
}

func shapeTools() []models.ToolDefinition {
	return []models.ToolDefinition{{Function: models.ToolFunctionDef{Name: "read", Description: "read a file"}}}
}

// The first region that differs names the cause; a request that only
// grew at the end changed nothing the cache held.
func TestDiffPrefixShapeNamesTheFirstChangedRegion(t *testing.T) {
	base := shapeOf(shapeHistory(), shapeTools())
	if !diffPrefixShape(nil, base).none() {
		t.Fatal("the first request has nothing to compare with")
	}
	grown := shapeOf(append(shapeHistory(), models.Message{Role: "assistant", Content: "done"}), shapeTools())
	if !diffPrefixShape(base, grown).none() {
		t.Fatal("appending at the end is not a change ahead of the tail")
	}

	tools := shapeTools()
	tools = append(tools, models.ToolDefinition{Function: models.ToolFunctionDef{Name: "write"}})
	if c := diffPrefixShape(base, shapeOf(shapeHistory(), tools)); c.Kind != prefixCauseTools {
		t.Fatalf("tool set change: %+v", c)
	}

	h := shapeHistory()
	h[0].SystemParts[1].Text = "tools + skills"
	if c := diffPrefixShape(base, shapeOf(h, shapeTools())); c.Kind != prefixCauseSystem || c.Index != 2 {
		t.Fatalf("second system block: %+v", c)
	}

	h = shapeHistory()
	h[3].Content = "big output ... [truncated]"
	c := diffPrefixShape(base, shapeOf(h, shapeTools()))
	if c.Kind != prefixCauseMessage || c.Detail != "tool_result" || c.Index != 3 {
		t.Fatalf("rewritten tool result is message 3: %+v", c)
	}
	if c.key() != "message:tool_result" {
		t.Fatalf("the counter key aggregates by kind: %q", c.key())
	}

	h = shapeHistory()
	h[4].Content = "skills v2"
	if c := diffPrefixShape(base, shapeOf(h, shapeTools())); c.Detail != "skills" || c.Index != 4 {
		t.Fatalf("skills block: %+v", c)
	}

	if c := diffPrefixShape(base, shapeOf(shapeHistory()[:3], shapeTools())); c.Kind != prefixCauseShrunk || c.Detail != "2" {
		t.Fatalf("shrunk history: %+v", c)
	}
	if messageKind(models.TurnContextMessage("x")) != "turn_context" || messageKind(models.RunContextMessage("x")) != "run_context" {
		t.Fatal("injected context kinds")
	}
}

// A lost prefix is booked against what the request changed; one with no
// change is a server-side or lifetime event; the ttl promotion is
// declared and attributed to itself.
func TestLostPrefixesAreAttributed(t *testing.T) {
	t.Setenv(llmclient.PromptCacheTTLEnv, "")
	t.Cleanup(func() { llmclient.ResetPromptCacheTTL(); llmclient.SetPromptCacheTTLHint("5m") })
	llmclient.ResetPromptCacheTTL()
	c := &cacheTelemetry{}
	t0 := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	c.observe("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000), t0)
	c.observe("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20000, 400), t0.Add(time.Minute))

	c.pendingCause = prefixChange{Kind: prefixCauseMessage, Index: 3, Detail: "tool_result"}
	obs := c.observe("CLAUDEAI", "claude-sonnet-5", realUsage(300, 2000, 19000), t0.Add(2*time.Minute))
	if !obs.Miss || obs.Cause.key() != "message:tool_result" || c.causes["message:tool_result"] != 1 {
		t.Fatalf("attributed miss: %+v causes=%v", obs, c.causes)
	}
	if !c.pendingCause.none() {
		t.Fatal("the cause belonged to that request only")
	}
	obs = c.observe("CLAUDEAI", "claude-sonnet-5", realUsage(300, 1000, 21000), t0.Add(3*time.Minute))
	if obs.Cause.Kind != prefixCauseUnobserved || obs.Cause.Detail != "miss" {
		t.Fatalf("a loss without a change is unobserved: %+v", obs.Cause)
	}
	if len(c.events) != 2 || c.events[1].Outcome != "miss" || c.events[1].Request != 4 {
		t.Fatalf("events = %+v", c.events)
	}

	ct := NewCostTrackerAt(t.TempDir())
	ct.cache = *c
	ct.cache.idleExpiries = 1
	ct.promoteCacheTTLIfIdling("CLAUDEAI", "claude-sonnet-5")
	stats := ct.CacheStats()
	if stats.TTLPromotedAt.IsZero() || stats.TTL != "1h" {
		t.Fatalf("promotion must be visible: %+v", stats)
	}
	obs = ct.cache.observe("CLAUDEAI", "claude-sonnet-5", realUsage(300, 0, 22000), t0.Add(4*time.Minute))
	if !obs.Rebuild || obs.Cause.Kind != prefixCauseTTL {
		t.Fatalf("the rewrite after a promotion is expected and attributed to it: %+v", obs)
	}
	ct.NotePrefixChange(prefixChange{})
	ct.cache.pendingCause = prefixChange{Kind: prefixCauseTTL}
	ct.NotePrefixChange(prefixChange{})
	if ct.cache.pendingCause.Kind != prefixCauseTTL {
		t.Fatal("an empty change must not erase a pending promotion")
	}
	if rep := ct.LostPrefixes(); rep.Causes["ttl_promotion"] != 1 || len(rep.Events) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	var none *CostTracker
	none.NotePrefixChange(prefixChange{Kind: prefixCauseTools})
	if len(none.LostPrefixes().Events) != 0 {
		t.Fatal("nil tracker")
	}
}

// The table and the events render with human labels.
func TestLostPrefixLines(t *testing.T) {
	if lostPrefixLines(LostPrefixReport{}) != nil {
		t.Fatal("nothing lost, nothing to show")
	}
	stats := LostPrefixReport{
		Causes: map[string]int{"message:tool_result": 2, "ttl_promotion": 1, "unobserved:expired": 1, "tools": 1, "system": 1, "shrunk:3": 1},
		Events: []lostPrefixEvent{
			{Request: 3, Outcome: "miss", Cause: prefixChange{Kind: prefixCauseMessage, Index: 3, Detail: "tool_result"}},
			{Request: 6, Outcome: "expired", Cause: prefixChange{Kind: prefixCauseUnobserved, Detail: "expired"}},
			{Request: 7, Outcome: "rebuild", Cause: prefixChange{Kind: prefixCauseTTL}},
		},
	}
	lines := lostPrefixLines(stats)
	if len(lines) != 4 {
		t.Fatalf("expected the table plus three events, got %q", lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"×2", "#3", "#7", "1h", "tool result"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lines lack %q:\n%s", want, joined)
		}
	}
	if prefixKindLabel("weird") != "weird" || prefixOutcomeLabel("odd") != "odd" || prefixCauseLabel(prefixChange{Kind: "x", Detail: "y"}) != "x:y" {
		t.Fatal("unknown values print as they are")
	}
}

// The session fingerprints each request before sending and hands the
// telemetry the change; /clear forgets the previous request.
func TestNotePrefixShapeFeedsTheTracker(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTrackerAt(t.TempDir())}
	cli.notePrefixShape(shapeHistory(), shapeTools())
	if !cli.costTracker.cache.pendingCause.none() {
		t.Fatal("first request: nothing to compare with")
	}
	h := shapeHistory()
	h[3].Content = "rewritten"
	cli.notePrefixShape(h, shapeTools())
	if c := cli.costTracker.cache.pendingCause; c.Kind != prefixCauseMessage || c.Detail != "tool_result" {
		t.Fatalf("pending cause = %+v", c)
	}
	cli.resetPrefixShape()
	cli.notePrefixShape(shapeHistory(), shapeTools())
	if !cli.costTracker.cache.pendingCause.none() {
		t.Fatal("after a reset the next request is a first request")
	}
	var none *ChatCLI
	none.notePrefixShape(nil, nil)
	none.resetPrefixShape()
}
