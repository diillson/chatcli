/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/cli/paste"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// AgentMode representa a funcionalidade de agente autônomo no ChatCLI
type AgentMode struct {
	cli                 *ChatCLI
	logger              *zap.Logger
	executor            *agent.CommandExecutor
	validator           *agent.CommandValidator
	contextManager      *agent.ContextManager
	taskTracker         *agent.TaskTracker
	executeCommandsFunc func(ctx context.Context, block agent.CommandBlock) (string, string)
	isCoderMode         bool
	isOneShot           bool
	coderBannerShown    bool
	lastPolicyMatch     *coder.Rule
	// Métricas
	turnTimer      *metrics.Timer
	agentsLaunched int // total de sub-agents lançados na sessão
	toolCallsExecd int // total de tool calls executadas na sessão
	// Multi-Agent Orchestration
	agentDispatcher *workers.Dispatcher
	agentRegistry   *workers.Registry
	fileLockMgr     *workers.FileLockManager
	policyAdapter   *workerPolicyAdapter
	parallelMode    bool
	// Seven-pattern quality pipeline (Self-Refine, CoVe, Reflexion, …).
	// qualityPipeline is wired into the dispatcher; qualityConfig holds
	// the env-loaded snapshot so /config quality can render it without
	// re-parsing env vars on every invocation.
	qualityPipeline *quality.Pipeline
	qualityConfig   quality.Config
	// Centralized stdin reader for type-ahead queue support
	stdinLines   chan string   // all stdin lines flow through here
	stdinDone    chan struct{} // signals reader goroutine to stop
	stdinWg      sync.WaitGroup
	multilineBuf MultilineBuffer // ``` delimited multiline input

	// Skill hints captured at the start of Run() from auto-activated or
	// manually invoked skills. Applied to each LLM turn via ctx for the
	// duration of the agent loop. Cleared when the loop exits.
	skillModelHint  string
	skillEffortHint llmclient.SkillEffort

	// Session-scoped flag: true once we have warned the user that the
	// history is approaching likely corporate-proxy payload limits and
	// no explicit CHATCLI_MAX_PAYLOAD is configured. Prevents the warning
	// from being emitted every turn.
	proxyPayloadWarned bool
}

// startStdinReader starts a goroutine that reads lines from stdin and sends
// them to the stdinLines channel. This centralizes all stdin reads in agent
// mode, enabling type-ahead queue support.
//
// Uses stdinPollReady (poll(2) on Unix, WaitForSingleObject on Windows) to
// check for available input before calling os.Stdin.Read. This ensures the
// goroutine never blocks for more than ~50ms, so it can check stdinDone and
// exit cleanly when agent mode ends — without requiring the user to press Enter.
func (a *AgentMode) startStdinReader() {
	a.stdinLines = make(chan string, 10)
	a.stdinDone = make(chan struct{})
	a.stdinWg.Add(1)

	go func() {
		defer a.stdinWg.Done()
		var lineBuf strings.Builder
		buf := make([]byte, 512)
		for {
			select {
			case <-a.stdinDone:
				return
			default:
			}

			// Poll stdin with 50ms timeout. On Unix (Linux/macOS) this uses
			// poll(2) which correctly handles TTY fds. On Windows this uses
			// WaitForSingleObject on the console input handle.
			if !stdinPollReady(50 * time.Millisecond) {
				continue // timeout — loop back and check stdinDone
			}

			// Data available — read won't block (on Unix).
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}

			for i := 0; i < n; i++ {
				if buf[i] == '\n' {
					rawLine := lineBuf.String() + "\n"
					lineBuf.Reset()

					// Detect and clean paste content
					cleaned, pasteInfo := paste.DetectInLine(rawLine)
					if pasteInfo != nil {
						if pasteInfo.LineCount > 1 {
							fmt.Printf("  %s\n", i18n.T("paste.detected", pasteInfo.CharCount, pasteInfo.LineCount))
						} else {
							fmt.Printf("  %s\n", i18n.T("paste.detected.short", pasteInfo.CharCount))
						}
						rawLine = cleaned
					}

					line := strings.TrimSpace(rawLine)

					select {
					case <-a.stdinDone:
						return
					case a.stdinLines <- line:
					}
				} else {
					lineBuf.WriteByte(buf[i])
				}
			}
		}
	}()
}

// stopStdinReader signals the stdin reader goroutine to stop and waits for it
// to exit. On Unix (Linux/macOS), the goroutine exits within ~50ms (one poll
// cycle). On Windows, it may be blocked in os.Stdin.Read after a
// WaitForSingleObject false positive; a safety timeout prevents indefinite
// blocking.
func (a *AgentMode) stopStdinReader() {
	if a.stdinDone != nil {
		close(a.stdinDone)

		// Wait with safety timeout. On Unix the goroutine exits in ~50ms.
		// On Windows it might be stuck in a blocking Read; don't wait forever.
		done := make(chan struct{})
		go func() {
			a.stdinWg.Wait()
			close(done)
		}()
		select {
		case <-done:
			// Clean exit
		case <-time.After(500 * time.Millisecond):
			// Goroutine stuck on blocking read (Windows edge case).
			// It will discard any data and exit on the next stdin input.
		}

		a.stdinLines = nil
		a.stdinDone = nil
	}
}

// readLine reads a single line from the centralized stdin reader.
// Falls back to direct stdin read if the reader is not active.
func (a *AgentMode) readLine() string {
	if a.stdinLines != nil {
		return <-a.stdinLines
	}
	// Fallback: direct stdin read
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// stdinStdout is an io.ReadWriter that reads from stdin and writes to stdout.
// Required by term.NewTerminal which needs a single ReadWriter.
type stdinStdout struct{}

func (stdinStdout) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdinStdout) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

// readLineWithEditing reads a single line with full terminal line-editing support
// (arrow keys, Home, End, Ctrl+A/E, backspace, delete, etc.) using golang.org/x/term.
// This is used for coder mode interactive input where the user needs to edit text.
func (a *AgentMode) readLineWithEditing() (string, error) {
	fd := int(os.Stdin.Fd()) // #nosec G115 -- Fd() returns uintptr, safe on all supported platforms

	// Put terminal into raw mode so term.Terminal can handle escape sequences
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		// Fallback to simple read if raw mode fails (e.g., piped input)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line), nil
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	// term.NewTerminal provides full readline: arrow keys, Ctrl+A/E, word
	// movement, backspace, delete — all processed correctly.
	t := term.NewTerminal(stdinStdout{}, "  > ")
	line, err := t.ReadLine()
	if err != nil {
		return "", err
	}

	trimmed := strings.TrimSpace(line)

	// Support multiline delimiter: if the user types "---", enter multiline mode
	// using the standard multilineBuf accumulator.
	if trimmed == "---" || trimmed == "```" {
		_ = term.Restore(fd, oldState) // restore terminal for multiline input
		a.multilineBuf.ProcessLine(trimmed)
		fmt.Printf("\n  \033[90m📝 %s\033[0m\n", i18n.T("multiline.hint", a.multilineBuf.Delimiter()))
		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Printf("  \033[90m... [%d] \033[0m", a.multilineBuf.LineCount()+1)
			nextLine, _ := reader.ReadString('\n')
			nextLine = strings.TrimRight(nextLine, "\r\n")
			complete, fullText := a.multilineBuf.ProcessLine(nextLine)
			if complete {
				return strings.TrimSpace(fullText), nil
			}
		}
	}

	return trimmed, nil
}

// drainStdinToQueue moves any pending stdin lines into the message queue.
// Returns the first message if any, for immediate injection into conversation.
func (a *AgentMode) drainStdinToQueue() string {
	var first string
	for {
		select {
		case line := <-a.stdinLines:
			if line == "" {
				continue // skip empty lines (bare Enter presses)
			}
			if first == "" {
				first = line
			} else {
				a.cli.messageQueueMu.Lock()
				a.cli.messageQueue = append(a.cli.messageQueue, line)
				a.cli.messageQueueMu.Unlock()
			}
		default:
			return first
		}
	}
}

// Aliases de tipos para manter compatibilidade
type (
	CommandBlock       = agent.CommandBlock
	CommandOutput      = agent.CommandOutput
	CommandContextInfo = agent.CommandContextInfo
	SourceType         = agent.SourceType
)

// Constantes re-exportadas
const (
	SourceTypeUserInput     = agent.SourceTypeUserInput
	SourceTypeFile          = agent.SourceTypeFile
	SourceTypeCommandOutput = agent.SourceTypeCommandOutput
)

// sttyPath is the resolved absolute path to the stty binary (resolved once at
// init time via exec.LookPath to avoid PATH-based injection).
var sttyPath = func() string {
	if p, err := exec.LookPath("stty"); err == nil {
		return p
	}
	return "stty" // fallback for systems where LookPath fails
}()

// NewAgentMode cria uma nova instância do modo agente
func NewAgentMode(cli *ChatCLI, logger *zap.Logger) *AgentMode {
	a := &AgentMode{
		cli:            cli,
		logger:         logger,
		executor:       agent.NewCommandExecutor(logger),
		validator:      agent.NewCommandValidator(logger),
		contextManager: agent.NewContextManager(logger),
		taskTracker:    agent.NewTaskTracker(logger),
		turnTimer:      metrics.NewTimer(),
	}
	a.executeCommandsFunc = a.executeCommandsWithOutput
	return a
}

