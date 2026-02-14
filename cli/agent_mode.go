/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/metrics"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/openai_assistant"
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
	tokenCounter *metrics.TokenCounter
	turnTimer    *metrics.Timer
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

// NewAgentMode cria uma nova instância do modo agente
func NewAgentMode(cli *ChatCLI, logger *zap.Logger) *AgentMode {
	// Obtém o nome do modelo para inicializar o contador de tokens
	modelName := ""
	provider := ""
	override := 0
	if cli != nil && cli.Client != nil {
		modelName = cli.Client.GetModelName()
		provider = cli.Provider
		effectiveLimit := cli.getMaxTokensForCurrentLLM()
		override = effectiveLimit
	}

	a := &AgentMode{
		cli:            cli,
		logger:         logger,
		executor:       agent.NewCommandExecutor(logger),
		validator:      agent.NewCommandValidator(logger),
		contextManager: agent.NewContextManager(logger),
		taskTracker:    agent.NewTaskTracker(logger),
		tokenCounter:   metrics.NewTokenCounter(provider, modelName, override),
		turnTimer:      metrics.NewTimer(),
	}
	a.executeCommandsFunc = a.executeCommandsWithOutput
	return a
}

func isCoderMinimalUI() bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_UI")))
	if val == "" || val == "full" || val == "false" || val == "0" {
		return false
	}
	if val == "minimal" || val == "min" || val == "true" || val == "1" {
		return true
	}
	return false
}

func isCoderBannerEnabled() bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_BANNER")))
	if val == "" || val == "true" || val == "1" || val == "yes" {
		return true
	}
	if val == "false" || val == "0" || val == "no" {
		return false
	}
	return true
}

func compactText(input string, maxLines int, maxLen int) string {
	lines := strings.Split(input, "\n")
	out := make([]string, 0, maxLines)
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
		if len(out) >= maxLines {
			break
		}
	}
	joined := strings.Join(out, " · ")
	if maxLen > 0 && len(joined) > maxLen {
		joined = joined[:maxLen] + "..."
	}
	return joined
}

// getInput obtém entrada do usuário de forma segura
func (a *AgentMode) getInput(prompt string) string {
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()

	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return "q"
		}
		a.logger.Warn("Erro ao ler entrada no modo agente", zap.Error(err))
		return ""
	}
	return strings.TrimSpace(input)
}

// Run inicia o modo agente com uma consulta do usuário, utilizando um loop de Raciocínio-Ação (ReAct).
// Agora aceita systemPromptOverride para definir personas específicas (ex: Coder).
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string, systemPromptOverride string) error {
	// --- 1. CONFIGURAÇÃO E PREPARAÇÃO DO AGENTE ---
	maxTurns := AgentMaxTurns()

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

	// Adiciona contexto de ferramentas (plugins) ao prompt
	systemInstruction += a.getToolContextString()

	// Banner curto com cheat sheet no modo /coder (apenas para o usuário humano)
	if isCoder && !a.coderBannerShown && isCoderBannerEnabled() {
		fmt.Println()
		fmt.Println("💡 Dica rápida (/coder):")
		fmt.Println("  - read: {\"cmd\":\"read\",\"args\":{\"file\":\"main.go\"}}")
		fmt.Println("  - search: {\"cmd\":\"search\",\"args\":{\"term\":\"Login\",\"dir\":\".\"}}")
		fmt.Println("  - write: {\"cmd\":\"write\",\"args\":{\"file\":\"x.go\",\"encoding\":\"base64\",\"content\":\"...\"}}")
		fmt.Println("  - exec: {\"cmd\":\"exec\",\"args\":{\"cmd\":\"mkdir -p testeapi\"}}")
		fmt.Println()
		a.coderBannerShown = true
	}

	// Inicializa ou atualiza o histórico com o System Prompt correto
	if len(a.cli.history) == 0 {
		a.cli.history = append(a.cli.history, models.Message{Role: "system", Content: systemInstruction})
	} else {
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

	a.cli.history = append(a.cli.history, models.Message{Role: "system", Content: systemInstruction})
	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: query})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

	aiResponse, err := a.cli.Client.SendPrompt(ctx, query, a.cli.history, 0)
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

// findLastMeaningfulLine extrai a última linha não vazia de um bloco de texto
func findLastMeaningfulLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "```") {
			return line
		}
	}
	return ""
}

// extractCommandBlocks extrai blocos de comando da resposta da IA
// extractCommandBlocks extrai blocos de comando da resposta da IA
func (a *AgentMode) extractCommandBlocks(response string) []CommandBlock {
	var commandBlocks []CommandBlock

	_, isAssistant := a.cli.Client.(*openai_assistant.OpenAIAssistantClient)
	if isAssistant {
		return a.extractCommandBlocksForAssistant(response)
	}

	// Regex para pegar blocos execute:lang ou blocos genéricos ```bash
	re := regexp.MustCompile("(?s)```execute:\\s*([a-zA-Z0-9_-]+)\\s*\n(.*?)```")
	matches := re.FindAllStringSubmatch(response, -1)

	// Fallback para blocos genéricos se não houver execute:
	if len(matches) == 0 {
		fb := regexp.MustCompile("(?s)```(?:sh|bash|shell)\\s*\\n(.*?)```").FindAllStringSubmatch(response, -1)
		for _, m := range fb {
			commandsStr := strings.TrimSpace(m[1])
			isScript := isShellScript(commandsStr)

			// LÓGICA CORRIGIDA AQUI:
			if isScript {
				// Se é script complexo, mantém junto
				commandBlocks = append(commandBlocks, CommandBlock{
					Description: "Script Shell (Bloco Completo)",
					Commands:    []string{commandsStr}, // Array com 1 elemento string
					Language:    "shell",
					ContextInfo: CommandContextInfo{SourceType: SourceTypeUserInput, IsScript: true, ScriptType: "shell"},
				})
			} else {
				// Se são comandos soltos, separa um por um
				cmds := splitCommandsByBlankLine(commandsStr)
				for _, cmd := range cmds {
					commandBlocks = append(commandBlocks, CommandBlock{
						Description: "Comando Shell",
						Commands:    []string{cmd},
						Language:    "shell",
						ContextInfo: CommandContextInfo{SourceType: SourceTypeUserInput, IsScript: false, ScriptType: "shell"},
					})
				}
			}
		}
		return commandBlocks
	}

	parts := re.Split(response, -1)

	for i, match := range matches {
		if len(match) >= 3 {
			language := strings.TrimSpace(match[1])
			commandsStr := strings.TrimSpace(match[2])

			var description string
			if i < len(parts) {
				explanationRe := regexp.MustCompile("(?s)<explanation>(.*?)</explanation>")
				explanationMatch := explanationRe.FindStringSubmatch(parts[i])

				if len(explanationMatch) > 1 {
					description = strings.TrimSpace(explanationMatch[1])
				} else {
					description = findLastMeaningfulLine(parts[i])
				}
			}

			isScript := false
			if strings.Contains(commandsStr, "<<") ||
				strings.Contains(commandsStr, "cat >") ||
				regexp.MustCompile(`if\s+.*\s+then`).MatchString(commandsStr) ||
				regexp.MustCompile(`for\s+.*\s+do`).MatchString(commandsStr) {
				isScript = true
			}

			// LÓGICA CORRIGIDA TAMBÉM PARA BLOCOS EXECUTE:
			if isScript {
				// Script complexo = 1 Bloco
				commandBlocks = append(commandBlocks, CommandBlock{
					Description: description,
					Commands:    []string{commandsStr},
					Language:    language,
					ContextInfo: CommandContextInfo{
						SourceType: SourceTypeUserInput,
						IsScript:   true,
						ScriptType: language,
					},
				})
			} else {
				// Lista de comandos simples = Múltiplos Blocos
				commandsList := splitCommandsByBlankLine(commandsStr)

				for _, cmd := range commandsList {
					cmd = strings.TrimSpace(cmd) // Garante que o comando não tem espaços inúteis nas pontas

					// Descrição padrão: começa com a do bloco
					specificDesc := description

					// Se temos mais de um comando splitado, DEVEMOS tentar dar um nome específico para cada
					if len(commandsList) > 1 {
						lines := strings.Split(cmd, "\n")
						derivedDesc := ""

						// 1. Tenta achar comentário (#) para usar como título
						for _, line := range lines {
							t := strings.TrimSpace(line)
							if strings.HasPrefix(t, "#") {
								derivedDesc = strings.TrimSpace(strings.TrimPrefix(t, "#"))
								break
							}
						}

						// 2. Se não achou comentário, usa a PRIMEIRA LINHA NÃO-VAZIA do comando
						if derivedDesc == "" {
							for _, line := range lines {
								t := strings.TrimSpace(line)
								// Ignora linhas vazias ou que só tenham continuadores de linha
								if t != "" && t != "\\" {
									// Limpeza visual
									cleanLine := strings.ReplaceAll(t, "\\", "")
									cleanLine = strings.TrimSpace(cleanLine)

									if len(cleanLine) > 60 {
										derivedDesc = cleanLine[:57] + "..."
									} else {
										derivedDesc = cleanLine
									}
									break // Achou a primeira linha útil, para.
								}
							}
						}

						// Se conseguimos derivar algo específico, substituímos a descrição geral
						if derivedDesc != "" {
							specificDesc = derivedDesc
						}
					}

					// Cria o bloco
					commandBlocks = append(commandBlocks, CommandBlock{
						Description: specificDesc,
						Commands:    []string{cmd},
						Language:    language,
						ContextInfo: CommandContextInfo{
							SourceType: SourceTypeUserInput,
							IsScript:   false,
							ScriptType: language, // shell, bash, etc
						},
					})
				}
			}
		}
	}

	return commandBlocks
}

