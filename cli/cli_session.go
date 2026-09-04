package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/ui/kit"
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
		return fmt.Errorf("%s", i18n.T("sess.cmd.invalid_one_shot_input", input))
	}

	// Processar contextos especiais como @file, @git, etc.
	query, additionalContext, images := cli.processSpecialCommands(ctx, query)
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc
	fullQuery := query
	if additionalContext != "" {
		fullQuery = query + "\n\nContexto adicional:\n" + additionalContext
	}

	// Assegurar que o modo agente está inicializado
	if cli.agentMode == nil {
		cli.agentMode = NewAgentMode(cli, cli.logger)
	}

	cli.agentMode.pendingUserImages = images
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

// remoteSessionCtx derives a 10-second-timeout context from the caller's
// context for remote session operations.
func remoteSessionCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 10*time.Second)
}

func (cli *ChatCLI) handleSaveSession(ctx context.Context, name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(kit.Notice(kit.LevelError, i18n.T("session.error_save", fmt.Errorf("remote client unavailable"))))
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
			reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := rc.SaveSessionV2(reqCtx, name, sd); err != nil {
				fmt.Println(i18n.T("session.error_save", err))
			} else {
				cli.currentSessionName = name
				cli.boundRemoteOnly = true
				fmt.Println(i18n.T("session.save_success_remote", name))
			}
		case "both":
			var localErr, remoteErr error
			localErr = cli.sessionManager.SaveSessionV2(name, sd)
			reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			remoteErr = rc.SaveSessionV2(reqCtx, name, sd)

			if localErr != nil {
				fmt.Println(i18n.T("session.error_save", fmt.Errorf("local: %w", localErr)))
			}
			if remoteErr != nil {
				fmt.Println(i18n.T("session.error_save", fmt.Errorf("remote: %w", remoteErr)))
			}
			if localErr == nil && remoteErr == nil {
				cli.currentSessionName = name
				cli.boundRemoteOnly = false
				fmt.Println(i18n.T("session.save_success_both", name))
			}
		default: // "local"
			if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
				fmt.Println(i18n.T("session.error_save", err))
			} else {
				cli.currentSessionName = name
				cli.boundRemoteOnly = false
				fmt.Println(i18n.T("session.save_success", name))
			}
		}
		return
	}

	// Local only (not connected)
	sd := cli.buildSessionData()
	if err := cli.sessionManager.SaveSessionV2(name, sd); err != nil {
		fmt.Println(kit.Notice(kit.LevelError, i18n.T("session.error_save", err)))
	} else {
		cli.currentSessionName = name
		cli.boundRemoteOnly = false
		fmt.Println(kit.Notice(kit.LevelSuccess, i18n.T("session.save_success", name)))
	}
}

func (cli *ChatCLI) handleLoadSession(ctx context.Context, name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(kit.Notice(kit.LevelError, i18n.T("session.error_load", fmt.Errorf("remote client unavailable"))))
			return
		}

		// Check both sources
		localSD, localErr := cli.sessionManager.LoadSessionV2(name)
		ctx, cancel := remoteSessionCtx(ctx)
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
				cli.applySessionAttachments(remoteSD, name)
				cli.boundRemoteOnly = true
				fmt.Println(i18n.T("session.load_success_remote", name))
			} else {
				cli.restoreSessionData(localSD)
				cli.currentSessionName = name
				cli.applySessionAttachments(localSD, name)
				cli.boundRemoteOnly = false
				fmt.Println(i18n.T("session.load_success", name))
			}
		case foundLocal:
			cli.restoreSessionData(localSD)
			cli.currentSessionName = name
			cli.applySessionAttachments(localSD, name)
			cli.boundRemoteOnly = false
			fmt.Println(i18n.T("session.load_success", name))
		case foundRemote:
			cli.restoreSessionData(remoteSD)
			cli.currentSessionName = name
			cli.applySessionAttachments(remoteSD, name)
			cli.boundRemoteOnly = true
			fmt.Println(i18n.T("session.load_success_remote", name))
		default:
			fmt.Println(i18n.T("session.error_load", localErr))
		}
		return
	}

	// Local only
	sd, err := cli.sessionManager.LoadSessionV2(name)
	if err != nil {
		fmt.Println(kit.Notice(kit.LevelError, i18n.T("session.error_load", err)))
	} else {
		cli.restoreSessionData(sd)
		cli.currentSessionName = name
		cli.applySessionAttachments(sd, name)
		cli.boundRemoteOnly = false
		fmt.Println(kit.Notice(kit.LevelSuccess, i18n.T("session.load_success", name)))
	}
}

