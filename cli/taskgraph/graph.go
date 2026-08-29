/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package taskgraph turns an approved multi-task plan into a persisted DAG
 * executed by parallel squad workers, where "done" is never the executor's
 * self-report: each task carries a validation contract, the engine runs the
 * deterministic gate itself, and an independent reviewer worker with a fresh
 * context issues the verdict that promotes (or fails) the task.
 *
 * This file holds the graph model and its parsing/validation. Structural
 * validation (unique ids, deps declared earlier, no cycles) is delegated to
 * the existing quality.Plan DAG instead of being reimplemented.
 */
package taskgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/agent/workers"
)

// Status is the lifecycle state of a task (and, at run level, of the graph).
type Status string

// Task/graph lifecycle states. A task is promoted to StatusDone exclusively
// by the engine after its gate and review pass — never by an executor.
const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusReviewing Status = "reviewing"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusBlocked   Status = "blocked"
)

// Terminal reports whether no further transitions are possible.
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusFailed || s == StatusBlocked
}

// ValidationStep is one deterministic check of a task's contract. Run is a
// shell command the ENGINE executes (exit 0 = pass); Expect is prose handed
// to the reviewer describing what the output must show. A step with an empty
// Run is prose-only: it skips the gate and informs the reviewer.
type ValidationStep struct {
	Run    string `json:"run,omitempty"`
	Expect string `json:"expect,omitempty"`
}

// ValidationList tolerates the three shapes the model produces for
// "validation": an array of steps, a single step object, or a bare prose
// string (which becomes one prose-only step).
type ValidationList []ValidationStep

// UnmarshalJSON implements the lenient decoding described on the type.
func (v *ValidationList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*v = nil
		return nil
	}
	switch trimmed[0] {
	case '[':
		var steps []ValidationStep
		if err := json.Unmarshal(data, &steps); err != nil {
			return err
		}
		*v = steps
	case '{':
		var step ValidationStep
		if err := json.Unmarshal(data, &step); err != nil {
			return err
		}
		*v = ValidationList{step}
	default:
		var prose string
		if err := json.Unmarshal(data, &prose); err != nil {
			return err
		}
		if strings.TrimSpace(prose) == "" {
			*v = nil
			return nil
		}
		*v = ValidationList{{Expect: prose}}
	}
	return nil
}

// GateResult records one executed validation command and its outcome.
type GateResult struct {
	Run    string `json:"run"`
	Output string `json:"output,omitempty"` // tail-truncated
	Passed bool   `json:"passed"`
}

// Attempt is the audit record of one executor→gate→reviewer cycle. RunIDs
// point into the agent run registry, proving executor and reviewer were
// distinct fresh workers.
type Attempt struct {
	N              int          `json:"n"`
	ExecutorRunID  string       `json:"executor_run_id,omitempty"`
	ExecutorOutput string       `json:"executor_output,omitempty"` // tail-truncated
	Gate           []GateResult `json:"gate,omitempty"`
	ReviewerRunID  string       `json:"reviewer_run_id,omitempty"`
	Verdict        string       `json:"verdict,omitempty"` // "PASS" | "FAIL"
	Evidence       string       `json:"evidence,omitempty"`
	FailureReason  string       `json:"failure_reason,omitempty"`
	CostUSD        float64      `json:"cost_usd,omitempty"`
	StartedAt      time.Time    `json:"started_at,omitempty"`
	EndedAt        time.Time    `json:"ended_at,omitempty"`
}

// Phase groups tasks into swimlanes for status rendering (and the dashboard).
type Phase struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// Task is one node of the graph.
type Task struct {
	ID    string `json:"id"`
	Phase string `json:"phase,omitempty"`
	Title string `json:"title,omitempty"`
	Agent string `json:"agent,omitempty"` // worker agent type; default "coder"
	// Prompt is what the executor worker receives. "task" and
	// "description" are accepted as aliases at parse time.
	Prompt        string         `json:"prompt,omitempty"`
	AltTask       string         `json:"task,omitempty"`
	AltDesc       string         `json:"description,omitempty"`
	Deps          []string       `json:"deps,omitempty"`
	Validation    ValidationList `json:"validation,omitempty"`
	RequireReview *bool          `json:"require_review,omitempty"` // nil = inherit graph default
	MaxAttempts   int            `json:"max_attempts,omitempty"`   // default defaultMaxAttempts
	// Tools grants the EXECUTOR worker session plugins for this task
	// (@browser, @websearch, mcp_*). Opt-in; the reviewer never gets them.
	Tools []string `json:"tools,omitempty"`

	// Runtime state — owned by the engine, persisted with the graph.
	Status    Status    `json:"status,omitempty"`
	Attempts  []Attempt `json:"attempts,omitempty"`
	CostUSD   float64   `json:"cost_usd,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// Graph is a full run: the plan plus its execution state.
type Graph struct {
	Name          string  `json:"name"`
	MaxParallel   int     `json:"max_parallel,omitempty"`
	RequireReview *bool   `json:"require_review,omitempty"` // default true
	Phases        []Phase `json:"phases,omitempty"`
	Tasks         []*Task `json:"tasks"`

	// Runtime state.
	RunID     string    `json:"run_id,omitempty"`
	Status    Status    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

const defaultMaxAttempts = 3

// graphJSONRE hunts the outermost JSON object so a plan wrapped in markdown
// fences or prose still parses.
var graphJSONRE = regexp.MustCompile(`(?s)\{.*\}`)

// ParseGraph decodes, normalizes and validates a graph plan from raw model
// output (JSON, possibly fenced or surrounded by prose).
func ParseGraph(raw string) (*Graph, error) {
	body := strings.TrimSpace(raw)
	if body == "" {
		return nil, errors.New("empty task graph plan")
	}
	if idx := strings.Index(body, "```"); idx >= 0 {
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
	blob := graphJSONRE.FindString(body)
	if blob == "" {
		return nil, errors.New("no JSON object found in task graph plan")
	}
	var g Graph
	if err := json.Unmarshal([]byte(blob), &g); err != nil {
		return nil, fmt.Errorf("task graph JSON unmarshal failed: %w", err)
	}
	g.Normalize()
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return &g, nil
}

