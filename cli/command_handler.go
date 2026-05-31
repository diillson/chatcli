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
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// cmdFunc handles one command. It receives the full user input and returns
// true when the REPL should exit.
type cmdFunc func(userInput string) bool

// prefixRoute matches a command by prefix. When word is true the match is
// "exact OR prefix followed by a space" (so /export matches "/export" and
// "/export x" but not "/exporting"); when false it is a raw HasPrefix (the
// historical behavior for stateful sub-command groups like /session).
type prefixRoute struct {
	prefix string
	word   bool
	fn     cmdFunc
}

func (r prefixRoute) matches(input string) bool {
	if r.word {
		return input == r.prefix || strings.HasPrefix(input, r.prefix+" ")
	}
	return strings.HasPrefix(input, r.prefix)
}

// commandRoutes holds the dispatch tables. Kept in a separate struct so that
// CommandHandler stays comparable (its only added field is a pointer) — the
// map/slice live here, behind that pointer.
type commandRoutes struct {
	exact    map[string]cmdFunc
	prefixes []prefixRoute
}

type CommandHandler struct {
	cli *ChatCLI

	// routes is the table-driven dispatch, built once in NewCommandHandler.
	// Replacing the former ~45-case switch keeps cyclomatic complexity low
	// and makes the command surface enumerable/testable.
	routes *commandRoutes
}

func NewCommandHandler(cli *ChatCLI) *CommandHandler {
	ch := &CommandHandler{cli: cli}
	ch.buildRoutes()
	return ch
}

// HandleCommand dispatches a slash command. Returns true to exit the REPL.
func (ch *CommandHandler) HandleCommand(userInput string) bool {
	// Track command usage for memory pattern detection.
	if strings.HasPrefix(userInput, "/") && ch.cli.memoryStore != nil {
		cmd := strings.Fields(userInput)[0]
		if mgr := ch.cli.memoryStore.Manager(); mgr != nil {
			mgr.Profile.RecordCommand(cmd)
		}
	}

	// Command palette. A bare "/" (or "/menu") opens the categorized root
	// listing; a bare, pickable command (e.g. "/model", "/config") opens
	// scoped to its own subcommands/flags/values. We only flag the request
	// here: the executor runs the overlay in place once this handler returns,
	// because go-prompt has already released raw mode by the time it calls the
	// executor — no panic-based unwind is required.
	if target, ok := ch.cli.paletteTrigger(userInput); ok {
		ch.cli.paletteTarget = target
		ch.cli.paletteRequested = true
		return false
	}

	// Mode-switch commands raise a sentinel to unwind out of the go-prompt
	// loop (a plain return cannot escape it), so they stay as explicit cases
	// rather than table entries.
	switch {
	case strings.HasPrefix(userInput, "/agent"):
		// /agent pode ser gerenciamento de personas OU iniciar modo agente
		if !ch.handleAgentPersonaSubcommand(userInput) {
			// Não é um subcomando, inicia modo agente.
			// Smart-routing: if the query looks conversational, the
			// default "hint" mode prints a tip and falls through;
			// "auto" mode redirects to chat and we return here without
			// spinning up the ReAct loop.
			task := strings.TrimSpace(strings.TrimPrefix(userInput, "/agent"))
			if ch.cli.maybeReroute("/agent", task) {
				return false
			}
			ch.cli.pendingAction = "agent"
			panic(errAgentModeRequest)
		}
		return false
	case strings.HasPrefix(userInput, "/run"):
		// /run inicia o modo agente (com ou sem persona ativa)
		ch.cli.pendingAction = "agent"
		panic(errAgentModeRequest)
	case strings.HasPrefix(userInput, "/coder"):
		ch.cli.pendingAction = "coder"
		panic(errCoderModeRequest)
	case userInput == "/plan" || strings.HasPrefix(userInput, "/plan "):
		switch ch.cli.handlePlanCommand(userInput) {
		case planRouteAgent:
			panic(errAgentModeRequest)
		case planRouteCoder:
			panic(errCoderModeRequest)
		}
		return false
	}

	if fn, ok := ch.lookup(userInput); ok {
		return fn(userInput)
	}
	return ch.handleDefault(userInput)
}

// lookup resolves the handler for an input: exact match first, then the
// ordered prefix routes. ok is false when nothing matches (chat/skill input).
func (ch *CommandHandler) lookup(userInput string) (cmdFunc, bool) {
	if fn, ok := ch.routes.exact[userInput]; ok {
		return fn, true
	}
	for _, r := range ch.routes.prefixes {
		if r.matches(userInput) {
			return r.fn, true
		}
	}
	return nil, false
}

