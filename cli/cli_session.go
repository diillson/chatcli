package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

func (cli *ChatCLI) RunAgentOnce(ctx context.Context, input string, autoExecute bool) error {
	cli.setExecutionProfile(ProfileAgent)
	defer cli.setExecutionProfile(ProfileNormal)

	var query string
	if strings.HasPrefix(input, "/agent ") {
		query = strings.TrimPrefix(input, "/agent ")
	} else if strings.HasPrefix(input, "/run ") {
		query = strings.TrimPrefix(input, "/run ")
	} else {
		return fmt.Errorf("entrada inválida para o modo agente one-shot: %s", input)
	}

	// Processar contextos especiais como @file, @git, etc.
	query, additionalContext := cli.processSpecialCommands(query)
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	// Assegurar que o modo agente está inicializado
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	// Chama a nova função não-interativa do AgentMode
	return cli.agentMode.RunOnce(ctx, fullQuery, autoExecute)
}

// getRemoteClient extracts the *remote.Client from cli.Client via type assertion.
func (cli *ChatCLI) getRemoteClient() *remote.Client {
	if rc, ok := cli.Client.(*remote.Client); ok {
		return rc
	}
	return nil
}

// askSessionChoice displays an interactive prompt with the given options and returns the user's choice.
// options is a list of i18n keys to display; validChoices maps single-char inputs to return values.
func askSessionChoice(optionKeys []string, validChoices map[string]string, defaultChoice string) string {
	for _, key := range optionKeys {
		fmt.Println(i18n.T(key))
	}
	fmt.Print(i18n.T("session.prompt_choice"))

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if val, ok := validChoices[input]; ok {
		return val
	}
	return defaultChoice
}

// remoteSessionCtx creates a context with a 10-second timeout for remote session operations.
func remoteSessionCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (cli *ChatCLI) handleSaveSession(name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(i18n.T("session.error_save", fmt.Errorf("remote client unavailable")))
			return
		}

		fmt.Println(i18n.T("session.save_where_prompt", name))
		choice := askSessionChoice(
			[]string{"session.save_option_local", "session.save_option_remote", "session.save_option_both"},
			map[string]string{"l": "local", "r": "remote", "b": "both"},
			"local",
		)

		sd := cli.buildSessionData()

		switch choice {
		case "remote":
			ctx, cancel := remoteSessionCtx()
			defer cancel()
			if err := rc.SaveSessionV2(ctx, name, sd); err != nil {
				fmt.Println(i18n.T("session.error_save", err))
			} else {
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.save_success_remote", name))
			}
		case "both":
			var localErr, remoteErr error
			localErr = cli.sessionManager.SaveSessionV2(name, sd)
			ctx, cancel := remoteSessionCtx()
			defer cancel()
			remoteErr = rc.SaveSessionV2(ctx, name, sd)

			if localErr != nil {
				fmt.Println(i18n.T("session.error_save", fmt.Errorf("local: %w", localErr)))
			}
			if remoteErr != nil {
				fmt.Println(i18n.T("session.error_save", fmt.Errorf("remote: %w", remoteErr)))
			}
			if localErr == nil && remoteErr == nil {
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.save_success_both", name))
			}
		default: // "local"
			if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
				fmt.Println(i18n.T("session.error_save", err))
			} else {
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.save_success", name))
			}
		}
		return
	}

	// Local only (not connected)
	sd := cli.buildSessionData()
	if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
		fmt.Println(i18n.T("session.error_save", err))
	} else {
		cli.currentSessionName = name
		fmt.Println(i18n.T("session.save_success", name))
	}
}

func (cli *ChatCLI) handleLoadSession(name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(i18n.T("session.error_load", fmt.Errorf("remote client unavailable")))
			return
		}

		// Check both sources
		localSD, localErr := cli.sessionManager.LoadSessionV2(name)
		ctx, cancel := remoteSessionCtx()
		defer cancel()
		remoteSD, remoteErr := rc.LoadSessionV2(ctx, name)

		foundLocal := localErr == nil
		foundRemote := remoteErr == nil

		switch {
		case foundLocal && foundRemote:
			// Found in both — ask user
			fmt.Println(i18n.T("session.load_found_both", name))
			choice := askSessionChoice(
				[]string{"session.option_local", "session.option_remote"},
				map[string]string{"l": "local", "r": "remote"},
				"local",
			)
			if choice == "remote" {
				cli.restoreSessionData(remoteSD)
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.load_success_remote", name))
			} else {
				cli.restoreSessionData(localSD)
				cli.currentSessionName = name
				fmt.Println(i18n.T("session.load_success", name))
			}
		case foundLocal:
			cli.restoreSessionData(localSD)
			cli.currentSessionName = name
			fmt.Println(i18n.T("session.load_success", name))
		case foundRemote:
			cli.restoreSessionData(remoteSD)
			cli.currentSessionName = name
			fmt.Println(i18n.T("session.load_success_remote", name))
		default:
			fmt.Println(i18n.T("session.error_load", localErr))
		}
		return
	}

	// Local only
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil {
		fmt.Println(i18n.T("session.error_load", err))
	} else {
		cli.restoreSessionData(sd)
		cli.currentSessionName = name
		fmt.Println(i18n.T("session.load_success", name))
	}
}

