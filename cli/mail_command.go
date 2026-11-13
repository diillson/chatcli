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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/i18n"
)

// squadMailBus resolves the mail bus; a package-level indirection so tests
// can point the command layer at an isolated registry.
var squadMailBus = mail.Default

// mailCommandRecentN is how many messages /mail list shows by default.
const mailCommandRecentN = 20

// handleMailCommand routes /mail subcommands.
func (cli *ChatCLI) handleMailCommand(userInput string) {
	args := strings.Fields(strings.TrimSpace(userInput))
	sub := "list"
	if len(args) >= 2 {
		sub = args[1]
	}
	var rest []string
	if len(args) > 2 {
		rest = args[2:]
	}

	switch sub {
	case "list", "history", "ls":
		cli.mailList(userInput)
	case "send":
		cli.mailSend(rest)
	case "pending":
		cli.mailPending()
	case "peek", "look":
		cli.mailPeek(userInput)
	case "help", "-h", "--help":
		cli.printMailUsage()
	default:
		fmt.Println(colorize("  "+i18n.T("mail.unknown", sub), ColorYellow))
		cli.printMailUsage()
	}
}

// mailSend handles "send <agent> [--card <id>] <text>".
func (cli *ChatCLI) mailSend(words []string) {
	to, cardID, text, err := splitMailSend(words, func(id string) bool {
		_, getErr := squadBoard().Get(id)
		return getErr == nil
	})
	if err != nil {
		printSquadNotice(err.Error())
	}
	if err != nil || text == "" {
		cli.printMailUsage()
		return
	}
	msg, err := squadMailBus().Send("user", to, cardID, text)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	if msg.CardID != "" {
		fmt.Println(colorize("  "+i18n.T("mail.send.ok.card", msg.To, msg.ID, msg.CardID), ColorGreen))
		return
	}
	fmt.Println(colorize("  "+i18n.T("mail.send.ok", msg.To, msg.ID), ColorGreen))
}

// splitMailSend splits a send tail into recipient, card and text. The text
// is prose and is taken as typed — no quoting, and "--words" inside it stay
// text. --card is a flag in two places only: right after the recipient,
// where any other flag is a mistake worth reporting, and as the last two
// words when the id names a real card. Without that check "remember to pass
// --card card-3" lost its ending and gained a card nobody asked for.
func splitMailSend(words []string, cardExists func(id string) bool) (to, cardID, text string, err error) {
	if len(words) == 0 {
		return "", "", "", nil
	}
	isFlag := func(w string) bool { return strings.HasPrefix(w, "--") && len(w) > 2 }
	to, body := words[0], words[1:]
	if strings.HasPrefix(to, "-") {
		// "send --card card-1 coder hi" used to mail a recipient called "--card".
		return "", "", "", errors.New(i18n.T("mail.send.recipient_invalid", to))
	}
	switch n := len(body); {
	case n > 0 && isFlag(body[0]):
		if body[0] != "--card" {
			return "", "", "", errors.New(i18n.T("squad.flag.unknown", body[0]))
		}
		if n < 2 || isFlag(body[1]) {
			return "", "", "", errors.New(i18n.T("squad.flag.missing_value", "--card"))
		}
		cardID, body = body[1], body[2:]
	case n >= 3 && body[n-2] == "--card" && cardExists(body[n-1]):
		cardID, body = body[n-1], body[:n-2]
	}
	return to, cardID, strings.Join(body, " "), nil
}

// mailList shows recent traffic, newest first: "list [n]".
func (cli *ChatCLI) mailList(userInput string) {
	parsed, err := parseSquadTail(userInput, nil)
	if err != nil || len(parsed.pos) > 1 {
		if err != nil {
			printSquadNotice(err.Error())
		}
		cli.printMailUsage()
		return
	}
	limit := mailCommandRecentN
	if raw := parsed.positional(0); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit <= 0 {
			printSquadNotice(i18n.T("mail.history.limit_invalid", raw))
			return
		}
	}
	msgs := squadMailBus().Recent(limit)
	if len(msgs) == 0 {
		fmt.Println(colorize("  "+i18n.T("mail.list.empty"), ColorGray))
		return
	}
	fmt.Println(colorize(fmt.Sprintf("  %s (%d)", i18n.T("mail.list.title"), len(msgs)), ColorCyan+ColorBold))
	for _, m := range msgs {
		fmt.Println(mailLineHuman(m, m.At.Format("15:04:05")+" "+colorize(m.From, ColorBold)+" → "+colorize(m.To, ColorBold), 80))
	}
}

// mailLineHuman renders one message after its lead-in; maxText <= 0 keeps
// the whole text.
func mailLineHuman(m mail.Message, lead string, maxText int) string {
	line := "    " + lead
	if m.CardID != "" {
		line += colorize(" ["+m.CardID+"]", ColorPurple)
	}
	text := m.Text
	if maxText > 0 {
		text = truncateForUI(text, maxText)
	}
	return line + " — " + text
}

// mailPeek shows the queued messages themselves — for one recipient, or
// for every inbox with mail when none is named. Nothing is drained: the
// messages still reach the recipient on its next turn.
func (cli *ChatCLI) mailPeek(userInput string) {
	parsed, err := parseSquadTail(userInput, nil)
	if err != nil || len(parsed.pos) > 1 {
		if err != nil {
			printSquadNotice(err.Error())
		}
		cli.printMailUsage()
		return
	}
	bus := squadMailBus()
	recipients := []string{parsed.positional(0)}
	if recipients[0] == "" {
		recipients = recipients[:0]
		for r := range bus.Pending() {
			recipients = append(recipients, r)
		}
		sort.Strings(recipients)
		if len(recipients) == 0 {
			fmt.Println(colorize("  "+i18n.T("mail.pending.empty"), ColorGray))
			return
		}
	}
	for _, r := range recipients {
		msgs := bus.Peek(r)
		if len(msgs) == 0 {
			fmt.Println(colorize("  "+i18n.T("mail.peek.empty", r), ColorGray))
			continue
		}
		fmt.Println(colorize("  "+i18n.T("mail.peek.title", r, len(msgs)), ColorCyan+ColorBold))
		for _, m := range msgs {
			fmt.Println(mailLineHuman(m, m.ID+" "+m.At.Format("15:04:05")+" "+colorize(m.From, ColorBold), 0))
		}
	}
}

// mailPending shows queued (undelivered) message counts per recipient.
func (cli *ChatCLI) mailPending() {
	pending := squadMailBus().Pending()
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
	printSquadUsage("mail.usage.verbs")
}
