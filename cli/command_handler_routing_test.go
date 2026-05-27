package cli

import "testing"

// TestCommandRoutingCoverage guards the table-driven dispatcher: every known
// command must resolve to a handler, and non-commands must fall through to the
// default (chat/skill) path. It exercises lookup only (no handler is invoked),
// so it needs no live ChatCLI.
func TestCommandRoutingCoverage(t *testing.T) {
	ch := NewCommandHandler(&ChatCLI{})

	known := []string{
		"/exit", "exit", "/quit", "quit", "/reload", "/help",
		"/version", "/v", "/nextchunk", "/retry", "/retryall", "/skipchunk",
		"/newsession", "/disconnect", "/rewind", "/metrics", "/cost",
		"/reset", "/redraw", "/clear",
		"/agent", "/agent build a thing", "/run task", "/coder fix", "/switch openai",
		"/config", "/config providers", "/status", "/settings",
		"/session list", "/context show", "/auth login", "/plugin list",
		"/skill list", "/connect host", "/watch x", "/compact", "/memory",
		"/mcp", "/hooks", "/ratelimit", "/limits", "/export", "/export f.jsonl",
		"/moa hi", "/thinking", "/plan x", "/refine", "/verify", "/reflect",
		"/worktree", "/schedule x", "/wait x", "/jobs", "/parked", "/resume x",
		"/cancel-park", "/channel", "/websearch x",
	}
	for _, in := range known {
		if _, ok := ch.lookup(in); !ok {
			t.Errorf("expected a route for %q", in)
		}
	}

	notCommands := []string{"/totallyunknown", "hello world", "/exporting", "/moax", "just text"}
	for _, in := range notCommands {
		if _, ok := ch.lookup(in); ok {
			t.Errorf("did not expect a route for %q", in)
		}
	}
}

func TestPrefixRouteMatches(t *testing.T) {
	word := prefixRoute{prefix: "/export", word: true}
	if !word.matches("/export") || !word.matches("/export f") {
		t.Error("word route should match exact and prefix+space")
	}
	if word.matches("/exporting") || word.matches("/exportx") {
		t.Error("word route must not match a longer token")
	}

	raw := prefixRoute{prefix: "/session", word: false}
	if !raw.matches("/session") || !raw.matches("/session list") || !raw.matches("/sessionx") {
		t.Error("raw route should match by bare prefix")
	}
	if raw.matches("/sess") {
		t.Error("raw route must not match a shorter string")
	}
}