// extractCommandBlocksForAssistant extrai comandos de respostas do OpenAI Assistant
func (a *AgentMode) extractCommandBlocksForAssistant(response string) []CommandBlock {
	var commandBlocks []CommandBlock

	codeBlockRe := regexp.MustCompile("```(?:sh|bash|shell)?\\s*\n([\\s\\S]*?)```")
	codeMatches := codeBlockRe.FindAllStringSubmatch(response, -1)

	commandLineRe := regexp.MustCompile(`(?m)^[$#]\s*(.+)$`)
	commandMatches := commandLineRe.FindAllStringSubmatch(response, -1)

	for _, match := range codeMatches {
		if len(match) >= 2 {
			commands := splitCommandsByBlankLine(match[1])
			description := findDescriptionBeforeBlock(response, match[0])

			commandBlocks = append(commandBlocks, CommandBlock{
				Description: description,
				Commands:    commands,
				Language:    "shell",
				ContextInfo: CommandContextInfo{
					SourceType: SourceTypeUserInput,
					IsScript:   len(commands) > 1 || isShellScript(match[1]),
					ScriptType: "shell",
				},
			})
		}
	}

	if len(commandBlocks) == 0 && len(commandMatches) > 0 {
		var commands []string
		for _, match := range commandMatches {
			if len(match) >= 2 {
				cmd := strings.TrimSpace(match[1])
				if cmd != "" {
					commands = append(commands, cmd)
				}
			}
		}

		if len(commands) > 0 {
			commandBlocks = append(commandBlocks, CommandBlock{
				Description: "Comandos extraídos da resposta",
				Commands:    commands,
				Language:    "shell",
				ContextInfo: CommandContextInfo{
					SourceType: SourceTypeUserInput,
					IsScript:   false,
				},
			})
		}
	}

	return commandBlocks
}

// findDescriptionBeforeBlock encontra descrição antes de um bloco de código
func findDescriptionBeforeBlock(response, block string) string {
	blockIndex := strings.Index(response, block)
	if blockIndex <= 0 {
		return ""
	}

	startIndex := max(0, blockIndex-200)
	prefix := response[startIndex:blockIndex]

	lines := strings.Split(prefix, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}

	return ""
}

// splitCommandsByBlankLine divide comandos por linha em branco
func splitCommandsByBlankLine(src string) []string {
	var cmds []string
	var buf []string
	lines := strings.Split(src, "\n")
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			if len(buf) > 0 {
				cmds = append(cmds, strings.Join(buf, "\n"))
				buf = nil
			}
		} else {
			buf = append(buf, l)
		}
	}
	if len(buf) > 0 {
		cmds = append(cmds, strings.Join(buf, "\n"))
	}
	return cmds
}

// displayResponseWithoutCommands exibe resposta sem os blocos de comando
func (a *AgentMode) displayResponseWithoutCommands(response string, blocks []CommandBlock) {
	displayResponse := response
	for i, block := range blocks {
		originalBlock := fmt.Sprintf("```execute:%s\n%s```",
			block.Language,
			strings.Join(block.Commands, "\n"))

		replacement := fmt.Sprintf("\n[Comando #%d: %s]\n", i+1, block.Description)
		displayResponse = strings.Replace(displayResponse, originalBlock, replacement, 1)
	}

	renderedResponse := a.cli.renderMarkdown(displayResponse)
	a.cli.typewriterEffect(fmt.Sprintf("\n%s:\n%s\n", a.cli.Client.GetModelName(), renderedResponse), 2*time.Millisecond)
}

