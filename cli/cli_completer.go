package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

func (cli *ChatCLI) completer(d prompt.Document) []prompt.Suggest {
	// 1. Lidar com estados especiais primeiro (como a troca de provedor)
	if cli.interactionState == StateSwitchingProvider {
		providers := cli.manager.GetAvailableProviders()
		s := make([]prompt.Suggest, len(providers))
		for i, p := range providers {
			s[i] = prompt.Suggest{Text: strconv.Itoa(i + 1), Description: p}
		}
		return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
	}

	// 2. Extrair informações do documento atual
	lineBeforeCursor := d.TextBeforeCursor()
	wordBeforeCursor := d.GetWordBeforeCursor()
	args := strings.Fields(lineBeforeCursor)

	// --- Lógica de Autocomplete Contextual ---

	// 2.5. Detectar comandos /context e /session mesmo após espaço
	if strings.HasPrefix(lineBeforeCursor, "/context") {
		return cli.getContextSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/session") {
		return cli.getSessionSuggestions(d)
	}
	if strings.HasPrefix(lineBeforeCursor, "/plugin ") {
		return cli.getPluginSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/skill") {
		return cli.getSkillSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/memory") {
		return cli.getMemorySuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/agent") {
		return cli.getAgentSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/auth") {
		return cli.getAuthSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/connect ") {
		return cli.getConnectSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/watch") {
		return cli.getWatchSuggestions(d)
	}

	// 3. Autocomplete para argumentos de comandos @ (como caminhos para @file)
	if len(args) > 0 {
		var previousWord string
		if strings.HasSuffix(lineBeforeCursor, " ") {
			previousWord = args[len(args)-1]
		} else if len(args) > 1 {
			previousWord = args[len(args)-2]
		}

		// Apenas autocompletar caminhos se a palavra atual NÃO for uma flag
		if previousWord == "@file" && !strings.HasPrefix(wordBeforeCursor, "-") {
			return cli.filePathCompleter(wordBeforeCursor)
		}

		if previousWord == "@command" && !strings.HasPrefix(wordBeforeCursor, "-") {
			suggestions := cli.systemCommandCompleter(wordBeforeCursor)
			suggestions = append(suggestions, cli.filePathCompleter(wordBeforeCursor)...)
			return suggestions
		}
	}

	// 4. Autocomplete para iniciar comandos
	if !strings.Contains(lineBeforeCursor, " ") {
		if strings.HasPrefix(wordBeforeCursor, "/") {
			return prompt.FilterHasPrefix(cli.GetInternalCommands(), wordBeforeCursor, true)
		}
	}

	if strings.HasPrefix(wordBeforeCursor, "@") {
		return prompt.FilterHasPrefix(cli.GetContextCommands(), wordBeforeCursor, true)
	}

	// 5. Sugestões de flags e valores
	if len(args) > 1 {
		command := args[0]
		prevWord := args[len(args)-1]
		if !strings.HasSuffix(lineBeforeCursor, " ") && len(args) > 1 {
			prevWord = args[len(args)-2]
		}
		currWord := d.GetWordBeforeCursor()

		if flagsForCommand, commandExists := CommandFlags[command]; commandExists {
			// Cenário 1: O usuário digitou uma flag (ex: "--mode ") e agora quer ver os valores.
			if values, flagHasValues := flagsForCommand[prevWord]; flagHasValues && len(values) > 0 {
				return prompt.FilterHasPrefix(values, currWord, true)
			}

			// Cenário 2: O usuário está digitando uma flag (ex: "--m").
			if strings.HasPrefix(currWord, "-") {
				var flagSuggests []prompt.Suggest
				for flag, values := range flagsForCommand {
					var desc string
					// 1. Primeiro, procurar por descrições personalizadas
					if flag == "--mode" {
						desc = "Define o modo de processamento de arquivos (full, summary, chunked, smart)"
					} else if flag == "--model" {
						desc = "Troque o modelo (Runtime) baseado no provedor atual (grpt-5, grok-4, etc.)"
					} else if flag == "--max-tokens" {
						desc = "Define o máximo de tokens para as próximas respostas (0 para padrão)"
					} else if flag == "--agent-id" {
						desc = "Altera o agent em tempo de execução (Apenas para STACKSPOT)"
					} else if flag == "--realm" {
						desc = "Altera o Realm/Tenant em tempo de execução (Apenas para STACKSPOT)"
					} else if flag == "-i" {
						desc = "Ideal para comandos interativos evitando sensação de bloqueio do terminal"
					} else if flag == "--ai" {
						desc = "Envia a saída do comando direto para a IA analisar, para contexto adicional digite ( @command --ai <comando> > <contexto>)"
					} else {
						// 2. Se não houver descrição personalizada, criar uma genérica
						desc = fmt.Sprintf("Opção para %s", command)
						if len(values) > 0 {
							desc += " (valores: " + strings.Join(extractTexts(values), ", ") + ")"
						}
					}
					flagSuggests = append(flagSuggests, prompt.Suggest{Text: flag, Description: desc})
				}
				return prompt.FilterHasPrefix(flagSuggests, currWord, true)
			}
		}
	}

	// 6. Se nenhum dos casos acima se aplicar, não sugira nada.
	return []prompt.Suggest{}
}

