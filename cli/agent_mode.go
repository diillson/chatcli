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
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/metrics"
	"github.com/diillson/chatcli/cli/paste"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
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
	// Centralized stdin reader for type-ahead queue support
	stdinLines chan string   // all stdin lines flow through here
	stdinDone  chan struct{} // signals reader goroutine to stop
	stdinWg    sync.WaitGroup
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

// Run inicia o modo agente com uma consulta do usuário, utilizando um loop de Raciocínio-Ação (ReAct).
// Agora aceita systemPromptOverride para definir personas específicas (ex: Coder).
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string, systemPromptOverride string) error {
	// --- 1. CONFIGURAÇÃO E PREPARAÇÃO DO AGENTE ---
	maxTurns := AgentMaxTurns()

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

	a.isCoderMode = isCoder
	a.isOneShot = false

	// Prepend workspace context (SOUL.md, USER.md, IDENTITY.md, RULES.md, MEMORY.md)
	if a.cli.contextBuilder != nil {
		if wsCtx := a.cli.contextBuilder.BuildSystemPromptPrefix(); wsCtx != "" {
			systemInstruction = wsCtx + "\n\n---\n\n" + systemInstruction
		}
		if dynCtx := a.cli.contextBuilder.BuildDynamicContext(); dynCtx != "" {
			systemInstruction += "\n\n" + dynCtx
		}
	}

	// Adiciona contexto de ferramentas (plugins) ao prompt
	systemInstruction += a.getToolContextString()

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
	systemInstruction := i18n.T("agent.system_prompt.oneshot")

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
	plugins := a.cli.pluginManager.GetPlugins()
	if len(plugins) == 0 {
		return ""
	}

	var toolDescriptions []string
	coderCheatSheet := ""
	for _, plugin := range plugins {
		if a.isCoderMode && !strings.EqualFold(plugin.Name(), "@coder") {
			continue
		}

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

		// Compact history if over budget (before building turn history)
		cfg := DefaultCompactConfig(a.cli.Provider, a.cli.Model)
		cfg.BudgetRatio = 0.60 // tighter budget — tool outputs are large
		cfg.MinKeepRecent = 8  // ~4 tool call cycles
		if a.cli.historyCompactor.NeedsCompaction(a.cli.history, cfg) {
			if compacted, compactErr := a.cli.historyCompactor.Compact(ctx, a.cli.history, a.cli.Client, cfg); compactErr == nil {
				a.cli.history = compacted
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

		// Chamada à LLM
		aiResponse, err := a.cli.Client.SendPrompt(ctx, "", turnHistory, 0)
		// Auto-retry on OAuth token expiration (401)
		if a.cli.refreshClientOnAuthError(err) {
			aiResponse, err = a.cli.Client.SendPrompt(ctx, "", turnHistory, 0)
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
			// Se for cancelamento, retorna limpo para o cli.go tratar
			if errors.Is(err, context.Canceled) {
				return err
			}
			return fmt.Errorf("erro ao obter resposta da IA no turno %d: %w", turn+1, err)
		}

		// Persistir a resposta no histórico "real"
		a.cli.history = append(a.cli.history, models.Message{Role: "assistant", Content: aiResponse})

		// Parsear Tool Calls (XML)
		toolCalls, parseErr := agent.ParseToolCalls(aiResponse)
		if parseErr != nil {
			a.logger.Warn("Falha ao parsear tool_calls", zap.Error(parseErr))
			toolCalls = nil
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

		if strings.TrimSpace(reasoning) != "" {
			if coderMinimal {
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
			if coderMinimal {
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
			if coderMinimal {
				renderer.RenderTimelineEvent("🧩", "STATUS", compactText(progress, 2, 220), agent.ColorLime)
			} else {
				renderMDCard("🧩", "PLANO DE AÇÃO", progress, agent.ColorLime)
			}
		}

		// Renderizar progresso inicial das tarefas (somente no modo /coder)
		renderPlanProgress()
		if strings.TrimSpace(remaining) != "" {
			if coderMinimal {
				renderer.RenderTimelineEvent("💬", "RESUMO", compactText(remaining, 2, 220), agent.ColorGray)
			} else {
				renderMDCard("💬", "RESPOSTA", remaining, agent.ColorGray)
			}
		}

		// =========================
		// VALIDAÇÕES ESTRITAS DO /CODER
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

				// Require exclusive use of @coder
				if !strings.EqualFold(strings.TrimSpace(toolCalls[0].Name), "@coder") {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "FORMAT ERROR: In /coder mode, the ONLY allowed tool is @coder. " +
							"Resend using: <tool_call name=\"@coder\" args='{\"cmd\":\"...\",\"args\":{...}}' />",
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
							"You MUST use <reasoning> followed by <tool_call name=\"@coder\" args='{\"cmd\":\"exec\",\"args\":{\"cmd\":\"your command\"}}' />",
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
				n := len(agentCalls)
				agentWord := "agent"
				if n > 1 {
					agentWord = "agents"
				}
				if coderMinUI {
					renderer.RenderTimelineEvent("🚀", "AGENTS", fmt.Sprintf("%d %s dispatched", n, agentWord), agent.ColorPurple)
				} else {
					renderer.RenderTimelineEvent("🚀", "MULTI-AGENT DISPATCH", fmt.Sprintf("Dispatching %d %s", n, agentWord), agent.ColorPurple)
				}

				for i, ac := range agentCalls {
					renderer.RenderTimelineEvent("🤖", fmt.Sprintf("[%s] #%d", ac.Agent, i+1), truncateForUI(ac.Task, 120), agent.ColorCyan)
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
						renderer.RenderTimelineEvent("❌", fmt.Sprintf("[%s] FAILED", ar.Agent), ar.Error.Error(), agent.ColorYellow)
					} else {
						successCount++
						summary := truncateForUI(ar.Output, 200)
						// Mostra info de paralelismo quando houver
						parallelInfo := ""
						if ar.ParallelCalls > 1 {
							parallelInfo = fmt.Sprintf(", %d em paralelo", ar.ParallelCalls)
						}
						tcLabel := "tool call"
						if tcCount != 1 {
							tcLabel = "tool calls"
						}
						title := fmt.Sprintf("[%s] OK (%s, %d %s%s)", ar.Agent, ar.Duration.Round(time.Millisecond), tcCount, tcLabel, parallelInfo)
						renderer.RenderTimelineEvent("✅", title, summary, agent.ColorGreen)
					}
				}
				a.toolCallsExecd += totalAgentToolCalls
				turnToolCalls += totalAgentToolCalls

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
			var batchOutputBuilder strings.Builder
			var batchHasError bool
			successCount := 0
			totalActions := len(toolCalls)

			// 1. Renderiza cabeçalho do lote se houver mais de 1 ação
			if totalActions > 1 {
				if coderMinimal {
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
							msg := "🛫 AÇÃO BLOQUEADA PELO USUÁRIO (Regra de Segurança). NÃO TENTE NOVAMENTE."
							if coderMinimal {
								renderer.RenderToolResultMinimal(msg, true)
							} else {
								renderer.RenderToolResult(msg, true)
							}
							a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
							batchHasError = true
							break
						}
						if action == coder.ActionAsk {
							decision := coder.PromptSecurityCheck(ctx, tc.Name, tc.Args, a.stdinLines)
							pattern := coder.GetSuggestedPattern(tc.Name, tc.Args)
							switch decision {
							case coder.DecisionAllowAlways:
								// Only persist rule if pattern is non-empty.
								// Exec commands return "" to prevent blanket allow.
								if pattern != "" {
									_ = pm.AddRule(pattern, coder.ActionAllow)
								}
							case coder.DecisionDenyForever:
								if pattern != "" {
									_ = pm.AddRule(pattern, coder.ActionDeny)
								}
								msg := "🛫 AÇÃO BLOQUEADA PERMANENTEMENTE. NÃO TENTE NOVAMENTE."
								if coderMinimal {
									renderer.RenderToolResultMinimal(msg, true)
								} else {
									renderer.RenderToolResult(msg, true)
								}
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionDenyOnce:
								msg := "🛫 AÇÃO NEGADA PELO USUÁRIO DESTA VEZ. Tente uma abordagem diferente ou pergunte ao usuário."
								if coderMinimal {
									renderer.RenderToolResultMinimal(msg, true)
								} else {
									renderer.RenderToolResult(msg, true)
								}
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionCancelled:
								msg := "⏹ OPERAÇÃO CANCELADA PELO USUÁRIO (Ctrl+C). Pode tentar a mesma ação novamente se necessário."
								if coderMinimal {
									renderer.RenderToolResultMinimal(msg, true)
								} else {
									renderer.RenderToolResult(msg, true)
								}
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

				// UX: Pequena pausa para separar visualmente o pensamento da ação
				time.Sleep(200 * time.Millisecond)

				// 2. Renderiza a BOX de ação IMEDIATAMENTE (antes de processar)
				// Isso dá feedback visual "Real-Time" do que está prestes a acontecer
				if coderMinimal {
					renderer.RenderToolCallMinimal(toolName, toolArgsStr, i+1, totalActions)
				} else {
					renderer.RenderToolCallWithProgress(toolName, toolArgsStr, i+1, totalActions)
				}

				// UX: Força flush e pausa para leitura
				os.Stdout.Sync()
				time.Sleep(300 * time.Millisecond)

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
						if coderMinimal {
							renderer.RenderToolResultMinimal("Format error: args contain line breaks.\n"+msg, true)
						} else {
							renderer.RenderToolResult("Format error: args contain line breaks.\n"+msg, true)
						}

						a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
						batchHasError = true
						break
					}
				}

				toolArgs, parseErr := parseToolArgsWithJSON(normalizedArgsStr)
				var toolOutput string
				var execErr error

				// --- Preparação da Execução ---
				if parseErr != nil {
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
							a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "Ferramenta não encontrada. Use @coder."})
							batchHasError = true
						}
					} else {
						// Guard-rail do /coder (@coder) - Argumentos obrigatórios
						if a.isCoderMode && strings.EqualFold(strings.TrimSpace(toolName), "@coder") {
							if missing, which := isCoderArgsMissingRequiredValue(toolArgs); missing {
								msg := buildCoderToolCallFixPrompt(which)
								// Feedback visual
								if coderMinimal {
									renderer.RenderToolResultMinimal("Args inválido para @coder: falta argumento válido em "+which, true)
								} else {
									renderer.RenderToolResult("Args inválido para @coder: falta argumento válido em "+which, true)
								}

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
								if coderMinimal {
									renderer.RenderToolResultMinimal(msg, true)
								} else {
									renderer.RenderToolResult(msg, true)
								}
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
							// UX: Animação durante a execução
							subCmd := "ação"
							if len(toolArgs) > 0 {
								subCmd = toolArgs[0]
							}
							a.cli.animation.StopThinkingAnimation()

							renderer.RenderStreamBoxStart("🔨", fmt.Sprintf("EXECUTANDO: %s %s", toolName, subCmd), agent.ColorPurple)

							streamCallback := func(line string) {
								renderer.StreamOutput(line)
							}

							// Marca tarefa como em andamento ANTES de executar
							agent.MarkTaskInProgress(a.taskTracker)
							toolOutput, execErr = plugin.ExecuteWithStream(ctx, toolArgs, streamCallback)

							renderer.RenderStreamBoxEnd(agent.ColorPurple)

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
				if coderMinimal {
					renderer.RenderToolResultMinimal(displayForHuman, execErr != nil)
				} else {
					renderer.RenderToolResult(displayForHuman, execErr != nil)
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
				renderer.RenderBatchSummary(successCount, totalActions, batchHasError)
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

	a.parallelMode = true

	a.logger.Info("Multi-agent orchestration enabled",
		zap.Int("maxWorkers", maxWorkers),
		zap.Duration("workerTimeout", workerTimeout),
		zap.String("provider", provider),
		zap.String("model", model),
	)

	return true
}