// getMultilineInput obtém entrada de múltiplas linhas
func (a *AgentMode) getMultilineInput(prompt string) string {
	fmt.Print(prompt)
	fmt.Println(i18n.T("agent.multiline_input_tip"))

	var lines []string
	reader := bufio.NewReader(os.Stdin)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			break
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

// max retorna o maior entre dois inteiros
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// detectHeredocs verifica presença de heredocs
func detectHeredocs(script string) bool {
	heredocPattern := regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?`)
	return heredocPattern.MatchString(script)
}

// isShellScript determina se o conteúdo é um script shell
func isShellScript(content string) bool {
	return detectHeredocs(content) ||
		strings.Contains(content, "#!/bin/") ||
		regexp.MustCompile(`if\s+.*\s+then`).MatchString(content) ||
		regexp.MustCompile(`for\s+.*\s+in\s+.*\s+do`).MatchString(content) ||
		regexp.MustCompile(`while\s+.*\s+do`).MatchString(content) ||
		regexp.MustCompile(`case\s+.*\s+in`).MatchString(content) ||
		strings.Contains(content, "function ") ||
		strings.Count(content, "{") > 1 && strings.Count(content, "}") > 1
}

// handleCommandBlocks processa blocos de comandos com UI refinada
func (a *AgentMode) handleCommandBlocks(ctx context.Context, blocks []CommandBlock) {
	outputs := make([]*CommandOutput, len(blocks))
	showFullPlan := false
	lastExecuted := -1

	renderer := agent.NewUIRenderer(a.logger)
	renderer.SetSkipClearOnNextDraw(true)

	//mainLoop:
	for {
		renderer.ClearScreen()
		renderer.PrintHeader()

		if showFullPlan {
			renderer.PrintPlanFull(blocks, outputs, a.validator)
		} else {
			renderer.PrintPlanCompact(blocks, outputs)
		}

		renderer.PrintLastResult(outputs, lastExecuted)
		renderer.PrintMenu()

		prompt := renderer.PrintPrompt()
		answer := a.getInput(prompt)
		answer = strings.ToLower(strings.TrimSpace(answer))

		switch {
		case answer == "q":
			fmt.Println(renderer.Colorize(i18n.T("agent.status.exiting"), agent.ColorGray))
			return

		case answer == "r":
			continue

		case answer == "p":
			showFullPlan = !showFullPlan
			continue

		case answer == "":
			continue

		case strings.HasPrefix(answer, "v"):
			nStr := strings.TrimPrefix(answer, "v")
			n, err := strconv.Atoi(nStr)
			if err != nil || n < 1 || n > len(outputs) || outputs[n-1] == nil {
				fmt.Println(i18n.T("agent.status.no_output_to_show"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			_ = renderer.ShowInPager(outputs[n-1].Output)
			continue

		case strings.HasPrefix(answer, "w"):
			nStr := strings.TrimPrefix(answer, "w")
			n, err := strconv.Atoi(nStr)
			if err != nil || n < 1 || n > len(outputs) || outputs[n-1] == nil {
				fmt.Println(i18n.T("agent.status.no_output_to_save"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			dir := filepath.Join(os.TempDir(), "chatcli-agent-logs")
			_ = os.MkdirAll(dir, 0o700)
			fpath := filepath.Join(dir, fmt.Sprintf("cmd-%d-%d.log", n, time.Now().Unix()))
			if writeErr := os.WriteFile(fpath, []byte(outputs[n-1].Output), 0o600); writeErr != nil {
				fmt.Println(i18n.T("agent.status.error_saving"), writeErr)
			} else {
				fmt.Println(i18n.T("agent.status.file_saved_at"), fpath)
			}
			_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
			continue

		case answer == "a":
			hasDanger := false
			for _, b := range blocks {
				for _, c := range b.Commands {
					if a.validator.IsDangerous(c) {
						hasDanger = true
						break
					}
				}
				if hasDanger {
					break
				}
			}

			if hasDanger {
				fmt.Println(i18n.T("agent.status.batch_warning"))
				fmt.Println(i18n.T("agent.status.batch_check_individual"))
			}

			cmd := exec.Command("stty", "sane")
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			_ = cmd.Run()

			fmt.Print(i18n.T("agent.status.batch_confirm"))
			reader := bufio.NewReader(os.Stdin)
			confirmationInput, _ := reader.ReadString('\n')
			confirmation := strings.ToLower(strings.TrimSpace(confirmationInput))
			if confirmation != "s" && confirmation != "y" {
				fmt.Println(i18n.T("agent.status.batch_cancelled"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			for i, block := range blocks {
				fmt.Printf(i18n.T("agent.status.executing_command", i+1)+"\n", i+1)
				fmt.Printf("  %s %s\n", i18n.T("agent.plan.field.type"), block.Language)
				for j, cmd := range block.Commands {
					fmt.Printf("  %s %d/%d: %s\n", "Comando", j+1, len(block.Commands), cmd)
				}

				freshCtx, freshCancel := a.contextManager.CreateExecutionContext()
				outStr, errStr := a.executeCommandsFunc(freshCtx, block)
				freshCancel()

				outputs[i] = &CommandOutput{
					CommandBlock: block,
					Output:       outStr,
					ErrorMsg:     errStr,
				}
				lastExecuted = i
			}

			fmt.Println(i18n.T("agent.status.all_commands_executed"))
			fmt.Println(i18n.T("agent.status.summary"))
			for i, out := range outputs {
				status := "OK"
				if out == nil || strings.TrimSpace(out.ErrorMsg) != "" {
					status = "ERRO"
				}
				fmt.Printf("- #%d: %s\n", i+1, status)
			}
			_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
			continue

		case strings.HasPrefix(answer, "e"):
			cmdNumStr := strings.TrimPrefix(answer, "e")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println(i18n.T("agent.error.invalid_command_number_edit"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			edited, err := a.editCommandBlock(blocks[cmdNum-1])
			if err != nil {
				fmt.Println(i18n.T("agent.error.error_editing_command"), err)
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			freshCtx, freshCancel := a.contextManager.CreateExecutionContext()
			editedBlock := blocks[cmdNum-1]
			editedBlock.Commands = edited

			outStr, errStr := a.executeCommandsFunc(freshCtx, editedBlock)
			freshCancel()

			outputs[cmdNum-1] = &CommandOutput{
				CommandBlock: editedBlock,
				Output:       outStr,
				ErrorMsg:     errStr,
			}
			lastExecuted = cmdNum - 1
			_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
			continue

		case strings.HasPrefix(answer, "t"):
			cmdNumStr := strings.TrimPrefix(answer, "t")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println(i18n.T("agent.error.invalid_command_number_simulate"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			a.simulateCommandBlock(ctx, blocks[cmdNum-1])

			execNow := a.getInput(i18n.T("agent.status.confirm_exec_after_sim"))
			if strings.ToLower(strings.TrimSpace(execNow)) == "s" || strings.ToLower(strings.TrimSpace(execNow)) == "y" {
				freshCtx, freshCancel := a.contextManager.CreateExecutionContext()
				outStr, errStr := a.executeCommandsFunc(freshCtx, blocks[cmdNum-1])
				freshCancel()

				outputs[cmdNum-1] = &CommandOutput{
					CommandBlock: blocks[cmdNum-1],
					Output:       outStr,
					ErrorMsg:     errStr,
				}
				lastExecuted = cmdNum - 1
			} else {
				fmt.Println(i18n.T("agent.status.simulation_done"))
			}
			_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
			continue

		case strings.HasPrefix(answer, "ac"):
			cmdNumStr := strings.TrimPrefix(answer, "ac")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println(i18n.T("agent.error.invalid_command_number_context"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			if outputs[cmdNum-1] == nil {
				fmt.Println(i18n.T("agent.status.command_not_executed"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			fmt.Println(i18n.T("agent.output_header"))
			fmt.Println("---------------------------------------")
			fmt.Print(outputs[cmdNum-1].Output)
			fmt.Println("---------------------------------------")

			userContext := a.getMultilineInput(i18n.T("agent.prompt.additional_context"))

			// Monta o prompt para a IA
			toolContext := a.getToolContextString()
			prompt := i18n.T("agent.llm_prompt.continuation_with_context",
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				outputs[cmdNum-1].Output,
				outputs[cmdNum-1].ErrorMsg,
				userContext,
			) + toolContext

			a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: prompt})

			// Chama o loop de processamento unificado
			a.continueWithNewAIResponse(ctx)

			// Ao retornar do loop, o agente pode ter terminado ou apresentado um novo plano.
			// Em ambos os casos, saímos do loop do plano de ação atual.
			return

		case strings.HasPrefix(answer, "c"):
			cmdNumStr := strings.TrimPrefix(answer, "c")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println(i18n.T("agent.error.invalid_command_number_continue"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			if outputs[cmdNum-1] == nil {
				fmt.Println(i18n.T("agent.status.command_not_executed"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			// Monta o prompt para a IA
			toolContext := a.getToolContextString()
			prompt := i18n.T("agent.llm_prompt.continuation",
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				outputs[cmdNum-1].Output,
				outputs[cmdNum-1].ErrorMsg,
			) + toolContext

			a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: prompt})

			// Chama o loop de processamento unificado
			a.continueWithNewAIResponse(ctx)

			// Sai do loop do plano de ação atual
			return

		case strings.HasPrefix(answer, "pc"):
			cmdNumStr := strings.TrimPrefix(answer, "pc")
			cmdNum, err := strconv.Atoi(cmdNumStr)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println(i18n.T("agent.error.invalid_command_number_context"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			userContext := a.getMultilineInput(i18n.T("agent.prompt.additional_context"))
			if userContext == "" {
				fmt.Println(i18n.T("agent.error.no_context_provided"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			fmt.Println(i18n.T("agent.status.context_received"))
			// Monta o prompt para a IA
			toolContext := a.getToolContextString()
			prompt := i18n.T("agent.llm_prompt.pre_execution_context",
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				userContext,
			) + toolContext

			a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: prompt})

			// Chama o loop de processamento unificado
			a.continueWithNewAIResponse(ctx)

			// Sai do loop do plano de ação atual
			return

		default:
			cmdNum, err := strconv.Atoi(answer)
			if err != nil || cmdNum < 1 || cmdNum > len(blocks) {
				fmt.Println(i18n.T("agent.error.invalid_option"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			execCtx, execCancel := a.contextManager.CreateExecutionContext()
			outStr, errStr := a.executeCommandsFunc(execCtx, blocks[cmdNum-1])
			execCancel()

			outputs[cmdNum-1] = &CommandOutput{
				CommandBlock: blocks[cmdNum-1],
				Output:       outStr,
				ErrorMsg:     errStr,
			}
			lastExecuted = cmdNum - 1
			_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
		}
	}
}

// executeCommandsWithOutput executa comandos usando o CommandExecutor
func (a *AgentMode) executeCommandsWithOutput(ctx context.Context, block agent.CommandBlock) (string, string) {
	var allOutput strings.Builder
	var lastError string

	langNorm := strings.ToLower(block.Language)
	if langNorm == "git" || langNorm == "docker" || langNorm == "kubectl" {
		langNorm = "shell"
	}

	renderer := agent.NewUIRenderer(a.logger)
	titleContent := i18n.T("agent.status.executing", langNorm)
	contentWidth := agent.VisibleLen(titleContent)
	topBorder := strings.Repeat("─", contentWidth)

	fmt.Println("\n" + renderer.Colorize(topBorder, agent.ColorGray))
	fmt.Println(renderer.Colorize(titleContent, agent.ColorLime+agent.ColorBold))

	allOutput.WriteString(fmt.Sprintf("\nExecutando: %s (tipo: %s)\n", block.Description, langNorm))

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	if block.ContextInfo.IsScript {
		scriptContent := block.Commands[0]
		tmpFile, err := os.CreateTemp("", "chatcli-script-*.sh")
		if err != nil {
			errorMsg := i18n.T("agent.error.create_temp_file", err)
			errMsg := fmt.Sprintf("%s\n", errorMsg)
			fmt.Print(errMsg)
			allOutput.WriteString(errMsg)
			lastError = err.Error()
		} else {
			scriptPath := tmpFile.Name()
			defer func() { _ = os.Remove(scriptPath) }()

			if _, werr := tmpFile.WriteString(scriptContent); werr != nil {
				errorMsg := i18n.T("agent.error.write_script", werr)
				errMsg := fmt.Sprintf("%s\n", errorMsg)
				fmt.Print(errMsg)
				allOutput.WriteString(errMsg)
				lastError = werr.Error()
			}
			_ = tmpFile.Close()
			_ = os.Chmod(scriptPath, 0755)

			header := i18n.T("agent.status.executing_script", shell) + "\n"
			fmt.Print(header)
			allOutput.WriteString(header)

			result, err := a.executor.Execute(ctx, scriptPath, false)

			safe := utils.SanitizeSensitiveText(result.Output)
			for _, line := range strings.Split(strings.TrimRight(safe, "\n"), "\n") {
				fmt.Println("  " + line)
			}
			allOutput.WriteString(safe + "\n")

			if err != nil {
				errMsg := fmt.Sprintf("❌ Erro: %v\n", err)
				allOutput.WriteString(errMsg)
				lastError = err.Error()
			}

			meta := fmt.Sprintf("  [exit=%d, duração=%s]\n", result.ExitCode, result.Duration)
			fmt.Print(meta)
			allOutput.WriteString(fmt.Sprintf("[meta] exit=%d duration=%s\n", result.ExitCode, result.Duration))
		}
	} else {
		for i, cmd := range block.Commands {
			if cmd == "" {
				continue
			}

			trimmed := strings.TrimSpace(cmd)

			if strings.HasPrefix(trimmed, "cd ") || trimmed == "cd" {
				target := strings.TrimSpace(strings.TrimPrefix(trimmed, "cd"))
				if target == "" {
					target = "~"
				}
				if strings.HasPrefix(target, "~") {
					if home, err := os.UserHomeDir(); err == nil {
						if target == "~" {
							target = home
						} else if strings.HasPrefix(target, "~/") {
							target = filepath.Join(home, target[2:])
						}
					}
				}
				if err := os.Chdir(target); err != nil {
					errorMsg := i18n.T("agent.error.change_dir", target, err)
					msg := fmt.Sprintf("%s\n", errorMsg)
					fmt.Print(msg)
					allOutput.WriteString(msg)
					lastError = err.Error()
				} else {
					wd, _ := os.Getwd()
					msg := fmt.Sprintf(i18n.T("agent.status.dir_changed", wd)+"\n", wd)
					fmt.Print(msg)
					allOutput.WriteString(msg)
				}
				continue
			}

			if a.validator.IsDangerous(trimmed) {
				confirmPrompt := i18n.T("agent.status.dangerous_command_confirm")
				confirm := a.getCriticalInput(confirmPrompt)
				if confirm != "sim, quero executar conscientemente" {
					outText := i18n.T("agent.status.dangerous_command_aborted") + "\n"
					fmt.Print(renderer.Colorize(outText, agent.ColorYellow))
					allOutput.WriteString(outText)
					continue
				}
				fmt.Println(renderer.Colorize(i18n.T("agent.status.dangerous_command_confirmed"), agent.ColorYellow))
			}

			header := i18n.T("agent.status.executing_command_n", i+1, len(block.Commands), trimmed) + "\n"
			fmt.Print(header)
			allOutput.WriteString(header)

			isInteractive := false
			if strings.HasSuffix(trimmed, " --interactive") {
				trimmed = strings.TrimSuffix(trimmed, " --interactive")
				isInteractive = true
			} else if strings.Contains(trimmed, "#interactive") {
				trimmed = strings.ReplaceAll(trimmed, "#interactive", "")
				trimmed = strings.TrimSpace(trimmed)
				isInteractive = true
			} else {
				isInteractive = a.validator.IsLikelyInteractive(trimmed)
			}

			if !isInteractive && mightBeInteractive(trimmed, block.ContextInfo) {
				isInteractive = a.askUserIfInteractive(trimmed, block.ContextInfo)
			}

			if isInteractive {
				outText := i18n.T("agent.status.interactive_mode") + "\n"
				fmt.Print(renderer.Colorize(outText, agent.ColorGray))
				allOutput.WriteString(outText)

				time.Sleep(1 * time.Second)

				result, err := a.executor.Execute(ctx, trimmed, true)

				if err != nil {
					errMsg := fmt.Sprintf("❌ Erro: %v\n", err)
					fmt.Print(errMsg)
					allOutput.WriteString(errMsg)
					lastError = err.Error()
				} else {
					okMsg := i18n.T("agent.status.command_finished") + "\n"
					fmt.Print(okMsg)
					allOutput.WriteString(okMsg)
				}

				meta := fmt.Sprintf("  [exit=%d, duração=%s]\n", result.ExitCode, result.Duration)
				fmt.Print(meta)
				allOutput.WriteString(fmt.Sprintf("[meta] exit=%d duration=%s\n", result.ExitCode, result.Duration))
			} else {
				result, err := a.executor.Execute(ctx, trimmed, false)

				safe := utils.SanitizeSensitiveText(result.Output)
				for _, line := range strings.Split(strings.TrimRight(safe, "\n"), "\n") {
					fmt.Println("  " + line)
				}
				allOutput.WriteString(safe + "\n")

				if err != nil {
					errMsg := fmt.Sprintf("❌ Erro: %v\n", err)
					allOutput.WriteString(errMsg)
					lastError = err.Error()
				}

				meta := fmt.Sprintf("  [exit=%d, duração=%s]\n", result.ExitCode, result.Duration)
				fmt.Print(meta)
				allOutput.WriteString(fmt.Sprintf("[meta] exit=%d duration=%s\n", result.ExitCode, result.Duration))
			}
		}
	}

	footerContent := i18n.T("agent.status.execution_complete")
	if lastError != "" {
		footerContent = i18n.T("agent.status.execution_complete_with_errors")
	}
	footerWidth := agent.VisibleLen(footerContent)

	paddingWidth := contentWidth - footerWidth
	if paddingWidth < 0 {
		paddingWidth = 0
	}
	leftPadding := paddingWidth / 2
	rightPadding := paddingWidth - leftPadding

	finalBorder := strings.Repeat("─", leftPadding) + footerContent + strings.Repeat("─", rightPadding)
	fmt.Println(renderer.Colorize(finalBorder, agent.ColorGray))

	allOutput.WriteString("Execução concluída.\n")
	return allOutput.String(), lastError
}

// getCriticalInput obtém entrada para decisões críticas
func (a *AgentMode) getCriticalInput(prompt string) string {
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()

	fmt.Print("\n")
	fmt.Print(prompt)

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	return strings.TrimSpace(response)
}

// askUserIfInteractive pergunta se comando deve ser interativo
func (a *AgentMode) askUserIfInteractive(cmd string, contextInfo agent.CommandContextInfo) bool {
	if contextInfo.SourceType == agent.SourceTypeFile && hasCodeStructures(cmd) {
		return false
	}

	prompt := fmt.Sprintf(i18n.T("agent.status.interactive_confirm", cmd), cmd)
	response := a.getCriticalInput(prompt)
	return strings.HasPrefix(strings.ToLower(response), "s") || strings.HasPrefix(strings.ToLower(response), "y")
}

// simulateCommandBlock simula execução (dry-run)
func (a *AgentMode) simulateCommandBlock(ctx context.Context, block agent.CommandBlock) {
	fmt.Printf(i18n.T("agent.status.simulating_commands", block.Language)+"\n", block.Language)
	fmt.Println("---------------------------------------")

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	for i, cmd := range block.Commands {
		if cmd == "" {
			continue
		}
		fmt.Printf("🔸 Dry-run %d/%d: %s\n", i+1, len(block.Commands), cmd)

		simCmd := fmt.Sprintf("echo '[dry-run] Vai executar: %s'", cmd)

		if block.Language == "shell" {
			out, err := a.executor.CaptureOutput(ctx, shell, []string{"-c", simCmd})
			fmt.Println(string(out))
			if err != nil {
				fmt.Printf("❗ Dry-run falhou: %v\n", err)
			}
		} else if block.Language == "kubernetes" && strings.Contains(cmd, "apply") {
			cmdDry := cmd + " --dry-run=client"
			out, err := a.executor.CaptureOutput(ctx, shell, []string{"-c", cmdDry})
			fmt.Println(string(out))
			if err != nil {
				fmt.Printf("❗ Dry-run falhou: %v\n", err)
			}
		} else {
			out, _ := a.executor.CaptureOutput(ctx, shell, []string{"-c", "echo '[dry-run] " + cmd + "'"})
			fmt.Println(string(out))
		}
	}
	fmt.Println("---------------------------------------")
}

// editCommandBlock abre comandos em editor
func (a *AgentMode) editCommandBlock(block agent.CommandBlock) ([]string, error) {
	choice := a.getInput(i18n.T("agent.status.edit_prompt"))
	choice = strings.ToLower(strings.TrimSpace(choice))

	if choice == "t" {
		editedCommands := make([]string, len(block.Commands))

		for i, cmd := range block.Commands {
			if cmd == "" {
				continue
			}

			prompt := fmt.Sprintf(i18n.T("agent.status.edit_command_prompt", i+1, len(block.Commands), block.Language), i+1, len(block.Commands), block.Language)
			edited := a.getInput(prompt)

			if edited == "" {
				edited = cmd
			}

			editedCommands[i] = edited
		}

		return editedCommands, nil
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	tmpfile, err := os.CreateTemp("", "agent-edit-*.sh")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := strings.Join(block.Commands, "\n")
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		return nil, err
	}
	if err := tmpfile.Close(); err != nil {
		return nil, err
	}

	cmd := exec.Command(editor, tmpfile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	edited, err := os.ReadFile(tmpfile.Name())
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.ReplaceAll(string(edited), "\r\n", "\n"), "\n")
	return lines, nil
}

// mightBeInteractive verifica se comando pode ser interativo
func mightBeInteractive(cmd string, contextInfo agent.CommandContextInfo) bool {
	if contextInfo.SourceType == agent.SourceTypeFile {
		if contextInfo.FileExtension != "" {
			nonInteractiveExtensions := map[string]bool{
				".log": true, ".js": true, ".ts": true, ".py": true, ".go": true,
				".java": true, ".php": true, ".rb": true, ".c": true, ".cpp": true,
			}
			if nonInteractiveExtensions[contextInfo.FileExtension] {
				return false
			}
		}

		if hasCodeStructures(cmd) {
			return false
		}
	}

	possiblyInteractivePatterns := []string{
		"^ping\\s", "^traceroute\\s", "^nc\\s", "^netcat\\s", "^telnet\\s",
		"^ssh\\s", "^top$", "^htop$", "^vi\\s", "^vim\\s", "^nano\\s",
		"^less\\s", "^more\\s", "^tail -f", "^mysql\\s", "^psql\\s",
		"^docker exec -it", "^kubectl exec -it", "^python\\s+-i", "^node\\s+-i",
	}

	for _, pattern := range possiblyInteractivePatterns {
		matched, _ := regexp.MatchString(pattern, cmd)
		if matched {
			return true
		}
	}

	return false
}

// hasCodeStructures detecta estruturas de código
func hasCodeStructures(content string) bool {
	codePatterns := []string{
		"try\\s*{", "catch\\s*\\(", "function\\s+\\w+\\s*\\(", "=>\\s*{",
		"import\\s+[\\w{}\\s]+from", "export\\s+", "class\\s+\\w+",
		"if\\s*\\(.+\\)\\s*{", "for\\s*\\(.+\\)\\s*{", "while\\s*\\(.+\\)\\s*{",
		"switch\\s*\\(.+\\)\\s*{", "\\}\\s*else\\s*\\{",
		"};", "});", "});",
	}

	for _, pattern := range codePatterns {
		matched, _ := regexp.MatchString(pattern, content)
		if matched {
			return true
		}
	}

	openBraces := strings.Count(content, "{")
	closeBraces := strings.Count(content, "}")

	return openBraces > 1 && closeBraces > 1
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
	renderer := agent.NewUIRenderer(a.logger)

	// Helper para construir o histórico com a "âncora" (System Prompt reforçado por turno)
	buildTurnHistoryWithAnchor := func() []models.Message {
		h := make([]models.Message, 0, len(a.cli.history)+1)
		h = append(h, a.cli.history...)

		var anchor string
		if a.isCoderMode {
			anchor = "LEMBRETE (MODO /CODER): Você DEVE responder com <reasoning> curto (2-6 linhas) e depois, se precisar agir, " +
				"enviar um ou mais <tool_call name=\"@coder\" args=\"...\" />. " +
				"Pode agrupar múltiplas ações na mesma resposta (ex: tree + read). " +
				"NÃO use blocos de código (```), NÃO use ```execute:...```. " +
				"Para write/patch: encoding base64 e conteúdo em linha única são OBRIGATÓRIOS."
		} else {
			anchor = "LEMBRETE (MODO /AGENT): Você pode usar ferramentas via <tool_call name=\"@tool\" args=\"...\" /> quando fizer sentido. " +
				"Se for sugerir comandos, use blocos ```execute:<tipo>``` (shell/git/docker/kubectl...). " +
				"Evite comandos destrutivos sem avisos claros e alternativas."
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

		a.logger.Debug("Iniciando turno do agente", zap.Int("turn", turn+1), zap.Int("max_turns", maxTurns))

		// Inicia o timer do turno (substitui a animação de "Pensando...")
		modelName := a.cli.Client.GetModelName()
		a.turnTimer.Start(ctx, func(d time.Duration) {
			fmt.Print(metrics.FormatTimerStatus(d, modelName, "Processando..."))
		})

		turnHistory := buildTurnHistoryWithAnchor()

		// Chamada à LLM
		aiResponse, err := a.cli.Client.SendPrompt(ctx, "", turnHistory, 0)

		// Para o timer e obtém a duração
		turnDuration := a.turnTimer.Stop()
		fmt.Print(metrics.ClearLine()) // Limpa a linha do timer

		// Contabiliza tokens (estimativa)
		var promptText string
		for _, msg := range turnHistory {
			promptText += msg.Content
		}
		a.tokenCounter.AddTurn(promptText, aiResponse)

		// Exibe métricas do turno
		fmt.Println()
		fmt.Println(metrics.FormatTurnInfo(turn+1, maxTurns, turnDuration, a.tokenCounter))

		// Alerta se estiver próximo do limite
		if a.tokenCounter.IsCritical() {
			fmt.Println(metrics.FormatWarning("Atenção: Janela de contexto acima de 90%! Considere iniciar uma nova sessão."))
		} else if a.tokenCounter.IsNearLimit() {
			fmt.Println(metrics.FormatWarning("Janela de contexto acima de 70%."))
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
		// Renderizar progresso das tarefas (somente no modo /coder)
		if a.isCoderMode && a.taskTracker != nil && a.taskTracker.GetPlan() != nil {
			progress := a.taskTracker.FormatProgress()
			if strings.TrimSpace(progress) != "" {
				if coderMinimal {
					renderer.RenderTimelineEvent("🧩", "STATUS", compactText(progress, 2, 220), agent.ColorLime)
				} else {
					renderMDCard("🧩", "PLANO DE AÇÃO", progress, agent.ColorLime)
				}
			}
		}
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
							decision := coder.PromptSecurityCheck(ctx, tc.Name, tc.Args)
							pattern := coder.GetSuggestedPattern(tc.Name, tc.Args)
							switch decision {
							case coder.DecisionAllowAlways:
								_ = pm.AddRule(pattern, coder.ActionAllow)
							case coder.DecisionDenyForever:
								_ = pm.AddRule(pattern, coder.ActionDeny)
								msg := "🛫 AÇÃO BLOQUEADA PERMANENTEMENTE. NÃO TENTE NOVAMENTE."
								if coderMinimal {
									renderer.RenderToolResultMinimal(msg, true)
								} else {
									renderer.RenderToolResult(msg, true)
								}
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionDenyOnce:
								msg := "🛫 AÇÃO NEGADA PELO USUÁRIO. NÃO TENTE O MESMO COMANDO NOVAMENTE. Peça novas instruções ou tente uma alternativa."
								if coderMinimal {
									renderer.RenderToolResultMinimal(msg, true)
								} else {
									renderer.RenderToolResult(msg, true)
								}
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
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

				// Atualiza status da tarefa
				if execErr != nil {
					agent.MarkTaskFailed(a.taskTracker, execErr.Error())
				} else {
					agent.MarkTaskCompleted(a.taskTracker)
				}

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
		fmt.Println(renderer.Colorize("\n🏁 TAREFA CONCLUÍDA", agent.ColorGreen+agent.ColorBold))
		return nil
	}

	fmt.Println(renderer.Colorize(
		fmt.Sprintf("\n⚠️ Limite de %d passos atingido. O agente parou para evitar loop infinito.", maxTurns),
		agent.ColorYellow,
	))
	return nil
}

// sanitizeToolCallArgs normaliza args vindos de <tool_call ... args="..."/> para evitar
// que "continuação de linha" com "\" e/ou "\" pendurado quebre o parsing argv.
//
// Regras aplicadas em ordem:
// 1) Decodifica entidades HTML (&quot; -> ", &#10; -> \n, etc.)
// 2) Normaliza CRLF -> LF
// 3) Processa continuações de linha: "\" + espaços opcionais + newline -> espaço
// 4) Remove "\ " (fora de aspas) que não faz sentido como escape
// 5) Remove "\" final pendurado fora de aspas
// 6) Normaliza espaços múltiplos (fora de aspas)
// 7) Correções semânticas específicas para @coder
func sanitizeToolCallArgs(rawArgs string, logger *zap.Logger, toolName string, isCoderMode bool) string {
	// 1) Decodifica entidades HTML (&quot; -> ", &#10; -> \n, etc.)
	unescaped := html.UnescapeString(rawArgs)

	// 2) Normaliza quebras de linha (CRLF -> LF)
	normalized := strings.ReplaceAll(unescaped, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	// 3) Processa continuações de linha estilo shell: "\" + espaços opcionais + newline
	//    Substitui por um único espaço (para não colar argumentos)
	normalized = processLineContinuations(normalized)

	// 3b) Se parecer JSON escapado, faz unescape antes de correções de aspas
	if unescaped, ok := utils.MaybeUnescapeJSONishArgs(normalized); ok {
		normalized = unescaped
	}

	// 4) Remove "\ " (barra + espaço) fora de aspas que não faz sentido como escape
	//    Comum quando a IA erra a formatação: --search \ "valor"
	normalized = removeBogusBackslashSpace(normalized)

	// 4b) Corrige aspas desbalanceadas com barra final (ex. --search "\)
	if fixed, changed := fixUnbalancedQuotesWithTrailingBackslash(normalized); changed {
		if logger != nil {
			logger.Debug("Corrigidas aspas desbalanceadas com barra final",
				zap.String("tool", toolName))
		}
		normalized = fixed
	}

	// 5) Remove barras invertidas finais pendentes fora de aspas
	if fixed, changed := trimTrailingBackslashesOutsideQuotes(normalized); changed {
		if logger != nil {
			logger.Debug("Removida barra invertida final pendente",
				zap.String("tool", toolName))
		}
		normalized = fixed
	}

	// 6) Normaliza espaços múltiplos (mas preserva dentro de aspas)
	normalized = normalizeSpacesOutsideQuotes(normalized)

	// 7) Correções semânticas específicas para @coder
	if isCoderMode && strings.EqualFold(strings.TrimSpace(toolName), "@coder") {
		if fixed, changed := fixDanglingBackslashArgsForCoderTool(normalized); changed {
			if logger != nil {
				logger.Debug("Aplicada correção semântica para @coder",
					zap.String("tool", toolName))
			}
			normalized = fixed
		}
	}

	return strings.TrimSpace(normalized)
}

// tryCompactJSON attempts to compact a multiline string that contains valid JSON.
// This handles the common case where AI models send pretty-printed JSON args.
// Returns the compacted single-line JSON string, or empty string if not valid JSON.
func tryCompactJSON(input string) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) == 0 {
		return ""
	}

	// If it starts with { or [ it might be JSON - try to compact it
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(trimmed)); err == nil {
			return buf.String()
		}
	}

	// Not valid JSON - try collapsing newlines to spaces for CLI-style args
	collapsed := strings.Join(strings.Fields(trimmed), " ")
	if !hasAnyNewline(collapsed) {
		return collapsed
	}

	return ""
}

