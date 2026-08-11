/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - memory_bootstrap.go
 *
 * The persistent-memory bootstrap card: a short, stable block that tells the
 * model — in every surface's system prompt — that durable cross-session
 * memory EXISTS on this machine and how to navigate it.
 *
 * Why it exists: the memory layers (facts, episodes, saved sessions, board)
 * and their pull tools were all in place, yet a fresh session still opened
 * with the model claiming "each session starts fresh — I have no memory of
 * past interactions". Nothing in the system prompt ever said otherwise: the
 * Memory Index digest lists facts/topics/projects but never mentions saved
 * conversations, and the only anti-amnesia directive in the repo
 * (GatewayMemoryDirective) was gated to the messaging gateway. The model's
 * training prior ("assistants have no memory") wins by default — this card
 * is the explicit counter-evidence, with live counts so it reads as data,
 * not aspiration.
 *
 * Cache discipline: the card is computed ONCE per process (session-start
 * snapshot) and then reused verbatim, so it can live in the CACHED stable
 * prefix of the chat and agent/coder system prompts. Counts drifting
 * mid-session (the memory worker adds facts continuously) must NOT re-render
 * the card — that would invalidate the prompt-cache prefix every few turns.
 *
 * All strings are English model-facing constants, the same rationale as
 * memoryRecallHint and the recall block headers.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/board"
)

// bootstrapMaxBoardTitles caps how many doing-column card titles the board
// line names. The board is a pointer here, not an inventory — @board list
// has the rest.
const bootstrapMaxBoardTitles = 3

// boardStore resolves the work-board store for read-side surfaces (the
// bootstrap card and the knowledge graph). A package variable because
// board.Default() is a process-wide singleton over the user's real
// ~/.chatcli/board.json — hermetic tests point this at a temp file instead.
var boardStore = board.Default

// bootstrapCardHeader opens both variants. The "REAL, CROSS-SESSION" framing
// is deliberate: it must out-argue the model's trained default of denying
// persistent memory.
const bootstrapCardHeader = "[PERSISTENT MEMORY — REAL, CROSS-SESSION]\n" +
	"This machine keeps durable memory for this user across sessions. " +
	"Conversations are auto-saved: earlier sessions exist even when this one starts empty. " +
	"Snapshot at session start:\n"

// bootstrapCardAgentDirective teaches the pull routes agent/coder actually
// have. Mirrors GatewayMemoryDirective's "never deny without pulling first"
// contract, generalized beyond the gateway.
const bootstrapCardAgentDirective = "Never claim you have no memory of past sessions or that each session starts fresh. " +
	"Before answering that you don't know or don't remember something about this user or past work, pull first:\n" +
	`- @session search {"cmd":"search","args":{"query":"<keywords>"}} then @session get — past conversations (including work discussed through MCP tools like Jira/ServiceNow).` + "\n" +
	`- @memory recall {"cmd":"recall","args":{"query":"<keywords>"}} — long-term facts; {"cmd":"timeline"} for dated episodes.` + "\n" +
	"- @board list — the local work board.\n" +
	"If a pull returns nothing relevant, say what memory DOES contain — never deny that memory exists."

// bootstrapCardChatDirective is the chat-mode wording: chat's only sanctioned
// pull is the memory tool, and saved sessions are reachable through the user
// (/session attach) — never through a tool chat does not have.
const bootstrapCardChatDirective = "Never claim you have no memory of past sessions or that each session starts fresh. " +
	"Before answering that you don't know or don't remember something about this user or past work:\n" +
	`- call the memory tool ({"cmd":"recall","args":{"query":"<keywords>"}}) for long-term facts;` + "\n" +
	"- for past conversations, name the saved session that looks relevant (see any [SESSION RECALL] block) and suggest `/session attach <name>`;\n" +
	"- the user can inspect the local work board with /board.\n" +
	"If recall returns nothing relevant, say what memory DOES contain — never deny that memory exists."

// memoryBootstrapCards returns the (chat, agent) variants of the card,
// computing the session-start snapshot on first use. Both are "" when every
// memory surface is empty (a fresh install has nothing to navigate) or when
// memory injection is off entirely.
func (cli *ChatCLI) memoryBootstrapCards() (chatCard, agentCard string) {
	cli.bootstrapCardOnce.Do(func() {
		if loadMemoryMode() == memModeOff {
			return
		}
		lines := cli.bootstrapSnapshotLines()
		if len(lines) == 0 {
			return
		}
		body := bootstrapCardHeader + strings.Join(lines, "\n") + "\n"
		cli.bootstrapCardChat = body + bootstrapCardChatDirective
		cli.bootstrapCardAgent = body + bootstrapCardAgentDirective
	})
	return cli.bootstrapCardChat, cli.bootstrapCardAgent
}

// memoryBootstrapCardChat is the chat-surface accessor.
func (cli *ChatCLI) memoryBootstrapCardChat() string {
	c, _ := cli.memoryBootstrapCards()
	return c
}

// memoryBootstrapCardAgent is the agent/coder-surface accessor.
func (cli *ChatCLI) memoryBootstrapCardAgent() string {
	_, a := cli.memoryBootstrapCards()
	return a
}

// bootstrapSnapshotLines renders the live counts, one line per surface that
// actually has content. Every probe is best-effort: a failing store simply
// drops its line.
func (cli *ChatCLI) bootstrapSnapshotLines() []string {
	var lines []string

	if cli.memoryStore != nil {
		if mgr := cli.memoryStore.Manager(); mgr != nil {
			facts, episodes := 0, 0
			if mgr.Facts != nil {
				facts = len(mgr.Facts.GetAll())
			}
			if mgr.Episodes != nil {
				episodes = mgr.Episodes.Count()
			}
			if facts > 0 || episodes > 0 {
				lines = append(lines, fmt.Sprintf(
					"- Long-term facts: %d · Episodes (dated work log): %d", facts, episodes))
			}
		}
	}

	if cli.sessionManager != nil {
		if names, err := cli.sessionManager.ListSessions(); err == nil && len(names) > 0 {
			line := fmt.Sprintf("- Saved conversations: %d", len(names))
			if name, saved, title := cli.sessionManager.LatestSessionInfo(); name != "" {
				line += fmt.Sprintf(" — latest: %q (%s", name, formatSessionAge(saved))
				if title != "" {
					line += fmt.Sprintf(", %q", title)
				}
				line += ")"
			}
			lines = append(lines, line)
		}
	}

	if line := bootstrapBoardLine(); line != "" {
		lines = append(lines, line)
	}
	return lines
}

// bootstrapBoardLine summarizes in-flight board work (doing column), the
// state most likely to be orphaned by a restart — the in-memory run registry
// that used to be the only reminder path zeroes on every boot.
func bootstrapBoardLine() string {
	cards, err := boardStore().List(board.ColDoing)
	if err != nil || len(cards) == 0 {
		return ""
	}
	titles := make([]string, 0, bootstrapMaxBoardTitles)
	for _, c := range cards {
		if len(titles) >= bootstrapMaxBoardTitles {
			break
		}
		titles = append(titles, fmt.Sprintf("%q", c.Title))
	}
	line := fmt.Sprintf("- Work board: %d card(s) in doing: %s", len(cards), strings.Join(titles, ", "))
	if len(cards) > bootstrapMaxBoardTitles {
		line += ", …"
	}
	return line
}