// Helper para extrair só os Texts de um []Suggest (para descrições de flags)
func extractTexts(suggests []prompt.Suggest) []string {
	texts := make([]string, len(suggests))
	for i, s := range suggests {
		texts[i] = s.Text
	}
	return texts
}

func (cli *ChatCLI) GetInternalCommands() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "/exit", Description: "Sair do ChatCLI"},
		{Text: "/quit", Description: "Alias de /exit - Sair do ChatCLI"},
		{Text: "/switch", Description: "Trocar o provedor de LLM, seguido por --model troca o modelo"},
		{Text: "/help", Description: "Mostrar ajuda"},
		{Text: "/reload", Description: "Recarregar configurações do .env"},
		{Text: "/config", Description: "Mostrar configuração atual"},
		{Text: "/status", Description: "Alias de /config - Mostrar configuração atual"},
		{Text: "/agent", Description: "Iniciar modo agente para executar tarefas"},
		{Text: "/coder", Description: "Iniciar modo engenheiro (Criação e Edição de Código)"},
		{Text: "/run", Description: "Alias para /agent - Iniciar modo agente para executar tarefas"},
		{Text: "/newsession", Description: "Iniciar uma nova sessão de conversa"},
		{Text: "/version", Description: "Verificar a versão do ChatCLI"},
		{Text: "/nextchunk", Description: "Carregar o próximo chunk de arquivo"},
		{Text: "/retry", Description: "Tentar novamente o último chunk que falhou"},
		{Text: "/retryall", Description: "Tentar novamente todos os chunks que falharam"},
		{Text: "/skipchunk", Description: "Pular um chunk de arquivo"},
		{Text: "/session", Description: "Gerencia as sessões, new, save, list, load, delete"},
		{Text: "/context", Description: "Gerencia contextos persistentes (create, attach, detach, list, show, etc)"},
		{Text: "/plugin", Description: "Gerencia plugins (install, list, show, etc.)"},
		{Text: "/skill", Description: "Gerencia skills de registries (search, install, uninstall, list)"},
		{Text: "/clear", Description: "Força redesenho/limpeza da tela se o prompt estiver corrompido ou com artefatos visuais."},
		{Text: "/auth", Description: "Gerencia credenciais OAuth (status, login, logout)"},
		{Text: "/connect", Description: "Conectar a um servidor ChatCLI remoto (gRPC)"},
		{Text: "/disconnect", Description: "Desconectar do servidor remoto e voltar ao modo local"},
		{Text: "/watch", Description: "Exibe o status do K8s watcher (quando ativo)"},
		{Text: "/metrics", Description: "Exibe métricas de runtime (provider, sessão, tokens, memória)"},
		{Text: "/compact", Description: "Compacta o histórico (use /compact <instrução> para guiar o que preservar)"},
		{Text: "/rewind", Description: "Volta a um checkpoint anterior da conversa"},
		{Text: "/memory", Description: "Ver/carregar anotações de memória (today, yesterday, week, load <data>, longterm, list)"},
	}
}

