/*
 * Package builtins wires the built-in condition evaluators and action
 * executors into a Scheduler.
 *
 * Lives in its own subpackage so the scheduler package can be imported
 * by condition/ and action/ without triggering an import cycle.
 * RegisterAll depends on the three sibling packages simultaneously —
 * condition/, action/, and scheduler/ — and is the only place where
 * that triangle of imports exists.
 *
 * Usage:
 *
 *   s, err := scheduler.New(cfg, bridge, deps, logger)
 *   if err != nil { ... }
 *   builtins.RegisterAll(s)
 *   s.Start(ctx)
 */
package builtins

import (
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/cli/scheduler/action"
	"github.com/diillson/chatcli/cli/scheduler/condition"
)

// RegisterAll installs every standard condition evaluator and action
// executor into the Scheduler's registries. Call exactly once, after
// scheduler.New and before Scheduler.Start.
func RegisterAll(s *scheduler.Scheduler) {
	condition.RegisterAll(s.Conditions())
	action.RegisterAll(s.Actions())

	// Wire the composite evaluators to the registry so they can
	// resolve their children. (Composite.SetRegistry is a one-shot
	// setter; the evaluator was added by RegisterAll above.)
	for _, t := range []string{"all_of", "any_of"} {
		if e, ok := s.Conditions().Get(t); ok {
			if c, ok := e.(*condition.Composite); ok {
				c.SetRegistry(s.Conditions())
			}
		}
	}
}
