package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	// No longer depends on cli package — command data is maintained inline.
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
	builder.WriteString(generateSubcommandsSection())
	builder.WriteString(generateWatchInteractiveSection())

	out := builder.String()
	// Mantém o behavior atual: imprimir no stdout
	fmt.Println(out)

	// E gravar também na doc do ghpages para manter sempre atualizado
	outFile := filepath.Join("ghpages", "content", "docs", "reference", "command-reference.md")
	if err := os.MkdirAll(filepath.Dir(outFile), 0755); err != nil {
		panic(fmt.Errorf("falha ao criar diretório de saída do docgen: %w", err))
	}
	if err := os.WriteFile(outFile, []byte(out), 0644); err != nil {
		panic(fmt.Errorf("falha ao gravar a referência de comandos: %w", err))
	}
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

	commands := []struct{ Text, Description string }{
		{"/agent", "Iniciar modo agente para executar tarefas"},
		{"/auth", "Gerencia credenciais OAuth (status, login, logout)"},
		{"/clear", "Força redesenho/limpeza da tela"},
		{"/coder", "Iniciar modo engenheiro (Criação e Edição de Código)"},
		{"/compact", "Compacta o histórico"},
		{"/config", "Mostrar configuração atual"},
		{"/connect", "Conectar a um servidor ChatCLI remoto (gRPC)"},
		{"/context", "Gerencia contextos persistentes"},
		{"/disconnect", "Desconectar do servidor remoto"},
		{"/exit", "Sair do ChatCLI"},
		{"/help", "Mostrar ajuda"},
		{"/memory", "Ver/carregar anotações de memória"},
		{"/metrics", "Exibe métricas de runtime"},
		{"/newsession", "Iniciar uma nova sessão de conversa"},
		{"/nextchunk", "Carregar o próximo chunk de arquivo"},
		{"/plugin", "Gerencia plugins"},
		{"/quit", "Alias de /exit"},
		{"/reload", "Recarregar configurações do .env"},
		{"/retry", "Tentar novamente o último chunk que falhou"},
		{"/retryall", "Tentar novamente todos os chunks que falharam"},
		{"/rewind", "Volta a um checkpoint anterior da conversa"},
		{"/run", "Alias para /agent"},
		{"/session", "Gerencia as sessões"},
		{"/skill", "Gerencia skills de registries"},
		{"/skipchunk", "Pular um chunk de arquivo"},
		{"/status", "Alias de /config"},
		{"/switch", "Trocar o provedor de LLM"},
		{"/version", "Verificar a versão do ChatCLI"},
		{"/watch", "Exibe o status do K8s watcher"},
	}

	sort.Slice(commands, func(i, j int) bool {
		return commands[i].Text < commands[j].Text
	})

	var rows [][]string
	for _, cmd := range commands {
		rows = append(rows, []string{fmt.Sprintf("**%s**", cmd.Text), cmd.Description})

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

	var rows [][]string
	rows = append(rows, []string{"**@command** `<...>`", "Executa um comando do sistema e injeta a saída"})
	rows = append(rows, []string{"&nbsp; `-i`", "Executa o comando em modo interativo (ex: `ssh`, `vim`)."})
	rows = append(rows, []string{"&nbsp; `--ai`", "Envia a saída do comando diretamente para a IA para análise."})
	rows = append(rows, []string{"**@env** `<...>`", "Injeta variáveis de ambiente no contexto"})
	rows = append(rows, []string{"**@file** `<...>`", "Injeta conteúdo de arquivos/diretórios no prompt"})
	rows = append(rows, []string{"&nbsp; `--mode`", "Define o modo de processamento: `full`, `summary`, `chunked`, `smart`."})
	rows = append(rows, []string{"&nbsp;&nbsp;&nbsp;`full`", "Processa o conteúdo completo (padrão, trunca se necessário)"})
	rows = append(rows, []string{"&nbsp;&nbsp;&nbsp;`summary`", "Gera resumo estrutural (árvore de arquivos, tamanhos, sem conteúdo)"})
	rows = append(rows, []string{"&nbsp;&nbsp;&nbsp;`chunked`", "Divide grandes projetos em pedaços gerenciáveis (use /nextchunk para prosseguir)"})
	rows = append(rows, []string{"&nbsp;&nbsp;&nbsp;`smart`", "Seleciona arquivos relevantes com base no seu prompt (IA decide)"})
	rows = append(rows, []string{"**@git** `<...>`", "Injeta informações do repositório git"})
	rows = append(rows, []string{"**@history** `<...>`", "Injeta o histórico de comandos"})

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
	builder.WriteString("\n---\n\n")
	return builder.String()
}

