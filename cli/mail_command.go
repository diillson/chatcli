/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * /mail — human surface over the squad message bus.
 *
 * Lets the user audit recent agent↔agent traffic, see pending inboxes and
 * inject a directive into any agent's inbox (delivered at the recipient's
 * next ReAct turn — e.g. "/mail send coder prioritize the login fix").
 */
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/i18n"
)

// mailCommandRecentN caps how many messages /mail list shows.
const mailCommandRecentN = 20

// handleMailCommand routes /mail subcommands.
func (cli *ChatCLI) handleMailCommand(userInput string) {
	args := strings.Fields(strings.TrimSpace(userInput))
	sub := "list"
	if len(args) >= 2 {
		sub = args[1]
	}
	rest := args[2:]

	switch sub {
	case "list", "history", "ls":
		cli.mailList()
	case "send":
		if len(rest) < 2 {
			fmt.Println(colorize("  "+i18n.T("mail.send.usage"), ColorYellow))
			return
		}
		to := rest[0]
		text := strings.TrimSpace(strings.Join(rest[1:], " "))
		msg, err := mail.Default().Send("user", to, "", text)
		if err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		fmt.Println(colorize("  "+i18n.T("mail.send.ok", msg.To, msg.ID), ColorGreen))
	case "pending":
		cli.mailPending()
	case "help", "-h", "--help":
		cli.printMailUsage()
	default:
		fmt.Println(colorize("  "+i18n.T("mail.unknown", sub), ColorYellow))
		cli.printMailUsage()
	}
}

// mailList shows recent traffic, newest first.
func (cli *ChatCLI) mailList() {
	msgs := mail.Default().Recent(mailCommandRecentN)
	if len(msgs) == 0 {
		fmt.Println(colorize("  "+i18n.T("mail.list.empty"), ColorGray))
		return
	}
	fmt.Println(colorize(fmt.Sprintf("  %s (%d)", i18n.T("mail.list.title"), len(msgs)), ColorCyan+ColorBold))
	for _, m := range msgs {
		line := fmt.Sprintf("    %s %s → %s", m.At.Format("15:04:05"),
			colorize(m.From, ColorBold), colorize(m.To, ColorBold))
		if m.CardID != "" {
			line += colorize(" ["+m.CardID+"]", ColorPurple)
		}
		fmt.Println(line + " — " + truncateForUI(m.Text, 80))
	}
}

// mailPending shows queued (undelivered) message counts per recipient.
func (cli *ChatCLI) mailPending() {
	pending := mail.Default().Pending()
	if len(pending) == 0 {
		fmt.Println(colorize("  "+i18n.T("mail.pending.empty"), ColorGray))
		return
	}
	recipients := make([]string, 0, len(pending))
	for r := range pending {
		recipients = append(recipients, r)
	}
	sort.Strings(recipients)
	fmt.Println(colorize("  "+i18n.T("mail.pending.title"), ColorCyan+ColorBold))
	for _, r := range recipients {
		fmt.Printf("    %s: %d\n", colorize(r, ColorBold), pending[r])
	}
}

// printMailUsage prints /mail usage help.
func (cli *ChatCLI) printMailUsage() {
	fmt.Println(colorize("  "+i18n.T("mail.usage"), ColorGray))
}
