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

// cmdFunc handles one command. It receives the per-command context and the
// full user input and returns true when the REPL should exit. Threading ctx
// through the dispatch lets route handlers root their own context on the
// caller's instead of context.Background() (satisfies contextcheck).
type cmdFunc func(ctx context.Context, userInput string) bool

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
func (ch *CommandHandler) HandleCommand(ctx context.Context, userInput string) bool {
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
			if ch.cli.maybeReroute(ctx, "/agent", task) {
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
	case userInput == "/reload":
		// Handled here (not in the route table) so the per-command ctx flows
		// into the LLM-manager rebuild and model-cache refresh.
		ch.cli.reloadConfiguration(ctx)
		return false
	case userInput == "/version" || userInput == "/v":
		// Handled here (not in the route table) so the per-command ctx flows
		// into the update-check HTTP request.
		ch.handleVersionCommand(ctx)
		return false
	}

	if fn, ok := ch.lookup(userInput); ok {
		return fn(ctx, userInput)
	}
	return ch.handleDefault(ctx, userInput)
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
func (ch *CommandHandler) handleDefault(ctx context.Context, userInput string) bool {
	if ch.tryInvokeUserSkill(ctx, userInput) {
		return false
	}
	fmt.Println(i18n.T("error.unknown_command"))
	return false
}

// buildRoutes wires every command to its handler. Keeping this as data (not
// control flow) is what holds HandleCommand's complexity down.
func (ch *CommandHandler) buildRoutes() {
	c := ch.cli
	exit := func(_ context.Context, _ string) bool { fmt.Println(i18n.T("status.exiting")); return true }

	ch.routes = &commandRoutes{}
	ch.routes.exact = map[string]cmdFunc{
		"/exit": exit, "exit": exit, "/quit": exit, "quit": exit,
		"/help":       func(_ context.Context, _ string) bool { c.showHelp(); return false },
		"/nextchunk":  func(ctx context.Context, _ string) bool { return c.handleNextChunk(ctx) },
		"/retry":      func(ctx context.Context, _ string) bool { return c.handleRetryLastChunk(ctx) },
		"/retryall":   func(_ context.Context, _ string) bool { return c.handleRetryAllChunks() },
		"/skipchunk":  func(_ context.Context, _ string) bool { return c.handleSkipChunk() },
		"/disconnect": func(ctx context.Context, _ string) bool { ch.handleDisconnectCommand(ctx); return false },
		"/rewind":     func(_ context.Context, _ string) bool { c.showRewindMenu(); return false },
		"/metrics":    func(_ context.Context, _ string) bool { ch.handleMetricsCommand(); return false },
		"/cost":       func(_ context.Context, _ string) bool { c.handleCostCommand(); return false },
		"/newsession": func(ctx context.Context, _ string) bool {
			c.clearAllHistories()
			c.currentSessionName = ""
			// Rotate the shared cross-channel conversation too, so the new
			// session propagates to Telegram/Slack and any other connected CLI.
			if c.hubSync != nil {
				if err := c.hubSync.newSession(ctx); err != nil {
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
		{"/switch", false, func(ctx context.Context, in string) bool { c.handleSwitchCommand(ctx, in); return false }},
		{"/provider", false, func(ctx context.Context, in string) bool { c.handleProviderCommand(ctx, in); return false }},
		// Must precede "/model" (raw-prefix) so it isn't shadowed by it.
		{"/model-image", true, func(ctx context.Context, in string) bool { c.handleImageModelCommand(ctx, in); return false }},
		{"/model", false, func(ctx context.Context, in string) bool { c.handleModelCommand(ctx, in); return false }},
		{"/max-tokens", false, func(ctx context.Context, in string) bool { c.handleMaxTokensCommand(ctx, in); return false }},
		{"/config", true, ch.cmdConfig},
		{"/status", true, ch.cmdConfig},
		{"/settings", true, ch.cmdConfig},
		{"/session", false, func(ctx context.Context, in string) bool { ch.handleSessionCommand(ctx, in); return false }},
		{"/context", false, func(ctx context.Context, in string) bool { ch.handleContextCommand(ctx, in); return false }},
		{"/auth", false, func(ctx context.Context, in string) bool { ch.handleAuthCommand(ctx, in); return false }},
		{"/plugin", false, func(_ context.Context, in string) bool { ch.handlePluginCommand(in); return false }},
		{"/skill", false, func(ctx context.Context, in string) bool { ch.handleSkillCommand(ctx, in); return false }},
		{"/connect", false, func(ctx context.Context, in string) bool { ch.handleConnectCommand(ctx, in); return false }},
		{"/hub", false, func(ctx context.Context, in string) bool { c.handleHubCommand(ctx, in); return false }},
		{"/watch", false, func(ctx context.Context, in string) bool { ch.handleWatchCommand(ctx, in); return false }},
		{"/compact", false, func(ctx context.Context, in string) bool { c.handleCompactCommand(ctx, in); return false }},
		{"/memory", false, func(ctx context.Context, in string) bool { c.handleMemoryCommand(ctx, in); return false }},
		{"/mcp", false, func(ctx context.Context, in string) bool { c.handleMCPCommand(ctx, in); return false }},
		{"/hooks", false, func(_ context.Context, in string) bool { c.handleHooksCommand(in); return false }},
		{"/ratelimit", true, func(_ context.Context, _ string) bool { c.handleRateLimitCommand(); return false }},
		{"/limits", true, func(_ context.Context, _ string) bool { c.handleRateLimitCommand(); return false }},
		{"/export", true, func(_ context.Context, in string) bool { c.handleExportCommand(in); return false }},
		{"/moa", true, func(ctx context.Context, in string) bool { c.handleMoACommand(ctx, in); return false }},
		{"/gateway", true, func(_ context.Context, in string) bool { c.handleGatewayCommand(in); return false }},
		{"/lsp", true, func(ctx context.Context, in string) bool { c.handleLSPCommand(ctx, in); return false }},
		{"/thinking", true, func(_ context.Context, in string) bool { c.handleThinkingCommand(in); return false }},
		{"/refine", true, func(_ context.Context, in string) bool { c.handleRefineCommand(in); return false }},
		{"/verify", true, func(_ context.Context, in string) bool { c.handleVerifyCommand(in); return false }},
		{"/reflect", true, func(ctx context.Context, in string) bool { c.handleReflectCommand(ctx, in); return false }},
		{"/worktree", false, func(_ context.Context, in string) bool { c.handleWorktreeCommand(in); return false }},
		{"/schedule", false, func(ctx context.Context, in string) bool { c.handleScheduleCommand(ctx, in); return false }},
		{"/wait", false, func(ctx context.Context, in string) bool { c.handleWaitCommand(ctx, in); return false }},
		{"/jobs", false, func(ctx context.Context, in string) bool { c.handleJobsCommand(ctx, in); return false }},
		{"/parked", true, func(_ context.Context, in string) bool { c.handleParkedCommand(in); return false }},
		{"/resume", false, func(ctx context.Context, in string) bool { c.handleResumeCommand(ctx, in); return false }},
		{"/cancel-park", false, func(_ context.Context, in string) bool { c.handleCancelParkCommand(in); return false }},
		{"/channel", false, func(ctx context.Context, in string) bool { c.handleChannelCommand(ctx, in); return false }},
		{"/websearch", false, func(_ context.Context, in string) bool { c.handleWebSearchCommand(in); return false }},
	}
}

// cmdConfig routes /config, /status and /settings to the config sections.
func (ch *CommandHandler) cmdConfig(ctx context.Context, userInput string) bool {
	fields := strings.Fields(userInput)
	ch.cli.routeConfigCommand(ctx, fields[1:])
	return false
}

// resetTerminal handles /reset, /redraw and /clear.
func (ch *CommandHandler) resetTerminal(_ context.Context, _ string) bool {
	fmt.Print("\033[0m")
	_ = os.Stdout.Sync()
	ch.cli.restoreTerminal()
	time.Sleep(50 * time.Millisecond)
	ch.cli.forceRefreshPrompt()
	return false
}

// handleContextCommand delegates to the ContextHandler.
func (ch *CommandHandler) handleContextCommand(ctx context.Context, userInput string) {
	sessionID := ch.cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}

	if err := ch.cli.contextHandler.HandleContextCommand(ctx, sessionID, userInput); err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s", err.Error()), ColorYellow))
	}
}

// handleSessionCommand foi movido para cá para consolidar a lógica do handler
func (ch *CommandHandler) handleSessionCommand(ctx context.Context, userInput string) {
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
		ch.cli.handleSaveSession(ctx, name)
	case "load":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_load"))
			return
		}
		ch.cli.handleLoadSession(ctx, name)
	case "list":
		ch.cli.handleListSessions(ctx)
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
		ch.cli.handleDeleteSession(ctx, name)
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