// handleDefault treats "/<name> [args]" as a manual skill invocation when
// <name> resolves to an installed user-invocable skill; otherwise it reports
// an unknown command.
func (ch *CommandHandler) handleDefault(userInput string) bool {
	if ch.tryInvokeUserSkill(userInput) {
		return false
	}
	fmt.Println(i18n.T("error.unknown_command"))
	return false
}

// buildRoutes wires every command to its handler. Keeping this as data (not
// control flow) is what holds HandleCommand's complexity down.
func (ch *CommandHandler) buildRoutes() {
	c := ch.cli
	exit := func(string) bool { fmt.Println(i18n.T("status.exiting")); return true }

	ch.routes = &commandRoutes{}
	ch.routes.exact = map[string]cmdFunc{
		"/exit": exit, "exit": exit, "/quit": exit, "quit": exit,
		"/reload":     func(string) bool { c.reloadConfiguration(); return false },
		"/help":       func(string) bool { c.showHelp(); return false },
		"/version":    func(string) bool { ch.handleVersionCommand(); return false },
		"/v":          func(string) bool { ch.handleVersionCommand(); return false },
		"/nextchunk":  func(string) bool { return c.handleNextChunk() },
		"/retry":      func(string) bool { return c.handleRetryLastChunk() },
		"/retryall":   func(string) bool { return c.handleRetryAllChunks() },
		"/skipchunk":  func(string) bool { return c.handleSkipChunk() },
		"/disconnect": func(string) bool { ch.handleDisconnectCommand(); return false },
		"/rewind":     func(string) bool { c.showRewindMenu(); return false },
		"/metrics":    func(string) bool { ch.handleMetricsCommand(); return false },
		"/cost":       func(string) bool { c.handleCostCommand(); return false },
		"/newsession": func(string) bool {
			c.clearAllHistories()
			c.currentSessionName = ""
			// Rotate the shared cross-channel conversation too, so the new
			// session propagates to Telegram/Slack and any other connected CLI.
			if c.hubSync != nil {
				if err := c.hubSync.newSession(context.Background()); err != nil {
					c.logger.Warn("hub sync: new session failed: " + err.Error())
				}
			}
			fmt.Println(i18n.T("session.new_session_started"))
			return false
		},
		"/reset":  ch.resetTerminal,
		"/redraw": ch.resetTerminal,
		"/clear":  ch.resetTerminal,
	}

	// Order preserved from the historical switch. word=true entries match
	// "exact or +space"; word=false entries are raw-prefix sub-command groups.
	ch.routes.prefixes = []prefixRoute{
		{"/switch", false, func(in string) bool { c.handleSwitchCommand(in); return false }},
		{"/provider", false, func(in string) bool { c.handleProviderCommand(in); return false }},
		{"/model", false, func(in string) bool { c.handleModelCommand(in); return false }},
		{"/max-tokens", false, func(in string) bool { c.handleMaxTokensCommand(in); return false }},
		{"/config", true, ch.cmdConfig},
		{"/status", true, ch.cmdConfig},
		{"/settings", true, ch.cmdConfig},
		{"/session", false, func(in string) bool { ch.handleSessionCommand(in); return false }},
		{"/context", false, func(in string) bool { ch.handleContextCommand(in); return false }},
		{"/auth", false, func(in string) bool { ch.handleAuthCommand(in); return false }},
		{"/plugin", false, func(in string) bool { ch.handlePluginCommand(in); return false }},
		{"/skill", false, func(in string) bool { ch.handleSkillCommand(in); return false }},
		{"/connect", false, func(in string) bool { ch.handleConnectCommand(in); return false }},
		{"/hub", false, func(in string) bool { c.handleHubCommand(in); return false }},
		{"/watch", false, func(in string) bool { ch.handleWatchCommand(in); return false }},
		{"/compact", false, func(in string) bool { c.handleCompactCommand(in); return false }},
		{"/memory", false, func(in string) bool { c.handleMemoryCommand(in); return false }},
		{"/mcp", false, func(in string) bool { c.handleMCPCommand(in); return false }},
		{"/hooks", false, func(in string) bool { c.handleHooksCommand(in); return false }},
		{"/ratelimit", true, func(string) bool { c.handleRateLimitCommand(); return false }},
		{"/limits", true, func(string) bool { c.handleRateLimitCommand(); return false }},
		{"/export", true, func(in string) bool { c.handleExportCommand(in); return false }},
		{"/moa", true, func(in string) bool { c.handleMoACommand(in); return false }},
		{"/gateway", true, func(in string) bool { c.handleGatewayCommand(in); return false }},
		{"/lsp", true, func(in string) bool { c.handleLSPCommand(in); return false }},
		{"/thinking", true, func(in string) bool { c.handleThinkingCommand(in); return false }},
		{"/refine", true, func(in string) bool { c.handleRefineCommand(in); return false }},
		{"/verify", true, func(in string) bool { c.handleVerifyCommand(in); return false }},
		{"/reflect", true, func(in string) bool { c.handleReflectCommand(in); return false }},
		{"/worktree", false, func(in string) bool { c.handleWorktreeCommand(in); return false }},
		{"/schedule", false, func(in string) bool { c.handleScheduleCommand(in); return false }},
		{"/wait", false, func(in string) bool { c.handleWaitCommand(in); return false }},
		{"/jobs", false, func(in string) bool { c.handleJobsCommand(in); return false }},
		{"/parked", true, func(in string) bool { c.handleParkedCommand(in); return false }},
		{"/resume", false, func(in string) bool { c.handleResumeCommand(in); return false }},
		{"/cancel-park", false, func(in string) bool { c.handleCancelParkCommand(in); return false }},
		{"/channel", false, func(in string) bool { c.handleChannelCommand(in); return false }},
		{"/websearch", false, func(in string) bool { c.handleWebSearchCommand(in); return false }},
	}
}

