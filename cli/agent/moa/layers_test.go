/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package moa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
)

// scriptedTurn answers per (provider, round): round 1 is the first call a
// provider gets, round 2 the second, and so on. It records every prompt.
type scriptedTurn struct {
	mu      sync.Mutex
	calls   map[string]int
	prompts map[string][]string
	answer  func(provider string, round int, prompt string) (string, error)
}

func newScriptedTurn(answer func(provider string, round int, prompt string) (string, error)) *scriptedTurn {
	return &scriptedTurn{calls: map[string]int{}, prompts: map[string][]string{}, answer: answer}
}

func (s *scriptedTurn) turn(_ context.Context, ref Ref, prompt string, _ []models.Message) (string, error) {
	s.mu.Lock()
	s.calls[ref.Provider]++
	round := s.calls[ref.Provider]
	s.prompts[ref.Provider] = append(s.prompts[ref.Provider], prompt)
	s.mu.Unlock()
	return s.answer(ref.Provider, round, prompt)
}

func threeRefs() []Ref { return []Ref{{Provider: "a"}, {Provider: "b"}, {Provider: "c"}} }

func TestRunLayers_ReviewRoundReadsPeersRotated(t *testing.T) {
	st := newScriptedTurn(func(p string, round int, _ string) (string, error) {
		if p == "agg" {
			return "FINAL", nil
		}
		if round == 1 {
			return "first-" + p, nil
		}
		if p == "b" {
			return "[revised]\nsecond-b", nil
		}
		return "**[kept]**\nsecond-" + p, nil
	})
	var events []string
	cfg := Config{Layers: 2, Hooks: Hooks{
		OnLayer: func(layer, total, members int) {
			events = append(events, fmt.Sprintf("layer %d/%d %d", layer, total, members))
		},
		OnDone: func(layer int, r RefResult, s Stance) {
			events = append(events, fmt.Sprintf("done %d %s %s", layer, r.Ref, s))
		},
		OnSynthesis: func() { events = append(events, "synth") },
	}}
	out, err := RunLayers(context.Background(), "q?", nil, threeRefs(), Ref{Provider: "agg"}, st.turn, cfg)
	if err != nil {
		t.Fatalf("RunLayers: %v", err)
	}
	if out.Final != "FINAL" || len(out.Layers) != 2 || len(out.Stances) != 2 {
		t.Fatalf("unexpected outcome: %+v", out)
	}

	// Member a reads b then c; b reads c then a; c reads a then b.
	wantOrder := map[string][2]string{"a": {"first-b", "first-c"}, "b": {"first-c", "first-a"}, "c": {"first-a", "first-b"}}
	for p, order := range wantOrder {
		review := st.prompts[p][1]
		if !strings.Contains(review, ReviewInstruction) || !strings.Contains(review, "## Your previous answer\nfirst-"+p) {
			t.Fatalf("%s review prompt lacks its brief or own answer:\n%s", p, review)
		}
		i1, i2 := strings.Index(review, "### Answer 1\n"+order[0]), strings.Index(review, "### Answer 2\n"+order[1])
		if i1 < 0 || i2 < 0 {
			t.Fatalf("%s should read %v in that order:\n%s", p, order, review)
		}
	}

	if out.Stances[1][0] != StanceKept || out.Stances[1][1] != StanceRevised || out.Stances[0][0] != StanceNone {
		t.Fatalf("stances = %v", out.Stances)
	}
	if out.Answers[1].Output != "second-b" {
		t.Fatalf("markers must be stripped before anyone reads the answer: %q", out.Answers[1].Output)
	}
	agg := st.prompts["agg"][0]
	if !strings.Contains(agg, "second-a") || strings.Contains(agg, "first-a") || strings.Contains(agg, "[kept]") {
		t.Fatalf("aggregator should read the last round only:\n%s", agg)
	}
	if events[0] != "layer 1/2 3" || events[4] != "layer 2/2 3" || events[len(events)-1] != "synth" {
		t.Fatalf("hook order = %v", events)
	}
}

