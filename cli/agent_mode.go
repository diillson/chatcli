/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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

// Run inicia o modo agente com uma pergunta do usuário
func (a *AgentMode) Run(ctx context.Context, query string, additionalContext string) error {
	_, isAssistant := a.cli.Client.(*openai_assistant.OpenAIAssistantClient)

	if isAssistant {
		a.logger.Debug("Executando modo agente com OpenAI Assistant")
	}

	var systemInstruction string
	if isAssistant {
		systemInstruction = i18n.T("agent.system_prompt.interactive")
	} else {
		osName := runtime.GOOS
		shellName := utils.GetUserShell()
		currentDir, err := os.Getwd()
		if err != nil {
			a.logger.Warn("Não foi possível obter diretório atual", zap.Error(err))
			currentDir = "desconhecido"
		}

		systemInstruction = i18n.T("agent.system_prompt.default.base", osName, shellName, currentDir)
		codeFence := "```execute:shell\nfind . -name \"*.go\" -exec wc -l {} +\n```"
		systemInstruction = systemInstruction + "\n\n" + codeFence
	}

	a.cli.history = append(a.cli.history, models.Message{
		Role:    "system",
		Content: systemInstruction,
	})

	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	a.cli.history = append(a.cli.history, models.Message{
		Role:    "user",
		Content: fullQuery,
	})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())

	var responseCtx context.Context
	var cancel context.CancelFunc

	if isAssistant {
		a.logger.Debug("Usando timeout estendido para OpenAI Assistant")
		responseCtx, cancel = context.WithTimeout(ctx, 30*time.Minute)
	} else {
		responseCtx, cancel = context.WithTimeout(ctx, 30*time.Minute)
	}
	defer cancel()

	a.logger.Debug("Enviando prompt para o LLM",
		zap.String("provider", a.cli.Provider),
		zap.Int("historyLength", len(a.cli.history)),
		zap.Int("queryLength", len(fullQuery)))

	aiResponse, err := a.cli.Client.SendPrompt(responseCtx, fullQuery, a.cli.history, 0)

	if err != nil {
		a.logger.Error("Erro ao obter resposta do LLM", zap.Error(err))
	} else {
		a.logger.Debug("Resposta recebida do LLM",
			zap.Int("responseLength", len(aiResponse)))
	}

	a.cli.animation.StopThinkingAnimation()

	if err != nil {
		a.logger.Error("Erro ao obter resposta do LLM no modo agente", zap.Error(err))
		return fmt.Errorf("erro ao obter resposta da IA: %w", err)
	}

	a.cli.history = append(a.cli.history, models.Message{
		Role:    "assistant",
		Content: aiResponse,
	})

	commandBlocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, commandBlocks)

	if len(commandBlocks) > 0 {
		a.handleCommandBlocks(context.Background(), commandBlocks)
	} else {
		fmt.Println("\nNenhum comando executável encontrado na resposta.")
	}
	return nil
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

// requestLLMContinuation solicita continuação à LLM
func (a *AgentMode) requestLLMContinuation(ctx context.Context, userQuery, previousCommand, output, stderr string) ([]CommandBlock, error) {
	newCtx, cancel := a.contextManager.CreateExecutionContext()
	defer cancel()

	_ = ctx

	outSafe := utils.SanitizeSensitiveText(output)
	errSafe := utils.SanitizeSensitiveText(stderr)

	prompt := i18n.T("agent.llm_prompt.continuation", previousCommand, outSafe, errSafe)

	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: prompt})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())
	aiResponse, err := a.cli.Client.SendPrompt(newCtx, prompt, a.cli.history, 0)
	a.cli.animation.StopThinkingAnimation()

	if err != nil {
		fmt.Println(i18n.T("agent.error.continuation_failed"), err)
		return nil, err
	}

	a.cli.history = append(a.cli.history, models.Message{Role: "assistant", Content: aiResponse})

	blocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, blocks)
	return blocks, nil
}

