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

	"github.com/diillson/chatcli/cli/agent/workers"
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
//
// `/run`, `/agent`, `/coder` are intercepted here and routed directly
// to the agent ReAct loop via RunAgentTask / RunCoderTask. The REPL
// implementation of those commands relies on panic-flow control
// (errAgentModeRequest / errCoderModeRequest) to switch the main loop
// into agent mode — that flow is meaningless from the scheduler's
// dispatcher goroutine, so we route through the bridge directly. This
// makes the documented "do: /run X" / "/agent X" / "/coder X" forms
// fire correctly from a scheduled job.
//
// dangerousConfirmed flows from Job.DangerousConfirmed so the headless
// PolicyChecker can admit "Ask" classifications when the job was
// pre-authorized via --i-know / i_know:true.
func (b *schedulerBridge) ExecuteSlashCommand(ctx context.Context, line string, dangerousConfirmed bool) (string, bool, error) {
	if task, kind, ok := classifyAgentSlash(line); ok {
		if task == "" {
			return "", false, fmt.Errorf(
				"scheduler: slash_cmd %q requires a task argument (e.g. %q)",
				strings.TrimSpace(line), strings.TrimSpace(line)+" do something",
			)
		}
		if kind == agentSlashKindCoder {
			out, err := b.RunCoderTask(ctx, task, dangerousConfirmed)
			return out, false, err
		}
		out, err := b.RunAgentTask(ctx, task, "", dangerousConfirmed)
		return out, false, err
	}

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
				// errExitRequest / unexpected agent-mode panics are
				// flow-control. classifyAgentSlash already drains
				// /run|/agent|/coder upstream, so reaching the
				// errAgentModeRequest / errCoderModeRequest branch
				// here means a different slash command unexpectedly
				// requested mode-switch — surface it explicitly.
				if rec == errExitRequest {
					exit = true
					return
				}
				if rec == errAgentModeRequest || rec == errCoderModeRequest {
					panicErr = fmt.Errorf(
						"scheduler: slash_cmd %q unexpectedly requested an agent/coder mode switch — "+
							"only /run, /agent, /coder are routed through the agent loop from the scheduler",
						line,
					)
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

// agentSlashKind discriminates between the three slash forms that
// boot the agent loop. /run and /agent share the agent profile; /coder
// uses CoderSystemPrompt and the coder execution profile.
type agentSlashKind int

const (
	agentSlashKindAgent agentSlashKind = iota
	agentSlashKindCoder
)

// classifyAgentSlash recognizes "/run …", "/agent …", "/coder …"
// (with whitespace separator) and bare "/run", "/agent", "/coder".
// Returns the task body, the kind (agent vs coder profile), and ok=true.
// `task` is empty when the user passed a bare command without args —
// the caller surfaces a helpful error in that case.
func classifyAgentSlash(line string) (task string, kind agentSlashKind, ok bool) {
	t := strings.TrimSpace(line)
	matchPrefix := func(prefix string) (string, bool) {
		if t == prefix {
			return "", true
		}
		if len(t) > len(prefix) && (t[len(prefix)] == ' ' || t[len(prefix)] == '\t') &&
			strings.HasPrefix(t, prefix) {
			return strings.TrimSpace(t[len(prefix):]), true
		}
		return "", false
	}
	if body, hit := matchPrefix("/coder"); hit {
		return body, agentSlashKindCoder, true
	}
	if body, hit := matchPrefix("/agent"); hit {
		return body, agentSlashKindAgent, true
	}
	if body, hit := matchPrefix("/run"); hit {
		return body, agentSlashKindAgent, true
	}
	return "", 0, false
}

// RunAgentTask runs a scheduled `/run` or `/agent` task as a HEADLESS
// ReAct loop using the same engine subagents use (workers.RunDelegate).
// Critical: we do NOT call cli.agentMode.Run() — that path is
// interactive (it spawns a stdin reader, prompts for permissions on
// stdin, and writes Bubble-Tea-style UI to the user's terminal). When
// the scheduler dispatcher fires from a background goroutine while the
// user is at the chat/coder prompt, those side-effects fight with the
// active session and lock the terminal. The headless worker has no
// stdin reader, no TUI, and uses a non-prompting PolicyChecker so it
// can fire cleanly while the user keeps typing.
//
// systemHint is currently advisory: workers.RunDelegate composes its
// own subagent system prompt. We pass non-empty hints (e.g. the coder
// system prompt) as `system_preface`.
//
// dangerousConfirmed flows to the PolicyChecker so jobs with --i-know
// can call tools that would otherwise hit ShellPolicyAsk.
func (b *schedulerBridge) RunAgentTask(ctx context.Context, task, systemHint string, dangerousConfirmed bool) (string, error) {
	return b.runHeadlessAgent(ctx, task, systemHint, "scheduled /run task", dangerousConfirmed)
}

// RunCoderTask runs a scheduled `/coder` task headless. Same engine as
// RunAgentTask; the coder personality is conveyed by passing
// CoderSystemPrompt as the subagent's system_preface. We deliberately
// do NOT mutate cli.executionProfile or cli.agentMode.isCoderMode —
// those are user-session globals and the scheduler dispatcher must not
// touch them while the user is interactive.
func (b *schedulerBridge) RunCoderTask(ctx context.Context, task string, dangerousConfirmed bool) (string, error) {
	return b.runHeadlessAgent(ctx, task, CoderSystemPrompt, "scheduled /coder task", dangerousConfirmed)
}

// runHeadlessAgent runs a scheduler-driven /run|/agent|/coder task as
// a headless ReAct loop. Bypasses workers.RunDelegate because that
// path frames the LLM as "a focused subagent delegated by a parent
// agent" — which makes the model refuse top-level GUI/system tasks
// like "open -a Docker" with hallucinated apologetics ("I'm a CLI
// subagent, I cannot launch desktop applications") even though it
// has the exec tool available. Scheduler-driven tasks have no parent;
// they're top-level firings that need to actually do work.
//
// We compose our own system prompt that: (a) drops the subagent
// framing, (b) explicitly tells the LLM to use the exec tool for
// shell-style work, (c) layers in the user's systemPreface (e.g.
// CoderSystemPrompt for /coder) on top, and (d) lists the same full
// tool set workers/subagent.go grants for read_only=false runs.
func (b *schedulerBridge) runHeadlessAgent(ctx context.Context, task, systemPreface, description string, dangerousConfirmed bool) (string, error) {
	if b.cli == nil || b.cli.Client == nil {
		return "", fmt.Errorf("scheduler: LLM client not initialized")
	}
	if strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("scheduler: empty task body")
	}

	tools := []string{
		"read", "write", "patch", "tree", "search", "exec",
		"git-status", "git-diff", "git-log", "git-changed", "git-branch", "test",
	}

	cfg := workers.WorkerReActConfig{
		MaxTurns:        15,
		SystemPrompt:    buildSchedulerSystemPrompt(systemPreface, tools),
		AllowedCommands: tools,
		ReadOnly:        false,
	}

	// description is informational (job-history label, future telemetry).
	// Future: pipe through to the worker as a metadata tag so /jobs logs
	// can render it next to the result. For now the call site just
	// records it via the scheduler's own emit path.
	_ = description

	pc := newSchedulerPolicyChecker(b, b.cli.logger, dangerousConfirmed)
	res, err := workers.RunWorkerReAct(
		ctx,
		cfg,
		task,
		b.cli.Client,
		workers.NewFileLockManager(),
		nil, // skills — not propagated to scheduled tasks for now
		pc,
		b.cli.logger,
	)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", fmt.Errorf("scheduler: headless agent returned nil result")
	}
	return res.Output, nil
}