// getInput obtém entrada do usuário de forma segura.
// Uses the centralized stdin reader when active, falls back to direct read.
func (a *AgentMode) getInput(promptStr string) string {
	if runtime.GOOS != "windows" {
		cmd := exec.Command(sttyPath, "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	// Enable bracketed paste mode for paste detection
	if runtime.GOOS != "windows" || os.Getenv("WT_SESSION") != "" {
		_, _ = os.Stdout.WriteString("\x1b[?2004h")
		defer func() { _, _ = os.Stdout.WriteString("\x1b[?2004l") }()
	}

	fmt.Print(promptStr)

	// Use centralized stdin reader (paste detection already handled there)
	return a.readLine()
}

// clientAndCtxForTurn resolves the LLM client and context for a single ReAct
// turn, honoring any skill model/effort hints captured at Run() start.
//
// Delegates model resolution to ChatCLI.resolveSkillClient so chat mode and
// agent mode share the same logic: API-cached lookup → catalog → family
// heuristic → optimistic user-provider attempt → graceful fallback with a
// user-visible message when the hint's target provider is unavailable.
//
// The effort hint is attached to ctx so the provider's SendPrompt can opt
// into extended thinking / reasoning_effort for this single call.
//
// Neither hint mutates a.cli.Client / a.cli.Provider / a.cli.Model — the
// user's active choices are preserved across turns.
func (a *AgentMode) clientAndCtxForTurn(ctx context.Context) (llmclient.LLMClient, context.Context) {
	turnClient := a.cli.Client
	if a.skillModelHint != "" {
		resolution := a.cli.resolveSkillClient(a.skillModelHint)
		turnClient = resolution.Client
		// Log once per turn — the ReAct loop can spin many times and we
		// don't want to spam. agent_mode.go only calls this helper from
		// the turn loop, so a single log per turn is acceptable.
		if resolution.Changed {
			a.logger.Debug("agent turn: skill model hint honored",
				zap.String("note", resolution.Note),
				zap.String("to_provider", resolution.Provider),
				zap.String("to_model", resolution.Model))
		}
	}
	// Honour /thinking session override before falling back to the skill
	// effort hint. EffortUnset inside an active override means "thinking
	// explicitly off" → skip both branches so the provider sends no hint.
	if eff, overridden := a.cli.applyThinkingOverride(a.skillEffortHint); overridden {
		if eff != llmclient.EffortUnset {
			ctx = llmclient.WithEffortHint(ctx, eff)
		}
	} else if a.skillEffortHint != llmclient.EffortUnset {
		ctx = llmclient.WithEffortHint(ctx, a.skillEffortHint)
	}
	return turnClient, ctx
}

// Run inicia o modo agente com uma consulta do usuário, utilizando um loop de Raciocínio-Ação (ReAct).
// Agora aceita systemPromptOverride para definir personas específicas (ex: Coder).
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string, systemPromptOverride string) error {
	// --- 1. CONFIGURAÇÃO E PREPARAÇÃO DO AGENTE ---
	maxTurns := AgentMaxTurns()

	// Load the seven-pattern quality config eagerly so HyDE retrieval
	// (built BEFORE initMultiAgent) sees the right toggles. The
	// dispatcher pipeline wiring inside initMultiAgent re-reads the
	// same env, so the two views stay consistent.
	a.qualityConfig = quality.LoadFromEnv()
	// Apply session-level /refine and /verify overrides on top of env.
	if a.cli.qualityOverrides.Refine != nil {
		a.qualityConfig.Refine.Enabled = *a.cli.qualityOverrides.Refine
	}
	if a.cli.qualityOverrides.Verify != nil {
		a.qualityConfig.Verify.Enabled = *a.cli.qualityOverrides.Verify
	}

	// Save checkpoint before agent starts (for rewind support)
	a.cli.saveCheckpoint()

	a.logger.Info("Modo Agente iniciado", zap.Int("max_turns_limit", maxTurns))

	var systemInstruction string
	isCoder := (systemPromptOverride == CoderSystemPrompt)
	hasActivePersona := a.cli.personaHandler != nil && a.cli.personaHandler.GetManager().HasActiveAgent()

	// Lógica de Composição de Prompt:
	// 1. Se há persona ativa: Persona + Instruções de Formato (Coder ou Agent)
	// 2. Se não há persona: Prompt completo padrão (Coder ou Agent)
	if hasActivePersona {
		// Combina: Persona (quem a IA é) + Instruções de Formato (como responder)
		personaPrompt := a.cli.personaHandler.GetManager().GetSystemPrompt()
		activeAgent := a.cli.personaHandler.GetManager().GetActiveAgent()

		if isCoder {
			// Persona + Instruções do Coder (tool_call, base64, etc.)
			systemInstruction = personaPrompt + "\n\n" + CoderFormatInstructions
			a.logger.Info("Usando persona ativa + modo coder", zap.String("agent", activeAgent.Name))
		} else {
			// Persona + Instruções do Agent (execute:shell, reasoning, etc.)
			systemInstruction = personaPrompt + "\n\n" + AgentFormatInstructions
			a.logger.Info("Usando persona ativa + modo agent", zap.String("agent", activeAgent.Name))
		}
	} else if isCoder {
		// Sem persona: Prompt completo do Coder
		systemInstruction = CoderSystemPrompt
	} else {
		// Sem persona: Prompt padrão do Agent (Admin de Sistema / Shell)
		osName := runtime.GOOS
		shellName := utils.GetUserShell()
		currentDir, _ := os.Getwd()
		systemInstruction = i18n.T("agent.system_prompt.default.base", osName, shellName, currentDir)
	}

	// Append language instruction so the AI responds in the user's locale
	systemInstruction += "\n\n" + i18n.T("ai.response_language")

	a.isCoderMode = isCoder
	a.isOneShot = false

	// Prepend workspace context (SOUL.md, USER.md, IDENTITY.md, RULES.md, MEMORY.md)
	// Extract hints from recent history for smart memory retrieval
	if a.cli.contextBuilder != nil {
		var hints []string
		hintWindow := 3
		if len(a.cli.history) < hintWindow {
			hintWindow = len(a.cli.history)
		}
		if hintWindow > 0 {
			var recentTexts []string
			for _, msg := range a.cli.history[len(a.cli.history)-hintWindow:] {
				recentTexts = append(recentTexts, msg.Content)
			}
			hints = memory.ExtractKeywords(recentTexts)
		}
		// Phase 3 (#4): when HyDE is enabled in /config quality, use the
		// HyDE-aware retrieval (hypothesis expansion + optional vector
		// cosine). Falls through to the legacy keyword-only path
		// otherwise to preserve byte-for-byte behaviour.
		var wsCtx string
		if a.qualityConfig.HyDE.Enabled && a.qualityConfig.Enabled {
			wsCtx = a.cli.hydeRetrieveContext(ctx, query, hints, a.qualityConfig)
		} else {
			wsCtx = a.cli.contextBuilder.BuildSystemPromptPrefixWithHints(hints)
		}
		if wsCtx != "" {
			systemInstruction = wsCtx + "\n\n---\n\n" + systemInstruction
		}
		if dynCtx := a.cli.contextBuilder.BuildDynamicContext(); dynCtx != "" {
			systemInstruction += "\n\n" + dynCtx
		}
	}

	// Adiciona contexto de ferramentas (plugins) ao prompt
	systemInstruction += a.getToolContextString()

	// Session workspace + large-output handling hint.
	// Tells the model about $CHATCLI_AGENT_TMPDIR (writable scratch dir it
	// can stage temp scripts / data in) and the "full output saved to PATH"
	// marker pattern used by tool_result_budget — so the model knows it
	// should read_file on those paths instead of asking for the data again.
	systemInstruction += buildSessionWorkspaceHint()

	// Fase 5: Inject auto-activated skills (triggers + path globs) into the
	// agent-mode system prompt. Also honors a `/<skill-name>` manual
	// invocation that was staged right before Run() was called — that is
	// how `/coder` and `/agent` interplay with skill invocation.
	//
	// Only fires once at the start of the loop so we don't inflate cache
	// across tool iterations.
	//
	// Reset hints from any previous Run() so a second `/agent` call
	// without an active skill does not inherit the old model/effort.
	a.skillModelHint = ""
	a.skillEffortHint = llmclient.EffortUnset
	if a.cli.personaHandler != nil {
		mgr := a.cli.personaHandler.GetManager()
		if mgr != nil {
			filePaths := extractFilePaths(query + " " + additionalContext)
			activated := mgr.FindAutoActivatedSkills(query, filePaths)
			if len(activated) > 0 {
				systemInstruction += "\n\n" + buildSkillInjectionBlock(activated)
				// Honor the first skill's model/effort hint for the worker
				// by recording them onto the agent mode state. The ReAct
				// loop reads them via contextual WithEffortHint wrappers
				// in processAIResponseAndAct.
				model, effort, _ := pickSkillModelAndEffort(activated)
				if model != "" {
					a.skillModelHint = model
				}
				if effort != "" {
					a.skillEffortHint = llmclient.NormalizeEffort(effort)
				}
				a.logger.Info("agent mode: auto-activated skills injected",
					zap.Int("count", len(activated)),
					zap.String("model_hint", a.skillModelHint),
					zap.String("effort_hint", string(a.skillEffortHint)))
			}
		}
	}
	if a.cli.pendingManualSkill != nil {
		manual := a.cli.pendingManualSkill
		manualArgs := a.cli.pendingManualSkillArgs
		a.cli.pendingManualSkill = nil
		a.cli.pendingManualSkillArgs = ""
		if block := renderManualSkillBlock(manual, manualArgs); block != "" {
			systemInstruction += "\n\n" + block
		}
		if m := strings.TrimSpace(manual.Model); m != "" {
			a.skillModelHint = m
		}
		if e := strings.TrimSpace(manual.Effort); e != "" {
			a.skillEffortHint = llmclient.NormalizeEffort(e)
		}
	}

	// If we captured a skill model hint, pre-resolve it once so the user
	// sees exactly what will run (or why their preference is being
	// ignored) before the agent loop starts burning turns.
	if a.skillModelHint != "" {
		a.cli.ensureModelCacheWarm()
		preview := a.cli.resolveSkillClient(a.skillModelHint)
		if preview.Changed {
			fmt.Printf("  %s\n", colorize(
				fmt.Sprintf("skill model hint: running agent on %s/%s",
					preview.Provider, preview.Model),
				ColorGray))
		} else if preview.UserMessage != "" {
			fmt.Printf("  %s\n", colorize("⚠ "+preview.UserMessage, ColorYellow))
		}
	}

	// Multi-Agent Orchestration: sempre ativo nos modos /agent e /coder.
	// A env CHATCLI_AGENT_PARALLEL_MODE pode desativar explicitamente (=false ou =0).
	if a.initMultiAgent() {
		systemInstruction += workers.OrchestratorSystemPrompt(a.agentRegistry.CatalogString())
	}

	// Banner curto com cheat sheet no modo /coder (apenas para o usuário humano)
	if isCoder && !a.coderBannerShown && isCoderBannerEnabled() {
		fmt.Println()
		fmt.Println(i18n.T("coder.quick_tip.header"))
		fmt.Println(i18n.T("coder.quick_tip.read"))
		fmt.Println(i18n.T("coder.quick_tip.search"))
		fmt.Println(i18n.T("coder.quick_tip.write"))
		fmt.Println(i18n.T("coder.quick_tip.exec"))
		fmt.Println()
		a.coderBannerShown = true
	}

	// Inicializa ou atualiza o histórico com o System Prompt correto
	if len(a.cli.history) == 0 {
		a.cli.history = append(a.cli.history, models.Message{Role: "system", Content: systemInstruction})
	} else {
		// Remove any mode-reset system messages injected when leaving previous sessions.
		// These are mid-history system messages that would confuse the LLM on re-entry.
		cleaned := make([]models.Message, 0, len(a.cli.history))
		firstSystem := true
		for _, msg := range a.cli.history {
			if msg.Role == "system" {
				if firstSystem {
					// Keep the first system message (will be updated below)
					cleaned = append(cleaned, msg)
					firstSystem = false
				}
				// Drop any additional system messages (mode-reset markers)
				continue
			}
			cleaned = append(cleaned, msg)
		}
		a.cli.history = cleaned

		// Se já existe histórico (ex: uma sessão carregada), forçamos a atualização do system prompt
		// para garantir que a IA mude de comportamento se trocarmos de /agent para /coder
		foundSystem := false
		for i, msg := range a.cli.history {
			if msg.Role == "system" {
				a.cli.history[i].Content = systemInstruction
				foundSystem = true
				break
			}
		}
		if !foundSystem {
			// Insere no início se não houver
			a.cli.history = append([]models.Message{{Role: "system", Content: systemInstruction}}, a.cli.history...)
		}
	}

	currentQuery := query
	if additionalContext != "" {
		currentQuery += "\n\nContexto Adicional:\n" + additionalContext
	}

	// Inject K8s watcher context if active
	if a.cli.WatcherContextFunc != nil {
		if k8sCtx := a.cli.WatcherContextFunc(); k8sCtx != "" {
			currentQuery = k8sCtx + "\n\n" + currentQuery
		}
	}

	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: currentQuery})

	// Phase 2 (#2): Plan-and-Solve / ReWOO. When the quality config asks
	// for it (mode=always, mode=auto + high complexity, or the user-set
	// pendingPlanFirst flag from /plan), synthesize a structured plan
	// and execute it deterministically before handing the conversation
	// to the orchestrator. The plan execution report is injected as a
	// system message so the ReAct loop can finalize with full context.
	a.runPlanFirstIfApplicable(ctx, currentQuery)

	// --- 2. O LOOP DE RACIOCÍNIO-AÇÃO (ReAct) ---
	return a.processAIResponseAndAct(ctx, maxTurns)
}

// RunCoderOnce executa o modo coder de forma não-interativa (one-shot),
// mas mantendo o loop ReAct do AgentMode (com tool_calls/plugins).
func (cli *ChatCLI) RunCoderOnce(ctx context.Context, input string) error {
	cli.setExecutionProfile(ProfileCoder)
	defer cli.setExecutionProfile(ProfileNormal)

	var query string
	if strings.HasPrefix(input, "/coder ") {
		query = strings.TrimPrefix(input, "/coder ")
	} else if input == "/coder" {
		return fmt.Errorf("entrada inválida para o modo coder one-shot: %s", input)
	} else {
		return fmt.Errorf("entrada inválida para o modo coder one-shot: %s", input)
	}

	// Processar contextos especiais como @file, @git, etc.
	query, additionalContext := cli.processSpecialCommands(query)
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	// Assegurar que o modo agente está inicializado
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.agentMode.isCoderMode = true
	cli.agentMode.isOneShot = true

	// Executa o AgentMode no "perfil coder" (system prompt override)
	// Isso mantém exatamente o fluxo atual do /coder interativo:
	// - timeline
	// - tool_call
	// - execução automática de plugins
	return cli.agentMode.Run(ctx, fullQuery, "", CoderSystemPrompt)
}

