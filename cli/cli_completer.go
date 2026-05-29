package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/ui/theme"
)

// loadPolicyForCompleter is a thin wrapper around
// coder.NewPolicyManager for the completer path. Isolated so tests
// can replace it with a stub that does not touch disk.
var loadPolicyForCompleter = func(cli *ChatCLI) (*coder.PolicyManager, error) {
	return coder.NewPolicyManager(cli.logger)
}

// slashPrefixRoute pairs a `/cmd` prefix with the suggestion handler that
// powers it. Centralizing the table lets `completer` stay a thin dispatcher
// while new slash commands plug in without growing the function.
type slashPrefixRoute struct {
	prefix  string
	handler func(*ChatCLI, prompt.Document) []prompt.Suggest
}

// slashPrefixRoutes is the ordered routing table consulted by `completer`.
// Order matters when one prefix shadows another (e.g. "/cancel-park" before
// any future "/cancel" prefix); the first matching entry wins.
var slashPrefixRoutes = []slashPrefixRoute{
	{"/context", (*ChatCLI).getContextSuggestions},
	{"/session", (*ChatCLI).getSessionSuggestions},
	{"/hub", (*ChatCLI).getHubSuggestions},
	{"/plugin ", (*ChatCLI).getPluginSuggestions},
	{"/skill", (*ChatCLI).getSkillSuggestions},
	{"/memory", (*ChatCLI).getMemorySuggestions},
	{"/agent", (*ChatCLI).getAgentSuggestions},
	{"/switch", (*ChatCLI).getSwitchSuggestions},
	{"/auth", (*ChatCLI).getAuthSuggestions},
	{"/connect ", (*ChatCLI).getConnectSuggestions},
	{"/watch", (*ChatCLI).getWatchSuggestions},
	{"/mcp", (*ChatCLI).getMCPSuggestions},
	{"/hooks ", (*ChatCLI).getHooksSuggestions},
	{"/worktree ", (*ChatCLI).getWorktreeSuggestions},
	{"/channel ", (*ChatCLI).getChannelSuggestions},
	{"/websearch", (*ChatCLI).getWebSearchSuggestions},
	{"/config", (*ChatCLI).getConfigSuggestions},
	{"/status", (*ChatCLI).getConfigSuggestions},
	{"/settings", (*ChatCLI).getConfigSuggestions},
	{"/thinking", (*ChatCLI).getThinkingSuggestions},
	{"/refine", (*ChatCLI).getRefineSuggestions},
	{"/verify", (*ChatCLI).getVerifySuggestions},
	{"/plan", (*ChatCLI).getPlanSuggestions},
	{"/reflect", (*ChatCLI).getReflectSuggestions},
	{"/schedule", (*ChatCLI).getScheduleSuggestions},
	{"/wait", (*ChatCLI).getWaitSuggestions},
	{"/jobs", (*ChatCLI).getJobsSuggestions},
	{"/parked", (*ChatCLI).getParkedSuggestions},
	{"/cancel-park", func(c *ChatCLI, d prompt.Document) []prompt.Suggest {
		return c.getParkTokenSuggestions("/cancel-park", d)
	}},
	{"/resume", func(c *ChatCLI, d prompt.Document) []prompt.Suggest {
		return c.getParkTokenSuggestions("/resume", d)
	}},
	{"/export", (*ChatCLI).getExportSuggestions},
	{"/gateway", (*ChatCLI).getGatewaySuggestions},
	{"/lsp", (*ChatCLI).getLSPSuggestions},
}

// getLSPSuggestions completes the file path argument of /lsp.
func (cli *ChatCLI) getLSPSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	if len(strings.Fields(line)) <= 1 && !strings.HasSuffix(line, " ") {
		return nil // still typing the command name itself
	}
	return cli.filePathCompleter(d.GetWordBeforeCursor())
}

// getGatewaySuggestions completes the /gateway subcommands.
func (cli *ChatCLI) getGatewaySuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "start", Description: i18n.T("complete.gateway.start")},
			{Text: "stop", Description: i18n.T("complete.gateway.stop")},
			{Text: "status", Description: i18n.T("complete.gateway.status")},
		}, d.GetWordBeforeCursor(), true)
	}
	return nil
}

// getExportSuggestions completes the optional output path of /export.
func (cli *ChatCLI) getExportSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	if len(strings.Fields(line)) <= 1 && !strings.HasSuffix(line, " ") {
		return nil // still typing the command name itself
	}
	return cli.filePathCompleter(d.GetWordBeforeCursor())
}

