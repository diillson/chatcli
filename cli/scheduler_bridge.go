/*
 * ChatCLI - scheduler_bridge.go
 *
 * Implements scheduler.CLIBridge on top of *ChatCLI. Owned by the CLI
 * package so the scheduler subpackage stays free of the circular
 * dependency the top-level struct would introduce.
 *
 * The bridge is stateless except for a back-pointer to ChatCLI. Every
 * method routes through existing chatcli code paths so scheduled work
 * behaves identically to interactive work — hooks fire, quality
 * pipeline runs, session policy applies, etc.
 */
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// schedulerBridge is the CLIBridge implementation.
type schedulerBridge struct {
	cli *ChatCLI
	mu  sync.Mutex

	// policyMu guards policyMgr; it's loaded lazily on the first
	// ClassifyShellCommand / RunShell call so an auth-only session
	// that never agendas shell jobs pays zero cost.
	policyMu  sync.RWMutex
	policyMgr *coder.PolicyManager
}

func newSchedulerBridge(cli *ChatCLI) *schedulerBridge {
	return &schedulerBridge{cli: cli}
}

// getPolicyManager returns a PolicyManager, lazy-loading the coder
// policy from disk on first use. A nil return means the manager
// failed to load (missing config, unreadable home dir) — callers MUST
// treat that as fail-closed (classify every command as Ask).
func (b *schedulerBridge) getPolicyManager() *coder.PolicyManager {
	b.policyMu.RLock()
	pm := b.policyMgr
	b.policyMu.RUnlock()
	if pm != nil {
		return pm
	}

	b.policyMu.Lock()
	defer b.policyMu.Unlock()
	if b.policyMgr != nil {
		return b.policyMgr
	}
	newPM, err := coder.NewPolicyManager(b.cli.logger)
	if err != nil {
		if b.cli.logger != nil {
			b.cli.logger.Warn("scheduler: failed to load coder policy manager; every shell command will be classified as Ask (fail-closed)",
				zap.Error(err))
		}
		return nil
	}
	b.policyMgr = newPM
	return newPM
}

// reloadPolicyManager forces a re-read of the on-disk policy. Used
// before the fire-time re-check so a policy update since the Enqueue
// is picked up (the user may have added a Deny rule minutes ago).
func (b *schedulerBridge) reloadPolicyManager() *coder.PolicyManager {
	b.policyMu.Lock()
	defer b.policyMu.Unlock()
	newPM, err := coder.NewPolicyManager(b.cli.logger)
	if err != nil {
		if b.cli.logger != nil {
			b.cli.logger.Warn("scheduler: failed to reload coder policy manager", zap.Error(err))
		}
		return b.policyMgr
	}
	b.policyMgr = newPM
	return newPM
}

// classifyCommand is the shared classification pipeline used by both
// ClassifyShellCommand (enqueue preflight) and RunShell (fire-time
// re-check). It wraps the raw shell command in the @coder exec
// envelope the PolicyManager expects — the same envelope
// agent_mode.go uses for its own tool-call checks — so scheduler
// allowlist/denylist behavior is identical to interactive coder
// mode.
func (b *schedulerBridge) classifyCommand(pm *coder.PolicyManager, cmd string) scheduler.ShellPolicy {
	if pm == nil {
		return scheduler.ShellPolicyAsk
	}
	args := buildCoderExecArgs(cmd)
	switch pm.Check("@coder", args) {
	case coder.ActionAllow:
		return scheduler.ShellPolicyAllow
	case coder.ActionDeny:
		return scheduler.ShellPolicyDeny
	case coder.ActionAsk:
		return scheduler.ShellPolicyAsk
	default:
		return scheduler.ShellPolicyAsk
	}
}

// buildCoderExecArgs serializes a raw shell command into the JSON
// envelope the coder PolicyManager normalizes against. Matching this
// shape is what lets the scheduler inherit the exact same
// allowlist/denylist the user has configured for /coder.
func buildCoderExecArgs(cmd string) string {
	payload := map[string]any{
		"cmd": "exec",
		"args": map[string]any{
			"cmd": cmd,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// Unreachable in practice (payload is a static shape);
		// return a syntactically valid empty envelope so Check can
		// still run its deny rules against the tool name.
		return "{}"
	}
	return string(b)
}

// ClassifyShellCommand is the enqueue-time preflight hook. See
// scheduler.CLIBridge for the contract.
func (b *schedulerBridge) ClassifyShellCommand(cmd string) scheduler.ShellPolicy {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		// Empty string cannot be executed anyway; treat as Allow so
		// upstream validation (which catches empties) produces the
		// clearer error instead of a surprising policy rejection.
		return scheduler.ShellPolicyAllow
	}
	return b.classifyCommand(b.getPolicyManager(), cmd)
}