// processLineContinuations processa "\" + whitespace + newline de forma robusta
// Respeita aspas e mantém o conteúdo que vem depois do newline
func processLineContinuations(input string) string {
	var result strings.Builder
	runes := []rune(input)
	n := len(runes)

	inSingle := false
	inDouble := false
	i := 0

	for i < n {
		ch := runes[i]

		// Controle de aspas (não escapadas)
		if ch == '\'' && !inDouble && (i == 0 || runes[i-1] != '\\') {
			inSingle = !inSingle
			result.WriteRune(ch)
			i++
			continue
		}
		if ch == '"' && !inSingle && (i == 0 || runes[i-1] != '\\') {
			inDouble = !inDouble
			result.WriteRune(ch)
			i++
			continue
		}

		// Detecta barra invertida seguida de newline (continuação de linha)
		if ch == '\\' {
			j := i + 1

			// Pula espaços opcionais até o newline
			for j < n && (runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\r') {
				j++
			}

			// Se encontrar newline, é continuação de linha
			if j < n && runes[j] == '\n' {
				// Pula o newline
				j++

				// Pula espaços depois do newline
				for j < n && (runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\r') {
					j++
				}

				// Dentro de aspas: remove a barra e o newline, concatena direto
				// Fora de aspas: substitui por espaço
				if !inSingle && !inDouble {
					result.WriteRune(' ')
				}
				// Dentro de aspas: não escreve nada (concatena)

				// Avança o índice para depois do newline e espaços
				i = j - 1 // -1 porque o loop faz i++
				i++
				continue
			}
		}

		// Não é continuação de linha, mantém o caractere
		result.WriteRune(ch)
		i++
	}

	return result.String()
}

