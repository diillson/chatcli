/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2026 Edilson Freitas
 * License: MIT
 */

package webui

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/i18n"
)

// slashModes maps the mode-switch tokens to the page's session modes, the
// same table the ACP surface honors.
var slashModes = map[string]string{
	"/chat":  "chat",
	"/agent": "agent",
	"/run":   "agent",
	"/coder": "coder",
}

// chatContextTools are the "@" mentions the chat turn itself expands into
// context (file, git, env, shell history). They are never run as tools.
var chatContextTools = map[string]bool{"file": true, "git": true, "env": true, "history": true}

// splitLeadingToken splits "token rest of line" at the first whitespace.
func splitLeadingToken(text string) (token, rest string) {
	text = strings.TrimSpace(text)
	idx := strings.IndexFunc(text, unicode.IsSpace)
	if idx < 0 {
		return text, ""
	}
	return text[:idx], strings.TrimSpace(text[idx:])
}

// routeInline mirrors the terminal for a composer line before it reaches the
// model: "/coder", "/agent", "/chat" switch the page's mode; an advertised
// slash command runs headless and answers in the transcript; a known REPL
// command that cannot run here says so; and in chat mode a leading "@tool"
// runs that tool directly, the way `chatcli tool` does, with its output as
// a tool block. It returns true when the line was fully handled; otherwise
// mode and text may have been rewritten ("/coder fix it" runs "fix it" in
// coder mode) and the caller continues with the model.
func (s *Server) routeInline(ctx context.Context, rn *run, session string, mode, text *string) bool {
	line := strings.TrimSpace(*text)
	switch {
	case strings.HasPrefix(line, "/"):
		return s.routeSlash(ctx, rn, session, line, mode, text)
	case strings.HasPrefix(line, "@") && normalizeMode(*mode) == "chat":
		return s.routeTool(ctx, rn, line)
	}
	return false
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "coder":
		return "coder"
	case "agent":
		return "agent"
	}
	return "chat"
}

func (s *Server) routeSlash(ctx context.Context, rn *run, session, line string, mode, text *string) bool {
	token, rest := splitLeadingToken(line)
	if id, isMode := slashModes[strings.ToLower(token)]; isMode {
		rn.sink.push(event{Type: "mode", Mode: id, Text: i18n.T("web.mode_switched", i18n.T("web.ui.mode."+id))})
		if rest == "" {
			rn.sink.push(event{Type: "done"})
			return true
		}
		*mode, *text = id, rest
		return false
	}
	if s.commandAdvertised(token) {
		out, err := s.opts.Backend.RunCommand(ctx, session, line)
		if err != nil {
			rn.sink.push(event{Type: "error", Error: i18n.T("web.command_failed", err.Error())})
			return true
		}
		rn.sink.push(event{Type: "done", Reply: out})
		return true
	}
	if _, known := palette.RootSummary(token); known {
		rn.sink.push(event{Type: "done", Reply: i18n.T("web.command_unsupported", token)})
		return true
	}
	// Anything else is user text: a slash-command template expands inside
	// the turn, and a path or a typo is never hijacked.
	return false
}

// commandAdvertised reports whether the token names a command the backend
// runs headless. Catalog names come with or without the leading slash.
func (s *Server) commandAdvertised(token string) bool {
	name := strings.TrimPrefix(token, "/")
	for _, c := range s.opts.Backend.Commands() {
		if strings.TrimPrefix(c.Name, "/") == name {
			return true
		}
	}
	return false
}

func (s *Server) routeTool(ctx context.Context, rn *run, line string) bool {
	token, args := splitLeadingToken(line)
	name := strings.TrimPrefix(token, "@")
	if name == "" || chatContextTools[strings.ToLower(name)] || !s.toolKnown(name) {
		return false
	}
	id := rn.id + "-tool"
	started := time.Now()
	rn.sink.push(event{Type: "tool_start", Tool: &toolEvent{ID: id, Name: "@" + name, Kind: "tool", Status: "running", Input: args}})
	out, err := s.opts.Backend.CallTool(ctx, name, args)
	te := &toolEvent{ID: id, Name: "@" + name, Kind: "tool", Status: "done", Input: args, Output: out, Duration: time.Since(started).Milliseconds()}
	if err != nil {
		te.Status, te.IsError, te.Output = "error", true, err.Error()
	}
	rn.sink.push(event{Type: "tool_end", Tool: te})
	rn.sink.push(event{Type: "done"})
	return true
}

func (s *Server) toolKnown(name string) bool {
	for _, tl := range s.opts.Backend.Tools() {
		if strings.EqualFold(strings.TrimPrefix(tl.Name, "@"), name) {
			return true
		}
	}
	return false
}
