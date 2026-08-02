/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent/mail"
)

// liveMailAdapter implements plugins.MailAdapter over the squad message bus,
// with the sender identity fixed to "orchestrator" (the plugin runs inside
// the orchestrator loop). Output is compact English text for the LLM.
type liveMailAdapter struct {
	reg *mail.Registry
}

// newLiveMailAdapter builds the adapter (nil = mail.Default()).
func newLiveMailAdapter(reg *mail.Registry) *liveMailAdapter {
	if reg == nil {
		reg = mail.Default()
	}
	return &liveMailAdapter{reg: reg}
}

// Send implements plugins.MailAdapter.
func (a *liveMailAdapter) Send(to, cardID, text string) (string, error) {
	msg, err := a.reg.Send("orchestrator", to, cardID, text)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("mail sent: orchestrator -> %s (%s); delivered on the recipient's next turn", msg.To, msg.ID), nil
}

// Inbox implements plugins.MailAdapter.
func (a *liveMailAdapter) Inbox() (string, error) {
	msgs := a.reg.Drain("orchestrator")
	if len(msgs) == 0 {
		return "inbox empty", nil
	}
	return mail.FormatInbox(msgs), nil
}

// History implements plugins.MailAdapter.
func (a *liveMailAdapter) History(n int) (string, error) {
	msgs := a.reg.Recent(n)
	if len(msgs) == 0 {
		return "no squad mail yet", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "RECENT SQUAD MAIL (%d, newest first):\n", len(msgs))
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s %s -> %s", m.ID, m.From, m.To)
		if m.CardID != "" {
			fmt.Fprintf(&b, " card=%s", m.CardID)
		}
		fmt.Fprintf(&b, " at=%s: %s\n", m.At.Format("15:04:05"), truncateForUI(m.Text, 120))
	}
	return b.String(), nil
}
