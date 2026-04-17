package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/plugins"
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

	if strings.HasPrefix(lineBeforeCursor, "/switch") {
		return cli.getSwitchSuggestions(d)
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

	if strings.HasPrefix(lineBeforeCursor, "/mcp") {
		return cli.getMCPSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/hooks ") {
		return cli.getHooksSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/worktree ") {
		return cli.getWorktreeSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/channel ") {
		return cli.getChannelSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/websearch") {
		return cli.getWebSearchSuggestions(d)
	}

	if strings.HasPrefix(lineBeforeCursor, "/config") ||
		strings.HasPrefix(lineBeforeCursor, "/status") ||
		strings.HasPrefix(lineBeforeCursor, "/settings") {
		return cli.getConfigSuggestions(d)
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
			suggestions := cli.GetInternalCommands()
			// Merge user-invocable skills so the user can discover and run
			// them by just typing "/" — names that collide with a built-in
			// are not added because tryInvokeUserSkill would refuse them
			// anyway (reservedSlashCommands guard).
			if skillSuggestions := cli.getUserInvocableSkillSuggestions(); len(skillSuggestions) > 0 {
				existing := make(map[string]bool, len(suggestions))
				for _, s := range suggestions {
					existing[s.Text] = true
				}
				for _, s := range skillSuggestions {
					if !existing[s.Text] {
						suggestions = append(suggestions, s)
					}
				}
			}
			return prompt.FilterHasPrefix(suggestions, wordBeforeCursor, true)
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
						desc = i18n.T("complete.generic.flag_mode")
					} else if flag == "--model" {
						desc = i18n.T("complete.generic.flag_model")
					} else if flag == "--max-tokens" {
						desc = i18n.T("complete.generic.flag_max_tokens")
					} else if flag == "--agent-id" {
						desc = i18n.T("complete.generic.flag_agent_id_stackspot")
					} else if flag == "--realm" {
						desc = i18n.T("complete.generic.flag_realm_stackspot")
					} else if flag == "-i" {
						desc = i18n.T("complete.generic.flag_i_interactive")
					} else if flag == "--ai" {
						desc = i18n.T("complete.generic.flag_ai")
					} else {
						// 2. Se não houver descrição personalizada, criar uma genérica
						desc = fmt.Sprintf(i18n.T("complete.generic.option_for"), command)
						if len(values) > 0 {
							desc += " " + fmt.Sprintf(i18n.T("complete.generic.values_suffix"), strings.Join(extractTexts(values), ", "))
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
		{Text: "/exit", Description: i18n.T("complete.root.exit")},
		{Text: "/quit", Description: i18n.T("complete.root.quit")},
		{Text: "/switch", Description: i18n.T("complete.root.switch")},
		{Text: "/help", Description: i18n.T("complete.root.help")},
		{Text: "/reload", Description: i18n.T("complete.root.reload")},
		{Text: "/config", Description: i18n.T("complete.config.root_desc")},
		{Text: "/status", Description: i18n.T("complete.config.status_alias")},
		{Text: "/agent", Description: i18n.T("complete.root.agent")},
		{Text: "/coder", Description: i18n.T("complete.root.coder")},
		{Text: "/run", Description: i18n.T("complete.root.run")},
		{Text: "/newsession", Description: i18n.T("complete.root.newsession")},
		{Text: "/version", Description: i18n.T("complete.root.version")},
		{Text: "/nextchunk", Description: i18n.T("complete.root.nextchunk")},
		{Text: "/retry", Description: i18n.T("complete.root.retry")},
		{Text: "/retryall", Description: i18n.T("complete.root.retryall")},
		{Text: "/skipchunk", Description: i18n.T("complete.root.skipchunk")},
		{Text: "/session", Description: i18n.T("complete.root.session")},
		{Text: "/context", Description: i18n.T("complete.root.context")},
		{Text: "/plugin", Description: i18n.T("complete.root.plugin")},
		{Text: "/skill", Description: i18n.T("complete.root.skill")},
		{Text: "/clear", Description: i18n.T("complete.root.clear")},
		{Text: "/auth", Description: i18n.T("complete.root.auth")},
		{Text: "/connect", Description: i18n.T("complete.root.connect")},
		{Text: "/disconnect", Description: i18n.T("complete.root.disconnect")},
		{Text: "/watch", Description: i18n.T("complete.root.watch")},
		{Text: "/metrics", Description: i18n.T("complete.root.metrics")},
		{Text: "/mcp", Description: i18n.T("complete.root.mcp")},
		{Text: "/hooks", Description: i18n.T("complete.root.hooks")},
		{Text: "/cost", Description: i18n.T("complete.root.cost")},
		{Text: "/worktree", Description: i18n.T("complete.root.worktree")},
		{Text: "/channel", Description: i18n.T("complete.root.channel")},
		{Text: "/compact", Description: i18n.T("complete.root.compact")},
		{Text: "/rewind", Description: i18n.T("complete.root.rewind")},
		{Text: "/memory", Description: i18n.T("complete.root.memory")},
		{Text: "/websearch", Description: i18n.T("complete.websearch.root_desc")},
	}
}

// getUserInvocableSkillSuggestions returns completer entries for every
// installed skill that has `user-invocable: true`. The description is taken
// from the skill's `description:` frontmatter, with the `argument-hint:` (if
// any) appended so the user can see the expected arg shape inline.
//
// These entries are merged into the top-level "/" completion list so the
// user can type "/" and immediately see available skills alongside built-ins.
func (cli *ChatCLI) getUserInvocableSkillSuggestions() []prompt.Suggest {
	if cli.personaHandler == nil {
		return nil
	}
	mgr := cli.personaHandler.GetManager()
	if mgr == nil {
		return nil
	}
	skills := mgr.ListAllSkills()
	if len(skills) == 0 {
		return nil
	}
	var out []prompt.Suggest
	for _, s := range skills {
		if !s.UserInvocable {
			continue
		}
		desc := s.Description
		if s.ArgumentHint != "" {
			if desc != "" {
				desc = desc + "  " + s.ArgumentHint
			} else {
				desc = s.ArgumentHint
			}
		}
		if desc == "" {
			desc = i18n.T("complete.skill.user_invocable_fallback")
		}
		out = append(out, prompt.Suggest{
			Text:        "/" + s.Name,
			Description: desc,
		})
	}
	return out
}

// getConnectSuggestions returns autocomplete suggestions for /connect flags.
func (cli *ChatCLI) getConnectSuggestions(d prompt.Document) []prompt.Suggest {
	wordBeforeCursor := d.GetWordBeforeCursor()

	if strings.HasPrefix(wordBeforeCursor, "-") {
		flags := []prompt.Suggest{
			{Text: "--token", Description: i18n.T("complete.connect.token")},
			{Text: "--provider", Description: i18n.T("complete.connect.provider")},
			{Text: "--model", Description: i18n.T("complete.connect.model")},
			{Text: "--llm-key", Description: i18n.T("complete.connect.llm_key")},
			{Text: "--use-local-auth", Description: i18n.T("complete.connect.use_local_auth")},
			{Text: "--tls", Description: i18n.T("complete.connect.tls")},
			{Text: "--ca-cert", Description: i18n.T("complete.connect.ca_cert")},
			{Text: "--client-id", Description: i18n.T("complete.connect.client_id")},
			{Text: "--client-key", Description: i18n.T("complete.connect.client_key")},
			{Text: "--realm", Description: i18n.T("complete.connect.realm")},
			{Text: "--agent-id", Description: i18n.T("complete.connect.agent_id")},
			{Text: "--ollama-url", Description: i18n.T("complete.connect.ollama_url")},
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
			{Text: "--deployment", Description: i18n.T("complete.watch.flag_deployment")},
			{Text: "--namespace", Description: i18n.T("complete.watch.flag_namespace")},
			{Text: "--interval", Description: i18n.T("complete.watch.flag_interval")},
			{Text: "--window", Description: i18n.T("complete.watch.flag_window")},
			{Text: "--max-log-lines", Description: i18n.T("complete.watch.flag_max_log_lines")},
			{Text: "--kubeconfig", Description: i18n.T("complete.watch.flag_kubeconfig")},
		}
		return prompt.FilterHasPrefix(flags, wordBeforeCursor, true)
	}

	// Suggest subcommands
	subcommands := []prompt.Suggest{
		{Text: "start", Description: i18n.T("complete.watch.sub_start")},
		{Text: "stop", Description: i18n.T("complete.watch.sub_stop")},
		{Text: "status", Description: i18n.T("complete.watch.sub_status")},
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
			{Text: "/context", Description: i18n.T("complete.context.root_desc")},
		}
	}

	// Se digitou "/context " (com espaço) mas ainda não completou o subcomando
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "create", Description: i18n.T("complete.context.sub_create")},
			{Text: "update", Description: i18n.T("complete.context.sub_update")},
			{Text: "attach", Description: i18n.T("complete.context.sub_attach")},
			{Text: "detach", Description: i18n.T("complete.context.sub_detach")},
			{Text: "list", Description: i18n.T("complete.context.sub_list")},
			{Text: "show", Description: i18n.T("complete.context.sub_show")},
			{Text: "inspect", Description: i18n.T("complete.context.sub_inspect")},
			{Text: "delete", Description: i18n.T("complete.context.sub_delete")},
			{Text: "merge", Description: i18n.T("complete.context.sub_merge")},
			{Text: "attached", Description: i18n.T("complete.context.sub_attached")},
			{Text: "export", Description: i18n.T("complete.context.sub_export")},
			{Text: "import", Description: i18n.T("complete.context.sub_import")},
			{Text: "metrics", Description: i18n.T("complete.context.sub_metrics")},
			{Text: "help", Description: i18n.T("complete.context.sub_help")},
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
					{Text: "--chunk", Description: i18n.T("complete.context.flag_chunk_inspect")},
					{Text: "-c", Description: i18n.T("complete.context.flag_chunk_short")},
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
				{Text: "--priority", Description: i18n.T("complete.context.flag_priority")},
				{Text: "-p", Description: i18n.T("complete.context.flag_priority_short")},
				{Text: "--chunk", Description: i18n.T("complete.context.flag_chunk_attach")},
				{Text: "-c", Description: i18n.T("complete.context.flag_chunk_short")},
				{Text: "--chunks", Description: i18n.T("complete.context.flag_chunks")},
				{Text: "-C", Description: i18n.T("complete.context.flag_chunks_short")},
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
				{Text: "--mode", Description: i18n.T("complete.context.flag_mode")},
				{Text: "-m", Description: i18n.T("complete.context.flag_mode_short")},
				{Text: "--description", Description: i18n.T("complete.context.flag_description")},
				{Text: "--desc", Description: i18n.T("complete.context.flag_desc_short")},
				{Text: "-d", Description: i18n.T("complete.context.flag_d_short")},
				{Text: "--tags", Description: i18n.T("complete.context.flag_tags")},
				{Text: "-t", Description: i18n.T("complete.context.flag_tags_short")},
				{Text: "--force", Description: i18n.T("complete.context.flag_force")},
				{Text: "-f", Description: i18n.T("complete.context.flag_force_short")},
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
					{Text: "full", Description: i18n.T("complete.context.mode_full")},
					{Text: "summary", Description: i18n.T("complete.context.mode_summary")},
					{Text: "chunked", Description: i18n.T("complete.context.mode_chunked")},
					{Text: "smart", Description: i18n.T("complete.context.mode_smart")},
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
				{Text: "", Description: i18n.T("complete.context.prompt_name")},
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
				{Text: "", Description: i18n.T("complete.context.prompt_merge_name")},
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
			{Text: "", Description: i18n.T("complete.context.not_chunked_warning")},
		}
	}

	// Criar sugestões para cada chunk
	suggestions := make([]prompt.Suggest, 0, len(ctx.Chunks))

	for _, chunk := range ctx.Chunks {
		suggestions = append(suggestions, prompt.Suggest{
			Text: fmt.Sprintf("%d", chunk.Index),
			Description: fmt.Sprintf(i18n.T("complete.context.chunk_desc_fmt"),
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
		descParts = append(descParts, fmt.Sprintf(i18n.T("complete.context.name_mode_fmt"), ctx.Mode))

		// Adicionar contagem de arquivos ou chunks
		if ctx.IsChunked {
			descParts = append(descParts, fmt.Sprintf(i18n.T("complete.context.name_chunks_fmt"), len(ctx.Chunks)))
		} else {
			descParts = append(descParts, fmt.Sprintf(i18n.T("complete.context.name_files_fmt"), ctx.FileCount))
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
			descParts = append(descParts, fmt.Sprintf(i18n.T("complete.context.name_tags_fmt"), strings.Join(ctx.Tags, ",")))
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
			{Text: "/session", Description: i18n.T("complete.session.root_desc")},
		}
	}

	// Se digitou "/session " (com espaço) mas ainda não completou o subcomando
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "new", Description: i18n.T("complete.session.sub_new")},
			{Text: "save", Description: i18n.T("complete.session.sub_save")},
			{Text: "load", Description: i18n.T("complete.session.sub_load")},
			{Text: "list", Description: i18n.T("complete.session.sub_list")},
			{Text: "delete", Description: i18n.T("complete.session.sub_delete")},
			{Text: "fork", Description: i18n.T("complete.session.sub_fork")},
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
			Description: i18n.T("complete.session.saved_session"),
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
			{Text: "list", Description: i18n.T("complete.plugin.sub_list")},
			{Text: "install", Description: i18n.T("complete.plugin.sub_install")},
			{Text: "reload", Description: i18n.T("complete.plugin.sub_reload")},
			{Text: "show", Description: i18n.T("complete.plugin.sub_show")},
			{Text: "inspect", Description: i18n.T("complete.plugin.sub_inspect")},
			{Text: "uninstall", Description: i18n.T("complete.plugin.sub_uninstall")},
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
			{Text: "list", Description: i18n.T("complete.agent.sub_list")},
			{Text: "load", Description: i18n.T("complete.agent.sub_load")},
			{Text: "attach", Description: i18n.T("complete.agent.sub_attach")},
			{Text: "detach", Description: i18n.T("complete.agent.sub_detach")},
			{Text: "skills", Description: i18n.T("complete.agent.sub_skills")},
			{Text: "show", Description: i18n.T("complete.agent.sub_show")},
			{Text: "status", Description: i18n.T("complete.agent.sub_status")},
			{Text: "off", Description: i18n.T("complete.agent.sub_off")},
			{Text: "help", Description: i18n.T("complete.agent.sub_help")},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// Se estamos digitando o subcomando
	if len(args) == 2 && !strings.HasSuffix(line, " ") {
		suggestions := []prompt.Suggest{
			{Text: "list", Description: i18n.T("complete.agent.sub_list")},
			{Text: "load", Description: i18n.T("complete.agent.sub_load")},
			{Text: "attach", Description: i18n.T("complete.agent.sub_attach")},
			{Text: "detach", Description: i18n.T("complete.agent.sub_detach")},
			{Text: "skills", Description: i18n.T("complete.agent.sub_skills")},
			{Text: "show", Description: i18n.T("complete.agent.sub_show")},
			{Text: "status", Description: i18n.T("complete.agent.sub_status")},
			{Text: "off", Description: i18n.T("complete.agent.sub_off")},
			{Text: "help", Description: i18n.T("complete.agent.sub_help")},
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
			{Text: "--full", Description: i18n.T("complete.agent.flag_full")},
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
			{Text: "search", Description: i18n.T("complete.skill.sub_search")},
			{Text: "install", Description: i18n.T("complete.skill.sub_install")},
			{Text: "uninstall", Description: i18n.T("complete.skill.sub_uninstall")},
			{Text: "list", Description: i18n.T("complete.skill.sub_list")},
			{Text: "info", Description: i18n.T("complete.skill.sub_info")},
			{Text: "registries", Description: i18n.T("complete.skill.sub_registries")},
			{Text: "registry", Description: i18n.T("complete.skill.sub_registry")},
			{Text: "prefer", Description: i18n.T("complete.skill.sub_prefer")},
			{Text: "help", Description: i18n.T("complete.skill.sub_help")},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	sub := ""
	if len(args) >= 2 {
		sub = strings.ToLower(args[1])
	}

	// For "uninstall", suggest installed skill names
	if sub == "uninstall" || sub == "remove" {
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

	// For "install" and "info": suggest --from after skill name, then registry names
	if sub == "install" || sub == "info" {
		if cli.skillHandler != nil && cli.skillHandler.registryMgr != nil {
			// After "--from" or "-f", suggest registry names
			lastArg := args[len(args)-1]
			prevArg := ""
			if len(args) >= 2 {
				prevArg = args[len(args)-1]
				if strings.HasSuffix(line, " ") {
					prevArg = args[len(args)-1]
				} else if len(args) >= 3 {
					prevArg = args[len(args)-2]
				}
			}
			if strings.HasSuffix(line, " ") && (lastArg == "--from" || lastArg == "-f") {
				return cli.getRegistryNameSuggestions(d)
			}
			if !strings.HasSuffix(line, " ") && (prevArg == "--from" || prevArg == "-f") {
				return cli.getRegistryNameSuggestions(d)
			}

			// After the skill name (3+ args), suggest --from
			if len(args) >= 3 && strings.HasSuffix(line, " ") {
				return prompt.FilterHasPrefix([]prompt.Suggest{
					{Text: "--from", Description: i18n.T("complete.skill.flag_from")},
				}, d.GetWordBeforeCursor(), true)
			}
			if len(args) >= 4 && !strings.HasSuffix(line, " ") {
				return prompt.FilterHasPrefix([]prompt.Suggest{
					{Text: "--from", Description: i18n.T("complete.skill.flag_from")},
				}, d.GetWordBeforeCursor(), true)
			}
		}
	}

	// For "registry": suggest enable/disable, then registry names
	if sub == "registry" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "enable", Description: i18n.T("complete.skill.sub_registry_enable")},
				{Text: "disable", Description: i18n.T("complete.skill.sub_registry_disable")},
			}, d.GetWordBeforeCursor(), true)
		}
		if len(args) >= 3 {
			return cli.getRegistryNameSuggestions(d)
		}
	}

	// For "prefer": suggest installed skill base names, then sources
	if sub == "prefer" {
		if cli.skillHandler != nil && cli.skillHandler.registryMgr != nil {
			if len(args) >= 4 || (len(args) == 4 && !strings.HasSuffix(line, " ")) {
				// After skill name, suggest sources + --reset
				suggestions := []prompt.Suggest{
					{Text: "--reset", Description: i18n.T("complete.skill.flag_reset")},
				}
				suggestions = append(suggestions, cli.getRegistryNameSuggestions(d)...)
				suggestions = append(suggestions, prompt.Suggest{Text: "local", Description: i18n.T("complete.skill.prefer_local")})
				return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
			}
		}
	}

	return []prompt.Suggest{}
}