// getConnectSuggestions returns autocomplete suggestions for /connect flags.
func (cli *ChatCLI) getConnectSuggestions(d prompt.Document) []prompt.Suggest {
	wordBeforeCursor := d.GetWordBeforeCursor()

	if strings.HasPrefix(wordBeforeCursor, "-") {
		flags := []prompt.Suggest{
			{Text: "--token", Description: "Token de autenticação do servidor"},
			{Text: "--provider", Description: "Provedor LLM (OPENAI, CLAUDEAI, GOOGLEAI, XAI, STACKSPOT, OLLAMA, COPILOT)"},
			{Text: "--model", Description: "Modelo LLM (gpt-4, claude-3, etc.)"},
			{Text: "--llm-key", Description: "API key/OAuth token para enviar ao servidor"},
			{Text: "--use-local-auth", Description: "Usar credenciais OAuth locais (de /auth login)"},
			{Text: "--tls", Description: "Habilitar conexão TLS"},
			{Text: "--ca-cert", Description: "Arquivo de certificado CA para TLS"},
			{Text: "--client-id", Description: "StackSpot: Client ID para autenticação"},
			{Text: "--client-key", Description: "StackSpot: Client Key para autenticação"},
			{Text: "--realm", Description: "StackSpot: Realm/Tenant"},
			{Text: "--agent-id", Description: "StackSpot: Agent ID"},
			{Text: "--ollama-url", Description: "Ollama: Base URL do servidor (ex: http://localhost:11434)"},
		}
		return prompt.FilterHasPrefix(flags, wordBeforeCursor, true)
	}

	return []prompt.Suggest{}
}

// getWatchSuggestions returns autocomplete suggestions for /watch subcommands and flags.
func (cli *ChatCLI) getWatchSuggestions(d prompt.Document) []prompt.Suggest {
	wordBeforeCursor := d.GetWordBeforeCursor()
	lineBeforeCursor := d.TextBeforeCursor()

	// Suggest flags after /watch start
	if strings.Contains(lineBeforeCursor, "/watch start") && strings.HasPrefix(wordBeforeCursor, "-") {
		flags := []prompt.Suggest{
			{Text: "--deployment", Description: "Deployment K8s a monitorar (obrigatório)"},
			{Text: "--namespace", Description: "Namespace do deployment (padrão: default)"},
			{Text: "--interval", Description: "Intervalo de coleta (ex: 10s, 1m)"},
			{Text: "--window", Description: "Janela de observação (ex: 1h, 4h)"},
			{Text: "--max-log-lines", Description: "Máximo de linhas de log por pod"},
			{Text: "--kubeconfig", Description: "Caminho do kubeconfig"},
		}
		return prompt.FilterHasPrefix(flags, wordBeforeCursor, true)
	}

	// Suggest subcommands
	subcommands := []prompt.Suggest{
		{Text: "start", Description: "Iniciar monitoramento K8s (ex: /watch start --deployment myapp)"},
		{Text: "stop", Description: "Parar o monitoramento K8s"},
		{Text: "status", Description: "Exibir status do watcher ativo"},
	}
	return prompt.FilterHasPrefix(subcommands, wordBeforeCursor, true)
}

// GetContextCommands retorna a lista de sugestões para comandos com @
func (cli *ChatCLI) GetContextCommands() []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "@history", Description: i18n.T("help.command.history")},
		{Text: "@git", Description: i18n.T("help.command.git")},
		{Text: "@env", Description: i18n.T("help.command.env")},
		{Text: "@file", Description: i18n.T("help.command.file")},
		{Text: "@command", Description: i18n.T("help.command.command")},
	}

	// Adicionar plugins customizados
	if cli != nil && cli.pluginManager != nil {
		for _, plugin := range cli.pluginManager.GetPlugins() {
			suggestions = append(suggestions, prompt.Suggest{
				Text:        plugin.Name(),
				Description: plugin.Description(),
			})
		}
	}
	return suggestions
}

// filePathCompleter é uma função dedicada para autocompletar caminhos de arquivo
func (cli *ChatCLI) filePathCompleter(prefix string) []prompt.Suggest {
	var suggestions []prompt.Suggest
	completions := cli.completeFilePath(prefix)
	for _, c := range completions {
		suggestions = append(suggestions, prompt.Suggest{Text: c})
	}
	return suggestions
}

