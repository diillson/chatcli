/*
 * Composite — evaluator that evaluates a conjunction (all_of) or
 * disjunction (any_of) of child conditions. Children are expressed as
 * Condition.Children on the parent — the scheduler delegates to the
 * composite evaluator, which in turn resolves each child's Type via
 * the registry.
 *
 * Short-circuiting:
 *   all_of — first non-satisfied / erroring child wins.
 *   any_of — first satisfied child wins; errors don't stop the scan.
 *
 * Negation: set Condition.Negate=true on either the composite or any
 * child. The scheduler applies negation on the outer result; composites
 * respect it natively on children too.
 */
package condition

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/scheduler"
)

// Composite implements scheduler.ConditionEvaluator.
type Composite struct {
	typeName string

	// lookupMu guards an optional registry reference. The registry is
	// set once after construction via SetRegistry — we can't inject it
	// at NewComposite time because builtins.go registers before the
	// Scheduler has wired everything.
	lookupMu sync.RWMutex
	lookup   *scheduler.ConditionRegistry
}

// NewComposite returns a composite evaluator bound to the given type
// name ("all_of" or "any_of").
func NewComposite(typeName string) *Composite {
	return &Composite{typeName: typeName}
}

// Type returns the Condition.Type literal.
func (c *Composite) Type() string { return c.typeName }

// SetRegistry binds the evaluator to a registry so it can resolve
// children. Called by scheduler.RegisterBuiltins after construction.
func (c *Composite) SetRegistry(r *scheduler.ConditionRegistry) {
	c.lookupMu.Lock()
	defer c.lookupMu.Unlock()
	c.lookup = r
}

// ValidateSpec enforces presence of children (validated by
// scheduler.Condition.Validate already, but double-checked here).
func (c *Composite) ValidateSpec(spec map[string]any) error {
	// No spec keys are read for composites — children live on the
	// Condition.Children field handled by scheduler.
	return nil
}

// Evaluate walks the children.
func (c *Composite) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	c.lookupMu.RLock()
	reg := c.lookup
	c.lookupMu.RUnlock()
	if reg == nil {
		return scheduler.EvalOutcome{Err: fmt.Errorf("%s: registry not wired", c.typeName)}
	}
	if len(cond.Children) == 0 {
		return scheduler.EvalOutcome{Err: fmt.Errorf("%s: no children", c.typeName)}
	}

	isAll := c.typeName == "all_of"
	overall := isAll // all_of starts true; any_of starts false
	details := []string{}
	var firstErr error
	transient := true

	for i, child := range cond.Children {
		eval, ok := reg.Get(child.Type)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("child[%d] unknown type %q", i, child.Type)
			}
			if isAll {
				overall = false
				break
			}
			continue
		}
		out := eval.Evaluate(ctx, child, env)
		if child.Negate {
			out.Satisfied = !out.Satisfied
		}
		details = append(details, fmt.Sprintf("[%d]%s:%v", i, child.Type, out.Satisfied))
		if out.Err != nil && firstErr == nil {
			firstErr = out.Err
			transient = out.Transient
		}
		if isAll {
			overall = overall && out.Satisfied
			if !overall && out.Err == nil {
				break
			}
		} else {
			overall = overall || out.Satisfied
			if overall {
				// Any_of satisfied; ignore later errors.
				firstErr = nil
				break
			}
		}
	}

	if firstErr != nil && (isAll && !overall || !isAll && !overall) {
		return scheduler.EvalOutcome{
			Err:       firstErr,
			Transient: transient,
			Details:   strings.Join(details, ", "),
		}
	}
	return scheduler.EvalOutcome{
		Satisfied: overall,
		Details:   strings.Join(details, ", "),
	}
}