// removeBogusBackslashSpace remove "\ " fora de aspas que não faz sentido
// Exemplo: --search \ "valor" -> --search "valor"
func removeBogusBackslashSpace(input string) string {
	var result strings.Builder
	runes := []rune(input)
	n := len(runes)

	inSingle := false
	inDouble := false
	i := 0

	for i < n {
		ch := runes[i]

		// Controle de aspas
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			result.WriteRune(ch)
			i++
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			result.WriteRune(ch)
			i++
			continue
		}

		// Fora de aspas, detecta "\ " ou "\t" que não é escape válido
		if ch == '\\' && !inSingle && !inDouble {
			// Olha o próximo caractere
			if i+1 < n {
				next := runes[i+1]

				// Se for espaço ou tab, é provavelmente erro da IA
				if next == ' ' || next == '\t' {
					// Pula a barra, mas mantém o espaço
					i++
					continue
				}

				// Se for outra barra (\\), é escape válido - mantém ambas
				if next == '\\' {
					result.WriteRune(ch)
					result.WriteRune(next)
					i += 2
					continue
				}

				// Se for aspa (\" ou \'), pode ser erro - remove a barra
				if next == '"' || next == '\'' {
					// Mantém só a aspa, remove a barra
					i++
					continue
				}
			}
		}

		result.WriteRune(ch)
		i++
	}

	return result.String()
}

