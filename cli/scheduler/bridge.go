/*
 * ChatCLI - Scheduler: CLIBridge interface.
 *
 * The scheduler lives inside the cli package tree but deliberately does
 * NOT import the top-level cli.ChatCLI struct — that would create a
 * circular dependency with command_handler.go, cli_completer.go,
 * agent_mode.go, and everything else that will in turn want to call
 * the scheduler.
 *
 * Instead, everything the scheduler needs from the host process is
 * abstracted behind CLIBridge. The implementation lives in
 * cli/scheduler_bridge.go (top-level cli package) and is constructed
 * at NewChatCLI time. Unit tests provide a mock implementation.
 *
 * The bridge surface is deliberately wide — a narrow surface would
 * force us to duplicate logic, and the scheduler is already a
 * privileged subsystem (it can fire any slash command the user can).
 */
package scheduler

import (
	"context"

	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// CLIBridge is implemented by the top-level cli.ChatCLI so the
// scheduler can invoke slash commands, dispatch agent tasks, query the
// LLM, fire hooks, and read K8s / workspace state.
type CLIBridge interface {
	// ExecuteSlashCommand runs a /foo command as if the user typed it.
	// Returns the output captured and whether the command asked to
	// exit the CLI loop.
	ExecuteSlashCommand(ctx context.Context, line string) (output string, exit bool, err error)

	// RunAgentTask boots a ReAct loop with the given task + system
	// prompt hint (same API as ChatCLI.agentMode.Run). Returns the
	// final assistant message.
	RunAgentTask(ctx context.Context, task, systemHint string) (string, error)

	// DispatchWorker runs a single worker (see cli/agent/workers) with
	// the named agent type and task. Useful as a lightweight action
	// when the user wants one-shot behavior without full ReAct state.
	DispatchWorker(ctx context.Context, agentType, task string) (string, error)

	// SendLLMPrompt calls the currently-configured LLM client outside
	// the chat history. Used by ActionLLMPrompt and llm_check
	// evaluator.
	SendLLMPrompt(ctx context.Context, system, prompt string, maxTokens int) (text string, tokens int, cost float64, err error)

	// FireHook dispatches a chatcli hook event synchronously and
	// returns the HookResult (for scheduler use of action_type=hook).
	FireHook(event hooks.HookEvent) *hooks.HookResult

	// RunShell executes a shell command under CoderMode safety rules
	// (same allowlist the agent mode enforces). When coderSafetyBypass
	// is true and the operator has explicitly granted it, the command
	// runs without the allowlist — reserved for trusted automation.
	RunShell(ctx context.Context, cmd string, envOverrides map[string]string, coderSafetyBypass bool) (stdout string, stderr string, exitCode int, err error)

	// KubeconfigPath returns the path the K8s evaluator should use
	// (honors CHATCLI_KUBECONFIG, KUBECONFIG, or the watcher's config).
	KubeconfigPath() string

	// DockerSocketPath returns the docker engine socket (env
	// DOCKER_HOST or unix:///var/run/docker.sock).
	DockerSocketPath() string

	// WorkspaceDir returns the chatcli workspace root (project dir or
	// CWD), used for resolving relative paths in conditions/actions.
	WorkspaceDir() string

	// LLMClient returns the currently-configured LLM client for
	// evaluators that want to issue their own prompts (llm_check).
	LLMClient() client.LLMClient

	// AppendHistory lets async actions add a message to the chat
	// history (tagged ownership=scheduler so compaction recognizes it).
	AppendHistory(msg models.Message)

	// PublishEvent forwards a scheduler Event to the live cli.bus
	// so the Ctrl+J overlay and the status line can react.
	PublishEvent(evt Event)
}

// noopBridge is used when scheduler runs without a host (daemon mode
// before any CLI has attached). Actions that require the bridge
// return an explanatory error via the specific executor.
type noopBridge struct{}

// NewNoopBridge returns a bridge whose methods all return stubs. Used
// in tests and by the daemon when no CLI is attached.
func NewNoopBridge() CLIBridge { return noopBridge{} }

func (noopBridge) ExecuteSlashCommand(_ context.Context, _ string) (string, bool, error) {
	return "", false, ErrNoDaemon
}
func (noopBridge) RunAgentTask(_ context.Context, _, _ string) (string, error) {
	return "", ErrNoDaemon
}
func (noopBridge) DispatchWorker(_ context.Context, _, _ string) (string, error) {
	return "", ErrNoDaemon
}
func (noopBridge) SendLLMPrompt(_ context.Context, _, _ string, _ int) (string, int, float64, error) {
	return "", 0, 0, ErrNoDaemon
}
func (noopBridge) FireHook(_ hooks.HookEvent) *hooks.HookResult { return nil }
func (noopBridge) RunShell(_ context.Context, _ string, _ map[string]string, _ bool) (string, string, int, error) {
	return "", "", -1, ErrNoDaemon
}
func (noopBridge) KubeconfigPath() string         { return "" }
func (noopBridge) DockerSocketPath() string       { return "" }
func (noopBridge) WorkspaceDir() string           { return "" }
func (noopBridge) LLMClient() client.LLMClient    { return nil }
func (noopBridge) AppendHistory(_ models.Message) {}
func (noopBridge) PublishEvent(_ Event)           {}