// RunOnce executa modo agente one-shot
func (a *AgentMode) RunOnce(ctx context.Context, query string, autoExecute bool) error {
	systemInstruction := i18n.T("agent.system_prompt.oneshot") + "\n\n" + i18n.T("ai.response_language")

	// Inject K8s watcher context if active
	enrichedQuery := query
	if a.cli.WatcherContextFunc != nil {
		if k8sCtx := a.cli.WatcherContextFunc(); k8sCtx != "" {
			enrichedQuery = k8sCtx + "\n\n" + query
		}
	}

	a.cli.history = append(a.cli.history, models.Message{Role: "system", Content: systemInstruction})
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: enrichedQuery})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

	aiResponse, err := a.cli.Client.SendPrompt(ctx, enrichedQuery, a.cli.history, 0)
	// Auto-retry on OAuth token expiration (401)
	if a.cli.refreshClientOnAuthError(err) {
		aiResponse, err = a.cli.Client.SendPrompt(ctx, enrichedQuery, a.cli.history, 0)
	}
	a.cli.animation.StopThinkingAnimation()
	if err != nil {
		return fmt.Errorf("erro ao obter resposta da IA: %w", err)
	}

	// Track cost for agent mode initial call
	if a.cli.costTracker != nil {
		usage := llmclient.GetUsageOrEstimate(a.cli.Client, len(enrichedQuery), len(aiResponse))
		a.cli.costTracker.RecordRealUsage(a.cli.Provider, a.cli.Model, usage)
	}

	commandBlocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, commandBlocks)

	if len(commandBlocks) == 0 {
		fmt.Println(i18n.T("agent.oneshot.no_command"))
		return nil
	}

	if !autoExecute {
		fmt.Println(i18n.T("agent.oneshot.header"))
		fmt.Println("==============================================")
		fmt.Println(i18n.T("agent.oneshot.auto_exec_tip"))

		block := commandBlocks[0]
		fmt.Println(i18n.T("agent.oneshot.block_header", block.Description))
		fmt.Println(i18n.T("agent.oneshot.language", block.Language))
		for _, cmd := range block.Commands {
			fmt.Printf("    $ %s\n", cmd)
		}

		return nil
	}

	fmt.Println(i18n.T("agent.oneshot.header_auto_exec"))
	fmt.Println("===============================================")

	blockToExecute := commandBlocks[0]

	for _, cmd := range blockToExecute.Commands {
		if a.validator.IsDangerous(cmd) {
			errMsg := i18n.T("agent.oneshot.auto_exec_aborted", cmd)
			fmt.Printf("⚠️ %s\n", errMsg)
			return errors.New(errMsg)
		}
	}

	fmt.Println(i18n.T("agent.oneshot.auto_exec_running"))
	_, errorMsg := a.executeCommandsWithOutput(ctx, blockToExecute)

	if errorMsg != "" {
		finalError := i18n.T("agent.oneshot.error_with_output", errorMsg)
		return fmt.Errorf("%s", finalError)
	}

	return nil
}

// getToolContextString centraliza a geração do contexto de ferramentas.
func (a *AgentMode) getToolContextString() string {
	if a.cli.pluginManager == nil {
		return ""
	}

	// Sync MCP shadow state: hide built-ins overridden by connected MCP servers,
	// restore them automatically when servers disconnect.
	if a.cli.mcpManager != nil {
		a.cli.pluginManager.SetShadowedBuiltins(a.cli.mcpManager.GetShadowedBuiltins())
	} else {
		a.cli.pluginManager.SetShadowedBuiltins(nil)
	}

	plugins := a.cli.pluginManager.GetPlugins()
	if len(plugins) == 0 {
		return ""
	}

	var toolDescriptions []string
	coderCheatSheet := ""
	for _, plugin := range plugins {

		var b strings.Builder
		b.WriteString(fmt.Sprintf("- Ferramenta: %s\n", plugin.Name()))
		b.WriteString(fmt.Sprintf("  Descrição: %s\n", plugin.Description()))

		if plugin.Schema() != "" {
			// Decodifica o schema para um formato estruturado
			var schema struct {
				ArgsFormat  string `json:"argsFormat"`
				Subcommands []struct {
					Name        string   `json:"name"`
					Description string   `json:"description"`
					Examples    []string `json:"examples"`
					Flags       []struct {
						Name        string `json:"name"`
						Description string `json:"description"`
						Type        string `json:"type"`
						Default     string `json:"default"`
						Required    bool   `json:"required"`
					} `json:"flags"`
				} `json:"subcommands"`
			}

			if err := json.Unmarshal([]byte(plugin.Schema()), &schema); err == nil {
				if a.isCoderMode {
					// Modo /coder: contexto compacto para reduzir ruído
					if schema.ArgsFormat != "" {
						b.WriteString(fmt.Sprintf("  Formato args: %s\n", schema.ArgsFormat))
					}
					b.WriteString("  Subcomandos:\n")
					for _, sub := range schema.Subcommands {
						b.WriteString(fmt.Sprintf("    - %s: %s\n", sub.Name, sub.Description))
						var requiredFlags []string
						for _, flag := range sub.Flags {
							if flag.Required {
								requiredFlags = append(requiredFlags, fmt.Sprintf("%s (%s)", flag.Name, flag.Type))
							}
						}
						if len(requiredFlags) > 0 {
							b.WriteString("      Obrigatórios: " + strings.Join(requiredFlags, ", ") + "\n")
						}
						if len(sub.Examples) > 0 {
							b.WriteString(fmt.Sprintf("      Ex: %s\n", sub.Examples[0]))
						}
					}
				} else {
					// Modo /agent: contexto completo
					if schema.ArgsFormat != "" {
						b.WriteString(fmt.Sprintf("  Formato args: %s\n", schema.ArgsFormat))
					}
					b.WriteString("  Subcomandos Disponíveis:\n")
					for _, sub := range schema.Subcommands {
						b.WriteString(fmt.Sprintf("    - %s: %s\n", sub.Name, sub.Description))
						if len(sub.Flags) > 0 {
							b.WriteString("      Flags:\n")
							for _, flag := range sub.Flags {
								req := ""
								if flag.Required {
									req = " [obrigatório]"
								}
								flagDesc := fmt.Sprintf("        - %s (%s)%s: %s", flag.Name, flag.Type, req, flag.Description)
								if flag.Default != "" {
									flagDesc += fmt.Sprintf(" (padrão: %s)", flag.Default)
								}
								b.WriteString(flagDesc + "\n")
							}
						}
						if len(sub.Examples) > 0 {
							limit := 2
							if len(sub.Examples) < limit {
								limit = len(sub.Examples)
							}
							b.WriteString("      Exemplos:\n")
							for i := 0; i < limit; i++ {
								b.WriteString(fmt.Sprintf("        - %s\n", sub.Examples[i]))
							}
						}
					}
				}
			} else {
				// Fallback para o uso antigo se o JSON do schema for inválido
				b.WriteString(fmt.Sprintf("  Uso: %s\n", plugin.Usage()))
			}
		} else {
			// Fallback para plugins que não têm o schema
			b.WriteString(fmt.Sprintf("  Uso: %s\n", plugin.Usage()))
		}

		toolDescriptions = append(toolDescriptions, b.String())
	}

	if a.isCoderMode {
		coderCheatSheet = "Cheat sheet (@coder):\n" +
			"- read: {\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}\n" +
			"- search: {\"cmd\":\"search\",\"args\":{\"term\":\"Login\",\"dir\":\".\"}}\n" +
			"- write: {\"cmd\":\"write\",\"args\":{\"file\":\"x.go\",\"encoding\":\"base64\",\"content\":\"...\"}}\n" +
			"- exec: {\"cmd\":\"exec\",\"args\":{\"cmd\":\"mkdir -p testeapi\"}}\n\n"
	}

	// Include MCP tools from connected servers (deferred schemas — only name+description)
	// Full parameter schemas are fetched on-demand when the tool is invoked.
	if a.cli.mcpManager != nil {
		mcpTools := a.cli.mcpManager.GetToolsSummary()
		if len(mcpTools) > 0 {
			var mcpSection strings.Builder
			mcpSection.WriteString("MCP Tools (external):\n")
			mcpSection.WriteString("  Para invocar: <tool_call name=\"mcp_<tool>\" args='{\"param\":\"value\"}' />\n")
			mcpSection.WriteString("  Se precisar dos parâmetros exatos, invoque e o sistema retornará o schema.\n\n")
			for _, t := range mcpTools {
				mcpSection.WriteString(fmt.Sprintf("  - %s: %s\n", t.Function.Name, t.Function.Description))
			}
			toolDescriptions = append(toolDescriptions, mcpSection.String())
		}
	}

	toolContext := "\n\n" + i18n.T("agent.system_prompt.tools_header") + "\n" + coderCheatSheet + strings.Join(toolDescriptions, "\n") + "\n\n" + i18n.T("agent.system_prompt.tools_instruction")
	if a.isCoderMode {
		toolContext += "\nDicas rápidas (@coder):\n" +
			"- Use args JSON sempre que possível: {\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}\n" +
			"- Subcomando obrigatório: use \"cmd\" ou \"argv\".\n" +
			"- Para exec, use \"cmd\" (ou \"command\") dentro de args.\n"
	}
	return toolContext
}

