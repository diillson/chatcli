package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli" // Importando o pacote da nossa aplicação
)

// main é o ponto de entrada da nossa ferramenta de geração de documentação.
func main() {
	var builder strings.Builder

	header := []string{
		"+++",
		`title = "Referência Completa de Comandos"`,
		`linkTitle = "Referência de Comandos"`,
		`weight = 20`,
		`description = "Uma folha de consulta (cheatsheet) para todos os comandos, flags e opções disponíveis no ChatCLI."`,
		"+++",
		"",
		"Esta página é uma referência rápida e **gerada automaticamente** para todos os comandos e flags disponíveis no **ChatCLI**.",
		"",
	}
	builder.WriteString(strings.Join(header, "\n"))

	// Gera cada seção da documentação
	builder.WriteString(generateInternalCommandsSection())
	builder.WriteString(generateContextCommandsSection())
	builder.WriteString(generateAgentModeSection())
	builder.WriteString(generateSessionContextSection())
	builder.WriteString(generateOneShotFlagsSection())

	fmt.Println(builder.String())
}

// generateMarkdownTable é uma função utilitária para criar tabelas em Markdown.
func generateMarkdownTable(headers []string, rows [][]string) string {
	var builder strings.Builder
	builder.WriteString("| " + strings.Join(headers, " | ") + " |\n")
	builder.WriteString("|" + strings.Repeat(" --- |", len(headers)) + "\n")
	for _, row := range rows {
		builder.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	return builder.String()
}

// generateInternalCommandsSection gera a tabela para comandos como /help, /switch, etc.
func generateInternalCommandsSection() string {
	var builder strings.Builder
	builder.WriteString("## Comandos Internos (`/`)\n\n")
	builder.WriteString("Estes comandos controlam a aplicação e o fluxo da conversa.\n\n")

	var mockCLI *cli.ChatCLI
	commands := mockCLI.GetInternalCommands()

	// CORREÇÃO: Ordenar os comandos principais antes de processar para garantir uma ordem consistente.
	sort.Slice(commands, func(i, j int) bool {
		return commands[i].Text < commands[j].Text
	})

	var rows [][]string
	for _, cmd := range commands {
		name := strings.ReplaceAll(cmd.Text, " ou ", ", ")
		rows = append(rows, []string{fmt.Sprintf("**%s**", name), cmd.Description})

		// CORREÇÃO: Agrupar as flags diretamente sob o comando a que pertencem.
		if cmd.Text == "/switch" {
			rows = append(rows, []string{"&nbsp; `--model <nome>`", "Muda o modelo para o provedor atual."})
			rows = append(rows, []string{"&nbsp; `--max-tokens <num>`", "Define um limite máximo de tokens para a resposta."})
			rows = append(rows, []string{"&nbsp; `--realm <nome>`", "**(StackSpot)** Define o `realm` (tenant)."})
			rows = append(rows, []string{"&nbsp; `--agent-id <id>`", "**(StackSpot)** Define o `Agent ID` a ser usado."})
		}
	}

	builder.WriteString(generateMarkdownTable([]string{"Comando", "Descrição"}, rows))
	builder.WriteString("\n---\n\n")
	return builder.String()
}

// generateContextCommandsSection gera a tabela para comandos @file, @git, etc.
func generateContextCommandsSection() string {
	var builder strings.Builder
	builder.WriteString("## Comandos de Contexto (`@`)\n\n")
	builder.WriteString("Estes comandos injetam informações do seu ambiente local no prompt.\n\n")

	var mockCLI *cli.ChatCLI
	commands := mockCLI.GetContextCommands()
	sort.Slice(commands, func(i, j int) bool { return commands[i].Text < commands[j].Text })

	var rows [][]string
	for _, cmd := range commands {
		rows = append(rows, []string{fmt.Sprintf("**%s** `<...>`", cmd.Text), cmd.Description})

		if flags, ok := cli.CommandFlags[cmd.Text]; ok {
			var flagNames []string
			for flag := range flags {
				flagNames = append(flagNames, flag)
			}
			sort.Strings(flagNames)
			processedFlags := make(map[string]bool)

			for _, flag := range flagNames {
				if processedFlags[flag] {
					continue
				}

				var desc, flagNameToDisplay string
				values := flags[flag]

				if flag == "-i" || flag == "--interactive" {
					flagNameToDisplay = "`-i`, `--interactive`"
					desc = "Executa o comando em modo interativo (ex: `ssh`, `vim`)."
					processedFlags["-i"] = true
					processedFlags["--interactive"] = true
				} else if flag == "--mode" {
					flagNameToDisplay = "`--mode`"
					desc = "Define o modo de processamento: `full`, `summary`, `chunked`, `smart`."
				} else if flag == "--ai" {
					flagNameToDisplay = "`--ai`"
					desc = "Envia a saída do comando diretamente para a IA para análise."
				} else {
					flagNameToDisplay = fmt.Sprintf("`%s`", flag)
					desc = "Opção para " + cmd.Text
				}

				rows = append(rows, []string{fmt.Sprintf("&nbsp; %s", flagNameToDisplay), desc})

				for _, val := range values {
					rows = append(rows, []string{fmt.Sprintf("&nbsp;&nbsp;&nbsp;`%s`", val.Text), val.Description})
				}
			}
		}
	}

	builder.WriteString(generateMarkdownTable([]string{"Comando", "Descrição"}, rows))
	builder.WriteString("\n---\n\n")
	return builder.String()
}

// generateAgentModeSection gera a documentação para as ações do Modo Agente.
func generateAgentModeSection() string {
	actions := []struct{ Action, Description string }{
		{"`[N]`", "Executa o comando de número `N`."},
		{"`a`", "Executa todos os comandos pendentes."},
		{"`eN`", "Edita o comando `N` antes de executar."},
		{"`tN`", "Simula (dry-run) o comando `N`."},
		{"`cN`", "Pede continuação para a IA com a saída do comando `N`."},
		{"`pcN`", "Adiciona contexto pré-execução ao comando `N`."},
		{"`acN`", "Adiciona contexto pós-execução (à saída) do comando `N`."},
		{"`vN`", "Visualiza a saída completa do comando `N` em um pager."},
		{"`wN`", "Salva a saída do comando `N` em um arquivo temporário."},
		{"`p`", "Alterna a visualização do plano (compacta/completa)."},
		{"`r`", "Redesenha a tela."},
		{"`q`", "Sai do modo agente."},
	}

	var builder strings.Builder
	builder.WriteString("## Modo Agente (`/agent` ou `/run`)\n\n")
	builder.WriteString("Delega tarefas para a IA planejar e executar. O comando principal é `/agent <tarefa>`.\n\n")
	builder.WriteString("#### Ações Dentro do Modo Agente\n\n")

	var rows [][]string
	for _, action := range actions {
		rows = append(rows, []string{action.Action, action.Description})
	}

	builder.WriteString(generateMarkdownTable([]string{"Ação", "Descrição"}, rows))
	builder.WriteString("\n---\n\n")
	return builder.String()
}

// generateSessionContextSection gera as tabelas para /session e /context.
func generateSessionContextSection() string {
	sessionCmds := [][]string{
		{"`/session save <nome>`", "Salva a conversa atual com um nome."},
		{"`/session load <nome>`", "Carrega uma conversa salva."},
		{"`/session list`", "Lista todas as sessões salvas."},
		{"`/session delete <nome>`", "Deleta uma sessão salva."},
		{"`/session new`", "Inicia uma nova sessão limpa."},
	}

	contextCmds := [][]string{
		{"`/context create <nome> ...`", "Cria um 'snapshot' persistente de arquivos/diretórios."},
		{"`/context update <nome> ...`", "Atualiza um contexto existente."},
		{"`/context attach <nome> ...`", "Anexa um contexto salvo à sessão atual."},
		{"`/context detach <nome>`", "Desanexa um contexto da sessão."},
		{"`/context list`", "Lista todos os contextos salvos."},
		{"`/context show <nome>`", "Mostra detalhes e arquivos de um contexto."},
		{"`/context inspect <nome> ...`", "Mostra estatísticas detalhadas de um contexto."},
		{"`/context delete <nome>`", "Deleta um contexto permanentemente."},
		{"`/context merge <novo> <c1> <c2>`", "Combina múltiplos contextos em um novo."},
		{"`/context attached`", "Mostra os contextos atualmente anexados."},
		{"`/context export <nome> <arq>`", "Exporta um contexto para um arquivo JSON."},
		{"`/context import <arq>`", "Importa um contexto de um arquivo JSON."},
		{"`/context metrics`", "Exibe estatísticas gerais de uso dos contextos."},
	}

	var builder strings.Builder
	builder.WriteString("## Gerenciamento de Sessões e Contextos\n\n")

	builder.WriteString("#### Comandos de Sessão (`/session`)\n\n")
	builder.WriteString(generateMarkdownTable([]string{"Comando", "Descrição"}, sessionCmds))

	builder.WriteString("\n#### Comandos de Contexto (`/context`)\n\n")
	builder.WriteString(generateMarkdownTable([]string{"Comando", "Descrição"}, contextCmds))

	builder.WriteString("\n---\n\n")
	return builder.String()
}

// generateOneShotFlagsSection gera a tabela para as flags de linha de comando.
func generateOneShotFlagsSection() string {
	flags := [][]string{
		{"`-p`, `--prompt \"<texto>`", "Executa um único prompt e sai."},
		{"`--provider <nome>`", "Sobrescreve o provedor de IA (ex: `GOOGLEAI`)."},
		{"`--model <nome>`", "Sobrescreve o modelo de IA (ex: `gemini-1.5-pro-latest`)."},
		{"`--timeout <duração>`", "Define o tempo limite para a requisição (ex: `10s`, `1m`)."},
		{"`--max-tokens <num>`", "Limita o número de tokens na resposta."},
		{"`--agent-auto-exec`", "No modo agente one-shot, executa o primeiro comando se for seguro."},
		{"`--no-anim`", "Desabilita a animação 'Pensando...', útil para scripts."},
		{"`-v`, `--version`", "Mostra a informação de versão."},
		{"`-h`, `--help`", "Mostra a tela de ajuda."},
	}

	var builder strings.Builder
	builder.WriteString("## Flags de Linha de Comando (Modo One-Shot)\n\n")
	builder.WriteString("Use estas flags ao executar `chatcli` diretamente do seu terminal para automações.\n\n")
	builder.WriteString(generateMarkdownTable([]string{"Flag", "Descrição"}, flags))
	return builder.String()
}
