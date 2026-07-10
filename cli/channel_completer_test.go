/*
 * ChatCLI - Tests for /channel and @-tool subcommand completion
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/mcp/triggers"
	"go.uber.org/zap"
)

// hasSuggest reports whether text appears among the suggestions. (suggestTexts
// lives in scheduler_completer_test.go — shared across the package's tests.)
func hasSuggest(s []prompt.Suggest, text string) bool {
	for _, x := range s {
		if x.Text == text {
			return true
		}
	}
	return false
}

func TestChannelCompletion_SubcommandSlot(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/channel "
	got := cli.getChannelSuggestions(docWithCursor(line, len(line)))
	for _, want := range []string{"list", "inject", "ack", "clear", "pause", "resume", "rules", "confirm", "run"} {
		if !hasSuggest(got, want) {
			t.Errorf("subcommand %q missing from completion; got %v", want, suggestTexts(got))
		}
	}
}

func TestChannelCompletion_ClearFlag(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/channel clear "
	got := cli.getChannelSuggestions(docWithCursor(line, len(line)))
	if !hasSuggest(got, "--all") {
		t.Errorf("`clear` must complete --all; got %v", suggestTexts(got))
	}
	// Prefix filtering still works.
	line = "/channel clear --a"
	got = cli.getChannelSuggestions(docWithCursor(line, len(line)))
	if !hasSuggest(got, "--all") {
		t.Errorf("`clear --a` must still offer --all; got %v", suggestTexts(got))
	}
}

func TestChannelCompletion_RulesReload(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/channel rules "
	got := cli.getChannelSuggestions(docWithCursor(line, len(line)))
	if !hasSuggest(got, "reload") {
		t.Errorf("`rules` must complete reload; got %v", suggestTexts(got))
	}
}

func TestChannelCompletion_ConfirmIDsAndDecision(t *testing.T) {
	cli := newCompleterTestCLI(t)
	cli.mcpManager = mcp.NewManagerWithOptions(zap.NewNop(), mcp.ChannelManagerOptions{PersistDir: t.TempDir()})
	st := &channelTriggerState{
		pendingConfirm: map[uint64]triggers.Action{
			7: {ID: 7, Mode: triggers.ModeConfirm},
			3: {ID: 3, Mode: triggers.ModeConfirm},
		},
	}
	cli.channelTriggers = st

	line := "/channel confirm "
	got := cli.getChannelSuggestions(docWithCursor(line, len(line)))
	if !hasSuggest(got, "3") || !hasSuggest(got, "7") {
		t.Errorf("`confirm` must complete pending IDs 3 and 7; got %v", suggestTexts(got))
	}

	// After an ID, the decision slot offers `no`.
	line = "/channel confirm 7 "
	got = cli.getChannelSuggestions(docWithCursor(line, len(line)))
	if !hasSuggest(got, "no") {
		t.Errorf("`confirm <id>` must complete `no`; got %v", suggestTexts(got))
	}
}

func TestChannelCompletion_RunSeqs(t *testing.T) {
	cli := newCompleterTestCLI(t)
	m := mcp.NewManagerWithOptions(zap.NewNop(), mcp.ChannelManagerOptions{PersistDir: t.TempDir()})
	m.Channels().Push(mcp.ChannelMessage{ServerName: "srv", Channel: "alerts", Content: "disk full"})
	cli.mcpManager = m

	line := "/channel run "
	got := cli.getChannelSuggestions(docWithCursor(line, len(line)))
	if len(got) == 0 {
		t.Fatal("`run` must complete recent message seqs")
	}
	// The pushed message got seq 1.
	if !hasSuggest(got, "1") {
		t.Errorf("`run` must offer seq 1; got %v", suggestTexts(got))
	}
	if !strings.Contains(got[0].Description, "srv/alerts") {
		t.Errorf("seq suggestion should describe the message, got %q", got[0].Description)
	}
}

func TestAtToolSubcommandCompletion(t *testing.T) {
	cli := newCompleterTestCLI(t)
	cases := map[string][]string{
		"@channels ": {"list", "unread", "ack"},
		"@tools ":    {"list", "describe"},
	}
	for line, want := range cases {
		args := strings.Fields(line)
		got, matched := cli.completeAtTokenArgs(args, line, "")
		if !matched {
			t.Errorf("%q must be handled by completeAtTokenArgs", line)
			continue
		}
		for _, w := range want {
			if !hasSuggest(got, w) {
				t.Errorf("%q must complete %q; got %v", line, w, suggestTexts(got))
			}
		}
	}
}
