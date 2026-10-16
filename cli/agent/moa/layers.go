/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Layered Mixture-of-Agents.
 *
 * Layer 1 is the classic fan-out: every member answers the request on its
 * own. Each later layer is a cross-review round, as in the layered
 * architecture of Wang et al.: every member reads its own previous answer
 * and its peers' answers, then writes an improved one. The aggregator
 * synthesizes the last round.
 *
 * Two details keep the rounds honest. The peers' answers are rotated per
 * member, so no answer always sits in the first slot (models favor what
 * they read first). And each member opens a revision with a [kept] or
 * [revised] marker, stripped before anyone else reads it, so the host can
 * show whether the panel is converging or holding its ground.
 */
package moa

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/models"
)

// MaxLayers bounds a run: every layer is one more call per member.
const MaxLayers = 4

// Stance is how a member's answer in a review round relates to its
// previous one.
type Stance string

const (
	// StanceNone is a first-round answer, or a revision without a marker.
	StanceNone Stance = ""
	// StanceKept means the member kept its bottom line.
	StanceKept Stance = "kept"
	// StanceRevised means the member changed its bottom line.
	StanceRevised Stance = "revised"
)

// Hooks report a layered run's progress. Every field is optional, and
// calls are serialized, so a hook may print without its own locking.
type Hooks struct {
	// OnLayer runs when a layer starts, with the number of members in it.
	OnLayer func(layer, total, members int)
	// OnDone runs as each member's answer of a layer lands.
	OnDone func(layer int, r RefResult, stance Stance)
	// OnSynthesis runs right before the aggregator's turn.
	OnSynthesis func()
}

// Config shapes a layered run.
type Config struct {
	// Layers is the number of proposal rounds; values below 1 mean 1 and
	// values above MaxLayers are capped.
	Layers int
	Hooks  Hooks
}

// Outcome is everything a layered run produced.
type Outcome struct {
	// Final is the aggregator's answer (empty from Propose).
	Final string
	// Layers holds what each layer produced, in reference order. A member
	// that failed in a layer, or was not part of it, shows its error.
	Layers [][]RefResult
	// Stances holds each layer's stance per member, aligned with Layers.
	Stances [][]Stance
	// Answers is each member's latest successful answer, in reference
	// order; a member that never answered carries its first error. This is
	// what the aggregator reads.
	Answers []RefResult
}

// ErrSkipped marks a member left out of a round because it gave no answer
// in an earlier one.
var ErrSkipped = errors.New("not in this round: no answer in an earlier round")

// ClampLayers bounds a requested layer count to [1, MaxLayers].
func ClampLayers(n int) int {
	switch {
	case n < 1:
		return 1
	case n > MaxLayers:
		return MaxLayers
	default:
		return n
	}
}

// RunLayers runs the proposal layers, then the aggregator over the last
// one. It fails only when no member answered the first layer or the
// aggregator failed; a member failing a review round keeps its previous
// answer.
func RunLayers(ctx context.Context, prompt string, history []models.Message, refs []Ref, aggregator Ref, turn Turn, cfg Config) (Outcome, error) {
	out, err := Propose(ctx, prompt, history, refs, turn, cfg)
	if err != nil {
		return out, err
	}
	if cfg.Hooks.OnSynthesis != nil {
		cfg.Hooks.OnSynthesis()
	}
	final, err := turn(ctx, aggregator, BuildAggregationPrompt(prompt, out.Answers), historyForAggregation(history, prompt))
	if err != nil {
		return out, fmt.Errorf("aggregation failed: %w", err)
	}
	out.Final = final
	return out, nil
}