// flagDescriptionKeys maps well-known flag names to their i18n description
// keys. Falls back to a generic "option for <command>" sentence when absent.
var flagDescriptionKeys = map[string]string{
	"--mode":       "complete.generic.flag_mode",
	"--model":      "complete.generic.flag_model",
	"--max-tokens": "complete.generic.flag_max_tokens",
	"--agent-id":   "complete.generic.flag_agent_id_stackspot",
	"--realm":      "complete.generic.flag_realm_stackspot",
	"-i":           "complete.generic.flag_i_interactive",
	"--ai":         "complete.generic.flag_ai",
}

func (cli *ChatCLI) completer(d prompt.Document) []prompt.Suggest {
	if cli.interactionState == StateSwitchingProvider {
		return cli.providerPickSuggestions(d)
	}

	line := d.TextBeforeCursor()
	word := d.GetWordBeforeCursor()
	args := strings.Fields(line)

	if out, matched := cli.routeSlashPrefix(line, d); matched {
		return out
	}
	if out, matched := cli.completeAtTokenArgs(args, line, word); matched {
		return out
	}
	if out, matched := cli.completeBareSlash(line, word); matched {
		return out
	}
	if strings.HasPrefix(word, "@") {
		return prompt.FilterHasPrefix(cli.GetContextCommands(), word, true)
	}
	if out, matched := cli.completeCommandFlags(args, line, d); matched {
		return out
	}
	return []prompt.Suggest{}
}

// providerPickSuggestions builds the numbered list shown while the user is
// in the StateSwitchingProvider modal.
func (cli *ChatCLI) providerPickSuggestions(d prompt.Document) []prompt.Suggest {
	providers := cli.manager.GetAvailableProviders()
	s := make([]prompt.Suggest, len(providers))
	for i, p := range providers {
		s[i] = prompt.Suggest{Text: strconv.Itoa(i + 1), Description: p}
	}
	return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
}

// routeSlashPrefix walks the routing table and returns the first match.
// The boolean return distinguishes "matched but no suggestions" from "no
// route applies — try the next strategy".
func (cli *ChatCLI) routeSlashPrefix(line string, d prompt.Document) ([]prompt.Suggest, bool) {
	for _, r := range slashPrefixRoutes {
		if strings.HasPrefix(line, r.prefix) {
			return r.handler(cli, d), true
		}
	}
	return nil, false
}

// completeAtTokenArgs handles `@file <path>` and `@command <cmd>` argument
// completion. Returns matched=false when the previous token is unrelated.
func (cli *ChatCLI) completeAtTokenArgs(args []string, line, word string) ([]prompt.Suggest, bool) {
	if len(args) == 0 {
		return nil, false
	}
	previous := previousToken(args, line)
	if previous == "" || strings.HasPrefix(word, "-") {
		return nil, false
	}
	switch previous {
	case "@file":
		return cli.filePathCompleter(word), true
	case "@command":
		out := cli.systemCommandCompleter(word)
		out = append(out, cli.filePathCompleter(word)...)
		return out, true
	}
	return nil, false
}

// previousToken returns the word immediately before the cursor — either the
// last completed token (when the line ends in whitespace) or the
// second-to-last token otherwise.
func previousToken(args []string, line string) string {
	if strings.HasSuffix(line, " ") {
		return args[len(args)-1]
	}
	if len(args) > 1 {
		return args[len(args)-2]
	}
	return ""
}

