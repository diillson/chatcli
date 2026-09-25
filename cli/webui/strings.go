/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import "github.com/diillson/chatcli/i18n"

// uiKeys are the page's translatable strings. The page carries English
// defaults for each; the server ships the process language's values and
// the page overlays them. Every key exists as web.ui.<key> in every locale
// fragment, which the i18n parity gate and the embed test both check.
var uiKeys = []string{
	"mode.chat", "mode.coder", "mode.agent", "btn.panel",
	"side.sessions", "side.new", "side.search", "side.attach", "side.fork", "side.delete",
	"empty.title", "empty.body", "empty.h1", "empty.h2", "empty.h3", "empty.h4",
	"composer.placeholder", "composer.attach", "composer.image", "composer.cancel", "composer.send", "composer.busy", "composer.images",
	"pane.status", "pane.tools", "pane.skills", "pane.mcp", "pane.memory", "pane.commands", "pane.filter",
	"status.version", "status.provider", "status.model", "status.policy", "status.maxTokens", "status.session", "status.cost", "status.requests", "status.tokens", "status.daily", "status.budgetBlocked", "status.none",
	"perm.title", "perm.deny_always", "perm.deny_once", "perm.allow_always", "perm.allow_once",
	"tool.run", "tool.running", "tool.done", "tool.error", "tool.readonly",
	"skill.view", "skill.use",
	"mcp.connected", "mcp.disconnected", "mcp.tools", "mcp.auth", "mcp.none",
	"memory.none", "commands.none",
	"thought", "cancelled", "error", "copied", "copy",
	"stt.recording", "stt.failed", "tts.speak", "image.prompt", "image.failed",
	"session.bound", "session.new", "session.deleted", "session.confirmDelete", "session.forkName", "session.live",
	"theme", "lang", "offline", "busy",
}

// UIStrings returns the page strings in the process language.
func UIStrings() map[string]string {
	out := make(map[string]string, len(uiKeys))
	for _, k := range uiKeys {
		out[k] = i18n.T("web.ui." + k)
	}
	return out
}
