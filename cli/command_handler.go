/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

type CommandHandler struct {
	cli *ChatCLI
}

func NewCommandHandler(cli *ChatCLI) *CommandHandler {
	return &CommandHandler{cli: cli}
}

func (ch *CommandHandler) HandleCommand(userInput string) bool {
	// Track command usage for memory pattern detection
	if strings.HasPrefix(userInput, "/") && ch.cli.memoryStore != nil {
		cmd := strings.Fields(userInput)[0]
		if mgr := ch.cli.memoryStore.Manager(); mgr != nil {
			mgr.Profile.RecordCommand(cmd)
		}
	}

	switch {
	case userInput == "/exit" || userInput == "exit" || userInput == "/quit" || userInput == "quit":
		fmt.Println(i18n.T("status.exiting"))
		return true
	case userInput == "/reload":
		ch.cli.reloadConfiguration()
		return false
	case strings.HasPrefix(userInput, "/agent"):
		// /agent pode ser gerenciamento de personas OU iniciar modo agente
		if !ch.handleAgentPersonaSubcommand(userInput) {
			// Não é um subcomando, inicia modo agente.
			// Smart-routing: if the query looks conversational, the
			// default "hint" mode prints a tip and falls through;
			// "auto" mode redirects to chat and we return here without
			// spinning up the ReAct loop.
			task := strings.TrimSpace(strings.TrimPrefix(userInput, "/agent"))
			if ch.cli.MaybeReroute("/agent", task) {
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
	case strings.HasPrefix(userInput, "/switch"):
		ch.cli.handleSwitchCommand(userInput)
		return false
	case userInput == "/help":
		ch.cli.showHelp()
		return false
	case userInput == "/config" || userInput == "/status" || userInput == "/settings" ||
		strings.HasPrefix(userInput, "/config ") ||
		strings.HasPrefix(userInput, "/status ") ||
		strings.HasPrefix(userInput, "/settings "):
		fields := strings.Fields(userInput)
		// Drop the command token; the rest is the optional section name.
		ch.cli.routeConfigCommand(fields[1:])
		return false
	case userInput == "/version" || userInput == "/v":
		ch.handleVersionCommand()
		return false
	case userInput == "/nextchunk":
		return ch.cli.handleNextChunk()
	case userInput == "/retry":
		return ch.cli.handleRetryLastChunk()
	case userInput == "/retryall":
		return ch.cli.handleRetryAllChunks()
	case userInput == "/skipchunk":
		return ch.cli.handleSkipChunk()
	case userInput == "/newsession":
		ch.cli.clearAllHistories()
		ch.cli.currentSessionName = ""
		fmt.Println(i18n.T("session.new_session_started"))
		return false
	case strings.HasPrefix(userInput, "/session"):
		ch.handleSessionCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/context"):
		ch.handleContextCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/auth"):
		ch.handleAuthCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/plugin"):
		ch.handlePluginCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/skill"):
		ch.handleSkillCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/connect"):
		ch.handleConnectCommand(userInput)
		return false
	case userInput == "/disconnect":
		ch.handleDisconnectCommand()
		return false
	case strings.HasPrefix(userInput, "/watch"):
		ch.handleWatchCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/compact"):
		ch.cli.handleCompactCommand(userInput)
		return false
	case userInput == "/rewind":
		ch.cli.showRewindMenu()
		return false
	case strings.HasPrefix(userInput, "/memory"):
		ch.cli.handleMemoryCommand(userInput)
		return false
	case userInput == "/metrics":
		ch.handleMetricsCommand()
		return false
	case strings.HasPrefix(userInput, "/mcp"):
		ch.cli.handleMCPCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/hooks"):
		ch.cli.handleHooksCommand(userInput)
		return false
	case userInput == "/cost":
		ch.cli.handleCostCommand()
		return false
	case userInput == "/thinking" || strings.HasPrefix(userInput, "/thinking "):
		ch.cli.handleThinkingCommand(userInput)
		return false
	case userInput == "/plan" || strings.HasPrefix(userInput, "/plan "):
		switch ch.cli.handlePlanCommand(userInput) {
		case planRouteAgent:
			panic(errAgentModeRequest)
		case planRouteCoder:
			panic(errCoderModeRequest)
		}
		return false
	case userInput == "/refine" || strings.HasPrefix(userInput, "/refine "):
		ch.cli.handleRefineCommand(userInput)
		return false
	case userInput == "/verify" || strings.HasPrefix(userInput, "/verify "):
		ch.cli.handleVerifyCommand(userInput)
		return false
	case userInput == "/reflect" || strings.HasPrefix(userInput, "/reflect "):
		ch.cli.handleReflectCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/worktree"):
		ch.cli.handleWorktreeCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/channel"):
		ch.cli.handleChannelCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/websearch"):
		ch.cli.handleWebSearchCommand(userInput)
		return false
	case userInput == "/reset" || userInput == "/redraw" || userInput == "/clear":
		fmt.Print("\033[0m")
		_ = os.Stdout.Sync()
		ch.cli.restoreTerminal()
		time.Sleep(50 * time.Millisecond)
		ch.cli.forceRefreshPrompt()
		return false
	default:
		// Fallback: treat "/<name> [args]" as a manual skill invocation
		// when <name> resolves to an installed skill with
		// `user-invocable: true`. Reserved built-in command names are
		// filtered out by tryInvokeUserSkill itself.
		if ch.tryInvokeUserSkill(userInput) {
			return false
		}
		fmt.Println(i18n.T("error.unknown_command"))
		return false
	}
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