// cmdConfig routes /config, /status and /settings to the config sections.
func (ch *CommandHandler) cmdConfig(userInput string) bool {
	fields := strings.Fields(userInput)
	ch.cli.routeConfigCommand(fields[1:])
	return false
}

// resetTerminal handles /reset, /redraw and /clear.
func (ch *CommandHandler) resetTerminal(string) bool {
	fmt.Print("\033[0m")
	_ = os.Stdout.Sync()
	ch.cli.restoreTerminal()
	time.Sleep(50 * time.Millisecond)
	ch.cli.forceRefreshPrompt()
	return false
}

// handleContextCommand delegates to the ContextHandler.
func (ch *CommandHandler) handleContextCommand(userInput string) {
	sessionID := ch.cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}

	if err := ch.cli.contextHandler.HandleContextCommand(sessionID, userInput); err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s", err.Error()), ColorYellow))
	}
}

// handleSessionCommand foi movido para cá para consolidar a lógica do handler
func (ch *CommandHandler) handleSessionCommand(userInput string) {
	args := strings.Fields(userInput)
	if len(args) < 2 {
		fmt.Println(i18n.T("session.usage_header"))
		fmt.Println(i18n.T("session.usage_save"))
		fmt.Println(i18n.T("session.usage_load"))
		fmt.Println(i18n.T("session.usage_list"))
		fmt.Println(i18n.T("session.usage_search"))
		fmt.Println(i18n.T("session.usage_delete"))
		fmt.Println(i18n.T("session.usage_new"))
		return
	}

	command := args[1]
	var name string
	if len(args) > 2 {
		name = args[2]
	}

	switch command {
	case "save":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_save"))
			return
		}
		ch.cli.handleSaveSession(name)
	case "load":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_load"))
			return
		}
		ch.cli.handleLoadSession(name)
	case "list":
		ch.cli.handleListSessions()
	case "search":
		// Everything after "search" is the query (may contain spaces).
		query := strings.TrimSpace(strings.TrimPrefix(userInput, args[0]))
		query = strings.TrimSpace(strings.TrimPrefix(query, "search"))
		if query == "" {
			fmt.Println(colorize("  "+i18n.T("session.search.usage"), ColorYellow))
			return
		}
		ch.cli.handleSearchSessions(query)
	case "delete":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_delete"))
			return
		}
		ch.cli.handleDeleteSession(name)
	case "new":
		ch.cli.clearAllHistories()
		ch.cli.currentSessionName = ""
		fmt.Println(i18n.T("session.new_session_started"))
	case "fork":
		if name == "" {
			fmt.Println(colorize("  "+i18n.T("cmd.core.session_fork_usage"), ColorYellow))
			return
		}
		ch.cli.handleForkSession(name)
	default:
		// CORREÇÃO: Usar Println com i18n.T
		fmt.Println(i18n.T("session.unknown_command", command))
	}
}
