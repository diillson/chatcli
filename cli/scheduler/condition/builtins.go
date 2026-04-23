/*
 * Package condition: built-in evaluators registry.
 *
 * RegisterAll adds every evaluator shipped with chatcli to the given
 * registry. Called once from cli/scheduler.RegisterBuiltins at
 * scheduler construction time. Idempotent — re-registration of the
 * same set on an empty registry is a no-op; on a populated registry it
 * will refuse with ErrAlreadyRegistered (via scheduler.Register).
 */
package condition

import "github.com/diillson/chatcli/cli/scheduler"

// RegisterAll installs every built-in evaluator.
func RegisterAll(r *scheduler.ConditionRegistry) {
	r.MustRegister(NewShellExit())
	r.MustRegister(NewHTTPStatus())
	r.MustRegister(NewFileExists())
	r.MustRegister(NewK8sReady())
	r.MustRegister(NewDockerRunning())
	r.MustRegister(NewTCPReachable())
	r.MustRegister(NewRegexMatch())
	r.MustRegister(NewLLMCheck())
	r.MustRegister(NewCustom())
	r.MustRegister(NewComposite("all_of"))
	r.MustRegister(NewComposite("any_of"))
}