func (a *AgentMode) processAIResponseAndAct(ctx context.Context, maxTurns int) error {
	// Start centralized stdin reader for type-ahead queue support
	a.startStdinReader()
	defer a.stopStdinReader()

	renderer := agent.NewUIRenderer(a.logger)

	// Helper para construir o histórico com a "âncora" (System Prompt reforçado por turno)
	buildTurnHistoryWithAnchor := func() []models.Message {
		h := make([]models.Message, 0, len(a.cli.history)+1)
		h = append(h, a.cli.history...)

		var anchor string
		if a.isCoderMode {
			anchor = "REMINDER (/CODER MODE): You MUST respond with a short <reasoning> (2-6 lines) then emit one or more <tool_call name=\"@coder\" args=\"...\" />. " +
				"CRITICAL: Emit ALL independent tool_calls in a SINGLE response. Do NOT split independent reads/searches/writes into separate turns. " +
				"If you need to read 3 files, emit 3 tool_calls NOW, not one per turn. Use <agent_call> for 3+ independent tasks when available. " +
				"Do NOT use code blocks (```). For write/patch: base64 encoding and single-line args are MANDATORY."
		} else {
			anchor = "REMINDER (/AGENT MODE): You can use tools via <tool_call name=\"@tool\" args=\"...\" /> when appropriate. " +
				"CRITICAL: Emit ALL independent operations in a SINGLE response. Do NOT waste turns on things that could run in parallel. " +
				"For shell commands, use ```execute:<type>``` blocks (shell/git/docker/kubectl...). " +
				"Avoid destructive commands without clear warnings and alternatives."
		}

		h = append(h, models.Message{Role: "system", Content: anchor})
		return h
	}

	// Helper para verificar tags de raciocínio
	hasReasoningTag := func(s string) bool {
		ls := strings.ToLower(s)
		return strings.Contains(ls, "<reasoning>") && strings.Contains(ls, "</reasoning>")
	}

	// Helper local: renderizar um card com markdown usando o renderer do cli (glamour).
	renderMDCard := func(icon, title, md, color string) {
		md = strings.TrimSpace(md)
		if md == "" {
			return
		}
		rendered := a.cli.renderMarkdown(md) // retorna ANSI
		renderer.RenderMarkdownTimelineEvent(icon, title, rendered, color)
	}

	// Context recovery state for the session
	contextRecovery := agent.NewContextRecovery(agent.DefaultContextRecoveryConfig(), a.logger)
	currentMaxTokens := 0           // 0 = use provider default
	providerMaxTokensCap := 128_000 // conservative default; providers may support more

	// --- LOOP PRINCIPAL DO AGENTE (ReAct) ---
	for turn := 0; turn < maxTurns; turn++ {
		// Verificar cancelamento pelo usuário (Ctrl+C)
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// In agent mode (not coder), check for type-ahead messages from user
		if !a.isCoderMode {
			if userMsg := a.drainStdinToQueue(); userMsg != "" {
				fmt.Printf("\n  %s\n\n",
					renderer.Colorize("📨 Nova instrução do usuário recebida", agent.ColorCyan))
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "user",
					Content: userMsg,
				})
			}
		}

		// Microcompact (Level 0): cheap, progressive compaction of OLD tool
		// results in history. Pure Go, no LLM call. Often keeps us below
		// budget so the expensive summarization path is never triggered.
		mcCfg := agent.DefaultMicrocompactConfig()
		if h, report := agent.ApplyMicrocompact(a.cli.history, turn, mcCfg, a.logger); report != nil && (report.Truncated > 0 || report.Summarized > 0) {
			a.cli.history = h
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("🗜", agent.ColorGray),
				renderer.Colorize(
					i18n.T("agent.microcompact.applied",
						report.Truncated, report.Summarized, FormatPayloadSize(int(report.CharsSaved))),
					agent.ColorGray))
		}

		// Compact history if over budget (before building turn history)
		cfg := DefaultCompactConfig(a.cli.Provider, a.cli.Model)
		cfg.BudgetRatio = 0.60 // tighter budget — tool outputs are large
		cfg.MinKeepRecent = 8  // ~4 tool call cycles

		// Pre-flight: measure the current history and react BEFORE the
		// request goes out. Two paths:
		//   (a) User has CHATCLI_MAX_PAYLOAD set and we're above 85%
		//       of it → force aggressive compaction this turn.
		//   (b) No explicit cap but history is already > 2.5 MB → emit a
		//       one-shot hint about the env var. Corporate proxies usually
		//       cap at 5 MB and the error they return is obscure (403/EOF),
		//       so flagging this early saves the user a painful surprise.
		totalHistoryChars := 0
		for _, m := range a.cli.history {
			totalHistoryChars += len(m.Content)
		}
		if cfg.MaxPayloadBytes > 0 && totalHistoryChars > int(float64(cfg.MaxPayloadBytes)*0.85) {
			cfg.BudgetRatio = 0.40 // force harder compaction
			a.logger.Warn("Pre-flight: history near payload cap, forcing aggressive compact",
				zap.Int("total_chars", totalHistoryChars),
				zap.Int("cap_bytes", cfg.MaxPayloadBytes))
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("ℹ", agent.ColorGray),
				renderer.Colorize(
					i18n.T("agent.preflight.near_cap",
						FormatPayloadSize(totalHistoryChars),
						(totalHistoryChars*100)/cfg.MaxPayloadBytes,
						FormatPayloadSize(cfg.MaxPayloadBytes)),
					agent.ColorGray))
		} else if cfg.MaxPayloadBytes == 0 && totalHistoryChars > 2_500_000 && !a.proxyPayloadWarned {
			a.proxyPayloadWarned = true
			a.logger.Warn("History exceeds 2.5 MB, no payload cap set — proxy 413/403 possible",
				zap.Int("total_chars", totalHistoryChars))
			fmt.Printf("\r\033[K  %s %s\n",
				renderer.Colorize("ℹ", agent.ColorYellow),
				renderer.Colorize(
					i18n.T("agent.preflight.warn_no_cap", FormatPayloadSize(totalHistoryChars)),
					agent.ColorYellow))
		}
		if a.cli.historyCompactor.NeedsCompaction(a.cli.history, cfg) {
			// Emit live status during compaction so the terminal is never
			// silent. Without this, Level 2 (LLM summarization) can block
			// for 30-90s with zero feedback — users assume a freeze.
			a.cli.historyCompactor.SetStatusCallback(func(stage CompactStage, msg string) {
				fmt.Printf("\r\033[K  %s %s\n",
					renderer.Colorize("│", agent.ColorCyan),
					renderer.Colorize(msg, agent.ColorGray))
			})
			compacted, compactErr := a.cli.historyCompactor.Compact(ctx, a.cli.history, a.cli.Client, cfg)
			a.cli.historyCompactor.SetStatusCallback(nil)
			if compactErr == nil {
				a.cli.history = compacted
			} else if errors.Is(compactErr, context.Canceled) {
				return compactErr
			}
		}

		a.logger.Debug("Iniciando turno do agente", zap.Int("turn", turn+1), zap.Int("max_turns", maxTurns))

		// Reset per-turn counters
		turnAgents := 0
		turnToolCalls := 0

		// Inicia o timer do turno (substitui a animação de "Pensando...")
		modelName := a.cli.Client.GetModelName()
		a.turnTimer.Start(ctx, func(d time.Duration) {
			fmt.Print(metrics.FormatTimerStatus(d, modelName, "Processando..."))
		})

		turnHistory := buildTurnHistoryWithAnchor()

		// Validate tool result pairing and enforce budget before sending to API
		turnHistory, pairingReport := agent.EnsureToolResultPairing(turnHistory, a.logger)
		if pairingReport.HasRepairs() {
			a.logger.Info("Tool result pairing repaired before API call",
				zap.Int("synthetic_results", pairingReport.SyntheticResultsInjected),
				zap.Int("orphans_removed", pairingReport.OrphanResultsRemoved))
		}
		turnHistory, _ = agent.EnforceToolResultBudget(turnHistory, a.logger)

		// Resolve per-turn client + effort hint from any active skill
		// hints. Model swap is transparent; effort flows via ctx so the
		// provider's SendPrompt can enable extended thinking / reasoning.
		turnClient, turnCtx := a.clientAndCtxForTurn(ctx)

		// Detect native function calling support
		var nativeToolCalls []models.ToolCall
		toolAwareClient, canUseNativeTools := llmclient.AsToolAware(turnClient)
		if canUseNativeTools && !toolAwareClient.SupportsNativeTools() {
			canUseNativeTools = false
		}

		// Get tool definitions for native mode.
		// In coder mode: coder tools + plugin tools (websearch, webfetch)
		// In agent mode: plugin tools only (websearch, webfetch)
		var nativeToolDefs []models.ToolDefinition
		if canUseNativeTools {
			if a.isCoderMode {
				nativeToolDefs = workers.CoderToolDefinitions(nil)
			}
			// Always include plugin tools (websearch, webfetch) for all modes.
			// This eliminates phantom tool call detection issues from XML parsing.
			nativeToolDefs = append(nativeToolDefs, workers.PluginToolDefinitions()...)
		}

		// Chamada à LLM (native tools or text)
		var aiResponse string
		var err error
		var llmResp *models.LLMResponse

		if canUseNativeTools && len(nativeToolDefs) > 0 {
			llmResp, err = toolAwareClient.SendPromptWithTools(turnCtx, "", turnHistory, nativeToolDefs, currentMaxTokens)
			if a.cli.refreshClientOnAuthError(err) {
				llmResp, err = toolAwareClient.SendPromptWithTools(turnCtx, "", turnHistory, nativeToolDefs, currentMaxTokens)
			}
			if err == nil && llmResp != nil {
				aiResponse = llmResp.Content
				nativeToolCalls = llmResp.ToolCalls
			}
		} else {
			aiResponse, err = turnClient.SendPrompt(turnCtx, "", turnHistory, currentMaxTokens)
			if a.cli.refreshClientOnAuthError(err) {
				aiResponse, err = turnClient.SendPrompt(turnCtx, "", turnHistory, currentMaxTokens)
			}
		}

		// Track cost for agent mode turn — prefer real API usage.
		// Attribute to whichever provider+model the resolver actually
		// served this turn (see clientAndCtxForTurn).
		if a.cli.costTracker != nil && err == nil {
			inputChars := 0
			for _, m := range turnHistory {
				inputChars += len(m.Content)
			}
			usage := llmclient.GetUsageOrEstimate(turnClient, inputChars, len(aiResponse))
			effProvider := a.cli.Provider
			effModel := a.cli.Model
			if a.skillModelHint != "" {
				resolution := a.cli.resolveSkillClient(a.skillModelHint)
				effProvider = resolution.Provider
				effModel = resolution.Model
			}
			a.cli.costTracker.RecordRealUsage(effProvider, effModel, usage)
		}

		// Para o timer e obtém a duração
		turnDuration := a.turnTimer.Stop()
		fmt.Print(metrics.ClearLine()) // Limpa a linha do timer
		fmt.Println()

		// Helper para exibir métricas ao final do turno (após execução)
		showTurnStats := func() {
			fmt.Println(metrics.FormatTurnInfo(turn+1, maxTurns, turnDuration, &metrics.TurnStats{
				TurnAgents:       turnAgents,
				TurnToolCalls:    turnToolCalls,
				SessionAgents:    a.agentsLaunched,
				SessionToolCalls: a.toolCallsExecd,
			}))
		}

		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}

			// Context overflow OR corporate-proxy payload rejection (413,
			// WAF 403, 431, or EOF-on-large-payload): try to recover by
			// aggressive compaction + retry. Proxy rejections are pernicious
			// — the user has no way to know why their perfectly-sized-for-
			// the-model request was rejected by a network middlebox.
			isCtxTooLong := agent.IsContextTooLongError(err)
			historyChars := 0
			for _, m := range a.cli.history {
				historyChars += len(m.Content)
			}
			isPayloadTooLarge := agent.IsLikelyPayloadProblem(err, historyChars)
			if (isCtxTooLong || isPayloadTooLarge) && contextRecovery.CanRecoverContextOverflow() {
				reason := i18n.T("agent.recovery.reason.ctx_too_long")
				if isPayloadTooLarge {
					switch {
					case agent.IsPayloadTooLargeError(err):
						reason = i18n.T("agent.recovery.reason.payload_413")
					case agent.IsProxyWAFRejection(err):
						reason = i18n.T("agent.recovery.reason.waf_403")
					default:
						reason = i18n.T("agent.recovery.reason.suspected_payload", FormatPayloadSize(historyChars))
					}
				}
				a.logger.Warn("Recoverable request failure — attempting recovery",
					zap.String("reason", reason),
					zap.Int("turn", turn+1), zap.Error(err))

				fmt.Printf("\r\033[K  %s %s\n",
					renderer.Colorize("⚠", agent.ColorYellow),
					renderer.Colorize(
						i18n.T("agent.recovery.retrying", reason),
						agent.ColorYellow))

				// For payload-too-large: also force the compactor to use a
				// hard byte cap on the next turn. Without a cap hint we would
				// just retry with the same size and fail again.
				if isPayloadTooLarge && os.Getenv("CHATCLI_MAX_PAYLOAD") == "" {
					// Educated guess: assume a 4 MB proxy cap as a sane default
					// when the user hasn't configured one. Err on the side of
					// caution — easier to set a higher value than to hit this
					// error repeatedly.
					_ = os.Setenv("CHATCLI_MAX_PAYLOAD", "4MB")
					fmt.Printf("  %s %s\n",
						renderer.Colorize("ℹ", agent.ColorGray),
						renderer.Colorize(
							i18n.T("agent.recovery.assumed_cap"),
							agent.ColorGray))
				}

				recoveredHistory, recovered := contextRecovery.RecoverContextOverflow(a.cli.history)
				if recovered {
					a.cli.history = recoveredHistory

					// Post-recovery hint for payload-related failures: without
					// this, the model tends to re-read the same huge file that
					// triggered the limit, looping through recovery until
					// MaxRecoveryAttempts is exhausted. A single user-role
					// instruction steers it toward surgical, line-ranged reads
					// that fit under the cap.
					if isPayloadTooLarge {
						a.cli.history = append(a.cli.history, models.Message{
							Role: "user",
							Content: "[SYSTEM NOTICE — PAYLOAD LIMIT HIT] A proxy/gateway rejected the previous request due to body size. History was compacted to recover. " +
								"Going forward: " +
								"(1) When reading files, prefer targeted reads with line ranges (e.g. sed -n '100,200p' file, or read_file with offset+limit) instead of reading entire files. " +
								"(2) Prefer grep/ripgrep with specific patterns over full-file reads. " +
								"(3) If you previously read a large file, its full content is persisted at the path shown in the tool-result preview — re-read specific ranges from that file rather than repeating the original read. " +
								"(4) Summarize findings incrementally rather than accumulating raw tool output.",
						})
						a.logger.Info("Injected payload-recovery hint into history")
					}
					continue // retry the turn with compacted history
				}
			}

			return fmt.Errorf("erro ao obter resposta da IA no turno %d: %w", turn+1, err)
		}

		// Max-output-tokens recovery: detect truncation and escalate
		stopReason := ""
		if llmResp != nil && llmResp.StopReason != "" {
			stopReason = llmResp.StopReason
		} else if src, ok := llmclient.AsStopReasonAware(a.cli.Client); ok {
			stopReason = src.LastStopReason()
		}
		if stopReason == "max_tokens" || stopReason == "length" {
			effectiveMax := currentMaxTokens
			if effectiveMax <= 0 {
				effectiveMax = 4096 // common default
			}
			if newMax, ok := contextRecovery.MaxTokensEscalation(effectiveMax, providerMaxTokensCap); ok {
				currentMaxTokens = newMax
				a.cli.history = append(a.cli.history, models.Message{
					Role:    "assistant",
					Content: aiResponse,
				})
				a.cli.history = append(a.cli.history, agent.ContinuationMessage())
				a.logger.Info("Max tokens hit — escalated and continuing",
					zap.Int("new_max_tokens", currentMaxTokens))
				continue // retry with higher limit
			}
		}

		// Persistir a resposta no histórico "real"
		if len(nativeToolCalls) > 0 {
			a.cli.history = append(a.cli.history, models.Message{
				Role:      "assistant",
				Content:   aiResponse,
				ToolCalls: nativeToolCalls,
			})
		} else {
			a.cli.history = append(a.cli.history, models.Message{Role: "assistant", Content: aiResponse})
		}

		// Parsear Tool Calls — native ou XML fallback
		var toolCalls []agent.ToolCall
		if len(nativeToolCalls) > 0 {
			// Convert native tool calls to agent.ToolCall format.
			// Plugin tools (web_search, web_fetch) map to their respective plugins.
			// Coder tools (read_file, write_file, etc.) map to @coder.
			for _, ntc := range nativeToolCalls {
				if pluginName, pluginArgs, isPlugin := workers.ResolveNativePluginTool(ntc.Name, ntc.Arguments); isPlugin {
					// Plugin tool: map to the plugin's CLI args format
					argsStr := strings.Join(pluginArgs, " ")
					toolCalls = append(toolCalls, agent.ToolCall{
						Name: pluginName,
						Args: argsStr,
						Raw:  argsStr,
					})
				} else {
					// Coder tool: map to @coder with JSON args
					subcmd, _ := workers.NativeToolNameToSubcmd(ntc.Name)
					argsJSON, _ := json.Marshal(map[string]interface{}{
						"cmd":  subcmd,
						"args": ntc.Arguments,
					})
					toolCalls = append(toolCalls, agent.ToolCall{
						Name: "@coder",
						Args: string(argsJSON),
						Raw:  string(argsJSON),
					})
				}
			}
		} else {
			var parseErr error
			toolCalls, parseErr = agent.ParseToolCalls(aiResponse)
			if parseErr != nil {
				a.logger.Warn("Falha ao parsear tool_calls", zap.Error(parseErr))
				toolCalls = nil
			}
		}

		// Separar pensamento (texto antes do primeiro tool_call)
		thoughtText := strings.TrimSpace(aiResponse)
		if len(toolCalls) > 0 {
			firstRaw := toolCalls[0].Raw
			parts := strings.Split(aiResponse, firstRaw)
			thoughtText = strings.TrimSpace(parts[0])
		}

		// ==============
		// RENDERIZAÇÃO DE PENSAMENTO (Timeline)
		// ==============
		reasoning, _ := extractXMLTagContent(thoughtText, "reasoning")
		explanation, _ := extractXMLTagContent(thoughtText, "explanation")

		remaining := thoughtText
		remaining = stripXMLTagBlock(remaining, "reasoning")
		remaining = stripXMLTagBlock(remaining, "explanation")
		remaining = stripXMLTagBlock(remaining, "final_summary")
		remaining = stripXMLTagBlock(remaining, "plan")
		remaining = stripXMLTagBlock(remaining, "summary")
		remaining = stripXMLTagBlock(remaining, "action")
		remaining = stripXMLTagBlock(remaining, "action_type")
		remaining = stripXMLTagBlock(remaining, "command")
		remaining = stripXMLTagBlock(remaining, "step")
		remaining = stripAgentCallTags(remaining)
		remaining = stripToolCallTags(remaining)
		remaining = strings.TrimSpace(removeXMLTags(remaining))

		coderMinimal := a.isCoderMode && isCoderMinimalUI()
		coderCompactGlobal := a.isCoderMode && isCoderCompactUI()

		if strings.TrimSpace(reasoning) != "" {
			if coderCompactGlobal {
				renderer.CompactMultiLine("●", "PLANO", reasoning, agent.ColorCyan, 5)
			} else if coderMinimal {
				renderer.RenderTimelineEvent("🧭", "PLANO", compactText(reasoning, 3, 260), agent.ColorCyan)
			} else {
				renderMDCard("🧠", "RACIOCÍNIO", reasoning, agent.ColorCyan)
			}
			// Integração de Task Tracking (somente no modo /coder)
			if a.isCoderMode {
				agent.IntegrateTaskTracking(a.taskTracker, reasoning, a.logger)
			}
		}
		if strings.TrimSpace(explanation) != "" {
			if coderCompactGlobal {
				renderer.CompactLine("◆", "NOTA", explanation, agent.ColorLime)
			} else if coderMinimal {
				renderer.RenderTimelineEvent("📝", "NOTA", compactText(explanation, 2, 220), agent.ColorLime)
			} else {
				renderMDCard("📌", "EXPLICAÇÃO", explanation, agent.ColorLime)
			}
		}
		// Helper para renderizar progresso atualizado do plano
		renderPlanProgress := func() {
			if !a.isCoderMode || a.taskTracker == nil || a.taskTracker.GetPlan() == nil {
				return
			}
			progress := a.taskTracker.FormatProgress()
			if strings.TrimSpace(progress) == "" {
				return
			}
			if coderCompactGlobal {
				renderer.CompactMultiLine("◇", "STATUS", progress, agent.ColorLime, 4)
			} else if coderMinimal {
				renderer.RenderTimelineEvent("🧩", "STATUS", compactText(progress, 2, 220), agent.ColorLime)
			} else {
				renderMDCard("🧩", "PLANO DE AÇÃO", progress, agent.ColorLime)
			}
		}

		// Renderizar progresso inicial das tarefas (somente no modo /coder)
		renderPlanProgress()
		if strings.TrimSpace(remaining) != "" {
			if coderCompactGlobal {
				renderer.CompactLine("◆", "", remaining, agent.ColorGray)
			} else if coderMinimal {
				renderer.RenderTimelineEvent("💬", "RESUMO", compactText(remaining, 2, 220), agent.ColorGray)
			} else {
				renderMDCard("💬", "RESPOSTA", remaining, agent.ColorGray)
			}
		}

		// =========================
		// VALIDAÇÕES DO /CODER
		// =========================
		if a.isCoderMode {
			if len(toolCalls) > 0 {
				// Require <reasoning> before acting
				if !hasReasoningTag(thoughtText) {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "FORMAT ERROR: In /coder mode, you MUST write a <reasoning> block (2-6 lines with task list) BEFORE any <tool_call>. " +
							"Rewrite your response starting with <reasoning>...</reasoning> then your <tool_call> tags.",
					})
					continue
				}

				// Validate that the tool exists (any registered plugin, MCP tool, or @coder)
				firstName := strings.TrimSpace(toolCalls[0].Name)
				isKnownTool := false
				if a.cli.pluginManager != nil {
					if _, found := a.cli.pluginManager.GetPlugin(firstName); found {
						isKnownTool = true
					}
				}
				if !isKnownTool && a.cli.mcpManager != nil && strings.HasPrefix(firstName, "mcp_") {
					mcpName := strings.TrimPrefix(firstName, "mcp_")
					if a.cli.mcpManager.IsMCPTool(mcpName) {
						isKnownTool = true
					}
				}
				if !isKnownTool {
					// Build list of available tools for the error message
					var availableTools []string
					if a.cli.pluginManager != nil {
						for _, p := range a.cli.pluginManager.GetPlugins() {
							availableTools = append(availableTools, p.Name())
						}
					}
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: fmt.Sprintf("FORMAT ERROR: Tool %q not found. Available tools: %s. "+
							"Use <tool_call name=\"TOOL\" args='...' />",
							firstName, strings.Join(availableTools, ", ")),
					})
					continue
				}
			}

			// Prohibit loose code blocks in coder mode
			if len(toolCalls) == 0 {
				if strings.Contains(aiResponse, "```") || strings.Contains(aiResponse, "```execute:") || regexp.MustCompile(`(?m)^[$#]\s+`).MatchString(aiResponse) {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "FORMAT ERROR: Code blocks and shell commands are NOT allowed in /coder mode. " +
							"You MUST use <reasoning> followed by <tool_call> tags. " +
							"For shell commands: <tool_call name=\"@coder\" args='{\"cmd\":\"exec\",\"args\":{\"cmd\":\"your command\"}}' />",
					})
					continue
				}
			}
		}

		// =========================================================
		// PRIORIDADE 0: DISPATCH AGENT_CALL(s) (MULTI-AGENT MODE)
		// =========================================================
		if a.parallelMode && a.agentDispatcher != nil {
			agentCalls, _ := workers.ParseAgentCalls(aiResponse)
			if len(agentCalls) > 0 {
				coderMinUI := a.isCoderMode && isCoderMinimalUI()
				coderCompactUI := a.isCoderMode && isCoderCompactUI()
				n := len(agentCalls)
				agentWord := "agent"
				if n > 1 {
					agentWord = "agents"
				}
				if coderCompactUI {
					renderer.CompactLine("●", "AGENTS", fmt.Sprintf("%d %s", n, agentWord), agent.ColorPurple)
				} else if coderMinUI {
					renderer.RenderTimelineEvent("🚀", "AGENTS", fmt.Sprintf("%d %s dispatched", n, agentWord), agent.ColorPurple)
				} else {
					renderer.RenderTimelineEvent("🚀", "MULTI-AGENT DISPATCH", fmt.Sprintf("Dispatching %d %s", n, agentWord), agent.ColorPurple)
				}

				if !coderCompactUI {
					for i, ac := range agentCalls {
						renderer.RenderTimelineEvent("🤖", fmt.Sprintf("[%s] #%d", ac.Agent, i+1), truncateForUI(ac.Task, 120), agent.ColorCyan)
					}
				}

				// Dispatch with live progress feedback
				a.agentsLaunched += len(agentCalls)
				turnAgents += len(agentCalls)

				// Build progress state for live display
				agentSlots := make([]struct{ CallID, Agent, Task string }, n)
				for idx, ac := range agentCalls {
					agentSlots[idx] = struct{ CallID, Agent, Task string }{ac.ID, string(ac.Agent), ac.Task}
				}
				progressState := metrics.NewAgentProgressState(n, agentSlots)

				// Progress event channel - consumed by a goroutine that updates state
				progressCh := make(chan workers.AgentEvent, n*2)
				go func() {
					for evt := range progressCh {
						switch evt.Type {
						case workers.AgentEventStarted:
							progressState.MarkStarted(evt.CallID)
						case workers.AgentEventCompleted:
							progressState.MarkCompleted(evt.CallID, evt.Duration)
						case workers.AgentEventFailed:
							errMsg := ""
							if evt.Error != nil {
								errMsg = evt.Error.Error()
							}
							progressState.MarkFailed(evt.CallID, evt.Duration, errMsg)
						}
					}
				}()

				// Track how many lines the progress display uses for clearing.
				// Both displayFunc and onPause closures share prevLines — safe
				// because both execute under Timer.mu.
				prevLines := 0
				a.turnTimer.SetOnPause(func() {
					// Clear the entire multi-line progress display before a
					// security prompt takes over. Reset prevLines so Resume
					// starts fresh and won't try to clear prompt lines.
					if prevLines > 0 {
						fmt.Print(metrics.ClearLines(prevLines))
						fmt.Print(metrics.ClearLine())
						prevLines = 0
					}
				})
				a.turnTimer.Start(ctx, func(d time.Duration) {
					if prevLines > 0 {
						fmt.Print(metrics.ClearLines(prevLines))
					}
					output := metrics.FormatDispatchProgress(progressState, modelName)
					fmt.Print(output)
					prevLines = progressState.LineCount()
				})

				// Give the policy adapter access to the spinner and stdin
				// channel so it can pause/resume around interactive
				// security prompts and read input without orphaning goroutines.
				if a.policyAdapter != nil {
					a.policyAdapter.setSpinner(a.turnTimer)
					a.policyAdapter.setStdinCh(a.stdinLines)
				}
				agentResults := a.agentDispatcher.DispatchWithProgress(ctx, agentCalls, progressCh)
				a.turnTimer.Stop()
				// Clear the live progress display
				if prevLines > 0 {
					fmt.Print(metrics.ClearLines(prevLines))
				}
				fmt.Print(metrics.ClearLine())

				// Render results and count internal tool calls
				totalAgentToolCalls := 0
				totalParallelCalls := 0
				successCount := 0
				var totalDuration time.Duration
				for _, ar := range agentResults {
					tcCount := len(ar.ToolCalls)
					totalAgentToolCalls += tcCount
					totalParallelCalls += ar.ParallelCalls
					totalDuration += ar.Duration
					if ar.Error != nil {
						if coderCompactUI {
							renderer.CompactToolDone(string(ar.Agent), ar.Duration.Round(time.Millisecond).String(), true)
						} else {
							renderer.RenderTimelineEvent("❌", fmt.Sprintf("[%s] FAILED", ar.Agent), ar.Error.Error(), agent.ColorYellow)
						}
					} else {
						successCount++
						if coderCompactUI {
							renderer.CompactToolDone(fmt.Sprintf("%s(%d calls)", ar.Agent, tcCount), ar.Duration.Round(time.Millisecond).String(), false)
						} else {
							summary := truncateForUI(ar.Output, 200)
							parallelInfo := ""
							if ar.ParallelCalls > 1 {
								parallelInfo = fmt.Sprintf(", %d em paralelo", ar.ParallelCalls)
							}
							tcLabel := "tool calls"
							if tcCount == 1 {
								tcLabel = "tool call"
							}
							title := fmt.Sprintf("[%s] OK (%s, %d %s%s)", ar.Agent, ar.Duration.Round(time.Millisecond), tcCount, tcLabel, parallelInfo)
							renderer.RenderTimelineEvent("✅", title, summary, agent.ColorGreen)
						}
					}
				}
				a.toolCallsExecd += totalAgentToolCalls
				turnToolCalls += totalAgentToolCalls

				if !coderCompactUI {
					// Resumo compacto do dispatch
					tcWord := "tool calls"
					if totalAgentToolCalls == 1 {
						tcWord = "tool call"
					}
					parallelSuffix := ""
					if totalParallelCalls > 1 {
						parallelSuffix = fmt.Sprintf(" | %d goroutines paralelas", totalParallelCalls)
					}
					renderer.RenderTimelineEvent("📊", "RESUMO",
						fmt.Sprintf("%d/%d %s concluidos | %d %s executadas%s | %s total",
							successCount, n, agentWord, totalAgentToolCalls, tcWord,
							parallelSuffix, totalDuration.Round(time.Millisecond)),
						agent.ColorGray)
				}

				// Inject results as feedback for the orchestrator
				feedback := workers.FormatResults(agentResults)
				a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: feedback})

				// If there are also tool_calls in the same response, skip them —
				// the orchestrator should use agent_calls OR tool_calls, not both in the same turn.
				if len(toolCalls) > 0 {
					a.logger.Info("Skipping tool_calls because agent_calls were dispatched in this turn")
				}
				showTurnStats()
				continue
			}
		}

		// =========================================================
		// PRIORIDADE 1: EXECUTAR TOOL_CALL(s) EM LOTE (BATCH)
		// =========================================================
		if len(toolCalls) > 0 {
			coderMinimal := a.isCoderMode && isCoderMinimalUI()
			coderCompact := a.isCoderMode && isCoderCompactUI()
			var batchOutputBuilder strings.Builder
			var batchHasError bool
			successCount := 0
			totalActions := len(toolCalls)

			// Helper: render error message respecting compact mode
			renderError := func(msg string) {
				if coderCompact {
					renderer.CompactError(msg)
				} else if coderMinimal {
					renderer.RenderToolResultMinimal(msg, true)
				} else {
					renderer.RenderToolResult(msg, true)
				}
			}

			// 1. Renderiza cabeçalho do lote se houver mais de 1 ação
			if totalActions > 1 {
				if coderCompact {
					// Compact mode: no batch header, just tool lines
				} else if coderMinimal {
					renderer.RenderTimelineEvent("📦", "LOTE", fmt.Sprintf("%d ações", totalActions), agent.ColorPurple)
				} else {
					renderer.RenderBatchHeader(totalActions)
				}
			}

			// Iterar sobre TODAS as chamadas de ferramenta sugeridas
			for i, tc := range toolCalls {
				// --- SECURITY CHECK START ---
				if a.isCoderMode {
					pm, err := coder.NewPolicyManager(a.logger)
					if err == nil {
						action := pm.Check(tc.Name, tc.Args)
						if rule, ok := pm.LastMatchedRule(); ok {
							a.lastPolicyMatch = &rule
						} else {
							a.lastPolicyMatch = nil
						}
						if action == coder.ActionDeny {
							msg := "AÇÃO BLOQUEADA (Regra de Segurança)"
							renderError(msg)
							a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
							batchHasError = true
							break
						}
						if action == coder.ActionAsk {
							decision := coder.PromptSecurityCheck(ctx, tc.Name, tc.Args, a.stdinLines)
							pattern := coder.GetSuggestedPattern(tc.Name, tc.Args)
							switch decision {
							case coder.DecisionAllowAlways:
								if pattern != "" {
									_ = pm.AddRule(pattern, coder.ActionAllow)
								}
							case coder.DecisionDenyForever:
								if pattern != "" {
									_ = pm.AddRule(pattern, coder.ActionDeny)
								}
								msg := "AÇÃO BLOQUEADA PERMANENTEMENTE"
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionDenyOnce:
								msg := "AÇÃO NEGADA PELO USUÁRIO"
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionCancelled:
								msg := "OPERAÇÃO CANCELADA (Ctrl+C)"
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
								batchHasError = true
							}
							if batchHasError {
								break
							}
						}
					}
				}
				// --- SECURITY CHECK END ---
				toolName := tc.Name
				toolArgsStr := tc.Args

				// Build compact label for aru-style display
				toolSubcmd := extractSubcmdFromArgs(toolArgsStr)
				compactLabel := agent.CompactToolLabel(toolSubcmd, toolArgsStr)
				toolStartTime := time.Now()

				// UX: Pequena pausa para separar visualmente o pensamento da ação
				if !coderCompact {
					time.Sleep(200 * time.Millisecond)
				}

				// 2. Renderiza a BOX de ação IMEDIATAMENTE (antes de processar)
				if coderCompact {
					renderer.CompactToolStart(compactLabel)
				} else if coderMinimal {
					renderer.RenderToolCallMinimal(toolName, toolArgsStr, i+1, totalActions)
				} else {
					renderer.RenderToolCallWithProgress(toolName, toolArgsStr, i+1, totalActions)
				}

				// UX: Força flush e pausa para leitura
				_ = os.Stdout.Sync()
				if !coderCompact {
					time.Sleep(300 * time.Millisecond)
				}

				// --- Lógica de Sanitização e Validação ---
				normalizedArgsStr := sanitizeToolCallArgs(toolArgsStr, a.logger, toolName, a.isCoderMode)

				// /coder mode: if args have newlines, try compacting JSON before failing.
				// Many AI models send pretty-printed JSON which is perfectly valid but multiline.
				if a.isCoderMode && hasAnyNewline(normalizedArgsStr) {
					compacted := tryCompactJSON(normalizedArgsStr)
					if compacted != "" && !hasAnyNewline(compacted) {
						// Successfully compacted multiline JSON into single line
						if a.logger != nil {
							a.logger.Debug("Compacted multiline JSON args to single line",
								zap.String("tool", toolName))
						}
						normalizedArgsStr = compacted
					} else {
						// Could not compact - enforce single line as before
						msg := buildCoderSingleLineArgsEnforcementPrompt(toolArgsStr)
						renderError("Format error: args contain line breaks")

						a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
						batchHasError = true
						break
					}
				}

				toolArgs, parseErr := parseToolArgsWithJSON(normalizedArgsStr)
				var toolOutput string
				var execErr error

				// --- Preparação da Execução ---
				// MCP tools handle their own arg parsing (JSON), so check them BEFORE parseErr
				if a.cli.mcpManager != nil && strings.HasPrefix(toolName, "mcp_") {
					// MCP tool dispatch — strip prefix and route to MCP server
					mcpToolName := strings.TrimPrefix(toolName, "mcp_")
					if a.cli.mcpManager.IsMCPTool(mcpToolName) {
						// Parse args into map for MCP
						mcpArgs := make(map[string]interface{})
						for i := 0; i < len(toolArgs)-1; i += 2 {
							mcpArgs[toolArgs[i]] = toolArgs[i+1]
						}
						// Also try JSON parsing if single arg
						if len(toolArgs) == 1 {
							_ = json.Unmarshal([]byte(toolArgs[0]), &mcpArgs)
						}
						// If toolArgs came from JSON parsing (parseToolArgsWithJSON), they're key=value pairs
						// Try re-parsing from normalized args string
						if len(mcpArgs) == 0 {
							_ = json.Unmarshal([]byte(normalizedArgsStr), &mcpArgs)
						}

						// Deferred schema: if model invoked with empty args, return the full schema
						if len(mcpArgs) == 0 {
							schema := a.cli.mcpManager.GetToolSchema(mcpToolName)
							if schema != nil {
								schemaJSON, _ := json.MarshalIndent(schema, "", "  ")
								toolOutput = fmt.Sprintf("MCP tool '%s' requires parameters. Here is the schema:\n%s\n\nPlease invoke again with the correct arguments.", mcpToolName, string(schemaJSON))
							} else {
								toolOutput = fmt.Sprintf("MCP tool '%s' requires arguments in JSON format: {\"param\": \"value\"}", mcpToolName)
							}
						} else {
							a.cli.animation.StopThinkingAnimation()
							if !coderCompact {
								renderer.RenderStreamBoxStart("🔌", fmt.Sprintf("MCP: %s", mcpToolName), agent.ColorPurple)
							}

							result, mcpErr := a.cli.mcpManager.ExecuteTool(ctx, mcpToolName, mcpArgs)
							if mcpErr != nil {
								execErr = mcpErr
								toolOutput = fmt.Sprintf("MCP tool error: %v", mcpErr)
							} else {
								toolOutput = result.Content
								if result.IsError {
									execErr = fmt.Errorf("MCP tool returned error")
								}
							}

							if !coderCompact {
								renderer.RenderStreamBoxEnd(agent.ColorPurple)
							}
						}

						if ctx.Err() != nil {
							return ctx.Err()
						}
					} else {
						execErr = fmt.Errorf("MCP tool não encontrado")
						toolOutput = fmt.Sprintf("Ferramenta MCP '%s' não existe ou servidor desconectado.", mcpToolName)
					}
				} else if parseErr != nil {
					// Non-MCP tool with parse error
					execErr = parseErr
					toolOutput = fmt.Sprintf("Args parsing error: %v", parseErr)

					if a.isCoderMode {
						fixMsg := fmt.Sprintf("ERROR: Your <tool_call> has invalid args (error: %v). In /coder mode, args MUST be valid single-line JSON.", parseErr)
						fixMsg += "\n\nQuick fix - use one of these formats:\n" +
							`<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />` + "\n" +
							`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"go test ./..."}}' />` + "\n" +
							`<tool_call name="@coder" args='{"cmd":"write","args":{"file":"out.go","content":"BASE64","encoding":"base64"}}' />` + "\n" +
							`<tool_call name="@coder" args="read --file main.go" />`
						a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: fixMsg})
						batchHasError = true
					}
				} else {
					plugin, found := a.cli.pluginManager.GetPlugin(toolName)
					if !found {
						execErr = fmt.Errorf("plugin não encontrado")
						toolOutput = fmt.Sprintf("Ferramenta '%s' não existe ou não está instalada.", toolName)

						if a.isCoderMode {
							var available []string
							for _, p := range a.cli.pluginManager.GetPlugins() {
								available = append(available, p.Name())
							}
							a.cli.history = append(a.cli.history, models.Message{
								Role:    "user",
								Content: fmt.Sprintf("Tool %q not found. Available: %s", toolName, strings.Join(available, ", ")),
							})
							batchHasError = true
						}
					} else {
						// Guard-rail do /coder (@coder) - Argumentos obrigatórios
						if a.isCoderMode && strings.EqualFold(strings.TrimSpace(toolName), "@coder") {
							if missing, which := isCoderArgsMissingRequiredValue(toolArgs); missing {
								msg := buildCoderToolCallFixPrompt(which)
								renderError("Args inválido: falta " + which)

								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
								batchHasError = true
								// Marca erro para parar o loop
								execErr = fmt.Errorf("argumento obrigatório faltando: %s", which)
							}
						}

						// --- DANGEROUS EXEC GUARD ---
						// Even if policy says "allow", NEVER auto-execute dangerous commands.
						// This catches cases where user clicked "Allow Always" for @coder exec.
						if a.isCoderMode && !batchHasError {
							if dangerous, shellCmd := a.isCoderExecDangerous(toolArgs); dangerous {
								msg := fmt.Sprintf(
									"BLOCKED: Dangerous command detected in @coder exec: %q. "+
										"This command is forbidden regardless of policy rules. "+
										"DO NOT retry this command.", shellCmd)
								renderError(msg)
								a.cli.history = append(a.cli.history, models.Message{
									Role: "user", Content: "SECURITY BLOCK: " + msg,
								})
								batchHasError = true
								execErr = fmt.Errorf("dangerous command blocked: %s", shellCmd)
							}
						}
						// --- END DANGEROUS EXEC GUARD ---

						// Se não houve erro de validação, EXECUTA
						if !batchHasError {
							// Fire PreToolUse hook — may block the action
							if a.cli.hookManager != nil {
								wd, _ := os.Getwd()
								hookResult := a.cli.hookManager.Fire(hooks.HookEvent{
									Type:       hooks.EventPreToolUse,
									Timestamp:  time.Now(),
									ToolName:   toolName,
									ToolArgs:   normalizedArgsStr,
									SessionID:  a.cli.currentSessionName,
									WorkingDir: wd,
								})
								if hookResult != nil && hookResult.Blocked {
									msg := fmt.Sprintf("BLOCKED by hook: %s", hookResult.BlockReason)
									renderError(msg)
									a.cli.history = append(a.cli.history, models.Message{
										Role: "user", Content: "HOOK BLOCK: " + msg,
									})
									batchHasError = true
									execErr = fmt.Errorf("blocked by hook: %s", hookResult.BlockReason)
								}
							}
						}

						if !batchHasError {
							// UX: Animação durante a execução
							subCmd := "ação"
							if len(toolArgs) > 0 {
								subCmd = toolArgs[0]
							}
							a.cli.animation.StopThinkingAnimation()

							if !coderCompact {
								renderer.RenderStreamBoxStart("🔨", fmt.Sprintf("EXECUTANDO: %s %s", toolName, subCmd), agent.ColorPurple)
							}

							streamCallback := func(line string) {
								if !coderCompact {
									renderer.StreamOutput(line)
								}
							}

							// Marca tarefa como em andamento ANTES de executar
							agent.MarkTaskInProgress(a.taskTracker)

							// Delegate interception: if this is an @coder call with
							// cmd=delegate, it's a subagent spawn — NOT a plugin
							// invocation. Route to workers.RunDelegate with our
							// LLM client so the subagent has its own isolated
							// context.
							if strings.EqualFold(strings.TrimSpace(toolName), "@coder") && isDelegateInvocation(normalizedArgsStr) {
								nativeArgs, rawInner := extractDelegateArgs(normalizedArgsStr)
								toolOutput, execErr = workers.RunDelegate(
									ctx,
									nativeArgs,
									rawInner,
									a.cli.Client,
									nil, // no file lock manager at top level — subagent uses its own
									nil, // no skills propagation for now
									nil, // policy handled upstream already
									a.logger,
								)
							} else {
								toolOutput, execErr = plugin.ExecuteWithStream(ctx, toolArgs, streamCallback)
							}

							if !coderCompact {
								renderer.RenderStreamBoxEnd(agent.ColorPurple)
							}

							// Se o contexto foi cancelado (Ctrl+C), propaga imediatamente
							if ctx.Err() != nil {
								return ctx.Err()
							}
						}
					}
				}

				// 3. Renderiza resultado individual (após a execução)
				displayForHuman := toolOutput
				if execErr != nil {
					errText := execErr.Error()
					if strings.TrimSpace(displayForHuman) == "" {
						displayForHuman = errText
					} else {
						displayForHuman = displayForHuman + "\n\n--- ERRO ---\n" + errText
					}
				}
				if coderCompact {
					elapsed := time.Since(toolStartTime)
					durationStr := ""
					if elapsed >= 500*time.Millisecond {
						durationStr = fmt.Sprintf("%.1fs", elapsed.Seconds())
					}
					renderer.CompactToolDone(compactLabel, durationStr, execErr != nil)
				} else if coderMinimal {
					renderer.RenderToolResultMinimal(displayForHuman, execErr != nil)
				} else {
					renderer.RenderToolResult(displayForHuman, execErr != nil)
				}

				// Fire PostToolUse / PostToolUseFailure hooks
				if a.cli.hookManager != nil {
					wd, _ := os.Getwd()
					eventType := hooks.EventPostToolUse
					errStr := ""
					if execErr != nil {
						eventType = hooks.EventPostToolUseFailure
						errStr = execErr.Error()
					}
					// Truncate output for hook payload
					hookOutput := toolOutput
					if len(hookOutput) > 2000 {
						hookOutput = hookOutput[:2000] + "...(truncated)"
					}
					a.cli.hookManager.FireAsync(hooks.HookEvent{
						Type:       eventType,
						Timestamp:  time.Now(),
						ToolName:   toolName,
						ToolArgs:   normalizedArgsStr,
						ToolOutput: hookOutput,
						Error:      errStr,
						SessionID:  a.cli.currentSessionName,
						WorkingDir: wd,
					})
				}

				// Atualiza status da tarefa e re-renderiza plano
				if execErr != nil {
					agent.MarkTaskFailed(a.taskTracker, execErr.Error())
				} else {
					agent.MarkTaskCompleted(a.taskTracker)
				}
				renderPlanProgress()

				// Acumula o resultado para a LLM
				batchOutputBuilder.WriteString(fmt.Sprintf("--- Resultado da Ação %d (%s) ---\n", i+1, toolName))

				if execErr != nil || batchHasError {
					batchOutputBuilder.WriteString(fmt.Sprintf("ERRO: %v\nSaída parcial: %s\n", execErr, toolOutput))
					batchOutputBuilder.WriteString("\n[EXECUÇÃO EM LOTE INTERROMPIDA PREMATURAMENTE DEVIDO A ERRO NA AÇÃO ANTERIOR]\n")

					// Garante flag de erro se veio de execErr
					batchHasError = true
					break // Fail-Fast: Para a execução do lote
				} else {
					// Truncamento opcional para economizar tokens no contexto da LLM (não na tela)
					if len(toolOutput) > 30000 {
						preview := toolOutput[:5000]
						suffix := toolOutput[len(toolOutput)-1000:]
						toolOutput = fmt.Sprintf("%s\n\n... [CONTEÚDO CENTRAL OMITIDO (%d chars) PARA ECONOMIZAR TOKENS] ...\n\n%s", preview, len(toolOutput)-6000, suffix)
					}

					batchOutputBuilder.WriteString(toolOutput)
					batchOutputBuilder.WriteString("\n\n")
					successCount++
					a.toolCallsExecd++
					turnToolCalls++
				}
			}

			// 4. Renderiza rodapé do lote
			if totalActions > 1 {
				if coderCompact {
					renderer.CompactBatchSummary(successCount, totalActions, batchHasError)
				} else {
					renderer.RenderBatchSummary(successCount, totalActions, batchHasError)
				}
			}

			// Lógica de Continuação:
			// Se houve erro de validação (onde já inserimos msg específica no histórico) E nenhuma ação rodou,
			// apenas damos continue para a IA tentar corrigir.
			if batchHasError && !strings.Contains(batchOutputBuilder.String(), "Resultado da Ação") {
				continue
			}

			// Caso contrário (sucesso ou erro de execução no meio), enviamos o output acumulado.
			feedbackForAI := i18n.T("agent.feedback.tool_output", "batch_execution", batchOutputBuilder.String())

			// Verifica se precisa de replanejamento
			if a.taskTracker != nil && a.taskTracker.NeedsReplanning() {
				feedbackForAI += "\n\nATENÇÃO: Múltiplas falhas detectadas. Crie um NOVO <reasoning> com uma lista replanejada de tarefas, considerando os erros anteriores."
			}

			a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: feedbackForAI})

			showTurnStats()
			continue
		}

		// =========================================================
		// PRIORIDADE 2: EXECUTE BLOCKS (Legado / Modo Agente Padrão)
		// =========================================================
		commandBlocks := a.extractCommandBlocks(aiResponse)
		if len(commandBlocks) > 0 {
			if a.isCoderMode && a.isOneShot {
				a.cli.history = append(a.cli.history, models.Message{
					Role: "user",
					Content: "Você respondeu com comandos em bloco (shell). No modo /coder você DEVE usar <tool_call> " +
						"para executar ferramentas/plugins (especialmente @coder). " +
						"Reenvie a próxima ação SOMENTE como <tool_call name=\"@coder\" ... /> (sem blocos ```).",
				})
				continue
			}

			if a.isCoderMode {
				a.cli.history = append(a.cli.history, models.Message{
					Role: "user",
					Content: "No modo /coder, não use blocos ```execute``` nem comandos shell. " +
						"Use <reasoning> e então emita <tool_call name=\"@coder\" ... />.",
				})
				continue
			}

			renderMDCard("🧩", "PLANO GERADO", "A IA gerou um plano de ação com comandos executáveis. Use o menu abaixo para executar.", agent.ColorLime)
			a.handleCommandBlocks(ctx, commandBlocks)
			return nil
		}

		// ==========================================
		// PRIORIDADE 3: RESPOSTA FINAL (sem ações)
		// ==========================================

		// In coder mode, the AI may respond without tool calls when it needs
		// information from the user (e.g., "What role should I use?", "Which
		// file do you mean?"). Instead of exiting, wait for user input so the
		// conversation can continue.
		if a.isCoderMode && !a.isOneShot {
			showTurnStats()
			fmt.Println()
			fmt.Print(renderer.Colorize("  ⏳ "+i18n.T("coder.waiting_for_input"), agent.ColorCyan))
			fmt.Println() // newline before input for clean cursor positioning

			// Stop the raw stdin reader so we can use line-editing input.
			// The raw reader captures escape sequences as literal bytes (^[[A for arrows),
			// making it impossible to navigate text. We temporarily switch to
			// golang.org/x/term which provides full readline support.
			hadStdinReader := a.stdinLines != nil
			if hadStdinReader {
				a.stopStdinReader()
			}

			userInput, err := a.readLineWithEditing()

			// Restart the stdin reader for subsequent agent turns
			if hadStdinReader {
				a.startStdinReader()
			}
			if err != nil {
				return err
			}
			userInput = strings.TrimSpace(userInput)

			// Allow the user to exit explicitly
			if userInput == "" || strings.EqualFold(userInput, "exit") || strings.EqualFold(userInput, "quit") || strings.EqualFold(userInput, "sair") {
				fmt.Println(renderer.Colorize("\n"+i18n.T("agent.status.task_completed"), agent.ColorGreen+agent.ColorBold))
				return nil
			}

			a.cli.history = append(a.cli.history, models.Message{
				Role:    "user",
				Content: userInput,
			})
			continue
		}

		showTurnStats()
		fmt.Println(renderer.Colorize("\n"+i18n.T("agent.status.task_completed"), agent.ColorGreen+agent.ColorBold))
		return nil
	}

	fmt.Println(renderer.Colorize(
		"\n"+i18n.T("agent.status.max_turns_stopped", maxTurns),
		agent.ColorYellow,
	))
	return nil
}