// Propose runs the proposal layers without aggregating, for hosts that
// synthesize on their own terms.
func Propose(ctx context.Context, prompt string, history []models.Message, refs []Ref, turn Turn, cfg Config) (Outcome, error) {
	if len(refs) == 0 {
		return Outcome{}, fmt.Errorf("no reference models configured")
	}
	if turn == nil {
		return Outcome{}, fmt.Errorf("no turn executor configured")
	}
	layers := ClampLayers(cfg.Layers)
	var hookMu sync.Mutex
	hook := func(f func()) {
		hookMu.Lock()
		defer hookMu.Unlock()
		f()
	}

	out := Outcome{Answers: make([]RefResult, len(refs))}
	reviewHistory := historyForAggregation(history, prompt)
	for layer := 1; layer <= layers; layer++ {
		active := activeMembers(out.Answers, layer)
		if layer > 1 && len(active) < 2 {
			break // a review round needs peers to read
		}
		if cfg.Hooks.OnLayer != nil {
			hook(func() { cfg.Hooks.OnLayer(layer, layers, len(active)) })
		}
		results := make([]RefResult, len(refs))
		for i := range refs {
			results[i] = RefResult{Ref: refs[i], Err: ErrSkipped}
		}
		stances := make([]Stance, len(refs))
		var wg sync.WaitGroup
		for slot, i := range active {
			memberPrompt, memberHistory := prompt, history
			if layer > 1 {
				memberPrompt = BuildReviewPrompt(prompt, out.Answers[i].Output, peerAnswers(out.Answers, active, slot))
				memberHistory = reviewHistory
			}
			wg.Add(1)
			go func(i int, memberPrompt string, memberHistory []models.Message) {
				defer wg.Done()
				// Each panel member registers as a run so the /agents view
				// shows the MoA fan-out alongside worker dispatches.
				refCtx, liveRun := runs.Default().Begin(ctx, runs.Info{Kind: runs.KindMoA, Agent: refs[i].String(), Task: prompt})
				text, err := turn(refCtx, refs[i], memberPrompt, memberHistory)
				liveRun.End(err)
				stance := StanceNone
				if layer > 1 {
					stance, text = TakeStance(text)
				}
				results[i] = RefResult{Ref: refs[i], Output: text, Err: err}
				stances[i] = stance
				if cfg.Hooks.OnDone != nil {
					hook(func() { cfg.Hooks.OnDone(layer, results[i], stance) })
				}
			}(i, memberPrompt, memberHistory)
		}
		wg.Wait()
		out.Layers = append(out.Layers, results)
		out.Stances = append(out.Stances, stances)
		for _, i := range active {
			if results[i].OK() || layer == 1 {
				out.Answers[i] = results[i]
			}
		}
		if layer == 1 && countOK(results) == 0 {
			return out, allFailed(results)
		}
	}
	return out, nil
}

// activeMembers lists who answers a layer: everyone in the first, and from
// then on the members holding an answer.
func activeMembers(answers []RefResult, layer int) []int {
	active := make([]int, 0, len(answers))
	for i, a := range answers {
		if layer == 1 || a.OK() {
			active = append(active, i)
		}
	}
	return active
}

// peerAnswers is what the member in the given slot of active reads: the
// other members' answers, starting with the member after it, so each member
// sees them in a different order.
func peerAnswers(answers []RefResult, active []int, slot int) []string {
	peers := make([]string, 0, len(active)-1)
	for k := 1; k < len(active); k++ {
		peers = append(peers, answers[active[(slot+k)%len(active)]].Output)
	}
	return peers
}

// allFailed reports a layer where nobody answered, naming the first
// underlying error so the failure is diagnosable (provider not available,
// 401, ...) instead of opaque.
func allFailed(results []RefResult) error {
	for _, r := range results {
		if r.Err != nil {
			return fmt.Errorf("all %d reference models failed: %s: %w", len(results), r.Ref.String(), r.Err)
		}
	}
	return fmt.Errorf("all %d reference models failed", len(results))
}

// ReviewInstruction briefs a member for a cross-review round.
const ReviewInstruction = "You are one member of a panel of independent experts answering the user's request below. " +
	"You see your previous answer and the other members' answers from the previous round. " +
	"Critically evaluate all of them, yours included: some may be biased, outdated or wrong. " +
	"Then write your improved, complete answer to the request. Keep your position where you can defend it, " +
	"adopt a better argument where another member made one, and fix every error you find. " +
	"Do not agree just to converge, and do not mention the other members or this review. " +
	"Start with exactly one marker on its own line: [kept] if your bottom line is unchanged, [revised] if it changed. " +
	"Then give the answer, opening with its bottom line. Answer in the same language as the user's request."

// BuildReviewPrompt assembles one member's cross-review prompt.
func BuildReviewPrompt(userPrompt, own string, peers []string) string {
	var b strings.Builder
	b.WriteString(ReviewInstruction)
	b.WriteString("\n\n## User request\n")
	b.WriteString(userPrompt)
	b.WriteString("\n\n## Your previous answer\n")
	b.WriteString(strings.TrimSpace(own))
	b.WriteString("\n\n## Other members' answers\n")
	for i, p := range peers {
		fmt.Fprintf(&b, "\n### Answer %d\n%s\n", i+1, strings.TrimSpace(p))
	}
	b.WriteString("\n## Your improved answer\n")
	return b.String()
}

// stanceMarker matches the [kept]/[revised] marker a review answer opens
// with, tolerating emphasis around it.
var stanceMarker = regexp.MustCompile(`(?i)^[\s*_` + "`" + `]*\[(kept|revised)\][\s*_` + "`" + `]*`)

// TakeStance strips a review answer's stance marker and reports it;
// an answer without one is returned as is, with StanceNone.
func TakeStance(answer string) (Stance, string) {
	m := stanceMarker.FindStringSubmatchIndex(answer)
	if m == nil {
		return StanceNone, answer
	}
	stance := StanceKept
	if strings.EqualFold(answer[m[2]:m[3]], string(StanceRevised)) {
		stance = StanceRevised
	}
	return stance, strings.TrimSpace(answer[m[1]:])
}