// systemCommandCompleter é uma função dedicada para autocompletar comandos do sistema
func (cli *ChatCLI) systemCommandCompleter(prefix string) []prompt.Suggest {
	var suggestions []prompt.Suggest
	completions := cli.completeSystemCommands(prefix)
	for _, c := range completions {
		suggestions = append(suggestions, prompt.Suggest{Text: c})
	}
	return suggestions
}

// completeFilePath autocompleta caminhos de arquivos
func (cli *ChatCLI) completeFilePath(prefix string) []string {
	var completions []string

	dir, filePrefix := filepath.Split(prefix)
	if dir == "" {
		dir = "."
	}

	// Expandir "~" para o diretório home
	dir = os.ExpandEnv(dir)
	if strings.HasPrefix(dir, "~") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			dir = filepath.Join(homeDir, dir[1:])
		}
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return completions
	}

	for _, entry := range files {
		name := entry.Name()
		if strings.HasPrefix(name, filePrefix) {
			path := filepath.Join(dir, name)
			if entry.IsDir() {
				path += string(os.PathSeparator)
			}
			completions = append(completions, path)
		}
	}

	return completions
}

// completeSystemCommands autocompleta comandos do sistema
func (cli *ChatCLI) completeSystemCommands(prefix string) []string {
	var completions []string

	// Obter o PATH do sistema
	pathEnv := os.Getenv("PATH")
	paths := strings.Split(pathEnv, string(os.PathListSeparator))

	seen := make(map[string]bool)

	for _, pathDir := range paths {
		files, err := os.ReadDir(pathDir)
		if err != nil {
			continue
		}

		for _, file := range files {
			name := file.Name()
			if strings.HasPrefix(name, prefix) && !seen[name] {
				seen[name] = true
				completions = append(completions, name)
			}
		}
	}

	return completions
}