// ExecuteSlashCommand runs a slash command as if the user typed it,
// capturing stdout.
func (b *schedulerBridge) ExecuteSlashCommand(_ context.Context, line string) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Capture stdout for the duration of the handler.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", false, err
	}
	os.Stdout = w
	var exit bool
	var panicErr error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				// Agent-mode panics (errAgentModeRequest / errExitRequest)
				// are flow-control, not errors. We capture and surface
				// them via the "exit" flag or an informative error.
				if rec == errExitRequest {
					exit = true
					return
				}
				if rec == errAgentModeRequest || rec == errCoderModeRequest {
					panicErr = fmt.Errorf("scheduler: slash_cmd %q requested agent/coder mode, not supported from scheduler", line)
					return
				}
				panicErr = fmt.Errorf("scheduler: slash_cmd panic: %v", rec)
			}
		}()
		if b.cli.commandHandler == nil {
			panicErr = fmt.Errorf("scheduler: commandHandler not initialized")
			return
		}
		if b.cli.commandHandler.HandleCommand(line) {
			exit = true
		}
	}()

	_ = w.Close()
	os.Stdout = origStdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String(), exit, panicErr
}

// RunAgentTask boots the ReAct loop with the given task.
func (b *schedulerBridge) RunAgentTask(ctx context.Context, task, systemHint string) (string, error) {
	if b.cli.agentMode == nil {
		return "", fmt.Errorf("scheduler: agent mode not initialized")
	}
	// AgentMode writes directly to stdout; capture it for the bridge.
	b.mu.Lock()
	defer b.mu.Unlock()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	runErr := b.cli.agentMode.Run(ctx, task, "", systemHint)
	_ = w.Close()
	os.Stdout = origStdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String(), runErr
}

// DispatchWorker runs a single worker. Not exposed yet — stub returns
// an explanatory error so scheduled agent_task actions still work.
func (b *schedulerBridge) DispatchWorker(_ context.Context, agentType, task string) (string, error) {
	// Route through the agent mode's orchestrator if available; else
	// fall back to a plain agent_task message.
	_ = agentType
	_ = task
	return "", fmt.Errorf("scheduler: worker_dispatch routed through agent_task (use RunAgentTask)")
}

// SendLLMPrompt runs a single LLM call using the currently-configured
// client.
func (b *schedulerBridge) SendLLMPrompt(ctx context.Context, system, prompt string, maxTokens int) (string, int, float64, error) {
	c := b.cli.Client
	if c == nil {
		return "", 0, 0, fmt.Errorf("scheduler: no LLM client configured")
	}
	if maxTokens <= 0 {
		maxTokens = 512
	}
	hist := []models.Message{}
	if strings.TrimSpace(system) != "" {
		hist = append(hist, models.Message{Role: "system", Content: system})
	}
	text, err := c.SendPrompt(ctx, prompt, hist, maxTokens)
	if err != nil {
		return "", 0, 0, err
	}
	// Token/cost accounting — if a cost tracker is wired, the
	// underlying provider will have updated it; we leave the numbers
	// at zero from the bridge's point of view.
	return text, 0, 0, nil
}

// FireHook dispatches a hook event synchronously.
func (b *schedulerBridge) FireHook(event hooks.HookEvent) *hooks.HookResult {
	if b.cli.hookManager == nil {
		return nil
	}
	return b.cli.hookManager.Fire(event)
}

