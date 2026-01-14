/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/diillson/chatcli/cli/coder"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

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
	executeCommandsFunc func(ctx context.Context, block agent.CommandBlock) (string, string)
	//skipClearOnNextDraw bool
	isCoderMode bool
	isOneShot   bool
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
	a := &AgentMode{
		cli:            cli,
		logger:         logger,
		executor:       agent.NewCommandExecutor(logger),
		validator:      agent.NewCommandValidator(logger),
		contextManager: agent.NewContextManager(logger),
	}
	a.executeCommandsFunc = a.executeCommandsWithOutput
	return a
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

	// Lógica de Seleção de Persona
	if systemPromptOverride != "" {
		systemInstruction = systemPromptOverride
	} else {
		// Persona Padrão (Admin de Sistema / Shell)
		osName := runtime.GOOS
		shellName := utils.GetUserShell()
		currentDir, _ := os.Getwd()
		systemInstruction = i18n.T("agent.system_prompt.default.base", osName, shellName, currentDir)
	}

	a.isCoderMode = (systemPromptOverride == CoderSystemPrompt)
	a.isOneShot = false

	// Adiciona contexto de ferramentas (plugins) ao prompt
	systemInstruction += a.getToolContextString()

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
func (a *AgentMode) extractCommandBlocks(response string) []CommandBlock {
	var commandBlocks []CommandBlock

	_, isAssistant := a.cli.Client.(*openai_assistant.OpenAIAssistantClient)
	if isAssistant {
		return a.extractCommandBlocksForAssistant(response)
	}

	re := regexp.MustCompile("(?s)```execute:\\s*([a-zA-Z0-9_-]+)\\s*\n(.*?)```")
	matches := re.FindAllStringSubmatch(response, -1)

	if len(matches) == 0 {
		fb := regexp.MustCompile("(?s)```(?:sh|bash|shell)\\s*\\n(.*?)```").FindAllStringSubmatch(response, -1)
		for _, m := range fb {
			commandsStr := strings.TrimSpace(m[1])
			commandBlocks = append(commandBlocks, CommandBlock{
				Description: "Comandos extraídos de bloco shell",
				Commands:    splitCommandsByBlankLine(commandsStr),
				Language:    "shell",
				ContextInfo: CommandContextInfo{SourceType: SourceTypeUserInput, IsScript: isShellScript(commandsStr), ScriptType: "shell"},
			})
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

			var commandsList []string
			if isScript {
				commandsList = []string{commandsStr}
			} else {
				commandsList = splitCommandsByBlankLine(commandsStr)
			}

			commandBlocks = append(commandBlocks, CommandBlock{
				Description: description,
				Commands:    commandsList,
				Language:    language,
				ContextInfo: CommandContextInfo{
					SourceType: SourceTypeUserInput,
					IsScript:   isScript,
					ScriptType: language,
				},
			})
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
			_ = os.MkdirAll(dir, 0755)
			fpath := filepath.Join(dir, fmt.Sprintf("cmd-%d-%d.log", n, time.Now().Unix()))
			if writeErr := os.WriteFile(fpath, []byte(outputs[n-1].Output), 0644); writeErr != nil {
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
				Subcommands []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Flags       []struct {
						Name        string `json:"name"`
						Description string `json:"description"`
						Type        string `json:"type"`
						Default     string `json:"default"`
					} `json:"flags"`
				} `json:"subcommands"`
			}

			if err := json.Unmarshal([]byte(plugin.Schema()), &schema); err == nil {
				b.WriteString("  Subcomandos Disponíveis:\n")
				for _, sub := range schema.Subcommands {
					b.WriteString(fmt.Sprintf("    - %s: %s\n", sub.Name, sub.Description))
					if len(sub.Flags) > 0 {
						b.WriteString("      Flags:\n")
						for _, flag := range sub.Flags {
							flagDesc := fmt.Sprintf("        - %s (%s): %s", flag.Name, flag.Type, flag.Description)
							if flag.Default != "" {
								flagDesc += fmt.Sprintf(" (padrão: %s)", flag.Default)
							}
							b.WriteString(flagDesc + "\n")
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

	return "\n\n" + i18n.T("agent.system_prompt.tools_header") + "\n" + strings.Join(toolDescriptions, "\n") + "\n\n" + i18n.T("agent.system_prompt.tools_instruction")
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

		// Animação de "Pensando..."
		a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

		turnHistory := buildTurnHistoryWithAnchor()

		// Chamada à LLM
		aiResponse, err := a.cli.Client.SendPrompt(ctx, "", turnHistory, 0)

		a.cli.animation.StopThinkingAnimation()

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

		if strings.TrimSpace(reasoning) != "" {
			renderMDCard("🧠", "RACIOCÍNIO", reasoning, agent.ColorCyan)
		}
		if strings.TrimSpace(explanation) != "" {
			renderMDCard("📌", "EXPLICAÇÃO", explanation, agent.ColorLime)
		}
		if strings.TrimSpace(remaining) != "" {
			renderMDCard("💬", "RESPOSTA", remaining, agent.ColorGray)
		}

		// =========================
		// VALIDAÇÕES ESTRITAS DO /CODER
		// =========================
		if a.isCoderMode {
			if len(toolCalls) > 0 {
				// Exige <reasoning> antes de agir
				if !hasReasoningTag(thoughtText) {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "Formato inválido no modo /coder. Antes de qualquer <tool_call>, escreva um <reasoning> curto (2-6 linhas) " +
							"com as etapas e critério de sucesso, e então envie as <tool_call ... />.",
					})
					continue
				}

				// Exige uso exclusivo de @coder
				if !strings.EqualFold(strings.TrimSpace(toolCalls[0].Name), "@coder") {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "No modo /coder, a ferramenta obrigatória é @coder. " +
							"Reenvie SOMENTE o próximo passo como <tool_call name=\"@coder\" args=\"...\" /> (sem blocos de código).",
					})
					continue
				}
			}

			// Proíbe blocos de código soltos (shell scripts) no modo coder
			if len(toolCalls) == 0 {
				if strings.Contains(aiResponse, "```") || strings.Contains(aiResponse, "```execute:") || regexp.MustCompile(`(?m)^[$#]\s+`).MatchString(aiResponse) {
					a.cli.history = append(a.cli.history, models.Message{
						Role: "user",
						Content: "Você respondeu com comandos/blocos, o que é proibido no modo /coder. " +
							"Use <reasoning> e então emita <tool_call name=\"@coder\" args=\"...\" />.",
					})
					continue
				}
			}
		}

		// =========================================================
		// PRIORIDADE 1: EXECUTAR TOOL_CALL(s) EM LOTE (BATCH)
		// =========================================================
		if len(toolCalls) > 0 {
			var batchOutputBuilder strings.Builder
			var batchHasError bool
			successCount := 0
			totalActions := len(toolCalls)

			// 1. Renderiza cabeçalho do lote se houver mais de 1 ação
			if totalActions > 1 {
				renderer.RenderBatchHeader(totalActions)
			}

			// Iterar sobre TODAS as chamadas de ferramenta sugeridas
			for i, tc := range toolCalls {
				// --- SECURITY CHECK START ---
				if a.isCoderMode {
					pm, err := coder.NewPolicyManager(a.logger)
					if err == nil {
						action := pm.Check(tc.Name, tc.Args)
						if action == coder.ActionDeny {
							msg := "🛫 AÇÃO BLOQUEADA PELO USUÁRIO (Regra de Segurança). NÃO TENTE NOVAMENTE."
							renderer.RenderToolResult(msg, true)
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
								renderer.RenderToolResult(msg, true)
								a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: "ERRO: " + msg})
								batchHasError = true
							case coder.DecisionDenyOnce:
								msg := "🛫 AÇÃO NEGADA PELO USUÁRIO. NÃO TENTE O MESMO COMANDO NOVAMENTE. Peça novas instruções ou tente uma alternativa."
								renderer.RenderToolResult(msg, true)
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
				renderer.RenderToolCallWithProgress(toolName, toolArgsStr, i+1, totalActions)

				// UX: Força flush e pausa para leitura
				os.Stdout.Sync()
				time.Sleep(300 * time.Millisecond)

				// --- Lógica de Sanitização e Validação ---
				normalizedArgsStr := sanitizeToolCallArgs(toolArgsStr, a.logger, toolName, a.isCoderMode)

				// HARD RULE (/coder): proibir multiline real em args.
				if a.isCoderMode && hasAnyNewline(normalizedArgsStr) {
					msg := buildCoderSingleLineArgsEnforcementPrompt(toolArgsStr)
					// Feedback visual de erro
					renderer.RenderToolResult("Erro de formato: Argumentos com quebra de linha real.\n"+msg, true)

					a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: msg})
					batchHasError = true
					break // Interrompe o lote
				}

				toolArgs, parseErr := splitToolArgsMultiline(normalizedArgsStr)
				var toolOutput string
				var execErr error

				// --- Preparação da Execução ---
				if parseErr != nil {
					execErr = parseErr
					toolOutput = fmt.Sprintf("Erro de parsing nos argumentos: %v", parseErr)

					if a.isCoderMode {
						fixMsg := fmt.Sprintf("Seu <tool_call> veio com args inválido (erro: %v). No modo /coder, args deve ser SEMPRE linha única.", parseErr)
						a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: fixMsg})
						batchHasError = true
						// Não break aqui, deixa cair no renderToolResult abaixo para feedback
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
								renderer.RenderToolResult("Args inválido para @coder: falta argumento válido em "+which, true)

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
							a.cli.animation.ShowThinkingAnimation(fmt.Sprintf("Executando %s", subCmd))

							toolOutput, execErr = plugin.Execute(ctx, toolArgs)

							a.cli.animation.StopThinkingAnimation()

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
				renderer.RenderToolResult(displayForHuman, execErr != nil)

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
// Regras:
// 1) Decodifica entidades HTML (&quot; etc).
// 2) Normaliza CRLF -> LF.
// 3) Trata continuations: "\" + (spaces/tabs/\r)* + "\n"
//   - Fora de aspas: vira espaço
//   - Dentro de aspas: remove (concatena), igual ao bash
//     4. Remove "\" final pendurado fora de aspas (erro comum de modelos), preservando
//     conteúdo se estiver dentro de aspas.
//     5. (NOVO) Corrige padrões comuns do /coder onde a IA manda --search "\" (ou "\,")
//     como valor inválido, o que explode no flag parser do plugin.
//     6. Trim final para reduzir ruído.
func sanitizeToolCallArgs(rawArgs string, logger *zap.Logger, toolName string, isCoderMode bool) string {
	unescaped := html.UnescapeString(rawArgs)
	normalized := strings.ReplaceAll(unescaped, "\r\n", "\n")

	// 3) Normaliza continuations estilo shell (atua em "\<spaces>\n")
	normalized = normalizeShellLineContinuations(normalized)

	// 4) Remove "\" final fora de aspas (auto-fix)
	if fixed, changed := trimTrailingBackslashesOutsideQuotes(normalized); changed {
		if logger != nil {
			logger.Warn("tool_call args terminou com barra invertida fora de aspas; aplicando auto-fix removendo '\\' final",
				zap.String("tool", toolName),
				zap.String("args_before", normalized),
				zap.String("args_after", fixed))
		}
		normalized = fixed
	}

	// 5) Correção semântica para /coder (quando a IA manda --search \ ou --content \ etc.)
	if isCoderMode && strings.EqualFold(strings.TrimSpace(toolName), "@coder") {
		fixed2, changed2 := fixDanglingBackslashArgsForCoderTool(normalized)
		if changed2 {
			if logger != nil {
				logger.Warn("Aplicando correção semântica em args do @coder (valor '\\' inválido em flag de conteúdo)",
					zap.String("tool", toolName),
					zap.String("args_before", normalized),
					zap.String("args_after", fixed2))
			}
			normalized = fixed2
		}
	}

	return strings.TrimSpace(normalized)
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
		"--search":   true,
		"--replace":  true,
		"--content":  true,
		"--file":     true,
		"--cmd":      true,
		"--dir":      true,
		"--term":     true,
		"--encoding": true,
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
		"patch":  {"--file", "--search"},
		"write":  {"--file", "--content"},
		"search": {"--term"}, // dir tem default
		"read":   {"--file"},
		"exec":   {"--cmd"},
		"tree":   {"--dir"},
		// rollback/clean podem ficar sem required estrito, mas se quiser:
		"rollback": {"--file"},
		"clean":    {"--dir"},
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

// buildCoderToolCallFixPrompt pede reenvio de um tool_call válido quando algum subcomando
// do @coder vier com flag obrigatória sem valor (ou valor lixo como "\,").
func buildCoderToolCallFixPrompt(missingFlag string) string {
	return fmt.Sprintf(
		"No modo /coder, seu <tool_call name=\"@coder\" ... /> veio INVÁLIDO: a flag obrigatória %s está sem um valor válido "+
			"(ex.: você enviou '\\\\' ou '\\\\,' como placeholder). "+
			"Reenvie SOMENTE um ÚNICO <tool_call name=\"@coder\" args=\"...\" /> em LINHA ÚNICA, com aspas balanceadas.\n\n"+
			"Exemplos válidos:\n"+
			"1) Search:\n"+
			"<tool_call name=\"@coder\" args=\"search --term 'LoginService' --dir .\" />\n"+
			"2) Patch (base64 em linha única):\n"+
			"<tool_call name=\"@coder\" args=\"patch --file 'caminho' --encoding base64 --search 'BASE64_OLD' --replace 'BASE64_NEW'\" />\n"+
			"3) Write (base64 em linha única):\n"+
			"<tool_call name=\"@coder\" args=\"write --file 'caminho' --encoding base64 --content 'BASE64'\" />",
		missingFlag,
	)
}

// hasAnyNewline retorna true se a string contém qualquer newline real.
// Isso diferencia newline real de "\n" literal dentro do texto.
func hasAnyNewline(s string) bool {
	return strings.Contains(s, "\n") || strings.Contains(s, "\r")
}

// buildCoderSingleLineArgsEnforcementPrompt cria uma mensagem dura (e repetível)
// para forçar a IA a reenviar args em linha única no /coder.
func buildCoderSingleLineArgsEnforcementPrompt(originalArgs string) string {
	trimmed := strings.TrimSpace(originalArgs)

	// Mostra um preview curto para ajudar debugging sem poluir.
	preview := trimmed
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}

	return fmt.Sprintf(
		"No modo /coder, o atributo args do <tool_call> DEVE ser uma única linha (SEM quebras de linha). "+
			"Seu args veio com multilinha (muitas vezes causado por uso de '\\\\' para continuação de linha). "+
			"Isso quebra o parser do ChatCLI antes do plugin executar.\n\n"+
			"Reenvie SOMENTE um ÚNICO <tool_call name=\"@coder\" args=\"...\" /> com args em LINHA ÚNICA.\n\n"+
			"Regras rápidas:\n"+
			"1) Não use '\\\\' para quebrar linha.\n"+
			"2) Não use newline real dentro de args.\n"+
			"3) Se precisar organizar, faça tudo em uma linha (use ';' ou '&&').\n\n"+
			"Preview do args recebido (truncado):\n"+
			"---\n%s\n---\n\n"+
			"Exemplo válido:\n"+
			"<tool_call name=\"@coder\" args=\"exec --cmd 'cd repo && ./script.sh | sed -n \\\"1,10p\\\"'\" />",
		preview,
	)
}
