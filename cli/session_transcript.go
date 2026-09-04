/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The transcript journal as a first-class record: /session export
 * (Markdown or JSONL of the full journal, not the compacted view),
 * /session transcript search (BM25 over every journaled message) and
 * /session transcript show (replay a range). Falls back to the live
 * history when the journal is off.
 */
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/trajectory"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// transcriptSearchHits is how many hits /session transcript search prints.
const transcriptSearchHits = 8

// fullTranscript returns the journal's messages (source "journal") or the
// live history (source "history") when the journal is unavailable.
func (cli *ChatCLI) fullTranscript() ([]models.Message, string) {
	if cli != nil && cli.transcript != nil && !cli.transcript.disabled && cli.transcript.path != "" {
		if msgs, err := readTranscript(cli.transcript.path); err == nil && len(msgs) > 0 {
			return msgs, "journal"
		}
	}
	if cli == nil {
		return nil, "history"
	}
	return cli.history, "history"
}

// handleSessionExport is /session export <md|jsonl> [path].
func (cli *ChatCLI) handleSessionExport(format, path string) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "md" && format != "markdown" && format != "jsonl" {
		fmt.Println(colorize("  "+i18n.T("session.export.usage"), ColorYellow))
		return
	}
	ext := "md"
	if format == "jsonl" {
		ext = "jsonl"
	}
	msgs, source := cli.fullTranscript()
	if len(msgs) == 0 {
		fmt.Println(colorize("  "+i18n.T("session.export.empty"), ColorGray))
		return
	}
	if path == "" {
		name := cli.currentSessionName
		if name == "" {
			name = "transcript"
		}
		path = fmt.Sprintf("chatcli-%s-%s.%s", sanitizeFileStem(name), time.Now().Format("20060102-150405"), ext)
	}
	if expanded, err := expandUserPath(path); err == nil {
		path = expanded
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- user-chosen export path
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("session.export.error", err), ColorRed))
		return
	}
	var count int
	if ext == "jsonl" {
		count, err = trajectory.WriteJSONL(f, msgs)
	} else {
		_, err = f.WriteString(renderTranscriptMarkdown(msgs, cli.currentSessionName, cli.transcriptID()))
		count = len(msgs)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("session.export.error", err), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("session.export.done", count, path, source), ColorGreen))
}

// sanitizeFileStem keeps a session name usable as a file stem.
func sanitizeFileStem(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "transcript"
	}
	return out
}

// expandUserPath expands ~ and makes the path absolute.
func expandUserPath(p string) (string, error) {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

// renderTranscriptMarkdown renders messages as a readable Markdown
// document: one section per message, tool calls and tool results in
// fenced blocks, system prompts collapsed under a details block.
func renderTranscriptMarkdown(msgs []models.Message, session, transcriptID string) string {
	var b strings.Builder
	title := session
	if title == "" {
		title = "ChatCLI transcript"
	}
	b.WriteString("# " + title + "\n\n")
	b.WriteString("_exported " + time.Now().Format(time.RFC3339) + "_")
	if transcriptID != "" {
		b.WriteString(" · journal `" + transcriptID + "`")
	}
	fmt.Fprintf(&b, " · %d messages\n\n", len(msgs))
	for i, m := range msgs {
		role := m.Role
		if role == "" {
			role = "unknown"
		}
		switch {
		case role == "system":
			fmt.Fprintf(&b, "<details><summary>#%d system</summary>\n\n```text\n%s\n```\n\n</details>\n\n", i+1, strings.TrimSpace(m.Content))
			continue
		case m.ToolCallID != "":
			fmt.Fprintf(&b, "### #%d tool result `%s`\n\n```text\n%s\n```\n\n", i+1, m.ToolCallID, strings.TrimSpace(m.Content))
			continue
		}
		label := role
		if m.Meta != nil && m.Meta.IsSummary {
			label = role + " · summary"
		}
		if m.IsTurnContext() {
			label = role + " · turn context (injected by ChatCLI)"
		}
		fmt.Fprintf(&b, "### #%d %s\n\n", i+1, label)
		if c := strings.TrimSpace(m.Content); c != "" {
			b.WriteString(c + "\n\n")
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "```json\n// tool call %s\n%s\n```\n\n", tc.Name, strings.TrimSpace(tc.ArgumentsJSON()))
		}
	}
	return b.String()
}