func (a *AgentMode) continueWithNewAIResponse(ctx context.Context) {
	turns := AgentMaxTurns()

	if err := a.processAIResponseAndAct(ctx, turns); err != nil {
		fmt.Println(colorize(
			i18n.T("agent.error.continuation_failed", err),
			ColorYellow,
		))
	}
}

// helper max turns
// splitToolArgsMultiline faz split de argv estilo shell, mas com suporte a multilinha.
// Regras:
// - separa por whitespace (inclui \n) quando NÃO estiver dentro de aspas
// - suporta aspas simples e duplas
// - permite newline dentro de aspas (vira parte do mesmo argumento)
// - "\" funciona como escape fora de aspas simples (ex: \" ou \n literal etc.)
// - não interpreta sequências como \n => newline; mantém literal \ + n (quem interpreta é o plugin, se quiser)
// - retorna erro se aspas não balanceadas ou escape pendente no final
func (a *AgentMode) initMultiAgent() bool {
	modeStr := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_AGENT_PARALLEL_MODE")))
	if modeStr == "false" || modeStr == "0" {
		a.parallelMode = false
		return false
	}

	// Registry already initialized — just update provider/model in case
	// the user switched providers at runtime.
	if a.agentRegistry != nil {
		a.parallelMode = true
		if a.agentDispatcher != nil {
			provider := a.cli.Provider
			if provider == "" {
				provider = os.Getenv("LLM_PROVIDER")
			}
			if provider == "" {
				provider = config.Global.GetString("LLM_PROVIDER")
			}
			model := a.cli.Model
			a.agentDispatcher.UpdateProviderModel(provider, model)
			a.logger.Info("Dispatcher provider/model updated for parallel agents",
				zap.String("provider", provider),
				zap.String("model", model),
			)
		}
		return true
	}

	a.agentRegistry = workers.SetupDefaultRegistry()

	// Load custom persona agents into the worker registry
	if a.cli.personaHandler != nil {
		mgr := a.cli.personaHandler.GetManager()
		if customCount := workers.LoadCustomAgents(a.agentRegistry, mgr, a.logger); customCount > 0 {
			a.logger.Info("Custom persona agents loaded as workers",
				zap.Int("count", customCount),
			)
		}
	}

	a.fileLockMgr = workers.NewFileLockManager()

	maxWorkersStr := os.Getenv("CHATCLI_AGENT_MAX_WORKERS")
	maxWorkers := workers.DefaultMaxWorkers
	if maxWorkersStr != "" {
		if v, err := strconv.Atoi(maxWorkersStr); err == nil && v > 0 {
			maxWorkers = v
		}
	}

	workerTimeout := workers.DefaultWorkerTimeout
	if ts := os.Getenv("CHATCLI_AGENT_WORKER_TIMEOUT"); ts != "" {
		if d, err := time.ParseDuration(ts); err == nil && d > 0 {
			workerTimeout = d
		}
	}

	// Determine provider/model from current active client (not defaults).
	// a.cli.Provider is the runtime-resolved provider (from -provider flag, env, or config).
	// Falling back to env/config only if cli.Provider is somehow empty.
	provider := a.cli.Provider
	if provider == "" {
		provider = os.Getenv("LLM_PROVIDER")
	}
	if provider == "" {
		provider = config.Global.GetString("LLM_PROVIDER")
	}
	// Use a.cli.Model (the actual API model ID, e.g. "claude-sonnet-4-6-20250514")
	// NOT a.cli.Client.GetModelName() which returns a display name (e.g. "Claude sonnet 4.6").
	model := a.cli.Model

	cfg := workers.DispatcherConfig{
		MaxWorkers:    maxWorkers,
		ParallelMode:  true,
		Provider:      provider,
		Model:         model,
		WorkerTimeout: workerTimeout,
	}

	a.agentDispatcher = workers.NewDispatcher(a.agentRegistry, a.cli.manager, cfg, a.logger)

	// Attach policy enforcement so parallel workers respect security rules
	if pa, err := newWorkerPolicyAdapter(a.logger); err == nil {
		a.policyAdapter = pa
		a.agentDispatcher.SetPolicyChecker(pa)
		a.logger.Info("Policy enforcement enabled for parallel workers")
	} else {
		a.logger.Warn("Failed to initialize policy checker for parallel workers", zap.Error(err))
	}

	// Attach the seven-pattern quality pipeline (Self-Refine, CoVe,
	// Reflexion, …). Pipeline starts with the hooks selected by the
	// CHATCLI_QUALITY_* env, with /refine and /verify session toggles
	// layered on top. With zero hooks (the default), Run is a thin
	// pass-through to agent.Execute — no measurable overhead.
	//
	// dispatchOne lets quality hooks invoke other agents (Refiner,
	// Verifier) without taking a direct dependency on the dispatcher.
	// Built-in ExcludeAgents prevent the obvious infinite-recursion
	// case (refining the refiner's output, verifying the verifier's).
	a.qualityConfig = quality.LoadFromEnv()
	if a.cli.qualityOverrides.Refine != nil {
		a.qualityConfig.Refine.Enabled = *a.cli.qualityOverrides.Refine
	}
	if a.cli.qualityOverrides.Verify != nil {
		a.qualityConfig.Verify.Enabled = *a.cli.qualityOverrides.Verify
	}
	dispatchOne := func(ctx context.Context, call workers.AgentCall) workers.AgentResult {
		results := a.agentDispatcher.Dispatch(ctx, []workers.AgentCall{call})
		if len(results) == 0 {
			return workers.AgentResult{
				CallID: call.ID, Agent: call.Agent, Task: call.Task,
				Error: fmt.Errorf("dispatcher returned no result for quality hook"),
			}
		}
		return results[0]
	}
	// Reflexion (Phase 4) needs an LLM call + a memory-persist
	// callback. Both are wired here so the pipeline package stays
	// independent of cli.ChatCLI internals.
	lessonLLM := a.cli.makeLessonLLM()
	persistLesson := a.cli.makeLessonPersister()
	a.qualityPipeline = quality.BuildPipeline(a.qualityConfig, a.logger, quality.BuildPipelineDeps{
		Dispatch:      dispatchOne,
		LessonLLM:     lessonLLM,
		PersistLesson: persistLesson,
	})
	a.agentDispatcher.SetPipeline(a.qualityPipeline)

	a.parallelMode = true

	a.logger.Info("Multi-agent orchestration enabled",
		zap.Int("maxWorkers", maxWorkers),
		zap.Duration("workerTimeout", workerTimeout),
		zap.String("provider", provider),
		zap.String("model", model),
	)

	return true
}

