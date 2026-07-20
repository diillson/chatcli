/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - session_autorecall.go
 *
 * Proactive recall for SAVED SESSIONS, the sibling of the fact auto-recall
 * in memory_autorecall.go. Autosave made every conversation retrievable, but
 * retrieval relied on the model CHOOSING to call @session — and models
 * routinely answer "I don't have that context" while the relevant autosave
 * sits unread in the store. Each agent/coder turn, this ranks the recent
 * sessions against the turn's hints (or against the user's own words when
 * they explicitly reference a past conversation) and floats the top matches
 * as a compact pointer block; the model then pulls detail via @session get.
 *
 * Cache discipline: hint-driven text changes turn to turn, so the block
 * rides in the UNCACHED trailing dynamic block, exactly like the fact
 * auto-recall — never in the stable workspace block.
 *
 * Gated by CHATCLI_SESSION_AUTORECALL (default on). Search is scoped to the
 * most recent sessions (they are what "ontem"/"where we left off" point at)
 * and served from the corpus cache, so the per-turn cost is bounded.
 */
package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/models"
)

const (
	// sessionAutoRecallMaxHits caps how many sessions ride per turn — this
	// is a pointer, not a retrieval; the model pulls detail via @session.
	sessionAutoRecallMaxHits = 2

	// sessionAutoRecallBudget caps the block size in bytes.
	sessionAutoRecallBudget = 900

	// sessionAutoRecallRecentWindow bounds the AMBIENT (hint-driven) per-turn
	// search to the N most recently saved sessions — ranking the whole store
	// on every turn would be wasteful. Explicit recall questions (referential
	// or temporal) search the ENTIRE store: "lembra do que fizemos há 2
	// semanas?" must reach a two-week-old autosave, not just the tail.
	sessionAutoRecallRecentWindow = 30

	// sessionAutoRecallMinMatches is the evidence floor for HINT-driven
	// recall (no explicit reference by the user): at least this many
	// messages of the candidate session must match before the pointer is
	// worth its tokens. Explicit references skip the floor.
	sessionAutoRecallMinMatches = 2
)

// sessionAutoRecallHeader is an English model-facing constant, like
// autoRecallHeader in memory_autorecall.go.
const sessionAutoRecallHeader = "[SESSION RECALL]\n" +
	"Saved past conversations that look relevant. Read them with " +
	`@session get {"name":"...","query":"..."} before claiming you lack that context:` + "\n"

// sessionReferentialRe detects the user explicitly asking about a past
// conversation (PT/EN). A match widens the recall and switches the search
// query to the user's own words, which carry the anchor terms.
var sessionReferentialRe = regexp.MustCompile(`(?i)` +
	`sess[aã]o (anterior|passada)|[uú]ltima sess[aã]o|outra sess[aã]o|` +
	`previous session|last session|other session|` +
	`de onde paramos|onde (a gente|n[oó]s) parou|retomar|continuar de onde|` +
	`where we left off|pick up where|` +
	`o que (a gente|n[oó]s)? ?(fez|fizemos|discutimos|falamos|conversamos|decidimos|vimos)|` +
	`what (did we|we were|have we)|` +
	`lembra (do|da|de|que)|você lembra|do you remember|` +
	`na [uú]ltima (conversa|vez)|last time we`)

// sessionAutoRecallEnabled reads CHATCLI_SESSION_AUTORECALL; unset means on.
func sessionAutoRecallEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_SESSION_AUTORECALL"))) {
	case "false", "0", "off", "no", "disabled":
		return false
	}
	return true
}

// sessionAutoRecallBlock ranks recent saved sessions against the turn and
// renders the top matches as a compact pointer block, or "" when nothing
// relevant (or the gate is off). lastUserMsg is the newest user message —
// when it explicitly references a past conversation, it becomes the query
// and the evidence floor is waived.
func (cli *ChatCLI) sessionAutoRecallBlock(hints []string, lastUserMsg string) string {
	if !sessionAutoRecallEnabled() || cli.sessionManager == nil {
		return ""
	}

	referential := sessionReferentialRe.MatchString(lastUserMsg)

	// A temporal expression in the user's words ("há 2 semanas", "last
	// week", "em 2026-07-05") is an explicit recall request too: scope the
	// search to that date window and drop the expression from the query.
	from, to, cleaned, temporal := memory.ParseTemporalRange(lastUserMsg, time.Now())

	query := strings.Join(hints, " ")
	if (referential || temporal) && strings.TrimSpace(lastUserMsg) != "" {
		query = lastUserMsg
		if temporal && strings.TrimSpace(cleaned) != "" {
			query = cleaned
		}
	}
	if strings.TrimSpace(query) == "" {
		return ""
	}

	// Ambient recall stays cheap (recent window); explicit recall reaches
	// the whole store, optionally date-filtered.
	window := sessionAutoRecallRecentWindow
	if referential || temporal {
		window = 0
	}
	if !temporal {
		from, to = time.Time{}, time.Time{}
	}
	hits, err := cli.sessionManager.searchSessionsFiltered(query, 1, window, from, to)
	if err != nil || len(hits) == 0 {
		return ""
	}
	referential = referential || temporal

	maxHits := 1
	if referential {
		maxHits = sessionAutoRecallMaxHits
	}

	var b strings.Builder
	b.WriteString(sessionAutoRecallHeader)
	written := 0
	for _, h := range hits {
		if written >= maxHits {
			break
		}
		// The live session's own autosave (or MCP mirror) can match the
		// conversation that is already in context — skip it.
		if h.Session == cli.currentSessionName {
			continue
		}
		if !referential && h.Matches < sessionAutoRecallMinMatches {
			continue
		}
		line := "- " + h.Session + " (" + formatSessionAge(h.SavedAt)
		if h.Title != "" {
			line += ", \"" + h.Title + "\""
		}
		line += ")"
		if len(h.Snippets) > 0 {
			line += ": " + strings.TrimSpace(h.Snippets[0])
		}
		if b.Len()+len(line)+1 > sessionAutoRecallBudget {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		written++
	}
	if written == 0 {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatSessionAge renders a save time as a compact model-facing age label.
func formatSessionAge(saved time.Time) string {
	if saved.IsZero() {
		return "age unknown"
	}
	age := time.Since(saved)
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	case age < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	}
}

// lastUserMessage returns the newest user-role message content in history,
// or "" when there is none.
func lastUserMessage(history []models.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return history[i].Content
		}
	}
	return ""
}