// getRegistryNameSuggestions returns autocomplete suggestions with registry names.
func (cli *ChatCLI) getRegistryNameSuggestions(d prompt.Document) []prompt.Suggest {
	if cli.skillHandler == nil || cli.skillHandler.registryMgr == nil {
		return nil
	}
	regs := cli.skillHandler.registryMgr.GetRegistries()
	suggestions := make([]prompt.Suggest, 0, len(regs))
	for _, r := range regs {
		status := "enabled"
		if !r.Enabled {
			status = "disabled"
		}
		suggestions = append(suggestions, prompt.Suggest{
			Text:        r.Name,
			Description: status,
		})
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}

// getSwitchSuggestions returns autocomplete suggestions for /switch command.
func (cli *ChatCLI) getSwitchSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	wordBeforeCursor := d.GetWordBeforeCursor()

	// Just typed "/switch" without space
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/switch", Description: i18n.T("complete.switch.root_desc")},
		}
	}

	// Detect if previous word is --model → suggest model names
	if len(args) >= 2 {
		prevWord := args[len(args)-1]
		if !strings.HasSuffix(line, " ") && len(args) > 1 {
			prevWord = args[len(args)-2]
		}

		if prevWord == "--model" {
			models := cli.getCachedModels()
			suggestions := make([]prompt.Suggest, 0, len(models))
			for _, m := range models {
				desc := m.DisplayName
				if desc == m.ID || desc == "" {
					desc = cli.Provider
				}
				// Indicate source: [API] or [catalog]
				if m.Source == "api" {
					desc += " [API]"
				} else {
					desc += " [catalog]"
				}
				suggestions = append(suggestions, prompt.Suggest{
					Text:        m.ID,
					Description: desc,
				})
			}
			return prompt.FilterHasPrefix(suggestions, wordBeforeCursor, true)
		}
	}

	// Suggest flags
	if strings.HasPrefix(wordBeforeCursor, "-") {
		flags := []prompt.Suggest{
			{Text: "--model", Description: i18n.T("complete.switch.flag_model")},
			{Text: "--max-tokens", Description: i18n.T("complete.switch.flag_max_tokens")},
		}
		if cli.Provider == "STACKSPOT" {
			flags = append(flags,
				prompt.Suggest{Text: "--realm", Description: i18n.T("complete.switch.flag_realm")},
				prompt.Suggest{Text: "--agent-id", Description: i18n.T("complete.switch.flag_agent_id")},
			)
		}
		return prompt.FilterHasPrefix(flags, wordBeforeCursor, true)
	}

	// After "/switch " with space, suggest flags
	if len(args) == 1 && strings.HasSuffix(line, " ") {
		flags := []prompt.Suggest{
			{Text: "--model", Description: i18n.T("complete.switch.flag_model")},
			{Text: "--max-tokens", Description: i18n.T("complete.switch.flag_max_tokens")},
		}
		if cli.Provider == "STACKSPOT" {
			flags = append(flags,
				prompt.Suggest{Text: "--realm", Description: i18n.T("complete.switch.flag_realm")},
				prompt.Suggest{Text: "--agent-id", Description: i18n.T("complete.switch.flag_agent_id")},
			)
		}
		return flags
	}

	return []prompt.Suggest{}
}