// requestLLMContinuationWithContext solicita continuação com contexto adicional
func (a *AgentMode) requestLLMContinuationWithContext(ctx context.Context, previousCommand, output, stderr, userContext string) ([]CommandBlock, error) {
	newCtx, cancel := a.contextManager.CreateExecutionContext()
	defer cancel()

	_ = ctx

	outSafe := utils.SanitizeSensitiveText(output)
	errSafe := utils.SanitizeSensitiveText(stderr)

	prompt := i18n.T("agent.llm_prompt.continuation_with_context", previousCommand, outSafe, errSafe, userContext)

	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: prompt})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())
	aiResponse, err := a.cli.Client.SendPrompt(newCtx, prompt, a.cli.history, 0)
	a.cli.animation.StopThinkingAnimation()

	if err != nil {
		fmt.Println(i18n.T("agent.error.continuation_failed"), err)
		return nil, err
	}

	a.cli.history = append(a.cli.history, models.Message{Role: "assistant", Content: aiResponse})

	blocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, blocks)
	return blocks, nil
}

// requestLLMWithPreExecutionContext solicita refinamento antes da execução
func (a *AgentMode) requestLLMWithPreExecutionContext(ctx context.Context, originalCommand, userContext string) ([]CommandBlock, error) {
	newCtx, cancel := a.contextManager.CreateExecutionContext()
	defer cancel()

	_ = ctx

	prompt := i18n.T("agent.llm_prompt.pre_execution_context", originalCommand, userContext)

	a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: prompt})

	a.cli.animation.ShowThinkingAnimation(a.cli.Client.GetModelName())
	aiResponse, err := a.cli.Client.SendPrompt(newCtx, prompt, a.cli.history, 0)
	a.cli.animation.StopThinkingAnimation()

	if err != nil {
		fmt.Println(i18n.T("agent.error.refinement_failed"), err)
		return nil, err
	}

	a.cli.history = append(a.cli.history, models.Message{Role: "assistant", Content: aiResponse})

	blocks := a.extractCommandBlocks(aiResponse)
	a.displayResponseWithoutCommands(aiResponse, blocks)
	return blocks, nil
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

mainLoop:
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

			freshCtx, freshCancel := a.contextManager.CreateExecutionContext()
			newBlocks, err := a.requestLLMContinuationWithContext(
				freshCtx,
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				outputs[cmdNum-1].Output,
				outputs[cmdNum-1].ErrorMsg,
				userContext,
			)
			freshCancel()

			if err != nil {
				fmt.Println(i18n.T("agent.error.continuation_failed"), err)
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			if len(newBlocks) > 0 {
				blocks = newBlocks
				outputs = make([]*CommandOutput, len(blocks))
				lastExecuted = -1
				renderer.SetSkipClearOnNextDraw(true)
				continue mainLoop
			} else {
				fmt.Println(i18n.T("agent.status.no_new_commands"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
			}
			continue

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

			freshCtx, freshCancel := a.contextManager.CreateExecutionContext()
			newBlocks, err := a.requestLLMContinuation(
				freshCtx,
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				outputs[cmdNum-1].Output,
				outputs[cmdNum-1].ErrorMsg,
			)
			freshCancel()

			if err != nil {
				fmt.Println(i18n.T("agent.error.continuation_failed"), err)
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			if len(newBlocks) > 0 {
				blocks = newBlocks
				outputs = make([]*CommandOutput, len(blocks))
				lastExecuted = -1
				renderer.SetSkipClearOnNextDraw(true)
				continue mainLoop
			} else {
				fmt.Println(i18n.T("agent.status.no_new_commands"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
			}
			continue

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
			newBlocks, err := a.requestLLMWithPreExecutionContext(
				ctx,
				strings.Join(blocks[cmdNum-1].Commands, "\n"),
				userContext,
			)
			if err != nil {
				fmt.Println(i18n.T("agent.error.refinement_failed"), err)
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}
			if len(newBlocks) > 0 {
				blocks = newBlocks
				outputs = make([]*CommandOutput, len(blocks))
				lastExecuted = -1
				renderer.SetSkipClearOnNextDraw(true)
				continue mainLoop
			} else {
				fmt.Println(i18n.T("agent.status.no_new_commands"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
			}
			continue

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

			header := fmt.Sprintf(i18n.T("agent.status.executing_command_n", i+1, len(block.Commands), trimmed)+"\n", i+1, len(block.Commands), trimmed)
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