// buildSchedulerSystemPrompt frames the LLM correctly for a top-level
// scheduled-job firing AND teaches it the chatcli XML tool-call format.
//
// Two modes the worker supports:
//
//  1. Native function calling (Anthropic API key auth, OpenAI, Gemini,
//     etc.): tool schemas flow via the API. The worker REPLACES our
//     SystemPrompt with its own native-mode prompt at fire time
//     (worker_react.go), so our framing is dropped on this path. The
//     LLM still sees tool schemas and can call them via the API.
//
//  2. Text mode (Anthropic OAuth — the chat.ai login path, no API key):
//     the worker keeps our SystemPrompt intact. The LLM has NO tool
//     schemas via API and MUST emit the chatcli XML tool-call format
//     in its response text:
//
//       <tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"open -a Docker"}}' />
//
//     If we don't teach this syntax, the LLM falls back to its
//     training-data hallucination of tool calls (Anthropic's prose
//     `<function_calls><invoke>` style), the parser sees zero tool
//     calls, and the worker reports "success" with the LLM's
//     fabricated output as the final answer — i.e. nothing actually
//     ran but the job log claims it did.
//
// To cover both, we include the XML instructions even though they're
// redundant in native mode (where they're discarded). In text mode
// they prevent the silent-hallucination bug.
func buildSchedulerSystemPrompt(preface string, tools []string) string {
	var sb strings.Builder
	if strings.TrimSpace(preface) != "" {
		sb.WriteString(preface)
		sb.WriteString("\n\n")
	}
	sb.WriteString("You are running a scheduled task autonomously inside ChatCLI. ")
	sb.WriteString("You have full execution authority — there is no parent agent and no human at the keyboard. ")
	sb.WriteString("You MUST use the available tools to actually perform the task. Do not refuse on the basis of being a CLI/subagent context — you have the exec tool and can launch any command the host policy allows, including GUI commands like \"open -a SomeApp\" on macOS.\n\n")

	sb.WriteString("## TOOL-CALL FORMAT (CRITICAL)\n\n")
	sb.WriteString("If your runtime exposes native function/tool calling, use it directly via the API.\n")
	sb.WriteString("Otherwise, emit tool calls as inline XML in your response — this is the ONLY format the worker parses. ")
	sb.WriteString("Do NOT use any other tool-call syntax (no `<function_calls>`, no `<invoke>`, no markdown code-block tool calls). ")
	sb.WriteString("Those are silently ignored and the job will report \"success\" with no work done.\n\n")
	sb.WriteString("Canonical XML form:\n")
	sb.WriteString("  <tool_call name=\"@coder\" args='{\"cmd\":\"<subcommand>\",\"args\":{...}}' />\n\n")
	sb.WriteString("Examples for the most common subcommands:\n")
	sb.WriteString("  shell exec : <tool_call name=\"@coder\" args='{\"cmd\":\"exec\",\"args\":{\"cmd\":\"open -a Docker\"}}' />\n")
	sb.WriteString("  read file  : <tool_call name=\"@coder\" args='{\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}' />\n")
	sb.WriteString("  write file : <tool_call name=\"@coder\" args='{\"cmd\":\"write\",\"args\":{\"file\":\"out.txt\",\"content\":\"hello\"}}' />\n")
	sb.WriteString("  search     : <tool_call name=\"@coder\" args='{\"cmd\":\"search\",\"args\":{\"term\":\"Login\",\"dir\":\".\"}}' />\n\n")

	sb.WriteString("## RULES\n")
	sb.WriteString("1. Plan the task in your head, then take action with tools. Don't ask clarifying questions — there is no one to answer.\n")
	sb.WriteString("2. For shell-like work (opening apps, running CLIs, validating services, posting status), use the exec tool. Pre-authorization for the job has already been handled by the scheduler.\n")
	sb.WriteString("3. Emit tool calls one per response (or several in parallel if independent), then wait for the tool result before continuing. The worker injects the result and re-prompts you.\n")
	sb.WriteString("4. After all tools have run, produce a short, structured final summary describing what you did and the outcome — that summary becomes the job's history entry shown via /jobs logs.\n")
	sb.WriteString("5. If a tool fails, surface the error in your summary instead of looping silently. Never fabricate tool output: only describe results that came back from real tool calls.\n")
	sb.WriteString(fmt.Sprintf("\nAvailable @coder subcommands: %s\n", strings.Join(tools, ", ")))
	if tmp := os.Getenv("CHATCLI_AGENT_TMPDIR"); tmp != "" {
		sb.WriteString(fmt.Sprintf("\nScratch dir (read/write allowed): %s  (exposed as $CHATCLI_AGENT_TMPDIR to exec)\n", tmp))
	}
	return sb.String()
}