// handleSessionTranscript is /session transcript search <query> |
// show <from> [count] | export <md|jsonl> [path] | stats.
func (cli *ChatCLI) handleSessionTranscript(args []string, rest string) {
	if len(args) == 0 {
		fmt.Println(colorize("  "+i18n.T("session.transcript.usage"), ColorYellow))
		return
	}
	switch strings.ToLower(args[0]) {
	case "search", "grep", "find":
		query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), args[0]))
		if query == "" {
			fmt.Println(colorize("  "+i18n.T("session.transcript.search_usage"), ColorYellow))
			return
		}
		cli.searchTranscript(query)
	case "show", "replay":
		from, count := 1, 10
		if len(args) > 1 {
			if n, err := strconv.Atoi(args[1]); err == nil && n > 0 {
				from = n
			}
		}
		if len(args) > 2 {
			if n, err := strconv.Atoi(args[2]); err == nil && n > 0 {
				count = n
			}
		}
		cli.showTranscript(from, count)
	case "export":
		format, path := "", ""
		if len(args) > 1 {
			format = args[1]
		}
		if len(args) > 2 {
			path = args[2]
		}
		cli.handleSessionExport(format, path)
	case "stats", "status":
		msgs, source := cli.fullTranscript()
		chars := 0
		for _, m := range msgs {
			chars += len(m.Content)
		}
		fmt.Println(colorize("  "+i18n.T("session.transcript.stats", len(msgs), FormatPayloadSize(chars), source, cli.transcriptID()), ColorCyan))
	default:
		fmt.Println(colorize("  "+i18n.T("session.transcript.usage"), ColorYellow))
	}
}

// searchTranscript ranks every non-system journaled message with BM25 and
// prints the top hits with their position (usable with show).
func (cli *ChatCLI) searchTranscript(query string) {
	msgs, source := cli.fullTranscript()
	docs := make([]string, len(msgs))
	for i, m := range msgs {
		if m.Role == "system" {
			continue
		}
		docs[i] = m.Content
	}
	hits := ctxmgr.RankDocsBM25(docs, query, transcriptSearchHits)
	if len(hits) == 0 {
		fmt.Println(colorize("  "+i18n.T("session.transcript.search_none", query), ColorGray))
		return
	}
	fmt.Println(colorize("  "+i18n.T("session.transcript.search_header", len(hits), query, source), ColorCyan))
	for _, h := range hits {
		m := msgs[h.Index]
		fmt.Printf("  %s %s %s\n",
			colorize(fmt.Sprintf("#%d", h.Index+1), ColorCyan),
			colorize(m.Role, ColorYellow),
			transcriptSnippet(m.Content, query, 160))
	}
}

// showTranscript prints messages from (1-based) with a length cap each.
func (cli *ChatCLI) showTranscript(from, count int) {
	msgs, source := cli.fullTranscript()
	if from > len(msgs) {
		fmt.Println(colorize("  "+i18n.T("session.transcript.show_range", len(msgs)), ColorGray))
		return
	}
	end := from - 1 + count
	if end > len(msgs) {
		end = len(msgs)
	}
	fmt.Println(colorize("  "+i18n.T("session.transcript.show_header", from, end, len(msgs), source), ColorCyan))
	for i := from - 1; i < end; i++ {
		m := msgs[i]
		body := strings.TrimSpace(m.Content)
		if len(body) > 600 {
			body = truncateRunes(body, 600) + "…"
		}
		fmt.Printf("  %s %s\n", colorize(fmt.Sprintf("#%d", i+1), ColorCyan), colorize(m.Role, ColorYellow))
		for _, line := range strings.Split(body, "\n") {
			fmt.Println("    " + line)
		}
	}
}

// transcriptSnippet returns a window of content around the first query
// term (case-insensitive), single-line.
func transcriptSnippet(content, query string, width int) string {
	flat := strings.Join(strings.Fields(content), " ")
	lower := strings.ToLower(flat)
	pos := -1
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if p := strings.Index(lower, term); p >= 0 && (pos < 0 || p < pos) {
			pos = p
		}
	}
	if pos < 0 {
		pos = 0
	}
	start := pos - width/3
	if start < 0 {
		start = 0
	}
	out := truncateRunes(flat[start:], width)
	if start > 0 {
		out = "…" + out
	}
	if len(flat)-start > width {
		out += "…"
	}
	return out
}

// truncateRunes cuts s to at most n bytes without splitting a rune.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