func TestPropose_FailuresAcrossRounds(t *testing.T) {
	st := newScriptedTurn(func(p string, round int, _ string) (string, error) {
		switch {
		case p == "c":
			return "", errors.New("401")
		case p == "b" && round == 2:
			return "", errors.New("timeout")
		default:
			return fmt.Sprintf("%s-r%d", p, round), nil
		}
	})
	out, err := Propose(context.Background(), "q", nil, threeRefs(), st.turn, Config{Layers: 3})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if st.calls["c"] != 1 {
		t.Fatalf("a member with no answer must leave the panel, c ran %d times", st.calls["c"])
	}
	if !errors.Is(out.Layers[1][2].Err, ErrSkipped) {
		t.Fatalf("c should be marked skipped in round 2: %v", out.Layers[1][2].Err)
	}
	if out.Layers[1][1].Err == nil || out.Answers[1].Output != "b-r3" {
		t.Fatalf("b failed round 2 but should answer round 3 from its round-1 answer: %+v", out.Answers[1])
	}
	if !strings.Contains(st.prompts["b"][2], "## Your previous answer\nb-r1") {
		t.Fatalf("b's round-3 prompt should carry its last good answer:\n%s", st.prompts["b"][2])
	}
	if out.Answers[2].Err == nil || out.Answers[0].Output != "a-r3" {
		t.Fatalf("answers = %+v", out.Answers)
	}
}

func TestPropose_StopsWithoutPeers(t *testing.T) {
	st := newScriptedTurn(func(p string, _ int, _ string) (string, error) {
		if p == "a" {
			return "only a", nil
		}
		return "", errors.New("down")
	})
	out, err := Propose(context.Background(), "q", nil, threeRefs(), st.turn, Config{Layers: 3})
	if err != nil || len(out.Layers) != 1 || st.calls["a"] != 1 {
		t.Fatalf("a lone member has no peers to review: layers=%d calls=%d err=%v", len(out.Layers), st.calls["a"], err)
	}
}

func TestPropose_Errors(t *testing.T) {
	st := newScriptedTurn(func(string, int, string) (string, error) { return "", errors.New("boom") })
	if _, err := Propose(context.Background(), "q", nil, threeRefs(), st.turn, Config{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("all failing should surface the first error, got %v", err)
	}
	if _, err := Propose(context.Background(), "q", nil, nil, st.turn, Config{}); err == nil {
		t.Fatal("no refs should fail")
	}
	if _, err := Propose(context.Background(), "q", nil, threeRefs(), nil, Config{}); err == nil {
		t.Fatal("nil turn should fail")
	}
	empty := newScriptedTurn(func(string, int, string) (string, error) { return " ", nil })
	if _, err := Propose(context.Background(), "q", nil, threeRefs(), empty.turn, Config{}); err == nil {
		t.Fatal("only empty answers should fail")
	}
}

func TestRunLayers_AggregatorFails(t *testing.T) {
	st := newScriptedTurn(func(p string, _ int, _ string) (string, error) {
		if p == "agg" {
			return "", errors.New("agg down")
		}
		return "ok " + p, nil
	})
	out, err := RunLayers(context.Background(), "q", nil, threeRefs(), Ref{Provider: "agg"}, st.turn, Config{})
	if err == nil || len(out.Answers) != 3 {
		t.Fatalf("aggregator failure should be reported with the answers kept: %v", err)
	}
	if _, err := RunLayers(context.Background(), "q", nil, nil, Ref{Provider: "agg"}, st.turn, Config{}); err == nil {
		t.Fatal("no refs should fail")
	}
}

func TestClampLayers(t *testing.T) {
	for in, want := range map[int]int{-1: 1, 0: 1, 1: 1, 3: 3, MaxLayers: MaxLayers, 99: MaxLayers} {
		if got := ClampLayers(in); got != want {
			t.Errorf("ClampLayers(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestTakeStance(t *testing.T) {
	cases := []struct {
		in, rest string
		want     Stance
	}{
		{"[kept]\nYes.", "Yes.", StanceKept},
		{"  [REVISED] No.", "No.", StanceRevised},
		{"**[revised]**\n\nNo.", "No.", StanceRevised},
		{"`[kept]`\nYes.", "Yes.", StanceKept},
		{"Yes, [kept] mid-text.", "Yes, [kept] mid-text.", StanceNone},
		{"", "", StanceNone},
	}
	for _, c := range cases {
		got, rest := TakeStance(c.in)
		if got != c.want || rest != c.rest {
			t.Errorf("TakeStance(%q) = %q, %q; want %q, %q", c.in, got, rest, c.want, c.rest)
		}
	}
}

func TestBuildAggregationPrompt_AsksForCriticalEvaluation(t *testing.T) {
	p := BuildAggregationPrompt("q", []RefResult{{Ref: Ref{Provider: "a"}, Output: "x"}})
	if !strings.Contains(p, "some may be biased, outdated or wrong") || !strings.Contains(p, "Do not just copy one") ||
		!strings.Contains(p, DivergenceHeadingNote) {
		t.Fatalf("aggregation prompt should ask for a critical synthesis:\n%s", p)
	}
}