// generateSubcommandsSection gera as tabelas de flags para os subcomandos serve, connect e watch.
func generateSubcommandsSection() string {
	var builder strings.Builder
	builder.WriteString("## Subcomandos\n\n")
	builder.WriteString("O ChatCLI suporta subcomandos para funcionalidades avançadas de servidor e monitoramento.\n\n")

	// --- chatcli server ---
	builder.WriteString("### `chatcli server` — Modo Servidor gRPC\n\n")
	builder.WriteString("Inicia o ChatCLI como servidor gRPC para acesso remoto.\n\n")

	serveFlags := [][]string{
		{"`--port <int>`", "Porta do servidor gRPC", "`50051`"},
		{"`--token <string>`", "Token de autenticação (vazio = sem auth)", "`\"\"`"},
		{"`--tls-cert <path>`", "Arquivo de certificado TLS", "`\"\"`"},
		{"`--tls-key <path>`", "Arquivo de chave TLS", "`\"\"`"},
		{"`--provider <nome>`", "Provedor de LLM padrão", "Auto-detectado"},
		{"`--model <nome>`", "Modelo de LLM padrão", "Auto-detectado"},
		{"`--watch-deployment <nome>`", "Deployment K8s a monitorar (habilita watcher)", "`\"\"`"},
		{"`--watch-namespace <ns>`", "Namespace do deployment", "`\"default\"`"},
		{"`--watch-interval <dur>`", "Intervalo de coleta do watcher", "`30s`"},
		{"`--watch-window <dur>`", "Janela de observação do watcher", "`2h`"},
		{"`--watch-max-log-lines <n>`", "Max linhas de log por pod", "`100`"},
		{"`--watch-kubeconfig <path>`", "Caminho do kubeconfig", "Auto-detectado"},
	}
	builder.WriteString(generateMarkdownTable([]string{"Flag", "Descrição", "Padrão"}, serveFlags))

	// --- chatcli connect ---
	builder.WriteString("\n### `chatcli connect` — Conexão Remota\n\n")
	builder.WriteString("Conecta a um servidor ChatCLI remoto via gRPC.\n\n")

	connectFlags := [][]string{
		{"`<address>`", "Endereço do servidor (posicional)", ""},
		{"`--addr <host:port>`", "Endereço do servidor (flag)", "`\"\"`"},
		{"`--token <string>`", "Token de autenticação", "`\"\"`"},
		{"`--provider <nome>`", "Sobrescreve o provedor LLM do servidor", "`\"\"`"},
		{"`--model <nome>`", "Sobrescreve o modelo LLM do servidor", "`\"\"`"},
		{"`--llm-key <string>`", "Sua própria API key (enviada ao servidor)", "`\"\"`"},
		{"`--use-local-auth`", "Usa credenciais OAuth do auth store local", "`false`"},
		{"`--tls`", "Habilita conexão TLS", "`false`"},
		{"`--ca-cert <path>`", "Certificado CA para TLS", "`\"\"`"},
		{"`-p <prompt>`", "One-shot: envia prompt e sai", "`\"\"`"},
		{"`--raw`", "Saída crua (sem formatação)", "`false`"},
		{"`--max-tokens <int>`", "Máximo de tokens na resposta", "`0`"},
		{"`--client-id <string>`", "StackSpot Client ID", "`\"\"`"},
		{"`--client-key <string>`", "StackSpot Client Key", "`\"\"`"},
		{"`--realm <string>`", "StackSpot Realm/Tenant", "`\"\"`"},
		{"`--agent-id <string>`", "StackSpot Agent ID", "`\"\"`"},
		{"`--ollama-url <url>`", "URL base do Ollama", "`\"\"`"},
	}
	builder.WriteString(generateMarkdownTable([]string{"Flag", "Descrição", "Padrão"}, connectFlags))

	// --- chatcli watch ---
	builder.WriteString("\n### `chatcli watch` — Monitoramento Kubernetes\n\n")
	builder.WriteString("Monitora um deployment Kubernetes e injeta contexto K8s nas conversas com a IA.\n\n")

	watchFlags := [][]string{
		{"`--deployment <nome>`", "Deployment a monitorar (obrigatório)", "`\"\"`"},
		{"`--namespace <ns>`", "Namespace do deployment", "`\"default\"`"},
		{"`--interval <dur>`", "Intervalo de coleta", "`30s`"},
		{"`--window <dur>`", "Janela de observação", "`2h`"},
		{"`--max-log-lines <n>`", "Max linhas de log por pod", "`100`"},
		{"`--kubeconfig <path>`", "Caminho do kubeconfig", "Auto-detectado"},
		{"`--provider <nome>`", "Provedor de LLM", "`.env`"},
		{"`--model <nome>`", "Modelo de LLM", "`.env`"},
		{"`-p <prompt>`", "One-shot: envia prompt com contexto K8s e sai", "`\"\"`"},
		{"`--max-tokens <int>`", "Máximo de tokens na resposta", "`0`"},
	}
	builder.WriteString(generateMarkdownTable([]string{"Flag", "Descrição", "Padrão"}, watchFlags))

	builder.WriteString("\n---\n\n")
	return builder.String()
}

// generateWatchInteractiveSection gera a tabela do comando /watch no modo interativo.
func generateWatchInteractiveSection() string {
	var builder strings.Builder
	builder.WriteString("## Comando `/watch` (Modo Interativo)\n\n")
	builder.WriteString("Disponível dentro do ChatCLI interativo (local ou remoto):\n\n")

	watchCmds := [][]string{
		{"`/watch status`", "Mostra o status do K8s Watcher (local ou remoto)"},
		{"`/watch`", "Mostra ajuda do comando watch"},
	}
	builder.WriteString(generateMarkdownTable([]string{"Comando", "Descrição"}, watchCmds))

	return builder.String()
}