// getContextSuggestions - Sugestões melhoradas para /context
func (cli *ChatCLI) getContextSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Se só digitou "/context" (sem espaço ou com espaço mas sem subcomando ainda)
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/context", Description: "📦 Gerencia contextos persistentes (create, attach, detach, list, show, inspect, etc)"},
		}
	}

	// Se digitou "/context " (com espaço) mas ainda não completou o subcomando
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "create", Description: "Criar contexto de arquivos/diretórios (use --mode, --description, --tags)"},
			{Text: "update", Description: "Atualizar contexto existente (use --mode, --description, --tags)"},
			{Text: "attach", Description: "Anexar contexto existente à sessão atual (use --priority, --chunk, --chunks)"},
			{Text: "detach", Description: "Desanexar contexto da sessão atual"},
			{Text: "list", Description: "Listar todos os contextos salvos"},
			{Text: "show", Description: "Ver detalhes completos de um contexto específico"},
			{Text: "inspect", Description: "Análise estatística profunda de um contexto (use --chunk N para chunk específico)"},
			{Text: "delete", Description: "Deletar contexto permanentemente (pede confirmação)"},
			{Text: "merge", Description: "Mesclar múltiplos contextos em um novo"},
			{Text: "attached", Description: "Ver quais contextos estão anexados à sessão"},
			{Text: "export", Description: "Exportar contexto para arquivo JSON"},
			{Text: "import", Description: "Importar contexto de arquivo JSON"},
			{Text: "metrics", Description: "Ver estatísticas de uso de contextos"},
			{Text: "help", Description: "Ajuda detalhada sobre o sistema de contextos"},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// A partir daqui, já temos subcomando definido (len(args) >= 2)
	subcommand := args[1]

	// Subcomandos que precisam de nome de contexto como próximo argumento
	needsContextName := map[string]bool{
		"attach": true, "detach": true, "show": true,
		"delete": true, "export": true, "inspect": true,
	}

	if needsContextName[subcommand] {
		// Se ainda não digitou o nome do contexto (ou está digitando)
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return cli.getContextNameSuggestions()
		}

		// Sugestões específicas para /context inspect
		if subcommand == "inspect" && len(args) >= 3 {
			word := d.GetWordBeforeCursor()

			// Se está digitando uma flag
			if strings.HasPrefix(word, "-") {
				return []prompt.Suggest{
					{Text: "--chunk", Description: "Inspecionar chunk específico (ex: --chunk 1)"},
					{Text: "-c", Description: "Atalho para --chunk"},
				}
			}

			// Se o argumento anterior era --chunk ou -c, sugerir números de chunks
			if len(args) >= 4 {
				prevArg := args[len(args)-1]
				if !strings.HasSuffix(line, " ") && len(args) >= 2 {
					prevArg = args[len(args)-2]
				}

				if prevArg == "--chunk" || prevArg == "-c" {
					return cli.getChunkNumberSuggestions(args[2])
				}
			}
		}

		// Se já digitou o nome e é attach, sugerir flags
		if subcommand == "attach" && len(args) >= 3 && strings.HasPrefix(d.GetWordBeforeCursor(), "-") {
			return []prompt.Suggest{
				{Text: "--priority", Description: "Define prioridade (menor = primeiro a ser enviado)"},
				{Text: "-p", Description: "Atalho para --priority"},
				{Text: "--chunk", Description: "Anexar chunk específico (ex: --chunk 1)"},
				{Text: "-c", Description: "Atalho para --chunk"},
				{Text: "--chunks", Description: "Anexar múltiplos chunks (ex: --chunks 1,2,3)"},
				{Text: "-C", Description: "Atalho para --chunks"},
			}
		}

		return []prompt.Suggest{}
	}

	// ===================================================================
	// Autocompletar paths para /context create e /context update
	// ===================================================================
	if subcommand == "create" || subcommand == "update" {
		word := d.GetWordBeforeCursor()

		// Se está digitando uma flag, mostrar flags disponíveis
		if strings.HasPrefix(word, "-") {
			return []prompt.Suggest{
				{Text: "--mode", Description: "Modo de processamento: full, summary, chunked, smart"},
				{Text: "-m", Description: "Atalho para --mode"},
				{Text: "--description", Description: "Descrição textual do contexto"},
				{Text: "--desc", Description: "Atalho para --description"},
				{Text: "-d", Description: "Atalho para --description"},
				{Text: "--tags", Description: "Tags separadas por vírgula (ex: api,golang)"},
				{Text: "-t", Description: "Atalho para --tags"},
				{Text: "--force", Description: "Sobrescreve se já existir (apenas create)"},
				{Text: "-f", Description: "Atalho para --force (apenas create)"},
			}
		}

		// Detectar se a palavra anterior é uma flag que espera valor
		if len(args) >= 2 {
			prevArg := args[len(args)-1]
			if !strings.HasSuffix(line, " ") && len(args) >= 2 {
				prevArg = args[len(args)-2]
			}

			// Se a flag anterior é --mode ou -m, sugerir modos
			if prevArg == "--mode" || prevArg == "-m" {
				return []prompt.Suggest{
					{Text: "full", Description: "Conteúdo completo dos arquivos"},
					{Text: "summary", Description: "Apenas estrutura de diretórios e metadados"},
					{Text: "chunked", Description: "Divide em chunks gerenciáveis"},
					{Text: "smart", Description: "IA seleciona arquivos relevantes ao prompt"},
				}
			}

			// Se a flag anterior espera texto (description, tags), não autocompletar paths
			if prevArg == "--description" || prevArg == "--desc" || prevArg == "-d" ||
				prevArg == "--tags" || prevArg == "-t" {
				return []prompt.Suggest{} // Deixar usuário digitar livremente
			}
		}

		// Para create: nome do contexto primeiro (se ainda não foi fornecido)
		if subcommand == "create" && len(args) == 2 {
			return []prompt.Suggest{
				{Text: "", Description: "Digite o nome do contexto (ex: meu-projeto)"},
			}
		}

		// Para update: nome do contexto (sugerir existentes)
		if subcommand == "update" && (len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " "))) {
			return cli.getContextNameSuggestions()
		}

		// Agora, autocompletar paths se não for flag e já passou pelos argumentos obrigatórios
		if !strings.HasPrefix(word, "-") {
			// Para create: após o nome (args >= 3)
			// Para update: após o nome e possivelmente flags (args >= 3)
			minArgsForPath := 3
			if subcommand == "update" {
				minArgsForPath = 3 // Nome do contexto é o primeiro argumento após update
			}

			if len(args) >= minArgsForPath {
				return cli.filePathCompleter(word)
			}
		}

		return []prompt.Suggest{}
	}

	// Para merge, precisa de: novo_nome + contextos existentes
	if subcommand == "merge" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return []prompt.Suggest{
				{Text: "", Description: "Digite o nome do novo contexto mesclado"},
			}
		}
		return cli.getContextNameSuggestions()
	}

	// Para export, precisa de: nome_contexto + caminho_arquivo
	if subcommand == "export" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return cli.getContextNameSuggestions()
		}
		if len(args) >= 3 {
			return cli.filePathCompleter(d.GetWordBeforeCursor())
		}
	}

	// Para import, sugerir path de arquivo
	if subcommand == "import" {
		if len(args) >= 2 {
			return cli.filePathCompleter(d.GetWordBeforeCursor())
		}
	}

	return []prompt.Suggest{}
}