// Normalize applies defaults and folds the prompt aliases so the rest of the
// package only ever reads canonical fields.
func (g *Graph) Normalize() {
	if g.MaxParallel <= 0 {
		g.MaxParallel = 0 // engine applies its configured default
	}
	if g.Status == "" {
		g.Status = StatusPending
	}
	for _, t := range g.Tasks {
		if t == nil {
			continue
		}
		if strings.TrimSpace(t.Prompt) == "" {
			if strings.TrimSpace(t.AltTask) != "" {
				t.Prompt = t.AltTask
			} else if strings.TrimSpace(t.AltDesc) != "" {
				t.Prompt = t.AltDesc
			}
		}
		t.AltTask, t.AltDesc = "", ""
		t.ID = strings.TrimSpace(t.ID)
		t.Agent = strings.ToLower(strings.TrimSpace(t.Agent))
		if t.Agent == "" {
			t.Agent = "coder"
		}
		if len(t.Tools) > 0 {
			t.Tools = workers.NormalizePluginGrant(t.Tools)
		}
		if strings.TrimSpace(t.Title) == "" {
			t.Title = firstLine(t.Prompt)
		}
		if t.MaxAttempts <= 0 {
			t.MaxAttempts = defaultMaxAttempts
		}
		if t.Status == "" {
			t.Status = StatusPending
		}
	}
}

// Validate checks structural invariants. The id/dep/cycle rules are enforced
// by converting to the existing quality.Plan DAG (deps must reference
// earlier-declared tasks, which also rules out cycles); graph-specific rules
// (phase references) are checked here.
func (g *Graph) Validate() error {
	if len(g.Tasks) == 0 {
		return errors.New("task graph has no tasks")
	}
	plan := &quality.Plan{Steps: make([]*quality.PlanStep, 0, len(g.Tasks))}
	for i, t := range g.Tasks {
		if t == nil {
			return fmt.Errorf("task %d is null", i)
		}
		plan.Steps = append(plan.Steps, &quality.PlanStep{
			ID:    t.ID,
			Agent: t.Agent,
			Task:  t.Prompt,
			Deps:  t.Deps,
		})
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("task graph invalid: %w", err)
	}
	phases := make(map[string]bool, len(g.Phases))
	for _, ph := range g.Phases {
		phases[ph.ID] = true
	}
	for _, t := range g.Tasks {
		if t.Phase != "" && len(phases) > 0 && !phases[t.Phase] {
			return fmt.Errorf("task %s references unknown phase %q", t.ID, t.Phase)
		}
	}
	return nil
}

// RequiresReview resolves the effective review requirement for a task:
// task override wins, then the graph default, then true — verification is
// opt-out, never silently absent.
func (g *Graph) RequiresReview(t *Task) bool {
	if t.RequireReview != nil {
		return *t.RequireReview
	}
	if g.RequireReview != nil {
		return *g.RequireReview
	}
	return true
}

// TaskByID returns the task with the given id, or nil.
func (g *Graph) TaskByID(id string) *Task {
	for _, t := range g.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// CountByStatus tallies tasks per status for compact summaries.
func (g *Graph) CountByStatus() map[Status]int {
	out := make(map[Status]int, 6)
	for _, t := range g.Tasks {
		out[t.Status]++
	}
	return out
}

// TotalCostUSD sums the attributed cost of every task.
func (g *Graph) TotalCostUSD() float64 {
	var sum float64
	for _, t := range g.Tasks {
		sum += t.CostUSD
	}
	return sum
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if r := []rune(t); len(r) > 80 {
				return string(r[:80]) + "…"
			}
			return t
		}
	}
	return ""
}
