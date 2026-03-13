/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
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
)

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

// getMultilineInput obtém entrada de múltiplas linhas.
// Uses the centralized stdin reader when active.
func (a *AgentMode) getMultilineInput(promptStr string) string {
	fmt.Print(promptStr)
	fmt.Println(i18n.T("agent.multiline_input_tip"))

	// Enable bracketed paste mode for paste detection
	if runtime.GOOS != "windows" || os.Getenv("WT_SESSION") != "" {
		_, _ = os.Stdout.WriteString("\x1b[?2004h")
		defer func() { _, _ = os.Stdout.WriteString("\x1b[?2004l") }()
	}

	var lines []string
	for {
		line := a.readLine()
		if line == "." {
			break
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
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

			if runtime.GOOS != "windows" {
				cmd := exec.Command(sttyPath, "sane")
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				_ = cmd.Run()
			}

			fmt.Print(i18n.T("agent.status.batch_confirm"))
			confirmationInput := a.readLine()
			confirmation := strings.ToLower(confirmationInput)
			if confirmation != "s" && confirmation != "y" {
				fmt.Println(i18n.T("agent.status.batch_cancelled"))
				_ = a.getInput(renderer.Colorize(i18n.T("agent.status.press_enter"), agent.ColorGray))
				continue
			}

			for i, block := range blocks {
				fmt.Printf(i18n.T("agent.status.executing_command", i+1)+"\n", i+1)
				fmt.Printf("  %s %s\n", i18n.T("agent.plan.field.type"), block.Language)
				for j, cmd := range block.Commands {
					fmt.Printf("  %s %d/%d: %s\n", i18n.T("agent.label.command"), j+1, len(block.Commands), cmd)
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
			_ = os.Chmod(scriptPath, 0700)

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
	if runtime.GOOS != "windows" {
		cmd := exec.Command(sttyPath, "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	fmt.Print("\n")
	fmt.Print(prompt)

	return a.readLine()
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
		fmt.Printf("🔸 %s %d/%d: %s\n", i18n.T("agent.dryrun.label"), i+1, len(block.Commands), cmd)

		simCmd := "echo " + utils.ShellQuote("[dry-run] Vai executar: "+cmd)

		if block.Language == "shell" {
			out, err := a.executor.CaptureOutput(ctx, shell, []string{"-c", simCmd})
			fmt.Println(string(out))
			if err != nil {
				fmt.Printf("❗ %s: %v\n", i18n.T("agent.dryrun.failed"), err)
			}
		} else if block.Language == "kubernetes" && strings.Contains(cmd, "apply") {
			cmdDry := cmd + " --dry-run=client"
			out, err := a.executor.CaptureOutput(ctx, shell, []string{"-c", cmdDry})
			fmt.Println(string(out))
			if err != nil {
				fmt.Printf("❗ %s: %v\n", i18n.T("agent.dryrun.failed"), err)
			}
		} else {
			out, _ := a.executor.CaptureOutput(ctx, shell, []string{"-c", "echo " + utils.ShellQuote("[dry-run] "+cmd)})
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

	editor, err := resolveEditor()
	if err != nil {
		return nil, fmt.Errorf("invalid editor configuration: %w", err)
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