// clearAllHistories resets the unified history.
func (cli *ChatCLI) clearAllHistories() {
	cli.history = make([]models.Message, 0)
	cli.checkpoints = nil
	cli.preCompaction = nil
}

// clearConversation is /clear: the conversation restarts empty while the
// session binding, attachments, memory and checkpoints stay — /rewind
// brings the cleared turns back. The screen itself is /redraw's job.
func (cli *ChatCLI) clearConversation(ctx context.Context) {
	n := 0
	for _, m := range cli.history {
		if !strings.EqualFold(m.Role, "system") {
			n++
		}
	}
	if n == 0 {
		fmt.Println(colorize(i18n.T("chat.clear_empty"), ColorGray))
		return
	}
	cli.saveCheckpoint()
	cli.history = make([]models.Message, 0)
	cli.syncTranscript()
	if cli.costTracker != nil {
		cli.costTracker.NoteExpectedCacheRebuild()
	}
	_ = ctx
	fmt.Println(colorize(i18n.T("chat.cleared", n), ColorGray))
}

// buildSessionData builds a SessionData from the current CLI state.
// Uses ChatHistory field to store the unified history for backwards compatibility.
func (cli *ChatCLI) buildSessionData() *SessionData {
	sd := &SessionData{
		Version:      models.SessionSchemaVersion,
		ChatHistory:  cli.history,
		TranscriptID: cli.transcriptID(),
		Attachments:  cli.sessionAttachments(),
		Checkpoints:  cli.checkpointRecords(),
		CCRKeys:      ccrKeysIn(cli.history),
	}
	if cli.costTracker != nil {
		sd.CostSessionID = cli.costTracker.Snapshot().SessionID
	}
	return sd
}

// ccrMarkerRe matches the <<ccr:KEY>> markers the compression layer leaves
// in place of archived content.
var ccrMarkerRe = regexp.MustCompile(`<<ccr:([A-Za-z0-9_.-]+)>>`)

// ccrKeysIn lists the distinct CCR keys the history references, in order.
func ccrKeysIn(history []models.Message) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range history {
		for _, sub := range ccrMarkerRe.FindAllStringSubmatch(m.Content, -1) {
			if !seen[sub[1]] {
				seen[sub[1]] = true
				keys = append(keys, sub[1])
			}
		}
	}
	return keys
}

// restoreSessionData restores history from a SessionData.
// Merges legacy separate histories into the unified history for backwards compatibility.
func (cli *ChatCLI) restoreSessionData(sd *SessionData) {
	cli.adoptTranscript(sd.TranscriptID)
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

	cli.checkpoints = cli.restoreCheckpoints(sd.Checkpoints)
	cli.preCompaction = nil
}

// handleSearchSessions runs a full-text search across saved sessions and
// prints the matching sessions with context snippets. It reuses the existing
// JSON session store — no separate index.
func (cli *ChatCLI) handleSearchSessions(query string) {
	hits, err := cli.sessionManager.SearchSessions(query, 3)
	if err != nil {
		fmt.Println(i18n.T("session.search.error", err))
		return
	}
	if len(hits) == 0 {
		fmt.Println(colorize("  "+i18n.T("session.search.none", query), ColorGray))
		return
	}

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("session.search.header", query), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))
	for _, h := range hits {
		fmt.Printf("  %s  %s\n",
			colorize(h.Session, ColorYellow),
			colorize(i18n.T("session.search.match_count", h.Matches), ColorGray))
		for _, snip := range h.Snippets {
			fmt.Printf("      %s\n", colorize(snip, ColorGray))
		}
	}
	fmt.Println()
}

