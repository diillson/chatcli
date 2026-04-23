/*
 * Package action: built-in executors registry.
 */
package action

import "github.com/diillson/chatcli/cli/scheduler"

// RegisterAll installs every built-in executor.
func RegisterAll(r *scheduler.ActionRegistry) {
	r.MustRegister(NewSlashCmd())
	r.MustRegister(NewShell())
	r.MustRegister(NewAgentTask())
	r.MustRegister(NewWorkerDispatch())
	r.MustRegister(NewLLMPrompt())
	r.MustRegister(NewWebhook())
	r.MustRegister(NewHookAction())
	r.MustRegister(NewNoop())
}