// clearAllHistories resets the unified history.
func (cli *ChatCLI) clearAllHistories() {
	cli.history = make([]models.Message, 0)
	cli.checkpoints = nil
}

// buildSessionData builds a SessionData from the current CLI state.
// Uses ChatHistory field to store the unified history for backwards compatibility.
func (cli *ChatCLI) buildSessionData() *SessionData {
	return &SessionData{
		Version:     2,
		ChatHistory: cli.history,
	}
}

// restoreSessionData restores history from a SessionData.
// Merges legacy separate histories into the unified history for backwards compatibility.
func (cli *ChatCLI) restoreSessionData(sd *SessionData) {
	cli.history = sd.ChatHistory
	if cli.history == nil {
		cli.history = make([]models.Message, 0)
	}

	// Backwards compatibility: merge legacy separate histories if present
	if len(sd.AgentHistory) > 0 || len(sd.CoderHistory) > 0 {
		// Append non-system messages from legacy agent/coder histories
		for _, msg := range sd.AgentHistory {
			if msg.Role != "system" {
				cli.history = append(cli.history, msg)
			}
		}
		for _, msg := range sd.CoderHistory {
			if msg.Role != "system" {
				cli.history = append(cli.history, msg)
			}
		}
	}

	cli.checkpoints = nil
}

func (cli *ChatCLI) handleListSessions() {
	if cli.isRemote {
		rc := cli.getRemoteClient()

		// Fetch both sources
		localSessions, localErr := cli.sessionManager.ListSessions()
		var remoteSessions []string
		var remoteErr error
		if rc != nil {
			ctx, cancel := remoteSessionCtx()
			defer cancel()
			remoteSessions, remoteErr = rc.ListSessions(ctx)
		}

		hasLocal := localErr == nil && len(localSessions) > 0
		hasRemote := remoteErr == nil && len(remoteSessions) > 0

		if !hasLocal && !hasRemote {
			if localErr != nil {
				fmt.Println(i18n.T("session.error_list", localErr))
			}
			if remoteErr != nil {
				fmt.Println(i18n.T("session.error_list", remoteErr))
			}
			if localErr == nil && remoteErr == nil {
				fmt.Println(i18n.T("session.list_empty"))
			}
			return
		}

		if hasLocal {
			fmt.Println(i18n.T("session.list_header_local"))
			for _, s := range localSessions {
				fmt.Printf("  - %s\n", s)
			}
		}
		if hasRemote {
			if hasLocal {
				fmt.Println()
			}
			fmt.Println(i18n.T("session.list_header_remote"))
			for _, s := range remoteSessions {
				fmt.Printf("  - %s\n", s)
			}
		}
		return
	}

	// Local only
	sessions, err := cli.sessionManager.ListSessions()
	if err != nil {
		fmt.Println(i18n.T("session.error_list", err))
		return
	}
	if len(sessions) == 0 {
		fmt.Println(i18n.T("session.list_empty"))
		return
	}
	fmt.Println(i18n.T("session.list_header"))
	for _, session := range sessions {
		fmt.Printf("- %s\n", session)
	}
}

