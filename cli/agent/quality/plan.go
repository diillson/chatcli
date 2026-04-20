/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Phase 2 (#2) — Plan-and-Solve / ReWOO core types and parser.
 *
 * The structured plan is a small, deliberately conservative DAG: every
 * step has an id (E1, E2…), an agent type, a task string with optional
 * #E<n> placeholders, and an optional dependency list. The runner
 * topologically sorts the steps, resolves placeholders against earlier
 * step outputs, and dispatches each step through the existing worker
 * dispatcher — no parallel new infrastructure.
 */
package quality

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// Plan is the parsed structured plan produced by PlannerAgent in
// JSON-output mode. JSON tags match the plan schema documented in
// docs/SEVEN_PATTERNS_PLAN.md §6.
type Plan struct {
	TaskSummary    string      `json:"task_summary"`
	Steps          []*PlanStep `json:"steps"`
	ParallelGroups [][]string  `json:"parallel_groups,omitempty"`
}

// PlanStep is one node in the DAG. Deps and ParallelGroups are mutually
// reinforcing: ParallelGroups defines what may run together; Deps is
// what an earlier-numbered step needs as inputs (used for placeholder
// resolution and topo sort).
type PlanStep struct {
	ID    string   `json:"id"`
	Agent string   `json:"agent"`
	Task  string   `json:"task"`
	Deps  []string `json:"deps,omitempty"`
}

// placeholderRE matches #E1, #E2, …, with optional .head=N or .summary
// suffix. Captures: (1) full step id, (2) optional modifier.
var placeholderRE = regexp.MustCompile(`#(E\d+)(?:\.([a-zA-Z][a-zA-Z0-9=]*))?`)

// jsonExtractRE finds the first JSON object in an arbitrary text blob —
// the planner LLM may wrap its output in markdown code fences or trailing
// prose, so we hunt for the outermost {...}.
var jsonExtractRE = regexp.MustCompile(`(?s)\{.*\}`)

// ParsePlan extracts and validates a structured plan from the planner's
// raw output. It tolerates surrounding markdown/prose by hunting for the
// first balanced JSON object. Returns (nil, err) when parsing fails or
// the plan is malformed (no steps, unknown deps, etc.).
func ParsePlan(raw string) (*Plan, error) {
	body := strings.TrimSpace(raw)
	if body == "" {
		return nil, errors.New("planner returned empty output")
	}
	// Strip ```json … ``` fences if present.
	if idx := strings.Index(body, "```"); idx >= 0 {
		// Drop everything before and including the first ```; keep up to
		// the next ``` if there is one.
		afterOpen := body[idx+3:]
		if nl := strings.IndexByte(afterOpen, '\n'); nl >= 0 {
			afterOpen = afterOpen[nl+1:]
		}
		if closing := strings.LastIndex(afterOpen, "```"); closing >= 0 {
			body = afterOpen[:closing]
		} else {
			body = afterOpen
		}
	}
	jsonBlob := jsonExtractRE.FindString(body)
	if jsonBlob == "" {
		return nil, fmt.Errorf("no JSON object found in planner output: %q", truncate(raw, 120))
	}
	var p Plan
	if err := json.Unmarshal([]byte(jsonBlob), &p); err != nil {
		return nil, fmt.Errorf("plan JSON unmarshal failed: %w", err)
	}
	if len(p.Steps) == 0 {
		return nil, errors.New("plan has no steps")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks structural invariants: every id is unique, every Dep
// references an existing earlier step, and every #E<n> placeholder in a
// task points at a declared id. Returns the first violation found.
func (p *Plan) Validate() error {
	ids := make(map[string]int) // id → index for ordering check
	for i, s := range p.Steps {
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("step %d has empty id", i)
		}
		if _, dup := ids[s.ID]; dup {
			return fmt.Errorf("duplicate step id %q", s.ID)
		}
		if strings.TrimSpace(s.Agent) == "" {
			return fmt.Errorf("step %s has empty agent", s.ID)
		}
		if strings.TrimSpace(s.Task) == "" {
			return fmt.Errorf("step %s has empty task", s.ID)
		}
		ids[s.ID] = i
	}
	for _, s := range p.Steps {
		for _, dep := range s.Deps {
			depIdx, ok := ids[dep]
			if !ok {
				return fmt.Errorf("step %s depends on unknown id %q", s.ID, dep)
			}
			if depIdx >= ids[s.ID] {
				return fmt.Errorf("step %s depends on later step %s", s.ID, dep)
			}
		}
		// Validate placeholders point at declared ids.
		for _, m := range placeholderRE.FindAllStringSubmatch(s.Task, -1) {
			if _, ok := ids[m[1]]; !ok {
				return fmt.Errorf("step %s references unknown placeholder #%s", s.ID, m[1])
			}
		}
	}
	return nil
}

// TopologicalOrder returns step ids ordered so every dep appears before
// the steps that need it. Falls back to the declared order on ties.
// Assumes the plan has already been Validated (no cycles, no forward
// deps).
func (p *Plan) TopologicalOrder() []string {
	indeg := make(map[string]int, len(p.Steps))
	rdeps := make(map[string][]string, len(p.Steps))
	for _, s := range p.Steps {
		indeg[s.ID] = len(s.Deps)
		for _, d := range s.Deps {
			rdeps[d] = append(rdeps[d], s.ID)
		}
	}
	// Use a stable-ordered ready queue (sort ids lexicographically) so
	// the result is reproducible across runs of the same plan.
	var ready []string
	for _, s := range p.Steps {
		if indeg[s.ID] == 0 {
			ready = append(ready, s.ID)
		}
	}
	sort.Strings(ready)
	out := make([]string, 0, len(p.Steps))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, id)
		next := rdeps[id]
		sort.Strings(next)
		for _, n := range next {
			indeg[n]--
			if indeg[n] == 0 {
				ready = append(ready, n)
			}
		}
	}
	if len(out) != len(p.Steps) {
		// Validate should have caught this; defensive fallback to
		// declared order.
		out = out[:0]
		for _, s := range p.Steps {
			out = append(out, s.ID)
		}
	}
	return out
}

