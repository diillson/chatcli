/*
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 * BuiltinAskPlugin — exposes interactive multiple-choice questions to the LLM
 * as the @ask / ask_user tool. The LLM emits 1-6 questions; the user picks
 * answers in a Bubble Tea overlay and the selections come back as the tool
 * result, in the SAME turn (unlike @park which suspends).
 *
 * The plugin is DECLARATIVE: it validates args and provides the schema. The
 * interactive rendering happens in the agent loop (which owns the TTY and the
 * stdin reader), mirroring the coder security gate. ExecuteWithStream is only
 * the FALLBACK path (invoked outside the loop): it returns the non-interactive
 * fallback result — the first option per question.
 */
package plugins

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/cli/agent/ask"
)

// BuiltinAskPlugin is the @ask tool.
type BuiltinAskPlugin struct{}

// NewBuiltinAskPlugin returns a registerable instance.
func NewBuiltinAskPlugin() *BuiltinAskPlugin { return &BuiltinAskPlugin{} }

// Name is the canonical tool name visible to the LLM.
func (*BuiltinAskPlugin) Name() string { return "@ask" }

// Description surfaces the tool in /plugin list and the agent prompt.
func (*BuiltinAskPlugin) Description() string {
	return "Ask the user 1-6 multiple-choice questions and get their decisions interactively. " +
		"Use when you need the user to choose between options before proceeding (e.g. which approach, " +
		"which target, confirm a plan). Each question has a header, options (label + description), " +
		"single or multi-select, and an implicit free-text 'Other' choice. Returns the selections."
}

// Usage explains the canonical invocation form.
func (*BuiltinAskPlugin) Usage() string {
	return `<tool_call name="@ask" args='{"questions":[{"header":"Database","question":"Which database should I use?","options":[{"label":"Postgres","description":"Relational, ACID"},{"label":"SQLite","description":"Embedded, zero-config"}]}]}' />

Fields per question:
  header       short label (1-3 words), required
  question     full question text, required
  multiSelect  allow multiple selections (default false)
  options      [{label, description?}], 1-8 items, required

The user can always type a free-text "Other" answer. Up to 6 questions per call.`
}

// Version is bumped whenever the surface changes.
func (*BuiltinAskPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinAskPlugin) Path() string { return "" }

// Schema returns the structured contract for the text-mode prompt builder.
func (*BuiltinAskPlugin) Schema() string { return ask.SchemaJSON() }

// Execute is the legacy entry-point; defers to ExecuteWithStream.
func (p *BuiltinAskPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream is the NON-INTERACTIVE fallback (see the file header). It
// validates the questions and returns the first-option-per-question fallback so
// callers outside the interactive loop never block. The real interactive path
// is the agent loop's @ask interception.
func (p *BuiltinAskPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	qs, err := ask.ParseRequest(payload)
	if err != nil {
		return ask.ErrorResult(err), err
	}
	return ask.FallbackResult(qs), nil
}
