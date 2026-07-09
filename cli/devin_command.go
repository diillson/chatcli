/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * /devin — human-facing surface of the Devin integration.
 *
 * Wraps the @devin builtin for direct use from the REPL (create/inspect/
 * steer sessions, secrets/knowledge/playbooks) and adds `watch`: a durable,
 * scheduler-backed poll (ActionDevinPoll) that follows a session in the
 * background and lands its final reply in /jobs — fire-and-forget tracking
 * that survives restarts via the WAL.
 *
 * Works with both API generations (v1 individual/Teams, v3 organizations/
 * enterprise); `/devin` with no args shows which one is active and why.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/devin"
)

// devinSubcommands lists the pass-through @devin subcommands (everything but
// watch/help, which are command-level features).
var devinSubcommands = map[string]bool{
	"run": true, "status": true, "wait": true, "message": true, "messages": true,
	"list": true, "terminate": true, "archive": true, "tags": true, "attach": true,
	"secrets": true, "knowledge": true, "playbooks": true, "info": true,
}

// handleDevinCommand implements /devin …
func (cli *ChatCLI) handleDevinCommand(ctx context.Context, input string) {
	args, err := tokenizeSchedulerInput(strings.TrimPrefix(input, "/devin"))
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	if len(args) == 0 {
		cli.showDevinStatus()
		return
	}
	sub := strings.ToLower(args[0])
	switch {
	case sub == "help":
		cli.printDevinUsage()
	case sub == "watch":
		cli.handleDevinWatch(ctx, args[1:])
	case devinSubcommands[sub]:
		cli.runDevinPassthrough(ctx, args)
	default:
		fmt.Println(colorize("  "+i18n.T("devin.cmd.unknown_sub", sub), ColorYellow))
		cli.printDevinUsage()
	}
}

// runDevinPassthrough executes an @devin subcommand and prints its output.
func (cli *ChatCLI) runDevinPassthrough(ctx context.Context, args []string) {
	plugin, ok := cli.pluginManager.GetPlugin("@devin")
	if !ok || plugin == nil {
		fmt.Println(colorize("  ❌ "+i18n.T("devin.cmd.plugin_missing"), ColorRed))
		return
	}
	out, err := plugin.Execute(ctx, args)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	fmt.Println()
	fmt.Println(out)
}

// handleDevinWatch enqueues the durable scheduler watch for a session.
func (cli *ChatCLI) handleDevinWatch(ctx context.Context, args []string) {
	if !cli.schedulerEnabled() {
		fmt.Println(colorize("  "+i18n.T("scheduler.disabled"), ColorYellow))
		return
	}
	var sessionID string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			sessionID = a
			break
		}
	}
	if sessionID == "" {
		fmt.Println(colorize("  "+i18n.T("devin.cmd.watch_usage"), ColorYellow))
		return
	}
	interval := 30 * time.Second
	if v := stringAfterFlag(args, "--interval"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 5*time.Second {
			interval = d
		}
	}
	watchFor := 6 * time.Hour
	if v := stringAfterFlag(args, "--for"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			watchFor = d
		}
	}

	in := scheduler.ToolInput{
		Name:     "devin-watch:" + sessionID,
		Schedule: &scheduler.Schedule{Kind: scheduler.ScheduleRelative, Relative: time.Second},
		Action: &scheduler.Action{
			Type: scheduler.ActionDevinPoll,
			Payload: map[string]any{
				"session_id":    sessionID,
				"interval":      interval.String(),
				"deadline_unix": time.Now().Add(watchFor).Unix(),
			},
		},
	}
	owner := cli.currentSchedulerOwner()
	enqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if cli.schedulerRemote != nil {
		out, err := cli.schedulerRemote.Enqueue(enqCtx, owner, in)
		if err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		if !out.OK {
			fmt.Println(colorize("  ❌ "+out.Error, ColorRed))
			return
		}
		fmt.Println(colorize("  ✔ "+i18n.T("devin.cmd.watch_created", out.JobID, sessionID), ColorGreen))
		return
	}

	adapter := scheduler.NewToolAdapter(cli.scheduler)
	res, _ := adapter.ScheduleJob(enqCtx, owner, mustJSON(in))
	var out scheduler.ToolOutput
	_ = jsonDecode(res, &out)
	if !out.OK {
		fmt.Println(colorize("  ❌ "+out.Error, ColorRed))
		return
	}
	fmt.Println(colorize("  ✔ "+i18n.T("devin.cmd.watch_created", out.JobID, sessionID), ColorGreen))
}