// getChunkNumberSuggestions - Sugestões de números de chunks para um contexto
func (cli *ChatCLI) getChunkNumberSuggestions(contextName string) []prompt.Suggest {
	// Buscar o contexto pelo nome
	ctx, err := cli.contextHandler.GetManager().GetContextByName(contextName)
	if err != nil {
		return nil
	}

	// Se não for chunked, retornar vazio
	if !ctx.IsChunked || len(ctx.Chunks) == 0 {
		return []prompt.Suggest{
			{Text: "", Description: "⚠️  Este contexto não está dividido em chunks"},
		}
	}

	// Criar sugestões para cada chunk
	suggestions := make([]prompt.Suggest, 0, len(ctx.Chunks))

	for _, chunk := range ctx.Chunks {
		suggestions = append(suggestions, prompt.Suggest{
			Text: fmt.Sprintf("%d", chunk.Index),
			Description: fmt.Sprintf("Chunk %d/%d: %s (%d arquivos, %.2f KB)",
				chunk.Index,
				chunk.TotalChunks,
				chunk.Description,
				len(chunk.Files),
				float64(chunk.TotalSize)/1024),
		})
	}

	return suggestions
}

// getContextNameSuggestions - Sugestões de nomes de contextos existentes com descrições ricas
func (cli *ChatCLI) getContextNameSuggestions() []prompt.Suggest {
	contexts, err := cli.contextHandler.GetManager().ListContexts(nil)
	if err != nil {
		return nil
	}

	suggestions := make([]prompt.Suggest, 0, len(contexts))
	for _, ctx := range contexts {
		// Criar descrição rica com informações úteis
		var descParts []string

		// Adicionar modo
		descParts = append(descParts, fmt.Sprintf("modo:%s", ctx.Mode))

		// Adicionar contagem de arquivos ou chunks
		if ctx.IsChunked {
			descParts = append(descParts, fmt.Sprintf("%d chunks", len(ctx.Chunks)))
		} else {
			descParts = append(descParts, fmt.Sprintf("%d arquivos", ctx.FileCount))
		}

		// Adicionar tamanho
		sizeMB := float64(ctx.TotalSize) / 1024 / 1024
		if sizeMB < 1 {
			descParts = append(descParts, fmt.Sprintf("%.0f KB", float64(ctx.TotalSize)/1024))
		} else {
			descParts = append(descParts, fmt.Sprintf("%.1f MB", sizeMB))
		}

		// Adicionar tags se houver
		if len(ctx.Tags) > 0 {
			descParts = append(descParts, fmt.Sprintf("tags:%s", strings.Join(ctx.Tags, ",")))
		}

		desc := strings.Join(descParts, " | ")
		if ctx.Description != "" {
			desc = ctx.Description + " — " + desc
		}

		// ═══════════════════════════════════════════════════════════════
		// Adicionar indicador visual para contextos chunked
		// ═══════════════════════════════════════════════════════════════
		icon := "📄"
		if ctx.IsChunked {
			icon = "🧩"
		}

		suggestions = append(suggestions, prompt.Suggest{
			Text:        ctx.Name,
			Description: fmt.Sprintf("%s %s", icon, desc),
		})
	}

	return suggestions
}

