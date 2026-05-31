/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package palette holds the interactive command-palette overlay and the small
// registry that powers its root listing.
//
// The overlay is completer-driven: a command's subcommands, flags and values
// (models, sessions, config sections, files, …) come live from the same
// suggestion engine the inline prompt uses, so there is a single source of
// truth and nothing to keep in sync by hand. The registry here only describes
// the *root* command list — name, category and one-line summary — so the
// "/menu" entry point can present commands grouped under section headers.
//
// Tokens (command names) are literal CLI syntax and are never translated;
// prose (category labels, summaries) always resolves through an i18n key.
package palette

import "github.com/diillson/chatcli/i18n"

// Category groups commands under section headers in the root listing.
type Category uint8

const (
	CatCore Category = iota
	CatModel
	CatSession
	CatContext
	CatAgent
	CatQuality
	CatIntegrations
	CatScheduler
	CatSystem
)

// categoryKey maps each category to its i18n label key.
var categoryKey = map[Category]string{
	CatCore:         "palette.cat.core",
	CatModel:        "palette.cat.model",
	CatSession:      "palette.cat.session",
	CatContext:      "palette.cat.context",
	CatAgent:        "palette.cat.agent",
	CatQuality:      "palette.cat.quality",
	CatIntegrations: "palette.cat.integrations",
	CatScheduler:    "palette.cat.scheduler",
	CatSystem:       "palette.cat.system",
}

// Label resolves the category's localized header text.
func (c Category) Label() string { return i18n.T(categoryKey[c]) }

// RootCommand describes one top-level command for the root listing.
type RootCommand struct {
	// Name is the full command token, including the leading slash.
	Name string
	// Category places the command under a section header.
	Category Category
	// SummaryKey is the i18n key for the one-line description.
	SummaryKey string
}

// Summary resolves the command's localized one-line description.
func (c RootCommand) Summary() string { return i18n.T(c.SummaryKey) }

// rootCommands mirrors the inline completer's top-level command surface
// (cli.GetInternalCommands), grouped into categories for display. Summary
// keys reuse the existing complete.root.* / help.command.* translations.
var rootCommands = []RootCommand{
	// ── Core ────────────────────────────────────────────────────────────
	{"/help", CatCore, "complete.root.help"},
	{"/menu", CatCore, "complete.root.menu"},
	{"/clear", CatCore, "complete.root.clear"},
	{"/reload", CatCore, "complete.root.reload"},
	{"/version", CatCore, "complete.root.version"},
	{"/cost", CatCore, "complete.root.cost"},
	{"/metrics", CatCore, "complete.root.metrics"},
	{"/ratelimit", CatCore, "complete.root.ratelimit"},
	{"/rewind", CatCore, "complete.root.rewind"},
	{"/nextchunk", CatCore, "complete.root.nextchunk"},
	{"/retry", CatCore, "complete.root.retry"},
	{"/retryall", CatCore, "complete.root.retryall"},
	{"/skipchunk", CatCore, "complete.root.skipchunk"},
	{"/exit", CatCore, "complete.root.exit"},
	{"/quit", CatCore, "complete.root.quit"},

	// ── Model ───────────────────────────────────────────────────────────
	{"/switch", CatModel, "complete.root.switch"},
	{"/provider", CatModel, "complete.root.provider"},
	{"/model", CatModel, "complete.root.model"},
	{"/max-tokens", CatModel, "complete.root.maxtokens"},

	// ── Session ─────────────────────────────────────────────────────────
	{"/session", CatSession, "complete.root.session"},
	{"/newsession", CatSession, "complete.root.newsession"},
	{"/compact", CatSession, "complete.root.compact"},
	{"/export", CatSession, "complete.root.export"},

	// ── Context ─────────────────────────────────────────────────────────
	{"/context", CatContext, "complete.root.context"},
	{"/memory", CatContext, "complete.root.memory"},

	// ── Agent / build ───────────────────────────────────────────────────
	{"/agent", CatAgent, "complete.root.agent"},
	{"/coder", CatAgent, "complete.root.coder"},
	{"/run", CatAgent, "complete.root.run"},
	{"/plan", CatAgent, "complete.root.plan"},
	{"/worktree", CatAgent, "complete.root.worktree"},

	// ── Quality ─────────────────────────────────────────────────────────
	{"/thinking", CatQuality, "complete.root.thinking"},
	{"/refine", CatQuality, "complete.root.refine"},
	{"/verify", CatQuality, "complete.root.verify"},
	{"/reflect", CatQuality, "complete.root.reflect"},
	{"/moa", CatQuality, "complete.root.moa"},

	// ── Integrations ────────────────────────────────────────────────────
	{"/hub", CatIntegrations, "complete.root.hub"},
	{"/connect", CatIntegrations, "complete.root.connect"},
	{"/disconnect", CatIntegrations, "complete.root.disconnect"},
	{"/channel", CatIntegrations, "complete.root.channel"},
	{"/gateway", CatIntegrations, "complete.root.gateway"},
	{"/watch", CatIntegrations, "complete.root.watch"},
	{"/mcp", CatIntegrations, "complete.root.mcp"},
	{"/hooks", CatIntegrations, "complete.root.hooks"},
	{"/plugin", CatIntegrations, "complete.root.plugin"},
	{"/skill", CatIntegrations, "complete.root.skill"},
	{"/websearch", CatIntegrations, "complete.websearch.root_desc"},
	{"/lsp", CatIntegrations, "complete.root.lsp"},

	// ── Scheduler ───────────────────────────────────────────────────────
	{"/schedule", CatScheduler, "help.command.schedule"},
	{"/wait", CatScheduler, "help.command.wait"},
	{"/jobs", CatScheduler, "help.command.jobs"},
	{"/parked", CatScheduler, "help.command.parked"},
	{"/resume", CatScheduler, "help.command.resume"},
	{"/cancel-park", CatScheduler, "help.command.cancel_park"},

	// ── System ──────────────────────────────────────────────────────────
	{"/config", CatSystem, "complete.config.root_desc"},
	{"/auth", CatSystem, "complete.root.auth"},
}

// RootCommands returns the categorized top-level command list.
func RootCommands() []RootCommand { return rootCommands }

// RootSummary returns the localized one-line summary for a command name, and
// whether it is a known root command. Used to describe what a bare command
// does on the palette's "run as-is" entry.
func RootSummary(name string) (string, bool) {
	for _, rc := range rootCommands {
		if rc.Name == name {
			return rc.Summary(), true
		}
	}
	return "", false
}