// buildSessionWorkspaceHint returns a compact prompt block that teaches the
// model how to use the per-session scratch dir and how to recover data from
// truncated tool results. Returns an empty string when the session workspace
// hasn't been initialized (shouldn't happen during normal startup).
func buildSessionWorkspaceHint() string {
	ws := agent.GetSessionWorkspace()
	if ws == nil {
		return ""
	}
	return "\n\n## SESSION WORKSPACE & LARGE OUTPUTS\n" +
		"\n" +
		"You have an isolated scratch directory for this session, exposed via the\n" +
		"environment variable `CHATCLI_AGENT_TMPDIR` (current value: `" + ws.ScratchDir + "`).\n" +
		"Both read and write are ALLOWED in this directory and in its subtree —\n" +
		"use it whenever you need to:\n" +
		"- stage a temporary shell script before exec'ing it;\n" +
		"- persist an intermediate artifact between tool calls;\n" +
		"- avoid polluting the project tree with one-off files.\n" +
		"\n" +
		"Example: `exec { \"cmd\": \"cat > $CHATCLI_AGENT_TMPDIR/patch.sh <<'EOF' ...\" }`.\n" +
		"\n" +
		"### Truncated tool outputs\n" +
		"\n" +
		"When a tool result is large, ChatCLI automatically truncates it inline\n" +
		"and saves the FULL output to a file in this session. You will see a\n" +
		"marker like:\n" +
		"\n" +
		"    ... [N chars omitted — full output saved to /tmp/chatcli-agent-XXX/tool-results/budget_xxx.txt]\n" +
		"    ... [full output saved to /tmp/chatcli-agent-XXX/tool-results/result_XXX.txt — N bytes total]\n" +
		"\n" +
		"When you see such a marker and the omitted portion matters, use the\n" +
		"`read_file` tool with `start` / `end` line numbers to examine specific\n" +
		"ranges of the saved file — do NOT re-run the original tool call.\n" +
		"\n" +
		"### Delegating heavy analysis\n" +
		"\n" +
		"For tasks that would otherwise flood your context with raw data (large\n" +
		"metrics endpoints, verbose logs, wide-scope searches), use the\n" +
		"`delegate_subagent` tool. The subagent runs with its OWN isolated\n" +
		"context window; only its final summary returns to you. Example use\n" +
		"cases: \"summarize memory hotspots from /metrics\", \"find all call\n" +
		"sites of func X across the repo\".\n"
}

// isDelegateInvocation reports whether an @coder JSON args payload is a
// delegate_subagent call rather than a normal engine subcommand.
func isDelegateInvocation(argsJSON string) bool {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var outer struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(trimmed), &outer); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(outer.Cmd), "delegate")
}

// extractDelegateArgs pulls the inner args map from an @coder delegate call.
// Returns (nativeArgsMap, rawJSONString) — the map is preferred when non-nil,
// and rawJSONString is a fallback for XML-style args.
func extractDelegateArgs(argsJSON string) (map[string]interface{}, string) {
	var outer struct {
		Cmd  string          `json:"cmd"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &outer); err != nil {
		return nil, argsJSON
	}
	if len(outer.Args) == 0 {
		return nil, ""
	}
	var inner map[string]interface{}
	if err := json.Unmarshal(outer.Args, &inner); err == nil {
		return inner, string(outer.Args)
	}
	return nil, string(outer.Args)
}