// completeBareSlash powers the "/" → list of slash commands flow, merging
// user-invocable skills so they surface alongside built-ins.
func (cli *ChatCLI) completeBareSlash(line, word string) ([]prompt.Suggest, bool) {
	if strings.Contains(line, " ") || !strings.HasPrefix(word, "/") {
		return nil, false
	}
	suggestions := cli.GetInternalCommands()
	skillSuggestions := cli.getUserInvocableSkillSuggestions()
	if len(skillSuggestions) > 0 {
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
	return prompt.FilterHasPrefix(suggestions, word, true), true
}

// completeCommandFlags drives the value-then-flag suggestion flow for any
// command whose flag set is declared in CommandFlags.
func (cli *ChatCLI) completeCommandFlags(args []string, line string, d prompt.Document) ([]prompt.Suggest, bool) {
	if len(args) < 2 {
		return nil, false
	}
	command := args[0]
	flagsForCommand, ok := CommandFlags[command]
	if !ok {
		return nil, false
	}
	currWord := d.GetWordBeforeCursor()
	prevWord := previousToken(args, line)

	if values, hasValues := flagsForCommand[prevWord]; hasValues && len(values) > 0 {
		return prompt.FilterHasPrefix(values, currWord, true), true
	}
	if strings.HasPrefix(currWord, "-") {
		return prompt.FilterHasPrefix(buildFlagSuggestions(command, flagsForCommand), currWord, true), true
	}
	return nil, false
}

// buildFlagSuggestions renders the prompt.Suggest list for every flag of a
// given command. Descriptions come from flagDescriptionKeys when available;
// otherwise a generic "option for <command>" line is generated, listing the
// known values for that flag.
func buildFlagSuggestions(command string, flagsForCommand map[string][]prompt.Suggest) []prompt.Suggest {
	out := make([]prompt.Suggest, 0, len(flagsForCommand))
	for flag, values := range flagsForCommand {
		out = append(out, prompt.Suggest{
			Text:        flag,
			Description: describeFlag(command, flag, values),
		})
	}
	return out
}

// describeFlag returns the i18n description for a flag, or a synthesized
// "option for <command>" line listing its known values.
func describeFlag(command, flag string, values []prompt.Suggest) string {
	if key, ok := flagDescriptionKeys[flag]; ok {
		return i18n.T(key)
	}
	desc := fmt.Sprintf(i18n.T("complete.generic.option_for"), command)
	if len(values) > 0 {
		desc += " " + fmt.Sprintf(i18n.T("complete.generic.values_suffix"),
			strings.Join(extractTexts(values), ", "))
	}
	return desc
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
		{Text: "/hub", Description: i18n.T("complete.root.hub")},
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
		{Text: "/ratelimit", Description: i18n.T("complete.root.ratelimit")},
		{Text: "/export", Description: i18n.T("complete.root.export")},
		{Text: "/moa", Description: i18n.T("complete.root.moa")},
		{Text: "/gateway", Description: i18n.T("complete.root.gateway")},
		{Text: "/lsp", Description: i18n.T("complete.root.lsp")},
		{Text: "/thinking", Description: i18n.T("complete.root.thinking")},
		{Text: "/plan", Description: i18n.T("complete.root.plan")},
		{Text: "/refine", Description: i18n.T("complete.root.refine")},
		{Text: "/verify", Description: i18n.T("complete.root.verify")},
		{Text: "/reflect", Description: i18n.T("complete.root.reflect")},
		{Text: "/worktree", Description: i18n.T("complete.root.worktree")},
		{Text: "/channel", Description: i18n.T("complete.root.channel")},
		{Text: "/compact", Description: i18n.T("complete.root.compact")},
		{Text: "/rewind", Description: i18n.T("complete.root.rewind")},
		{Text: "/memory", Description: i18n.T("complete.root.memory")},
		{Text: "/websearch", Description: i18n.T("complete.websearch.root_desc")},
		{Text: "/schedule", Description: i18n.T("help.command.schedule")},
		{Text: "/wait", Description: i18n.T("help.command.wait")},
		{Text: "/jobs", Description: i18n.T("help.command.jobs")},
		{Text: "/parked", Description: i18n.T("help.command.parked")},
		{Text: "/resume", Description: i18n.T("help.command.resume")},
		{Text: "/cancel-park", Description: i18n.T("help.command.cancel_park")},
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
// contextSubcommands is the static list of `/context <sub>` verbs.
func contextSubcommands() []prompt.Suggest {
	return []prompt.Suggest{
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
}

// contextSubcommandsNeedingName is the set of `/context <sub>` verbs whose
// first positional argument is the name of an existing context.
var contextSubcommandsNeedingName = map[string]bool{
	"attach": true, "detach": true, "show": true,
	"delete": true, "export": true, "inspect": true,
}

func (cli *ChatCLI) getContextSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	endsWithSpace := strings.HasSuffix(line, " ")

	if len(args) == 1 && !endsWithSpace {
		return []prompt.Suggest{
			{Text: "/context", Description: i18n.T("complete.context.root_desc")},
		}
	}
	if len(args) == 1 || (len(args) == 2 && !endsWithSpace) {
		return prompt.FilterHasPrefix(contextSubcommands(), d.GetWordBeforeCursor(), true)
	}

	sub := args[1]
	if contextSubcommandsNeedingName[sub] {
		return cli.contextNameOrFlagSuggestions(sub, args, line, d)
	}
	switch sub {
	case "create", "update":
		return cli.contextCreateOrUpdateSuggestions(sub, args, line, d)
	case "merge":
		return cli.contextMergeSuggestions(args, endsWithSpace)
	case "export":
		return cli.contextExportSuggestions(args, endsWithSpace, d)
	case "import":
		return cli.contextImportSuggestions(args, d)
	}
	return []prompt.Suggest{}
}

// contextNameOrFlagSuggestions powers verbs whose first arg is a context
// name, plus the per-verb flag completions (attach has its own flags;
// inspect has --chunk).
func (cli *ChatCLI) contextNameOrFlagSuggestions(
	sub string, args []string, line string, d prompt.Document,
) []prompt.Suggest {
	endsWithSpace := strings.HasSuffix(line, " ")
	wantsName := len(args) == 2 || (len(args) == 3 && !endsWithSpace)
	if wantsName {
		return cli.getContextNameSuggestions()
	}
	if sub == "inspect" && len(args) >= 3 {
		return cli.contextInspectFlagSuggestions(args, line, d)
	}
	if sub == "attach" && len(args) >= 3 && strings.HasPrefix(d.GetWordBeforeCursor(), "-") {
		return contextAttachFlagSuggestions()
	}
	return []prompt.Suggest{}
}

func contextInspectFlags() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "--chunk", Description: i18n.T("complete.context.flag_chunk_inspect")},
		{Text: "-c", Description: i18n.T("complete.context.flag_chunk_short")},
	}
}

func contextAttachFlagSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "--priority", Description: i18n.T("complete.context.flag_priority")},
		{Text: "-p", Description: i18n.T("complete.context.flag_priority_short")},
		{Text: "--chunk", Description: i18n.T("complete.context.flag_chunk_attach")},
		{Text: "-c", Description: i18n.T("complete.context.flag_chunk_short")},
		{Text: "--chunks", Description: i18n.T("complete.context.flag_chunks")},
		{Text: "-C", Description: i18n.T("complete.context.flag_chunks_short")},
	}
}

// contextInspectFlagSuggestions handles `--chunk` flag offerings plus chunk
// number completion when the previous token was `--chunk`/`-c`.
func (cli *ChatCLI) contextInspectFlagSuggestions(
	args []string, line string, d prompt.Document,
) []prompt.Suggest {
	word := d.GetWordBeforeCursor()
	if strings.HasPrefix(word, "-") {
		return contextInspectFlags()
	}
	if len(args) < 4 {
		return nil
	}
	prev := previousToken(args, line)
	if prev == "--chunk" || prev == "-c" {
		return cli.getChunkNumberSuggestions(args[2])
	}
	return nil
}

func contextCreateUpdateFlagSuggestions() []prompt.Suggest {
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

func contextModeValueSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "full", Description: i18n.T("complete.context.mode_full")},
		{Text: "summary", Description: i18n.T("complete.context.mode_summary")},
		{Text: "chunked", Description: i18n.T("complete.context.mode_chunked")},
		{Text: "smart", Description: i18n.T("complete.context.mode_smart")},
	}
}

// contextCreateOrUpdateSuggestions covers the flag-heavy create/update verbs.
func (cli *ChatCLI) contextCreateOrUpdateSuggestions(
	sub string, args []string, line string, d prompt.Document,
) []prompt.Suggest {
	word := d.GetWordBeforeCursor()
	if strings.HasPrefix(word, "-") {
		return contextCreateUpdateFlagSuggestions()
	}
	if out, matched := contextFlagValueSuggestions(args, line); matched {
		return out
	}
	if sub == "create" && len(args) == 2 {
		return []prompt.Suggest{{Text: "", Description: i18n.T("complete.context.prompt_name")}}
	}
	endsWithSpace := strings.HasSuffix(line, " ")
	if sub == "update" && (len(args) == 2 || (len(args) == 3 && !endsWithSpace)) {
		return cli.getContextNameSuggestions()
	}
	if len(args) >= 3 {
		return cli.filePathCompleter(word)
	}
	return []prompt.Suggest{}
}

// contextFlagValueSuggestions returns value suggestions for flags whose
// position is immediately after `--mode`/`-m` (mode list), or signals that
// the previous flag expects free text (description/tags) and the completer
// should bail out silently.
func contextFlagValueSuggestions(args []string, line string) ([]prompt.Suggest, bool) {
	if len(args) < 2 {
		return nil, false
	}
	prev := previousToken(args, line)
	switch prev {
	case "--mode", "-m":
		return contextModeValueSuggestions(), true
	case "--description", "--desc", "-d", "--tags", "-t":
		return []prompt.Suggest{}, true
	}
	return nil, false
}

// contextMergeSuggestions: prompt for the new name first, then existing
// context names to merge.
func (cli *ChatCLI) contextMergeSuggestions(args []string, endsWithSpace bool) []prompt.Suggest {
	if len(args) == 2 || (len(args) == 3 && !endsWithSpace) {
		return []prompt.Suggest{{Text: "", Description: i18n.T("complete.context.prompt_merge_name")}}
	}
	return cli.getContextNameSuggestions()
}

// contextExportSuggestions: existing context name first, then a file path.
func (cli *ChatCLI) contextExportSuggestions(
	args []string, endsWithSpace bool, d prompt.Document,
) []prompt.Suggest {
	if len(args) == 2 || (len(args) == 3 && !endsWithSpace) {
		return cli.getContextNameSuggestions()
	}
	if len(args) >= 3 {
		return cli.filePathCompleter(d.GetWordBeforeCursor())
	}
	return nil
}