func (cli *ChatCLI) getAuthSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Just typed "/auth" without space
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/auth", Description: i18n.T("complete.auth.root_desc")},
		}
	}

	// "/auth " — suggest subcommands
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		suggestions := []prompt.Suggest{
			{Text: "status", Description: i18n.T("complete.auth.sub_status")},
			{Text: "login", Description: i18n.T("complete.auth.sub_login")},
			{Text: "logout", Description: i18n.T("complete.auth.sub_logout")},
		}
		return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
	}

	// "/auth login " or "/auth logout " — suggest providers
	if len(args) >= 2 {
		sub := strings.ToLower(args[1])
		if sub == "login" || sub == "logout" {
			if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
				suggestions := []prompt.Suggest{
					{Text: "anthropic", Description: i18n.T("complete.auth.provider_anthropic")},
					{Text: "openai-codex", Description: i18n.T("complete.auth.provider_openai_codex")},
					{Text: "github-copilot", Description: i18n.T("complete.auth.provider_github_copilot")},
					{Text: "github-models", Description: i18n.T("complete.auth.provider_github_models")},
				}
				return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
			}
		}
	}

	return []prompt.Suggest{}
}

// getConfigSuggestions returns autocomplete suggestions for /config, /status,
// and /settings — all three share the same subsection routing.
//
//	/config <TAB>   → section names
func (cli *ChatCLI) getConfigSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// Just "/config" (or alias) with no trailing space
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{}
	}

	// Subsection slot
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		sections := []prompt.Suggest{
			{Text: "all", Description: i18n.T("complete.config.all")},
			{Text: "general", Description: i18n.T("complete.config.general")},
			{Text: "providers", Description: i18n.T("complete.config.providers")},
			{Text: "agent", Description: i18n.T("complete.config.agent")},
			{Text: "resilience", Description: i18n.T("complete.config.resilience")},
			{Text: "session", Description: i18n.T("complete.config.session")},
			{Text: "integrations", Description: i18n.T("complete.config.integrations")},
			{Text: "auth", Description: i18n.T("complete.config.auth")},
			{Text: "security", Description: i18n.T("complete.config.security")},
			{Text: "server", Description: i18n.T("complete.config.server")},
		}
		return prompt.FilterHasPrefix(sections, word, true)
	}

	return []prompt.Suggest{}
}

