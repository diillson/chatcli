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
	_, _, err := Run(context.Background(), "follow-up", hist, []Ref{{Provider: "a"}}, factory, Ref{Provider: "agg"})
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
	out, results, err := Run(context.Background(), "question", nil, refs, factory, Ref{Provider: "agg"})
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
	out, _, err := Run(context.Background(), "q", nil, []Ref{{Provider: "bad"}, {Provider: "good"}}, factory, Ref{Provider: "agg"})
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
	if _, _, err := Run(context.Background(), "q", nil, []Ref{{Provider: "a"}}, factory, Ref{Provider: "agg"}); err == nil {
		t.Error("expected error when all references fail")
	}
}

func TestRun_NoRefs(t *testing.T) {
	if _, _, err := Run(context.Background(), "q", nil, nil, nil, Ref{}); err == nil {
		t.Error("expected error with no refs")
	}
}