// showDevinStatus prints the integration status: credential presence, the
// resolved API generation and the pacing knobs.
func (cli *ChatCLI) showDevinStatus() {
	fmt.Println()
	fmt.Println(uiBox("🤖", i18n.T("devin.cmd.box_title"), ColorCyan))
	p := uiPrefix(ColorCyan)

	apiCfg := devin.ResolveAPIConfigFromEnv(cli.logger)
	keyState := i18n.T("devin.cmd.key_missing")
	if apiCfg.APIKey != "" {
		keyState = i18n.T("devin.cmd.key_present")
	}
	baseURL := apiCfg.BaseURL
	if baseURL == "" {
		baseURL = config.DevinDefaultBaseURL
	}
	orgID := apiCfg.OrgID
	if orgID == "" {
		orgID = "-"
	}
	poll := os.Getenv(config.DevinPollIntervalEnv)
	if poll == "" {
		poll = config.DefaultDevinPollInterval.String()
	}
	turn := os.Getenv(config.DevinTurnTimeoutEnv)
	if turn == "" {
		turn = config.DefaultDevinTurnTimeout.String()
	}

	line := func(label, value string) {
		fmt.Println(p + fmt.Sprintf("  %-22s %s", label, value))
	}
	line(config.DevinAPIKeyEnv, keyState)
	line(i18n.T("devin.cmd.api_generation"), apiCfg.ResolveVersion())
	line(config.DevinOrgIDEnv, orgID)
	line(config.DevinBaseURLEnv, baseURL)
	line(config.DevinPollIntervalEnv, poll)
	line(config.DevinTurnTimeoutEnv, turn)
	fmt.Println(p)
	if apiCfg.APIKey == "" {
		fmt.Println(p + "  " + colorize(i18n.T("devin.cmd.setup_hint", config.DevinAPIKeyEnv), ColorYellow))
	}
	fmt.Println(p + "  " + i18n.T("devin.cmd.usage_hint"))
	fmt.Println()
}

// printDevinUsage prints the /devin subcommand cheatsheet.
func (cli *ChatCLI) printDevinUsage() {
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("devin.cmd.usage_title"), ColorCyan))
	for _, line := range []string{
		`/devin run --prompt "…" [--title t] [--tags a,b] [--playbook id] [--mode fast] [--files f1,f2]`,
		"/devin list [--limit n] [--tags a,b]",
		"/devin status --session <id>",
		"/devin wait --session <id> [--timeout 10m]",
		"/devin watch <id> [--interval 30s] [--for 6h]",
		`/devin message --session <id> --message "…" [--files f1]`,
		"/devin messages --session <id> [--limit n]",
		"/devin attach --files f1,f2",
		"/devin tags --session <id> --tags a,b",
		"/devin terminate --session <id>   |   /devin archive --session <id>",
		"/devin secrets --action list|create|delete [--key k --value v --note n | --id id]",
		"/devin knowledge --action list|create|update|delete [--name n --body b --trigger t | --id id]",
		"/devin playbooks --action list|get|create|update|delete [--title t --body b | --id id]",
		"/devin info   |   /devin help",
	} {
		fmt.Println(colorize("    "+line, ColorGray))
	}
	fmt.Println()
}

// stringAfterFlag returns the value following a "--flag" token (or the
// "--flag=value" form) in argv.
func stringAfterFlag(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}