// normalizeSpacesOutsideQuotes reduz espaços múltiplos para um só, fora de aspas
func normalizeSpacesOutsideQuotes(input string) string {
	var result strings.Builder
	runes := []rune(input)
	n := len(runes)

	inSingle := false
	inDouble := false
	lastWasSpace := false
	i := 0

	for i < n {
		ch := runes[i]

		// Controle de aspas
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			result.WriteRune(ch)
			lastWasSpace = false
			i++
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			result.WriteRune(ch)
			lastWasSpace = false
			i++
			continue
		}

		// Dentro de aspas, preserva tudo
		if inSingle || inDouble {
			result.WriteRune(ch)
			lastWasSpace = false
			i++
			continue
		}

		// Fora de aspas, normaliza espaços
		if ch == ' ' || ch == '\t' {
			if !lastWasSpace {
				result.WriteRune(' ')
				lastWasSpace = true
			}
			// Se já teve espaço, pula este
			i++
			continue
		}

		result.WriteRune(ch)
		lastWasSpace = false
		i++
	}

	return result.String()
}

// fixUnbalancedQuotesWithTrailingBackslash corrige o caso onde a IA gera aspas desbalanceadas terminando com barra
// Exemplo: --search "\ -> --search "
// Exemplo: exec --cmd 'curl ... {\ -> exec --cmd 'curl ... {'
func fixUnbalancedQuotesWithTrailingBackslash(input string) (string, bool) {
	trimmed := strings.TrimRight(input, " \t\r\n")
	if trimmed == "" {
		return input, false
	}

	// Verifica se termina com barra
	if !strings.HasSuffix(trimmed, `\`) {
		return input, false
	}

	// Conta aspas para ver se estão balanceadas
	doubleQuotes := 0
	singleQuotes := 0
	inEscape := false

	for _, ch := range trimmed {
		if inEscape {
			inEscape = false
			continue
		}

		if ch == '\\' {
			inEscape = true
			continue
		}

		if ch == '"' {
			doubleQuotes++
		} else if ch == '\'' {
			singleQuotes++
		}
	}

	// Se aspas desbalanceadas E termina com barra, remove a barra e fecha as aspas
	if doubleQuotes%2 != 0 || singleQuotes%2 != 0 {
		fixed := strings.TrimSuffix(trimmed, `\`)

		// Fecha as aspas desbalanceadas
		if doubleQuotes%2 != 0 {
			fixed += `"`
		}
		if singleQuotes%2 != 0 {
			fixed += `'`
		}

		return fixed, true
	}

	return input, false
}