func (cli *ChatCLI) handleListSessions(ctx context.Context) {
	if cli.isRemote {
		rc := cli.getRemoteClient()

		// Fetch both sources
		localSessions, localErr := cli.sessionManager.ListSessions()
		var remoteSessions []string
		var remoteErr error
		if rc != nil {
			ctx, cancel := remoteSessionCtx(ctx)
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
			titles := cli.sessionManager.SessionTitles()
			for _, s := range localSessions {
				if t := titles[s]; t != "" {
					fmt.Printf("  - %s %s\n", s, colorize("— "+t, ColorGray))
				} else {
					fmt.Printf("  - %s\n", s)
				}
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
		fmt.Println(kit.Notice(kit.LevelError, i18n.T("session.error_list", err)))
		return
	}
	if len(sessions) == 0 {
		fmt.Println(i18n.T("session.list_empty"))
		return
	}
	fmt.Println(i18n.T("session.list_header"))
	titles := cli.sessionManager.SessionTitles()
	for _, session := range sessions {
		if t := titles[session]; t != "" {
			fmt.Printf("- %s %s\n", session, colorize("— "+t, ColorGray))
		} else {
			fmt.Printf("- %s\n", session)
		}
	}
}

func (cli *ChatCLI) handleDeleteSession(ctx context.Context, name string) {
	if cli.isRemote {
		rc := cli.getRemoteClient()
		if rc == nil {
			fmt.Println(kit.Notice(kit.LevelError, i18n.T("session.error_delete", fmt.Errorf("remote client unavailable"))))
			return
		}

		// Check both sources
		_, localErr := cli.sessionManager.LoadSession(name)
		ctx, cancel := remoteSessionCtx(ctx)
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
				ctxDel, cancelDel := remoteSessionCtx(ctx)
				defer cancelDel()
				if err := rc.DeleteSession(ctxDel, name); err != nil {
					fmt.Println(i18n.T("session.error_delete", err))
				} else {
					fmt.Println(i18n.T("session.delete_success_remote", name))
				}
			case "both":
				localDelErr := cli.sessionManager.DeleteSession(name)
				ctxDel, cancelDel := remoteSessionCtx(ctx)
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
			ctxDel, cancelDel := remoteSessionCtx(ctx)
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
		fmt.Println(kit.Notice(kit.LevelError, i18n.T("session.error_delete", err)))
	} else {
		fmt.Println(kit.Notice(kit.LevelSuccess, i18n.T("session.delete_success", name)))
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
	sd := cli.buildSessionData()
	sd.ChatHistory = make([]models.Message, len(cli.history))
	copy(sd.ChatHistory, cli.history)
	// A fork is its own timeline: it gets a new transcript journal seeded
	// from the parent's events (two sessions appending to one journal made
	// undo pick whichever fork rewrote last) and keeps the attachments.
	sd.TranscriptID = cli.forkTranscriptJournal()

	// If the current session has a name (was loaded/saved), we can fork from file
	if cli.currentSessionName != "" {
		if err := cli.sessionManager.ForkSession(cli.currentSessionName, newName); err != nil {
			fmt.Println(colorize(fmt.Sprintf("  %s", i18n.T("sess.cmd.fork_error", err)), ColorRed))
			return
		}
		if forked, err := cli.sessionManager.LoadSessionV2(newName); err == nil {
			forked.TranscriptID = sd.TranscriptID
			forked.Attachments = sd.Attachments
			forked.CostSessionID = sd.CostSessionID
			forked.CCRKeys = sd.CCRKeys
			_ = cli.sessionManager.SaveSessionV2(newName, forked)
		}
	} else {
		// Fork from in-memory state
		if err := cli.sessionManager.ForkCurrentToNew(newName, sd); err != nil {
			fmt.Println(colorize(fmt.Sprintf("  %s", i18n.T("sess.cmd.fork_error", err)), ColorRed))
			return
		}
	}

	// Switch to the forked session
	oldName := cli.currentSessionName
	if oldName == "" {
		oldName = i18n.T("sess.cmd.fork_unsaved")
	}
	cli.currentSessionName = newName
	cli.boundRemoteOnly = false

	fmt.Println()
	fmt.Println(uiBox("✅", i18n.T("sess.cmd.fork_header"), ColorGreen))
	p := uiPrefix(ColorGreen)
	fmt.Println(p + fmt.Sprintf("  %s%s%s       %s", ColorGray, i18n.T("sess.cmd.fork_from"), ColorReset, oldName))
	fmt.Println(p + fmt.Sprintf("  %s%s%s     %s", ColorGray, i18n.T("sess.cmd.fork_to"), ColorReset, colorize(newName, ColorCyan)))
	fmt.Println(p + fmt.Sprintf("  %s%s%s %d", ColorGray, i18n.T("sess.cmd.fork_messages"), ColorReset, len(cli.history)))
	fmt.Println(p + colorize("  "+i18n.T("sess.cmd.fork_footer"), ColorGray))
	fmt.Println(uiBoxEnd(ColorGreen))
	fmt.Println()
}