// contextImportSuggestions: always a file path once the verb is set.
func (cli *ChatCLI) contextImportSuggestions(
	args []string, d prompt.Document,
) []prompt.Suggest {
	if len(args) >= 2 {
		return cli.filePathCompleter(d.GetWordBeforeCursor())
	}
	return nil
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
			{Text: "search", Description: i18n.T("complete.session.sub_search")},
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

// skillSubcommandSuggestions returns the static catalog of `/skill <sub>`
// suggestions. Kept package-level so its cyclomatic weight does not bleed
// into getSkillSuggestions.
func skillSubcommandSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "search", Description: i18n.T("complete.skill.sub_search")},
		{Text: "install", Description: i18n.T("complete.skill.sub_install")},
		{Text: "uninstall", Description: i18n.T("complete.skill.sub_uninstall")},
		{Text: "list", Description: i18n.T("complete.skill.sub_list")},
		{Text: "info", Description: i18n.T("complete.skill.sub_info")},
		{Text: "registries", Description: i18n.T("complete.skill.sub_registries")},
		{Text: "registry", Description: i18n.T("complete.skill.sub_registry")},
		{Text: "prefer", Description: i18n.T("complete.skill.sub_prefer")},
		{Text: "pin", Description: i18n.T("complete.skill.sub_pin")},
		{Text: "unpin", Description: i18n.T("complete.skill.sub_unpin")},
		{Text: "pinned", Description: i18n.T("complete.skill.sub_pinned")},
		{Text: "help", Description: i18n.T("complete.skill.sub_help")},
	}
}

// skillSubcommandHandler returns the suggestion function for a /skill
// subcommand, or nil when the subcommand has no contextual suggestions.
// Centralizing this dispatch keeps getSkillSuggestions a thin router.
func (cli *ChatCLI) skillSubcommandHandler(sub string) func(prompt.Document) []prompt.Suggest {
	switch sub {
	case "uninstall", "remove":
		return cli.suggestInstalledSkills
	case "install", "info":
		return cli.suggestInstallOrInfoArgs
	case "registry":
		return cli.suggestRegistrySubcommand
	case "pin":
		return cli.suggestPinCandidates
	case "unpin":
		return cli.suggestPinnedNames
	case "prefer":
		return cli.suggestPreferArgs
	}
	return nil
}

func (cli *ChatCLI) getSkillSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	endsWithSpace := strings.HasSuffix(line, " ")

	// "/skill" without a space — wait for one.
	if len(args) <= 1 && !endsWithSpace {
		return []prompt.Suggest{}
	}

	// Show the subcommand catalog when the user is still typing the verb.
	typingSubcommand := len(args) == 1 || (len(args) == 2 && !endsWithSpace)
	if typingSubcommand {
		return prompt.FilterHasPrefix(skillSubcommandSuggestions(), d.GetWordBeforeCursor(), true)
	}

	sub := strings.ToLower(args[1])
	if handler := cli.skillSubcommandHandler(sub); handler != nil {
		return handler(d)
	}
	return []prompt.Suggest{}
}

// suggestInstalledSkills powers `/skill uninstall|remove`.
func (cli *ChatCLI) suggestInstalledSkills(d prompt.Document) []prompt.Suggest {
	if cli.skillHandler == nil || cli.skillHandler.registryMgr == nil {
		return nil
	}
	installed, err := cli.skillHandler.registryMgr.ListInstalled()
	if err != nil {
		return nil
	}
	suggestions := make([]prompt.Suggest, 0, len(installed))
	for _, s := range installed {
		desc := s.Description
		if desc == "" {
			desc = s.Source
		}
		suggestions = append(suggestions, prompt.Suggest{Text: s.Name, Description: desc})
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}

// suggestInstallOrInfoArgs powers `/skill install|info` — handles `--from`
// flag completion plus registry name completion after the flag value.
func (cli *ChatCLI) suggestInstallOrInfoArgs(d prompt.Document) []prompt.Suggest {
	if cli.skillHandler == nil || cli.skillHandler.registryMgr == nil {
		return nil
	}
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	endsWithSpace := strings.HasSuffix(line, " ")

	if isAtRegistryValuePosition(args, endsWithSpace) {
		return cli.getRegistryNameSuggestions(d)
	}
	if isAtFromFlagPosition(args, endsWithSpace) {
		return prompt.FilterHasPrefix(
			[]prompt.Suggest{{Text: "--from", Description: i18n.T("complete.skill.flag_from")}},
			d.GetWordBeforeCursor(), true)
	}
	return nil
}

// isAtRegistryValuePosition reports whether the cursor sits right after a
// `--from`/`-f` flag and should suggest a registry name.
func isAtRegistryValuePosition(args []string, endsWithSpace bool) bool {
	if len(args) == 0 {
		return false
	}
	last := args[len(args)-1]
	if endsWithSpace && isFromFlag(last) {
		return true
	}
	if !endsWithSpace && len(args) >= 2 && isFromFlag(args[len(args)-2]) {
		return true
	}
	return false
}

// isAtFromFlagPosition reports whether the cursor sits right after the skill
// name and should suggest the `--from` flag.
func isAtFromFlagPosition(args []string, endsWithSpace bool) bool {
	if endsWithSpace {
		return len(args) >= 3
	}
	return len(args) >= 4
}

func isFromFlag(s string) bool { return s == "--from" || s == "-f" }

// suggestRegistrySubcommand powers `/skill registry` — enable/disable verb
// then a registry name.
func (cli *ChatCLI) suggestRegistrySubcommand(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	endsWithSpace := strings.HasSuffix(line, " ")

	wantsVerb := len(args) == 2 || (len(args) == 3 && !endsWithSpace)
	if wantsVerb {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "enable", Description: i18n.T("complete.skill.sub_registry_enable")},
			{Text: "disable", Description: i18n.T("complete.skill.sub_registry_disable")},
		}, d.GetWordBeforeCursor(), true)
	}
	return cli.getRegistryNameSuggestions(d)
}