// RunShell executes a shell command with scheduler-scoped safety. When
// coderSafetyBypass is true and the operator permitted it via
// CHATCLI_SCHEDULER_SHELL_ALLOW_BYPASS=true, the command runs without
// the coder policy allowlist.
//
// Defense-in-depth: even though Enqueue already preflight-checked this
// command via ClassifyShellCommand, we reload the on-disk policy here
// and re-check. This catches the case where the operator adds a deny
// rule (or removes an allow) between a cron job's schedule and its
// fire — the job fails instead of running something that's now
// disallowed.
func (b *schedulerBridge) RunShell(ctx context.Context, cmd string, envOverrides map[string]string, coderSafetyBypass bool) (string, string, int, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", "", -1, fmt.Errorf("scheduler shell: empty command")
	}
	allowBypass := strings.EqualFold(strings.TrimSpace(os.Getenv("CHATCLI_SCHEDULER_SHELL_ALLOW_BYPASS")), "true")
	if coderSafetyBypass && !allowBypass {
		return "", "", -1, fmt.Errorf("scheduler shell: bypass_safety requires CHATCLI_SCHEDULER_SHELL_ALLOW_BYPASS=true")
	}

	// Fire-time policy re-check (skipped only when an operator
	// explicitly authorized the bypass above).
	if !coderSafetyBypass {
		switch b.classifyCommand(b.reloadPolicyManager(), cmd) {
		case scheduler.ShellPolicyDeny:
			return "", "", -1, fmt.Errorf("%w: policy denies %q at fire time",
				scheduler.ErrShellPolicyDeny, truncateBridge(cmd, 120))
		case scheduler.ShellPolicyAsk:
			// A command that passed enqueue (either Allow or
			// Ask + DangerousConfirmed) but whose classification
			// has since drifted to Ask still fails here. The
			// bridge does not see DangerousConfirmed at this
			// layer, so the safe choice is to refuse and let
			// the scheduler record the failure.
			return "", "", -1, fmt.Errorf("%w: policy requires approval for %q at fire time",
				scheduler.ErrShellPolicyAsk, truncateBridge(cmd, 120))
		case scheduler.ShellPolicyAllow:
			// proceed
		}
	}

	shellCmd := "/bin/sh"
	shellFlag := "-c"
	if runtime.GOOS == "windows" {
		shellCmd = "cmd.exe"
		shellFlag = "/C"
	}
	execCmd := exec.CommandContext(ctx, shellCmd, shellFlag, cmd) //#nosec G204 -- operator-scheduled; preflight + fire-time policy checks gate arbitrary input
	if len(envOverrides) > 0 {
		env := os.Environ()
		for k, v := range envOverrides {
			env = append(env, k+"="+v)
		}
		execCmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr
	err := execCmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
			// Completed process — err is not a transport error.
			return stdout.String(), stderr.String(), code, nil
		}
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), 0, nil
}

// KubeconfigPath resolves the kubeconfig for evaluators.
func (b *schedulerBridge) KubeconfigPath() string {
	for _, env := range []string{"CHATCLI_KUBECONFIG", "KUBECONFIG"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// DockerSocketPath resolves the Docker engine socket.
func (b *schedulerBridge) DockerSocketPath() string {
	if v := strings.TrimSpace(os.Getenv("DOCKER_HOST")); v != "" {
		return v
	}
	return ""
}

// WorkspaceDir resolves the chatcli workspace root.
func (b *schedulerBridge) WorkspaceDir() string {
	if wd := detectProjectDir(); wd != "" {
		return wd
	}
	wd, _ := os.Getwd()
	return wd
}

// LLMClient exposes the live client.
func (b *schedulerBridge) LLMClient() client.LLMClient { return b.cli.Client }

// AppendHistory inserts a message into chat history.
func (b *schedulerBridge) AppendHistory(msg models.Message) {
	b.cli.mu.Lock()
	defer b.cli.mu.Unlock()
	b.cli.history = append(b.cli.history, msg)
}

// PublishEvent forwards scheduler events into the CLI event bus /
// status line. The Ctrl+J overlay listens to version bumps; this hook
// is where we'd also push into a dedicated channel if desired.
func (b *schedulerBridge) PublishEvent(evt scheduler.Event) {
	// Ensure the status-line refresh cycle notices fresh events by
	// force-refreshing the go-prompt prefix. Cheap no-op if not in an
	// interactive loop.
	b.cli.markSchedulerDirty()
	_ = evt
	_ = time.Now
}

// truncateBridge mirrors scheduler.truncate but lives here so we
// don't export a helper from the scheduler package for one caller.
func truncateBridge(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
