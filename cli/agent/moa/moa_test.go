package moa

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
)

type fakeClient struct {
	reply       string
	err         error
	seen        string // last prompt seen
	seenHistory int    // length of history passed to the last SendPrompt
}

func (f *fakeClient) SendPrompt(_ context.Context, prompt string, history []models.Message, _ int) (string, error) {
	f.seen = prompt
	f.seenHistory = len(history)
	return f.reply, f.err
}

// Run must pass the conversation history to each proposer so a follow-up MoA is
// context-aware (regression: history was dropped, so a second /moa or a normal
// message after /moa had no context).
func TestRun_PassesHistoryToProposers(t *testing.T) {
	var mu sync.Mutex
	var clients []*fakeClient
	factory := func(provider, model string) (Client, error) {
		c := &fakeClient{reply: "ok"}
		mu.Lock()
		clients = append(clients, c)
		mu.Unlock()
		return c, nil
	}
	hist := []models.Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "reply"}}
	_, _, err := RunWithHistory(context.Background(), "follow-up", hist, []Ref{{Provider: "a"}}, factory, Ref{Provider: "agg"})
	if err != nil {
		t.Fatal(err)
	}
	var sawHistory bool
	for _, c := range clients {
		if c.seenHistory == 2 {
			sawHistory = true
		}
	}
	if !sawHistory {
		t.Fatal("a proposer should receive the 2-message history")
	}
}

func TestParseRefs(t *testing.T) {
	refs := ParseRefs("openai:gpt-5, claudeai:opus ; googleai")
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %v", refs)
	}
	if refs[0].Provider != "openai" || refs[0].Model != "gpt-5" {
		t.Errorf("ref0 wrong: %+v", refs[0])
	}
	if refs[2].Provider != "googleai" || refs[2].Model != "" {
		t.Errorf("bare provider should have empty model: %+v", refs[2])
	}
}

func TestRun_Aggregates(t *testing.T) {
	agg := &fakeClient{reply: "final synthesized"}
	factory := func(provider, model string) (Client, error) {
		if provider == "agg" {
			return agg, nil
		}
		return &fakeClient{reply: "answer from " + provider}, nil
	}
	refs := []Ref{{Provider: "a"}, {Provider: "b"}}
	out, results, err := Run(context.Background(), "question", refs, factory, Ref{Provider: "agg"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "final synthesized" {
		t.Errorf("expected aggregator output, got %q", out)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 ref results, got %d", len(results))
	}
	// Aggregator prompt must contain both candidate answers.
	if !strings.Contains(agg.seen, "answer from a") || !strings.Contains(agg.seen, "answer from b") {
		t.Errorf("aggregation prompt missing candidates: %q", agg.seen)
	}
}

func TestRun_ToleratesPartialFailure(t *testing.T) {
	factory := func(provider, model string) (Client, error) {
		switch provider {
		case "bad":
			return &fakeClient{err: errors.New("boom")}, nil
		case "agg":
			return &fakeClient{reply: "ok"}, nil
		default:
			return &fakeClient{reply: "good answer"}, nil
		}
	}
	out, _, err := Run(context.Background(), "q", []Ref{{Provider: "bad"}, {Provider: "good"}}, factory, Ref{Provider: "agg"})
	if err != nil {
		t.Fatalf("should tolerate one failure, got %v", err)
	}
	if out != "ok" {
		t.Errorf("expected aggregated output, got %q", out)
	}
}

func TestRun_AllFail(t *testing.T) {
	factory := func(provider, model string) (Client, error) {
		return &fakeClient{err: errors.New("down")}, nil
	}
	if _, _, err := Run(context.Background(), "q", []Ref{{Provider: "a"}}, factory, Ref{Provider: "agg"}); err == nil {
		t.Error("expected error when all references fail")
	}
}

func TestRun_NoRefs(t *testing.T) {
	if _, _, err := Run(context.Background(), "q", nil, nil, Ref{}); err == nil {
		t.Error("expected error with no refs")
	}
}

// RunSession must route every participant — proposers AND aggregator —
// through the injected turn executor, so host-granted capabilities (enriched
// system context, tool rounds) apply to the whole panel.
func TestRunSession_RoutesAllParticipantsThroughTurn(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{} // ref → prompt
	turn := func(_ context.Context, ref Ref, prompt string, _ []models.Message) (string, error) {
		mu.Lock()
		seen[ref.String()] = prompt
		mu.Unlock()
		if ref.Provider == "agg" {
			return "synthesized", nil
		}
		return "answer from " + ref.Provider, nil
	}
	out, results, err := RunSession(context.Background(), "question", nil,
		[]Ref{{Provider: "a"}, {Provider: "b"}}, Ref{Provider: "agg"}, turn)
	if err != nil {
		t.Fatal(err)
	}
	if out != "synthesized" {
		t.Errorf("expected aggregator output, got %q", out)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 ref results, got %d", len(results))
	}
	if seen["a"] != "question" || seen["b"] != "question" {
		t.Errorf("proposers should receive the user prompt, got %v", seen)
	}
	if !strings.Contains(seen["agg"], "answer from a") || !strings.Contains(seen["agg"], "answer from b") {
		t.Errorf("aggregator prompt missing candidates: %q", seen["agg"])
	}
}

func TestRunSession_NilTurn(t *testing.T) {
	if _, _, err := RunSession(context.Background(), "q", nil, []Ref{{Provider: "a"}}, Ref{}, nil); err == nil {
		t.Error("expected error with nil turn executor")
	}
}

// The aggregator must see the shared history minus the trailing user turn
// (its prompt already embeds the request — keeping both would put two
// consecutive user messages on the wire), while proposers see it whole.
func TestRunSession_AggregatorHistoryDropsTrailingUserTurn(t *testing.T) {
	hist := []models.Message{
		{Role: "system", Content: "briefing"},
		{Role: "user", Content: "question"},
	}
	var mu sync.Mutex
	histLen := map[string]int{}
	turn := func(_ context.Context, ref Ref, _ string, h []models.Message) (string, error) {
		mu.Lock()
		histLen[ref.String()] = len(h)
		mu.Unlock()
		return "ok", nil
	}
	if _, _, err := RunSession(context.Background(), "question", hist,
		[]Ref{{Provider: "a"}}, Ref{Provider: "agg"}, turn); err != nil {
		t.Fatal(err)
	}
	if histLen["a"] != 2 {
		t.Errorf("proposer should see the full 2-message history, got %d", histLen["a"])
	}
	if histLen["agg"] != 1 {
		t.Errorf("aggregator should see history without the trailing user turn, got %d", histLen["agg"])
	}
}

// A history that does not end with the duplicated user turn passes to the
// aggregator untouched.
func TestHistoryForAggregation_NoDuplicateTrailingTurn(t *testing.T) {
	hist := []models.Message{{Role: "assistant", Content: "earlier"}}
	if got := historyForAggregation(hist, "question"); len(got) != 1 {
		t.Errorf("history without a duplicate trailing user turn must be kept, got %d messages", len(got))
	}
}