// suggestPinCandidates powers `/skill pin` — installed skills that are not
// already pinned and do not carry `disable-model-invocation: true`.
func (cli *ChatCLI) suggestPinCandidates(d prompt.Document) []prompt.Suggest {
	if cli.skillHandler == nil || cli.personaHandler == nil {
		return nil
	}
	mgr := cli.personaHandler.GetManager()
	if mgr == nil {
		return nil
	}
	pinnedSet := make(map[string]struct{})
	for _, n := range cli.skillHandler.PinnedNames() {
		pinnedSet[n] = struct{}{}
	}
	all := mgr.ListAllSkills()
	suggestions := make([]prompt.Suggest, 0, len(all))
	for _, s := range all {
		if !isPinCandidate(s, pinnedSet) {
			continue
		}
		desc := s.Description
		if desc == "" {
			desc = i18n.T("complete.skill.sub_pin")
		}
		suggestions = append(suggestions, prompt.Suggest{Text: s.Name, Description: desc})
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}

// isPinCandidate reports whether a skill is eligible to be offered as a pin
// completion suggestion.
func isPinCandidate(s *persona.Skill, pinned map[string]struct{}) bool {
	if s == nil || s.DisableModelInvocation {
		return false
	}
	_, already := pinned[s.Name]
	return !already
}

// suggestPinnedNames powers `/skill unpin` — only skills currently pinned.
func (cli *ChatCLI) suggestPinnedNames(d prompt.Document) []prompt.Suggest {
	if cli.skillHandler == nil {
		return nil
	}
	pinned := cli.skillHandler.PinnedNames()
	suggestions := make([]prompt.Suggest, 0, len(pinned))
	for _, name := range pinned {
		suggestions = append(suggestions, prompt.Suggest{
			Text:        name,
			Description: i18n.T("complete.skill.unpin_desc"),
		})
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}

// suggestPreferArgs powers `/skill prefer <name> <source>`.
func (cli *ChatCLI) suggestPreferArgs(d prompt.Document) []prompt.Suggest {
	if cli.skillHandler == nil || cli.skillHandler.registryMgr == nil {
		return nil
	}
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	endsWithSpace := strings.HasSuffix(line, " ")

	// Only suggest sources once we are past the skill-name token.
	if len(args) < 4 && !(len(args) == 4 && !endsWithSpace) {
		return nil
	}
	suggestions := []prompt.Suggest{
		{Text: "--reset", Description: i18n.T("complete.skill.flag_reset")},
	}
	suggestions = append(suggestions, cli.getRegistryNameSuggestions(d)...)
	suggestions = append(suggestions, prompt.Suggest{Text: "local", Description: i18n.T("complete.skill.prefer_local")})
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
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
			{Text: "ui", Description: i18n.T("cfg.section.ui.title")},
			{Text: "quality", Description: i18n.T("complete.config.quality")},
			{Text: "resilience", Description: i18n.T("complete.config.resilience")},
			{Text: "session", Description: i18n.T("complete.config.session")},
			{Text: "integrations", Description: i18n.T("complete.config.integrations")},
			{Text: "auth", Description: i18n.T("complete.config.auth")},
			{Text: "security", Description: i18n.T("complete.config.security")},
			{Text: "scheduler", Description: i18n.T("cfg.section.scheduler.title")},
			{Text: "server", Description: i18n.T("complete.config.server")},
			{Text: "hub", Description: i18n.T("cfg.section.hub.title")},
		}
		return prompt.FilterHasPrefix(sections, word, true)
	}

	// /config security <TAB> → mutating subcommands
	if strings.ToLower(args[1]) == "security" {
		return cli.getConfigSecuritySuggestions(d)
	}

	// /config agent <TAB> → mutating subcommands (ui style)
	if strings.ToLower(args[1]) == "agent" {
		return cli.getConfigAgentSuggestions(d)
	}

	// /config hub <TAB> → mutating subcommands (set/reset)
	if strings.ToLower(args[1]) == "hub" {
		return cli.getConfigHubSuggestions(d)
	}

	// /config ui <TAB> → theme subcommand + values
	if strings.ToLower(args[1]) == "ui" {
		return cli.getConfigUISuggestions(d)
	}

	// /config theme <TAB> → theme names (alias for `/config ui theme`).
	if strings.ToLower(args[1]) == "theme" {
		word := d.GetWordBeforeCursor()
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return prompt.FilterHasPrefix(themeNameSuggestions(), word, true)
		}
		return []prompt.Suggest{}
	}

	return []prompt.Suggest{}
}