// getSessionSuggestions - Sugestões para /session
func (cli *ChatCLI) getSessionSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Se só digitou "/session" (sem espaço ou com espaço mas sem subcomando ainda)
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/session", Description: "Gerencia as sessões (new, save, list, load, delete)"},
		}
	}

	// Se digitou "/session " (com espaço) mas ainda não completou o subcomando
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "new", Description: "Criar nova sessão (limpa histórico atual)"},
			{Text: "save", Description: "Salvar sessão atual com um nome"},
			{Text: "load", Description: "Carregar sessão salva anteriormente"},
			{Text: "list", Description: "Listar todas as sessões salvas"},
			{Text: "delete", Description: "Deletar uma sessão salva"},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// A partir daqui, já temos subcomando definido
	subcommand := args[1]

	// Subcomandos que precisam de nome de sessão
	needsSessionName := map[string]bool{
		"load": true, "delete": true,
	}

	if needsSessionName[subcommand] {
		// Se ainda não digitou o nome (ou está digitando)
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return cli.getSessionNameSuggestions()
		}
		// Já tem nome, não sugerir mais nada
		return []prompt.Suggest{}
	}

	// Para save, deixar usuário digitar nome livremente
	if subcommand == "save" {
		return []prompt.Suggest{}
	}

	// Para new e list, não precisam de argumentos
	return []prompt.Suggest{}
}

// getSessionNameSuggestions - Sugestões de nomes de sessões existentes
func (cli *ChatCLI) getSessionNameSuggestions() []prompt.Suggest {
	sessions, err := cli.sessionManager.ListSessions()
	if err != nil {
		return nil
	}

	suggestions := make([]prompt.Suggest, 0, len(sessions))
	for _, session := range sessions {
		suggestions = append(suggestions, prompt.Suggest{
			Text:        session,
			Description: "Sessão salva",
		})
	}

	return suggestions
}

func (cli *ChatCLI) getPluginSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Sugerir subcomandos
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "list", Description: "Lista todos os plugins instalados."},
			{Text: "install", Description: "Instala um novo plugin a partir de um repositório Git."},
			{Text: "reload", Description: "Força o recarregamento de todos os plugins instalados."},
			{Text: "show", Description: "Mostra detalhes de um plugin específico."},
			{Text: "inspect", Description: "Mostra informações de depuração de um plugin."},
			{Text: "uninstall", Description: "Remove um plugin instalado."},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	subcommand := args[1]
	// Sugerir nomes de plugins para subcomandos que precisam de um nome
	if subcommand == "show" || subcommand == "inspect" || subcommand == "uninstall" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return cli.getPluginNameSuggestions(d.GetWordBeforeCursor())
		}
	}

	return []prompt.Suggest{}
}

func (cli *ChatCLI) getPluginNameSuggestions(prefix string) []prompt.Suggest {
	if cli.pluginManager == nil {
		return nil
	}
	plugins := cli.pluginManager.GetPlugins()
	suggestions := make([]prompt.Suggest, 0, len(plugins))
	for _, p := range plugins {
		// Remove o '@' para a sugestão, pois é mais fácil de digitar
		nameWithoutAt := strings.TrimPrefix(p.Name(), "@")
		suggestions = append(suggestions, prompt.Suggest{
			Text:        nameWithoutAt,
			Description: p.Description(),
		})
	}
	return prompt.FilterHasPrefix(suggestions, prefix, true)
}