func (cli *ChatCLI) handleDeleteSession(name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(i18n.T("session.error_delete", fmt.Errorf("remote client unavailable")))
			return
		}

		// Check both sources
		_, localErr := cli.sessionManager.LoadSession(name)
		ctx, cancel := remoteSessionCtx()
		defer cancel()
		_, remoteErr := rc.LoadSession(ctx, name)

		foundLocal := localErr == nil
		foundRemote := remoteErr == nil

		switch {
		case foundLocal && foundRemote:
			fmt.Println(i18n.T("session.delete_found_both", name))
			choice := askSessionChoice(
				[]string{"session.option_local", "session.option_remote", "session.option_both"},
				map[string]string{"l": "local", "r": "remote", "b": "both"},
				"local",
			)
			switch choice {
			case "remote":
				ctxDel, cancelDel := remoteSessionCtx()
				defer cancelDel()
				if err := rc.DeleteSession(ctxDel, name); err != nil {
					fmt.Println(i18n.T("session.error_delete", err))
				} else {
					fmt.Println(i18n.T("session.delete_success_remote", name))
				}
			case "both":
				localDelErr := cli.sessionManager.DeleteSession(name)
				ctxDel, cancelDel := remoteSessionCtx()
				defer cancelDel()
				remoteDelErr := rc.DeleteSession(ctxDel, name)
				if localDelErr != nil {
					fmt.Println(i18n.T("session.error_delete", fmt.Errorf("local: %w", localDelErr)))
				}
				if remoteDelErr != nil {
					fmt.Println(i18n.T("session.error_delete", fmt.Errorf("remote: %w", remoteDelErr)))
				}
				if localDelErr == nil && remoteDelErr == nil {
					fmt.Println(i18n.T("session.delete_success_both", name))
					if cli.currentSessionName == name {
						cli.clearAllHistories()
						cli.currentSessionName = ""
						fmt.Println(i18n.T("session.delete_active_cleared"))
					}
				}
			default: // "local"
				if err := cli.sessionManager.DeleteSession(name); err != nil {
					fmt.Println(i18n.T("session.error_delete", err))
				} else {
					fmt.Println(i18n.T("session.delete_success", name))
					if cli.currentSessionName == name {
						cli.clearAllHistories()
						cli.currentSessionName = ""
						fmt.Println(i18n.T("session.delete_active_cleared"))
					}
				}
			}
		case foundLocal:
			if err := cli.sessionManager.DeleteSession(name); err != nil {
				fmt.Println(i18n.T("session.error_delete", err))
			} else {
				fmt.Println(i18n.T("session.delete_success", name))
				if cli.currentSessionName == name {
					cli.clearAllHistories()
					cli.currentSessionName = ""
					fmt.Println(i18n.T("session.delete_active_cleared"))
				}
			}
		case foundRemote:
			ctxDel, cancelDel := remoteSessionCtx()
			defer cancelDel()
			if err := rc.DeleteSession(ctxDel, name); err != nil {
				fmt.Println(i18n.T("session.error_delete", err))
			} else {
				fmt.Println(i18n.T("session.delete_success_remote", name))
				if cli.currentSessionName == name {
					cli.clearAllHistories()
					cli.currentSessionName = ""
					fmt.Println(i18n.T("session.delete_active_cleared"))
				}
			}
		default:
			fmt.Println(i18n.T("session.error_delete", localErr))
		}
		return
	}

	// Local only
	if err := cli.sessionManager.DeleteSession(name); err != nil {
		fmt.Println(i18n.T("session.error_delete", err))
	} else {
		fmt.Println(i18n.T("session.delete_success", name))
		if cli.currentSessionName == name {
			cli.clearAllHistories()
			cli.currentSessionName = ""
			fmt.Println(i18n.T("session.delete_active_cleared"))
		}
	}
}

// handleForkSession creates a fork of the current session.
// If a session is loaded, forks from that. Otherwise, forks from in-memory history.
func (cli *ChatCLI) handleForkSession(newName string) {
	// Build session data from current state
	sd := &SessionData{
		Version:     2,
		ChatHistory: make([]models.Message, len(cli.history)),
	}
	copy(sd.ChatHistory, cli.history)

	// If the current session has a name (was loaded/saved), we can fork from file
	if cli.currentSessionName != "" {
		if err := cli.sessionManager.ForkSession(cli.currentSessionName, newName); err != nil {
			fmt.Println(colorize(fmt.Sprintf("  Erro ao fork: %v", err), ColorRed))
			return
		}
	} else {
		// Fork from in-memory state
		if err := cli.sessionManager.ForkCurrentToNew(newName, sd); err != nil {
			fmt.Println(colorize(fmt.Sprintf("  Erro ao fork: %v", err), ColorRed))
			return
		}
	}

	// Switch to the forked session
	oldName := cli.currentSessionName
	if oldName == "" {
		oldName = "(unsaved)"
	}
	cli.currentSessionName = newName

	fmt.Println()
	fmt.Println(uiBox("✅", "SESSION FORKED", ColorGreen))
	p := uiPrefix(ColorGreen)
	fmt.Println(p + fmt.Sprintf("  %sDe:%s       %s", ColorGray, ColorReset, oldName))
	fmt.Println(p + fmt.Sprintf("  %sPara:%s     %s", ColorGray, ColorReset, colorize(newName, ColorCyan)))
	fmt.Println(p + fmt.Sprintf("  %sMensagens:%s %d", ColorGray, ColorReset, len(cli.history)))
	fmt.Println(p + colorize("  Agora trabalhando no fork. O original permanece intacto.", ColorGray))
	fmt.Println(uiBoxEnd(ColorGreen))
	fmt.Println()
}