// fixDanglingBackslashArgsForCoderTool corrige casos comuns onde o modelo gera:
//
//	patch ... --search \
//	patch ... --search \,
//	write ... --content \
//
// sem realmente colocar o conteúdo na mesma linha/argumento.
// Isso quebra o flag parser do plugin (@coder) porque --search/--content exigem argumento.
//
// A função tenta:
// - remover argumento inválido "\" (ou "\" + pontuação) após flags de conteúdo
// - se houver um próximo token, usar ele como argumento real
//
// Retorna (novoTexto, mudou?).
func fixDanglingBackslashArgsForCoderTool(argLine string) (string, bool) {
	// Primeiro, tokeniza de forma robusta (respeita aspas), reaproveitando o mesmo
	// comportamento do splitToolArgsMultiline, mas sem retornar erro aqui.
	toks, err := splitToolArgsMultilineLenient(argLine)
	if err != nil || len(toks) == 0 {
		return argLine, false
	}

	// Flags do @coder que exigem valor imediato (no seu plugin):
	// - patch: --search, --replace (replace é opcional, mas quando aparece precisa valor)
	// - write: --content
	needsValue := map[string]bool{
		"--search":        true,
		"--replace":       true,
		"--content":       true,
		"--file":          true,
		"--cmd":           true,
		"--dir":           true,
		"--term":          true,
		"--encoding":      true,
		"--diff":          true,
		"--diff-encoding": true,
		"--start":         true,
		"--end":           true,
		"--head":          true,
		"--tail":          true,
		"--max-bytes":     true,
		"--context":       true,
		"--max-results":   true,
		"--glob":          true,
		"--timeout":       true,
		"--path":          true,
		"--limit":         true,
		"--pattern":       true,
	}

	isClearlyInvalidValue := func(v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return true
		}
		// Caso clássico: "\" sozinho
		if v == `\` {
			return true
		}
		// Casos vistos: "\," "\;" "\." etc (barra + pontuação, sem payload)
		if strings.HasPrefix(v, `\`) {
			rest := strings.TrimSpace(strings.TrimPrefix(v, `\`))
			// Se depois da barra só tiver pontuação (e/ou estiver vazio), é lixo
			if rest == "" {
				return true
			}
			allPunct := true
			for _, r := range rest {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
					allPunct = false
					break
				}
			}
			if allPunct {
				return true
			}
		}
		return false
	}

	changed := false
	out := make([]string, 0, len(toks))

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		out = append(out, t)

		if !needsValue[t] {
			continue
		}

		// Se é flag que precisa valor, olhe o próximo token
		if i+1 >= len(toks) {
			continue
		}

		val := toks[i+1]
		if !isClearlyInvalidValue(val) {
			// ok, mantém
			continue
		}

		// Valor inválido detectado: remove-o e tenta usar o próximo como valor real
		changed = true

		// Descarta o valor inválido
		i++ // pula o token inválido

		// Se existir um próximo token, ele vira o valor
		if i+1 < len(toks) {
			next := toks[i+1]
			out = append(out, next)
			i++ // consumiu o próximo também
		} else {
			return "", false
		}
	}

	rebuilt := strings.Join(out, " ")
	if rebuilt == strings.TrimSpace(argLine) {
		return argLine, changed
	}
	return rebuilt, changed
}

// splitToolArgsMultilineLenient é um tokenizer leniente para permitir correções.
// Ele tenta fazer split parecido com splitToolArgsMultiline, mas:
// - não retorna erro em escape pendente no final: trata "\" final como literal
// - se aspas não forem balanceadas, retorna erro
func splitToolArgsMultilineLenient(s string) ([]string, error) {
	var args []string
	var buf strings.Builder

	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if buf.Len() > 0 {
			args = append(args, buf.String())
			buf.Reset()
		}
	}

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		if (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') && !inSingle && !inDouble {
			flush()
			continue
		}

		buf.WriteByte(ch)
	}

	// leniente: se terminou "escaped", consideramos "\" literal
	if escaped {
		buf.WriteByte('\\')
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("aspas não balanceadas nos argumentos")
	}

	flush()
	return args, nil
}

// parseToolArgsWithJSON aceita args no formato JSON (object/array) e
// faz fallback para splitToolArgsMultiline no formato CLI tradicional.
func parseToolArgsWithJSON(argLine string) ([]string, error) {
	if args, ok, err := parseToolArgsMaybeJSON(argLine); ok {
		return args, err
	}
	return splitToolArgsMultiline(argLine)
}

// parseToolArgsMaybeJSON tenta interpretar os args como JSON.
// Retorna (args, true, nil) se parseou como JSON válido.
// Retorna (nil, true, err) se parecia JSON mas falhou.
// Retorna (nil, false, nil) se não parecia JSON.
func parseToolArgsMaybeJSON(argLine string) ([]string, bool, error) {
	trimmed := strings.TrimSpace(argLine)
	if trimmed == "" {
		return nil, false, nil
	}
	if unescaped, ok := utils.MaybeUnescapeJSONishArgs(trimmed); ok {
		trimmed = unescaped
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil, false, nil
	}

	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, true, err
	}

	switch v := payload.(type) {
	case []any:
		argv := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, true, fmt.Errorf("argv JSON deve conter apenas strings")
			}
			argv = append(argv, s)
		}
		return argv, true, nil
	case map[string]any:
		return buildArgvFromJSONMap(v)
	default:
		return nil, true, fmt.Errorf("JSON inválido para args")
	}
}

func buildArgvFromJSONMap(m map[string]any) ([]string, bool, error) {
	getString := func(key string) string {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	cmd := getString("cmd")
	if cmd == "" {
		if argvRaw, ok := m["argv"]; ok {
			if argvSlice, ok := argvRaw.([]any); ok && len(argvSlice) > 0 {
				if first, ok := argvSlice[0].(string); ok {
					cmd = first
				}
			}
		}
	}

	if argvRaw, ok := m["argv"]; ok {
		if argvSlice, ok := argvRaw.([]any); ok && len(argvSlice) > 0 {
			argv := make([]string, 0, len(argvSlice))
			for _, item := range argvSlice {
				s, ok := item.(string)
				if !ok {
					return nil, true, fmt.Errorf("argv JSON deve conter apenas strings")
				}
				argv = append(argv, s)
			}
			if cmd != "" && (len(argv) == 0 || argv[0] != cmd) {
				argv = append([]string{cmd}, argv...)
			}
			return argv, true, nil
		}
	}

	if cmd == "" {
		return nil, true, fmt.Errorf("JSON args requer campo 'cmd' ou 'argv'")
	}

	argv := []string{cmd}
	argsMap := map[string]any{}

	if raw, ok := m["args"]; ok {
		if mm, ok := raw.(map[string]any); ok {
			for k, v := range mm {
				if k == "command" {
					argsMap["cmd"] = v
					continue
				}
				argsMap[k] = v
			}
		}
	}
	if raw, ok := m["flags"]; ok {
		if mm, ok := raw.(map[string]any); ok {
			for k, v := range mm {
				if k == "command" {
					argsMap["cmd"] = v
					continue
				}
				argsMap[k] = v
			}
		}
	}

	keys := make([]string, 0, len(argsMap))
	for k := range argsMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		appendFlagValue(&argv, k, argsMap[k])
	}

	if posRaw, ok := m["positional"]; ok {
		appendPositionals(&argv, posRaw)
	}
	if posRaw, ok := m["_"]; ok {
		appendPositionals(&argv, posRaw)
	}

	return argv, true, nil
}

func normalizeFlagName(name string) string {
	if strings.HasPrefix(name, "-") {
		return name
	}
	return "--" + name
}

func appendPositionals(argv *[]string, raw any) {
	if raw == nil {
		return
	}
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				*argv = append(*argv, s)
			}
		}
	case []string:
		*argv = append(*argv, v...)
	case string:
		*argv = append(*argv, v)
	}
}

func appendFlagValue(argv *[]string, key string, value any) {
	if value == nil {
		return
	}
	flag := normalizeFlagName(key)
	switch v := value.(type) {
	case bool:
		if v {
			*argv = append(*argv, flag)
		}
	case string:
		*argv = append(*argv, flag, v)
	case float64, float32, int, int64, int32, uint, uint64, uint32:
		*argv = append(*argv, flag, fmt.Sprint(v))
	case []any:
		for _, item := range v {
			appendFlagValue(argv, key, item)
		}
	case []string:
		for _, item := range v {
			*argv = append(*argv, flag, item)
		}
	default:
		b, err := json.Marshal(v)
		if err != nil {
			*argv = append(*argv, flag, fmt.Sprint(v))
		} else {
			*argv = append(*argv, flag, string(b))
		}
	}
}

// removeXMLTags remove tags conhecidas, mantendo o conteúdo.
// Não remove conteúdo e não mexe em markdown dentro do texto.
func removeXMLTags(text string) string {
	re := regexp.MustCompile(`(?is)</?\s*(reasoning|explanation|thought)\s*>`)
	return re.ReplaceAllString(text, "")
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
func AgentMaxTurns() int {
	value := os.Getenv(config.AgentPluginMaxTurnsEnv)
	if value == "" {
		return config.DefaultAgentMaxTurns
	}

	turns, err := strconv.Atoi(value)
	if err != nil {
		return config.DefaultAgentMaxTurns
	}

	if turns <= 0 {
		return config.DefaultAgentMaxTurns
	}

	if turns > config.MaxAgentMaxTurns {
		return config.MaxAgentMaxTurns
	}

	return turns
}

// splitToolArgsMultiline faz split de argv estilo shell, mas com suporte a multilinha.
// Regras:
// - separa por whitespace (inclui \n) quando NÃO estiver dentro de aspas
// - suporta aspas simples e duplas
// - permite newline dentro de aspas (vira parte do mesmo argumento)
// - "\" funciona como escape fora de aspas simples (ex: \" ou \n literal etc.)
// - não interpreta sequências como \n => newline; mantém literal \ + n (quem interpreta é o plugin, se quiser)
// - retorna erro se aspas não balanceadas ou escape pendente no final
func splitToolArgsMultiline(s string) ([]string, error) {
	var args []string
	var buf strings.Builder

	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if buf.Len() > 0 {
			args = append(args, buf.String())
			buf.Reset()
		}
	}

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		if (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') && !inSingle && !inDouble {
			flush()
			continue
		}

		buf.WriteByte(ch)
	}

	if escaped {
		return nil, fmt.Errorf("escape pendente no fim dos argumentos (terminou com '\\')")
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("aspas não balanceadas nos argumentos")
	}

	flush()
	return args, nil
}

// trimTrailingBackslashesOutsideQuotes remove barras invertidas finais (\\) que ficarem
// NO FINAL DO TEXTO e que estejam fora de aspas.
// Isso evita o caso clássico em que o modelo termina uma linha com "\" e o parser entende como escape pendente.
// Retorna (novoTexto, mudou?).
func trimTrailingBackslashesOutsideQuotes(s string) (string, bool) {
	orig := s

	// Normaliza finais
	t := strings.TrimRight(s, " \t\r\n")
	if t == "" {
		return orig, false
	}

	// Para saber se o último "\" está fora de aspas, precisamos fazer um scan simples.
	// Vamos remover "\" finais repetidos enquanto:
	// - o texto termina com "\"
	// - e esse "\" está fora de aspas (single/double)
	for {
		t2 := strings.TrimRight(t, " \t\r\n")
		if !strings.HasSuffix(t2, `\`) {
			break
		}

		// Verifica se o "\" final está fora de aspas
		if !isLastBackslashOutsideQuotes(t2) {
			// se está dentro de aspas, não mexe (é conteúdo)
			break
		}

		// remove a "\" final
		t2 = strings.TrimSuffix(t2, `\`)
		t = strings.TrimRight(t2, " \t\r\n")
	}

	if t == strings.TrimRight(orig, " \t\r\n") {
		return orig, false
	}

	// preserva a parte original “antes” (mas retornamos trimmed, pois era um erro estrutural)
	return t, true
}

// isLastBackslashOutsideQuotes detecta se o último caractere "\" está fora de aspas.
// Supõe que s já termina com "\".
func isLastBackslashOutsideQuotes(s string) bool {
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' && !inSingle {
			// escape em modo normal/aspas duplas
			escaped = true
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
	}

	// Se termina com "\" e estamos fora de aspas, então é o caso problemático.
	return !inSingle && !inDouble
}

// extractXMLTagContent extrai o conteúdo de <tag>...</tag> (case-insensitive).
// Retorna ("", false) se não existir.
func extractXMLTagContent(s, tag string) (string, bool) {
	pat := fmt.Sprintf(`(?is)<%s>\s*(.*?)\s*</%s>`, regexp.QuoteMeta(tag), regexp.QuoteMeta(tag))
	re := regexp.MustCompile(pat)
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// stripXMLTagBlock remove completamente o bloco <tag>...</tag> do texto.
func stripXMLTagBlock(s, tag string) string {
	pat := fmt.Sprintf(`(?is)<%s>\s*.*?\s*</%s>`, regexp.QuoteMeta(tag), regexp.QuoteMeta(tag))
	re := regexp.MustCompile(pat)
	return re.ReplaceAllString(s, "")
}

// normalizeShellLineContinuations lida com quebras de linha escapadas (\ + Enter).
// - Fora de aspas: Substitui por espaço (para separar argumentos).
// - Dentro de aspas: Remove a sequência (para unir a string, igual ao bash).
func normalizeShellLineContinuations(input string) string {
	var result strings.Builder
	chars := []rune(input)
	length := len(chars)

	inDoubleQuote := false
	inSingleQuote := false

	for i := 0; i < length; i++ {
		char := chars[i]

		if char == '\\' {
			// Verifica se é continuação de linha (\ seguido de newline)
			j := i + 1
			// Pula espaços em branco opcionais entre a barra e o enter
			for j < length && (chars[j] == ' ' || chars[j] == '\t' || chars[j] == '\r') {
				j++
			}

			if j < length && chars[j] == '\n' {
				// É UMA CONTINUAÇÃO DE LINHA (\ + Enter)

				if !inDoubleQuote && !inSingleQuote {
					// Caso 1: Fora de aspas (ex: exec --cmd \ \n echo)
					// Substitui por espaço para não colar os argumentos
					result.WriteRune(' ')
				}
				// Caso 2: Dentro de aspas (ex: --content "\ \n code")
				// Não fazemos nada (não escrevemos espaço), efetivamente removendo
				// a barra e o enter, unindo o conteúdo limpo.

				i = j // Avança o índice para pular a barra e o enter
				continue
			}

			// Não é quebra de linha: é uma barra literal ou escape
			result.WriteRune(char)

			// Se for um escape de aspa (ex: \" ou \'), consumimos o próximo char
			// para não confundir a máquina de estados das aspas.
			if i+1 < length {
				nextChar := chars[i+1]
				if (inDoubleQuote && nextChar == '"') || (inSingleQuote && nextChar == '\'') {
					result.WriteRune(nextChar)
					i++
				}
			}
			continue
		}

		// Alternar estado das aspas
		if char == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
		} else if char == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
		}

		result.WriteRune(char)
	}
	return result.String()
}

// isCoderArgsMissingRequiredValue verifica se o comando @coder contém flags
// que exigem valor, mas estão sem argumento efetivo.
//
// Isso roda ANTES de executar o plugin, para evitar "flag needs an argument"
// e loops inúteis.
func isCoderArgsMissingRequiredValue(args []string) (bool, string) {
	if len(args) == 0 {
		return false, ""
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))

	// Flags mínimas obrigatórias por subcomando (as que realmente causam quebra no plugin)
	// OBS: patch: replace é opcional, mas se existir precisa ter valor.
	required := map[string][]string{
		"write":  {"--file", "--content"},
		"search": {"--term"},
		"read":   {"--file"},
		"exec":   {"--cmd"},
		// rollback/clean podem ficar sem required estrito, mas se quiser:
		"rollback": {"--file"},
	}

	if sub == "patch" {
		if hasFlag(args, "--diff") {
			required["patch"] = nil
		} else {
			required["patch"] = []string{"--file", "--search"}
		}
	}

	reqFlags, ok := required[sub]
	if !ok || len(reqFlags) == 0 {
		return false, ""
	}

	// mapeia flag -> encontrado
	found := make(map[string]bool, len(reqFlags))

	// percorre args procurando "--flag value"
	for i := 0; i < len(args); i++ {
		t := args[i]

		for _, rf := range reqFlags {
			if t != rf {
				continue
			}

			// Precisa ter próximo token
			if i+1 >= len(args) {
				return true, rf
			}

			val := strings.TrimSpace(args[i+1])

			// Se veio vazio, ou parece outra flag, ou é placeholder lixo (\ ou \,)
			if val == "" || strings.HasPrefix(val, "-") || isClearlyInvalidCoderValue(val) {
				return true, rf
			}

			found[rf] = true
		}

		// Caso especial: flags opcionais mas que, se presentes, exigem valor.
		// Ex: patch --replace <...>
		if sub == "patch" && t == "--replace" {
			if i+1 >= len(args) {
				return true, "--replace"
			}
			val := strings.TrimSpace(args[i+1])
			if val == "" || strings.HasPrefix(val, "-") || isClearlyInvalidCoderValue(val) {
				return true, "--replace"
			}
		}

		// Caso especial: search --dir exige valor se presente
		if sub == "search" && t == "--dir" {
			if i+1 >= len(args) {
				return true, "--dir"
			}
			val := strings.TrimSpace(args[i+1])
			if val == "" || strings.HasPrefix(val, "-") || isClearlyInvalidCoderValue(val) {
				return true, "--dir"
			}
		}
	}

	// se alguma obrigatória não apareceu
	for _, rf := range reqFlags {
		if !found[rf] {
			return true, rf
		}
	}

	return false, ""
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// isClearlyInvalidCoderValue identifica valores "lixo" gerados por continuação de linha,
// como "\" ou "\," ou "\" seguido apenas de pontuação.
//
// OBS: isso NÃO tenta validar base64 nem conteúdo real; apenas detecta placeholders
// típicos que o modelo usa quando erra a formatação.
func isClearlyInvalidCoderValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}

	// Caso clássico: "\" sozinho
	if v == `\` {
		return true
	}

	// Caso recorrente: "\," (ou "\;" "\." etc.)
	// Aqui consideramos inválido quando começa com "\" e o resto não contém nenhum caractere
	// "útil" (alfanumérico ou base64 charset).
	if strings.HasPrefix(v, `\`) {
		rest := strings.TrimSpace(strings.TrimPrefix(v, `\`))
		if rest == "" {
			return true
		}

		// Se depois da barra só tiver pontuação (e/ou espaços), é lixo.
		// Permitimos A-Z a-z 0-9 e também + / = (para base64) como "úteis".
		allPunct := true
		for _, r := range rest {
			switch {
			case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
				allPunct = false
			case r == '+' || r == '/' || r == '=':
				allPunct = false
			default:
				// continua (pontuação / espaços / etc.)
			}
			if !allPunct {
				break
			}
		}
		if allPunct {
			return true
		}
	}

	return false
}

// buildCoderToolCallFixPrompt requests a valid tool_call when a required flag is missing.
func buildCoderToolCallFixPrompt(missingFlag string) string {
	return fmt.Sprintf(
		"ERROR: Your @coder tool_call is missing required flag %s (or its value is invalid/empty).\n\n"+
			"Resend a SINGLE <tool_call> with valid args. Use JSON format (recommended):\n\n"+
			`<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"search","args":{"term":"LoginService","dir":"."}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"write","args":{"file":"out.go","content":"BASE64","encoding":"base64"}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"go test ./..."}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"patch","args":{"file":"f.go","search":"old","replace":"new"}}' />`,
		missingFlag,
	)
}

// hasAnyNewline retorna true se a string contém qualquer newline real.
// Isso diferencia newline real de "\n" literal dentro do texto.
func hasAnyNewline(s string) bool {
	return strings.Contains(s, "\n") || strings.Contains(s, "\r")
}

// buildCoderSingleLineArgsEnforcementPrompt enforces single-line args requirement.
func buildCoderSingleLineArgsEnforcementPrompt(originalArgs string) string {
	trimmed := strings.TrimSpace(originalArgs)

	preview := trimmed
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}

	return fmt.Sprintf(
		"ERROR: Your tool_call args contain line breaks, which is NOT allowed.\n\n"+
			"The args attribute MUST be a SINGLE LINE. Use JSON format with single quotes around it:\n\n"+
			`<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"go build && go test ./..."}}' />`+"\n\n"+
			"Rules:\n"+
			"1. NEVER use backslash (\\) for line continuation\n"+
			"2. NEVER put real newlines inside args\n"+
			"3. For multiline file content, use base64 encoding\n"+
			"4. Use single quotes around JSON args to avoid escaping issues\n\n"+
			"Your args (truncated):\n---\n%s\n---",
		preview,
	)
}