// getWebSearchSuggestions returns autocomplete suggestions for /websearch.
//
//	/websearch <TAB>                → subcommands
//	/websearch provider <TAB>       → provider names (searxng|duckduckgo|auto)
func (cli *ChatCLI) getWebSearchSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// Just "/websearch" with no trailing space
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/websearch", Description: i18n.T("complete.websearch.root_desc")},
		}
	}

	// Subcommand slot
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "status", Description: i18n.T("complete.websearch.sub_status")},
			{Text: "list", Description: i18n.T("complete.websearch.sub_list")},
			{Text: "provider", Description: i18n.T("complete.websearch.sub_provider")},
			{Text: "reset", Description: i18n.T("complete.websearch.sub_reset")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	sub := strings.ToLower(args[1])

	// /websearch provider <TAB> → provider names
	if sub == "provider" || sub == "set" || sub == "use" {
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			providers := []prompt.Suggest{
				{Text: string(plugins.ProviderSearXNG), Description: i18n.T("complete.websearch.prov_searxng")},
				{Text: string(plugins.ProviderDuckDuckGo), Description: i18n.T("complete.websearch.prov_duckduckgo")},
				{Text: string(plugins.ProviderAuto), Description: i18n.T("complete.websearch.prov_auto")},
			}
			return prompt.FilterHasPrefix(providers, word, true)
		}
	}

	return []prompt.Suggest{}
}