func (cli *ChatCLI) getAgentSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Se estamos apenas digitando /agent, sugerir subcomandos
	if len(args) <= 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{}
	}

	// Se já temos /agent e um espaço, sugerir subcomandos
	if len(args) == 1 && strings.HasSuffix(line, " ") {
		suggestions := []prompt.Suggest{
			{Text: "list", Description: "Lista agentes disponíveis"},
			{Text: "load", Description: "Carrega um agente específico"},
			{Text: "attach", Description: "Adicionar múltiplo agente a sessão existente"},
			{Text: "detach", Description: "Desanexar agente da sessão atual"},
			{Text: "skills", Description: "Lista skills disponíveis"},
			{Text: "show", Description: "Mostra o agente ativo"},
			{Text: "status", Description: "Mostra os agente anexados, Alias{attached ou list-attached}"},
			{Text: "off", Description: "Desativa o agente atual"},
			{Text: "help", Description: "Ajuda do comando /agent"},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// Se estamos digitando o subcomando
	if len(args) == 2 && !strings.HasSuffix(line, " ") {
		suggestions := []prompt.Suggest{
			{Text: "list", Description: "Lista agentes disponíveis"},
			{Text: "load", Description: "Carrega um agente específico"},
			{Text: "attach", Description: "Adicionar múltiplo agente a sessão existente"},
			{Text: "detach", Description: "Desanexar agente da sessão atual"},
			{Text: "skills", Description: "Lista skills disponíveis"},
			{Text: "show", Description: "Mostra o agente ativo"},
			{Text: "status", Description: "Mostra os agente anexados, Alias{attached ou list-attached}"},
			{Text: "off", Description: "Desativa o agente atual"},
			{Text: "help", Description: "Ajuda do comando /agent"},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// Se o subcomando é 'load', 'attach' ou 'detach', sugerir nomes de agentes
	if len(args) >= 2 && (args[1] == "load" || args[1] == "attach" || args[1] == "detach") {
		if cli.personaHandler == nil {
			return []prompt.Suggest{}
		}

		agents, err := cli.personaHandler.GetManager().ListAgents()
		if err != nil {
			return []prompt.Suggest{}
		}

		suggestions := make([]prompt.Suggest, 0, len(agents))
		for _, a := range agents {
			suggestions = append(suggestions, prompt.Suggest{
				Text:        a.Name,
				Description: a.Description,
			})
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	if len(args) >= 2 && (args[1] == "show") {
		return []prompt.Suggest{
			{Text: "--full", Description: "Mostra detalhes completos do agente"},
		}
	}

	return []prompt.Suggest{}
}

func (cli *ChatCLI) getSkillSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Just typed "/skill" without space
	if len(args) <= 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{}
	}

	// Suggest subcommands
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "search", Description: "Search for skills across registries"},
			{Text: "install", Description: "Install a skill from a registry"},
			{Text: "uninstall", Description: "Remove an installed skill"},
			{Text: "list", Description: "List installed skills"},
			{Text: "info", Description: "Show skill metadata from registry"},
			{Text: "registries", Description: "Show configured registries"},
			{Text: "help", Description: "Show skill command help"},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// For "uninstall", suggest installed skill names
	if len(args) >= 2 && (args[1] == "uninstall" || args[1] == "remove") {
		if cli.skillHandler != nil && cli.skillHandler.registryMgr != nil {
			installed, err := cli.skillHandler.registryMgr.ListInstalled()
			if err == nil {
				suggestions := make([]prompt.Suggest, 0, len(installed))
				for _, s := range installed {
					desc := s.Description
					if desc == "" {
						desc = s.Source
					}
					suggestions = append(suggestions, prompt.Suggest{
						Text:        s.Name,
						Description: desc,
					})
				}
				return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
			}
		}
	}

	return []prompt.Suggest{}
}

func (cli *ChatCLI) getAuthSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Just typed "/auth" without space
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/auth", Description: "Gerencia credenciais OAuth (status, login, logout)"},
		}
	}

	// "/auth " — suggest subcommands
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "status", Description: "Exibir status de autenticação de todos os provedores"},
			{Text: "login", Description: "Autenticar com um provedor (anthropic, openai-codex ou github-copilot)"},
			{Text: "logout", Description: "Remover credenciais de um provedor (anthropic, openai-codex ou github-copilot)"},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// "/auth login " or "/auth logout " — suggest providers
	if len(args) >= 2 {
		sub := strings.ToLower(args[1])
		if sub == "login" || sub == "logout" {
			if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
				suggestions := []prompt.Suggest{
					{Text: "anthropic", Description: "Anthropic (Claude)"},
					{Text: "openai-codex", Description: "OpenAI (GPT Plus / Codex)"},
					{Text: "github-copilot", Description: "GitHub Copilot"},
				}
				return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
			}
		}
	}

	return []prompt.Suggest{}
}
