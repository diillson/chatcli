/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// handleHubCommand implements /hub — inspect and manage the shared cross-channel
// conversation: who you are on the hub, the channel→principal bindings, and
// binding new channel identities to a principal.
//
//	/hub                         show the current principal and conversation
//	/hub whoami                  (alias of the above)
//	/hub bind <platform> <id> [principal]   bind a channel identity (default: self)
//	/hub bindings [principal]    list bindings (admins may filter; users see their own)
func (cli *ChatCLI) handleHubCommand(input string) {
	if cli.hubSync == nil {
		fmt.Println(colorize("  "+i18n.T("hub.cmd.not_connected"), ColorYellow))
		return
	}

	args := strings.Fields(strings.TrimSpace(strings.TrimPrefix(input, "/hub")))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}

	switch sub {
	case "", "whoami", "status":
		convID, principal := cli.hubSync.status()
		fmt.Printf("  %s %s\n", colorize(i18n.T("cfg.hub.principal")+":", ColorCyan), colorize(principal, ColorGray))
		fmt.Printf("  %s %s\n", colorize(i18n.T("cfg.hub.conversation")+":", ColorCyan), colorize(convID, ColorGray))

	case "bind":
		if len(args) < 3 {
			fmt.Println(colorize("  "+i18n.T("hub.cmd.bind_usage"), ColorGray))
			return
		}
		platform, userID := args[1], args[2]
		principal := "" // empty = bind to self
		if len(args) >= 4 {
			principal = args[3]
		}
		if err := cli.hubSync.bind(ctx, platform, userID, principal); err != nil {
			fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
			return
		}
		fmt.Println(colorize("  "+i18n.T("hub.cmd.bound", platform, userID), ColorGreen))

	case "bindings", "list":
		filter := ""
		if len(args) >= 2 {
			filter = args[1]
		}
		bindings, err := cli.hubSync.bindings(ctx, filter)
		if err != nil {
			fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
			return
		}
		if len(bindings) == 0 {
			fmt.Println(colorize("  "+i18n.T("hub.cmd.no_bindings"), ColorGray))
			return
		}
		for _, b := range bindings {
			fmt.Printf("  %s %s\n",
				colorize(b.Platform+":"+b.UserID, ColorCyan),
				colorize("→ "+b.Principal, ColorGray))
		}

	default:
		fmt.Println(colorize("  "+i18n.T("hub.cmd.usage"), ColorGray))
	}
}