// themeNameSuggestions lists the registry's theme names as completer entries,
// so new themes appear automatically. Shared by `/config ui theme` and the
// `/config theme` alias.
func themeNameSuggestions() []prompt.Suggest {
	names := theme.Names()
	vals := make([]prompt.Suggest, 0, len(names))
	for _, name := range names {
		vals = append(vals, prompt.Suggest{
			Text:        name,
			Description: i18n.T("cfg.ui.theme_desc_" + name),
		})
	}
	return vals
}

// getConfigUISuggestions drives the completer for `/config ui`. Slot 3 offers
// the `theme` subcommand; slot 4 enumerates the available theme names from
// the registry so new themes show up automatically.
func (cli *ChatCLI) getConfigUISuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// /config ui <TAB>
	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "theme", Description: i18n.T("complete.config.ui.theme")},
			{Text: "help", Description: i18n.T("cfg.ui.usage_header")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	if len(args) < 3 {
		return []prompt.Suggest{}
	}
	if strings.ToLower(args[2]) == "theme" {
		if len(args) == 3 || (len(args) == 4 && !strings.HasSuffix(line, " ")) {
			return prompt.FilterHasPrefix(themeNameSuggestions(), word, true)
		}
	}
	return []prompt.Suggest{}
}

// getConfigAgentSuggestions drives the completer for `/config agent`.
// Slot 3 lists the available subcommands; slot 4 lists the ui style
// values when the user typed `ui`. Empty for any deeper slot.
func (cli *ChatCLI) getConfigAgentSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// /config agent <TAB>
	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "ui", Description: i18n.T("complete.config.agent.ui")},
			{Text: "help", Description: i18n.T("cfg.agent.usage_header")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	// /config agent ui <TAB> → enumerated style values.
	// Guard against args with fewer than 3 elements (happens during
	// the brief windows where the completer fires before any token
	// after `agent` is materialized — observed when the cursor is at
	// column 0 of a freshly-typed line, before go-prompt has flushed
	// the buffer). Without the guard, args[2] panics with
	// "index out of range".
	if len(args) < 3 {
		return []prompt.Suggest{}
	}
	sub := strings.ToLower(args[2])
	if sub == "ui" || sub == "style" {
		if len(args) == 3 || (len(args) == 4 && !strings.HasSuffix(line, " ")) {
			vals := []prompt.Suggest{
				{Text: "full", Description: i18n.T("cfg.agent.ui_desc_full")},
				{Text: "compact", Description: i18n.T("cfg.agent.ui_desc_compact")},
				{Text: "minimal", Description: i18n.T("cfg.agent.ui_desc_minimal")},
			}
			return prompt.FilterHasPrefix(vals, word, true)
		}
	}
	return []prompt.Suggest{}
}

