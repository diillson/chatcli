/*
 * ChatCLI - Scheduler: evaluator + executor interfaces and registries.
 *
 * The scheduler stays extensible by deferring "how do I evaluate
 * k8s readiness" to a ConditionEvaluator plug-in, and "how do I run a
 * shell command" to an ActionExecutor. Both interfaces are small and
 * synchronous; the scheduler owns concurrency, the plug-in owns
 * semantics.
 *
 * Registration happens at scheduler construction time (see
 * builtin.go's RegisterBuiltins helper). Users who want to add a custom
 * evaluator / executor implement the interface and call
 * scheduler.ConditionRegistry().Register(...) before Start.
 */
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// ─── Condition evaluator ──────────────────────────────────────

// EvalOutcome is the result of a single condition evaluation.
type EvalOutcome struct {
	Satisfied bool
	// Transient means the evaluator is confident the check can be retried
	// (network blip, temporary HTTP error). Persistent errors (bad URL,
	// unknown k8s resource) bubble up via Err and drive the breaker.
	Transient bool
	// Details is shown in the /jobs show output to explain a poll result.
	Details string
	Err     error
}

// ConditionEvaluator is a plug-in that knows how to check one condition
// type. Methods are pure from the scheduler's standpoint — the caller
// will wrap Evaluate in a breaker and a timeout.
type ConditionEvaluator interface {
	// Type is the string that appears in Condition.Type.
	Type() string

	// ValidateSpec is called once at job admission. Failure here blocks
	// Enqueue with ErrInvalidCondition.
	ValidateSpec(spec map[string]any) error

	// Evaluate performs one check. ctx carries a timeout set by the
	// scheduler (from Budget.PollInterval by default). Network-bound
	// evaluators must honor ctx.Done().
	Evaluate(ctx context.Context, cond Condition, env *EvalEnv) EvalOutcome
}

// EvalEnv bundles runtime dependencies an evaluator may need (logger,
// the chatcli bridge for K8s client reuse, etc.). Passed by value so
// the plug-in doesn't accidentally hold a pointer across turns.
type EvalEnv struct {
	Logger *zap.Logger
	// Bridge is an optional interface for plug-ins that need to reach
	// into the host ChatCLI (for example to read a K8s kubeconfig path).
	// See CLIBridge in tool_adapter.go for the current contract.
	Bridge CLIBridge
	// DangerousConfirmed mirrors Job.DangerousConfirmed. Wait
	// conditions that shell out (k8s/llm/regex/shell_exit) must
	// forward it to Bridge.RunShell so a job admitted with --i-know
	// at enqueue passes its fire-time recheck on the wait predicate
	// too — not just on the action.
	DangerousConfirmed bool
}

// ConditionRegistry holds the set of registered evaluators.
type ConditionRegistry struct {
	mu    sync.RWMutex
	items map[string]ConditionEvaluator
}

// NewConditionRegistry builds an empty registry.
func NewConditionRegistry() *ConditionRegistry {
	return &ConditionRegistry{items: make(map[string]ConditionEvaluator)}
}

// Register adds an evaluator. Duplicates are rejected.
func (r *ConditionRegistry) Register(e ConditionEvaluator) error {
	t := strings.TrimSpace(e.Type())
	if t == "" {
		return errors.New("scheduler: evaluator with empty type")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[t]; exists {
		return fmt.Errorf("scheduler: evaluator %q already registered", t)
	}
	r.items[t] = e
	return nil
}

// MustRegister panics on duplicate — suitable for RegisterBuiltins.
func (r *ConditionRegistry) MustRegister(e ConditionEvaluator) {
	if err := r.Register(e); err != nil {
		panic(err)
	}
}

// Get returns the evaluator for a type, or (nil, false).
func (r *ConditionRegistry) Get(t string) (ConditionEvaluator, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.items[strings.TrimSpace(t)]
	return e, ok
}

// Types returns the registered type names, sorted.
func (r *ConditionRegistry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.items))
	for k := range r.items {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─── Action executor ──────────────────────────────────────────

// ActionResult is what an executor returns after a single invocation.
type ActionResult struct {
	Output string
	Tokens int
	Cost   float64
	// Transient mirrors EvalOutcome.Transient — retryable vs permanent.
	Transient bool
	Err       error
}

// ActionExecutor is a plug-in that can run one action type.
type ActionExecutor interface {
	Type() ActionType
	ValidateSpec(payload map[string]any) error
	Execute(ctx context.Context, action Action, env *ExecEnv) ActionResult
}

// ExecEnv bundles dependencies action executors may need. Mirrors
// EvalEnv but distinct because actions often need the full CLI bridge
// (run a slash command, invoke an agent, fire a hook).
type ExecEnv struct {
	Logger *zap.Logger
	Bridge CLIBridge
	// Job is a read-only summary of the job being executed, useful for
	// error messages ("action failed for job X").
	Job JobSummary
}

// ActionRegistry mirrors ConditionRegistry.
type ActionRegistry struct {
	mu    sync.RWMutex
	items map[ActionType]ActionExecutor
}

// NewActionRegistry builds an empty registry.
func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{items: make(map[ActionType]ActionExecutor)}
}

// Register adds an executor.
func (r *ActionRegistry) Register(e ActionExecutor) error {
	t := e.Type()
	if strings.TrimSpace(string(t)) == "" {
		return errors.New("scheduler: executor with empty type")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[t]; exists {
		return fmt.Errorf("scheduler: executor %q already registered", t)
	}
	r.items[t] = e
	return nil
}

// MustRegister panics on duplicate.
func (r *ActionRegistry) MustRegister(e ActionExecutor) {
	if err := r.Register(e); err != nil {
		panic(err)
	}
}

// Get returns the executor for a type.
func (r *ActionRegistry) Get(t ActionType) (ActionExecutor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.items[t]
	return e, ok
}

// Types returns the registered type names, sorted.
func (r *ActionRegistry) Types() []ActionType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ActionType, 0, len(r.items))
	for k := range r.items {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