// schedulerPolicyChecker satisfies workers.PolicyChecker for headless
// scheduled tasks. It NEVER prompts — the scheduler runs without a
// human at the keyboard, so any "Ask" classification must fail closed
// unless the job pre-authorized via i_know. This mirrors the same
// semantics used by ClassifyShellCommand at enqueue/fire time.
//
// dangerousConfirmed mirrors Job.DangerousConfirmed (set by --i-know
// on /schedule or i_know:true on @scheduler) and is threaded all the
// way down from action.Execute → bridge.ExecuteSlashCommand /
// RunAgentTask → runHeadlessAgent → here. Ask + dangerousConfirmed
// admits; denylist still rejects regardless (deny beats --i-know).
type schedulerPolicyChecker struct {
	bridge             *schedulerBridge
	logger             *zap.Logger
	dangerousConfirmed bool
}

func newSchedulerPolicyChecker(b *schedulerBridge, logger *zap.Logger, dangerousConfirmed bool) *schedulerPolicyChecker {
	return &schedulerPolicyChecker{bridge: b, logger: logger, dangerousConfirmed: dangerousConfirmed}
}

// CheckAndPrompt classifies a tool call against the live coder policy.
// Allow → permit. Deny → reject with the classification reason. Ask →
// admit when the job pre-authorized via i_know; otherwise reject with
// a hint pointing at /config security allow or shell:/--i-know.
// Never blocks on stdin.
func (p *schedulerPolicyChecker) CheckAndPrompt(_ context.Context, toolName, args string) (bool, string) {
	pm := p.bridge.getPolicyManager()
	if pm == nil {
		return false, "scheduler: policy manager unavailable; refusing tool call (fail-closed)"
	}
	switch pm.Check(toolName, args) {
	case coder.ActionAllow:
		return true, ""
	case coder.ActionDeny:
		// Denylist always wins — even with i_know.
		return false, fmt.Sprintf("scheduler: tool %q denied by coder policy", toolName)
	case coder.ActionAsk:
		if p.dangerousConfirmed {
			return true, ""
		}
		return false, fmt.Sprintf(
			"scheduler: tool %q requires user confirmation (\"Ask\") — scheduled tasks have no human to prompt. "+
				"Pre-authorize this job at enqueue with i_know:true (e.g. {\"cmd\":\"schedule\",\"args\":{...,\"i_know\":true}}) "+
				"or add a persistent allow rule via /config security allow.",
			toolName,
		)
	default:
		return false, fmt.Sprintf("scheduler: tool %q has unrecognized policy classification", toolName)
	}
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
// dangerousConfirmed mirrors Job.DangerousConfirmed (set by --i-know
// on /schedule or i_know:true on @scheduler). When true, the fire-time
// recheck admits "Ask" classifications — the user already pre-authorized
// at enqueue. Denylist still rejects regardless (deny beats --i-know).
//
// Defense-in-depth: even though Enqueue already preflight-checked this
// command via ClassifyShellCommand, we reload the on-disk policy here
// and re-check. This catches the case where the operator adds a deny
// rule (or removes an allow) between a cron job's schedule and its
// fire — the job fails instead of running something that's now
// disallowed.
func (b *schedulerBridge) RunShell(ctx context.Context, cmd string, envOverrides map[string]string, coderSafetyBypass, dangerousConfirmed bool) (string, string, int, error) {
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
			// Deny always wins — even with --i-know.
			return "", "", -1, fmt.Errorf("%w: policy denies %q at fire time",
				scheduler.ErrShellPolicyDeny, truncateBridge(cmd, 120))
		case scheduler.ShellPolicyAsk:
			if !dangerousConfirmed {
				return "", "", -1, fmt.Errorf("%w: policy requires approval for %q at fire time",
					scheduler.ErrShellPolicyAsk, truncateBridge(cmd, 120))
			}
			// dangerousConfirmed=true: user pre-authorized at
			// enqueue via --i-know / i_know:true. Proceed.
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