// getConfigHubSuggestions drives `/config hub set|reset <key> [value]` completion.
func (cli *ChatCLI) getConfigHubSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// /config hub <TAB> → set | reset
	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "set", Description: i18n.T("complete.confighub.set")},
			{Text: "reset", Description: i18n.T("complete.confighub.reset")},
		}, word, true)
	}

	sub := strings.ToLower(args[2])
	if sub != "set" && sub != "reset" && sub != "unset" {
		return nil
	}

	// /config hub set|reset <TAB> → setting keys
	if len(args) == 3 || (len(args) == 4 && !strings.HasSuffix(line, " ")) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: hubKeyEnabled, Description: i18n.T("complete.confighub.enabled")},
			{Text: hubKeyPrincipal, Description: i18n.T("complete.confighub.principal")},
			{Text: hubKeyIsolate, Description: i18n.T("complete.confighub.isolate")},
			{Text: hubKeyTTLHours, Description: i18n.T("complete.confighub.ttl")},
		}, word, true)
	}

	// /config hub set <key> <TAB> → value suggestions per key
	if sub == "set" && (len(args) == 4 || (len(args) == 5 && !strings.HasSuffix(line, " "))) {
		switch strings.ToLower(args[3]) {
		case hubKeyEnabled, hubKeyIsolate:
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "on", Description: i18n.T("cfg.val.enabled")},
				{Text: "off", Description: i18n.T("cfg.val.disabled")},
			}, word, true)
		case hubKeyPrincipal:
			return prompt.FilterHasPrefix(cli.hubPrincipalSuggestions(), word, true)
		case hubKeyTTLHours:
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "6", Description: i18n.T("complete.confighub.ttl_6h")},
				{Text: "24", Description: i18n.T("complete.confighub.ttl_24h")},
				{Text: "72", Description: i18n.T("complete.confighub.ttl_72h")},
				{Text: "168", Description: i18n.T("complete.confighub.ttl_168h")},
				{Text: "0", Description: i18n.T("complete.confighub.ttl_0")},
			}, word, true)
		}
	}
	return nil
}

// hubPrincipalSuggestions turns the known principals into completer entries.
func (cli *ChatCLI) hubPrincipalSuggestions() []prompt.Suggest {
	ps := cli.knownHubPrincipals()
	out := make([]prompt.Suggest, 0, len(ps))
	for _, p := range ps {
		out = append(out, prompt.Suggest{Text: p, Description: i18n.T("complete.confighub.principal_known")})
	}
	return out
}

// getHubSuggestions drives `/hub whoami|bind|bindings` completion.
func (cli *ChatCLI) getHubSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// /hub <TAB> → subcommands
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "whoami", Description: i18n.T("complete.hub.whoami")},
			{Text: "bind", Description: i18n.T("complete.hub.bind")},
			{Text: "bindings", Description: i18n.T("complete.hub.bindings")},
		}, word, true)
	}

	if strings.ToLower(args[1]) == "bind" {
		// /hub bind <TAB> → platform names
		if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "telegram"},
				{Text: "slack"},
				{Text: "whatsapp"},
				{Text: "discord"},
				{Text: "webhook"},
			}, word, true)
		}
		// /hub bind <platform> <userid> <TAB> → principal (known ones)
		if len(args) == 4 || (len(args) == 5 && !strings.HasSuffix(line, " ")) {
			return prompt.FilterHasPrefix(cli.hubPrincipalSuggestions(), word, true)
		}
	}

	// /hub bindings <TAB> → filter by a known principal
	if strings.ToLower(args[1]) == "bindings" && (len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " "))) {
		return prompt.FilterHasPrefix(cli.hubPrincipalSuggestions(), word, true)
	}
	return nil
}

// getConfigSecuritySuggestions drives the completer for the new
// hierarchical /config security surface. It offers subcommand names
// at position 3 and, for forget, a live list of existing patterns.
func (cli *ChatCLI) getConfigSecuritySuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// /config security <TAB>
	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "rules", Description: i18n.T("sec.cmd.rules_header")},
			{Text: "allow", Description: i18n.T("sec.cmd.added_allow")},
			{Text: "deny", Description: i18n.T("sec.cmd.added_deny")},
			{Text: "forget", Description: i18n.T("sec.cmd.rules_word")},
			{Text: "reload", Description: i18n.T("sec.cmd.reloaded")},
			{Text: "help", Description: i18n.T("sec.cmd.usage_header")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	// Subsequent slots — currently only `forget` has a live
	// vocabulary (existing patterns). allow / deny take freeform
	// patterns the user types by hand.
	sub := strings.ToLower(args[2])
	if sub == "forget" || sub == "remove" || sub == "rm" {
		return cli.getConfigSecurityPatternSuggestions(word)
	}
	return []prompt.Suggest{}
}

// getConfigSecurityPatternSuggestions lists every rule pattern in the
// live PolicyManager, suitable for `/config security forget <TAB>`.
// Loading is cheap (single JSON read); doing it at completer time
// keeps the list fresh even after a rule was just added.
func (cli *ChatCLI) getConfigSecurityPatternSuggestions(word string) []prompt.Suggest {
	pm, err := loadPolicyForCompleter(cli)
	if err != nil {
		return nil
	}
	rules := pm.RulesSnapshot()
	out := make([]prompt.Suggest, 0, len(rules))
	for _, r := range rules {
		out = append(out, prompt.Suggest{
			Text:        r.Pattern,
			Description: string(r.Action),
		})
	}
	return prompt.FilterHasPrefix(out, word, true)
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