// ResolvePlaceholders rewrites #E1, #E1.head=N, #E1.summary in task
// against the previously-recorded outputs. Unknown placeholders are
// left as-is so the agent can flag them in the LLM call.
//
// Modifiers:
//   - bare           → full output (trimmed)
//   - .head=N        → first N runes (truncated with ellipsis if cut)
//   - .summary       → first non-empty line (handy for one-line outputs)
//   - .last=N        → last N runes
func ResolvePlaceholders(task string, outputs map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(task, func(match string) string {
		groups := placeholderRE.FindStringSubmatch(match)
		id := groups[1]
		mod := groups[2]
		out, ok := outputs[id]
		if !ok {
			return match
		}
		out = strings.TrimSpace(out)
		switch {
		case mod == "" || mod == "all":
			return out
		case mod == "summary":
			for _, line := range strings.Split(out, "\n") {
				if t := strings.TrimSpace(line); t != "" {
					return t
				}
			}
			return out
		case strings.HasPrefix(mod, "head="):
			n := atoiSafe(strings.TrimPrefix(mod, "head="))
			return headRunes(out, n)
		case strings.HasPrefix(mod, "last="):
			n := atoiSafe(strings.TrimPrefix(mod, "last="))
			return lastRunes(out, n)
		default:
			return out
		}
	})
}

// ToAgentCalls converts plan steps into AgentCall instances ready for
// the existing dispatcher.Dispatch path. Used by PlanRunner per step
// (we dispatch one at a time so #E<n> outputs accumulate).
func (p *PlanStep) ToAgentCall(resolvedTask string) workers.AgentCall {
	return workers.AgentCall{
		Agent: workers.AgentType(strings.ToLower(strings.TrimSpace(p.Agent))),
		Task:  resolvedTask,
		ID:    p.ID,
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func headRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func lastRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
