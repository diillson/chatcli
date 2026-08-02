/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * @board — builtin tool over the squad work board.
 *
 * The management half of autonomous orchestration: the orchestrator LLM
 * breaks the user's goal into cards, assigns them to worker agent types,
 * moves them across backlog → doing → review → blocked → done as it
 * dispatches <agent_call> workers, records review verdicts and delivery
 * notes, and links agent runs / scheduler jobs to each card. Humans watch
 * the same board via /board.
 *
 * The plugin never imports cli/board directly — the cli package wires a
 * live adapter at boot (SetBoardAdapter), same pattern as @todo/@agents.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/i18n"
)

// BoardAdapter is the contract the cli package fulfills to expose the squad
// board to this plugin. All methods return machine-readable text for the LLM.
type BoardAdapter interface {
	Create(title, description, assignee, column string) (string, error)
	List(column string) (string, error)
	Show(id string) (string, error)
	Move(id, to, by string) (string, error)
	Assign(id, assignee string) (string, error)
	Note(id, author, text string) (string, error)
	Link(id, runID, jobID string) (string, error)
	Archive(olderThan string) (string, error)
}

var (
	boardAdapterMu sync.RWMutex
	boardAdapter   BoardAdapter
)

// SetBoardAdapter installs (or clears, with nil) the live adapter.
func SetBoardAdapter(a BoardAdapter) {
	boardAdapterMu.Lock()
	defer boardAdapterMu.Unlock()
	boardAdapter = a
}

func currentBoardAdapter() BoardAdapter {
	boardAdapterMu.RLock()
	defer boardAdapterMu.RUnlock()
	return boardAdapter
}

// BuiltinBoardPlugin implements the Plugin interface for @board.
type BuiltinBoardPlugin struct{}

// NewBuiltinBoardPlugin creates the builtin @board plugin.
func NewBuiltinBoardPlugin() *BuiltinBoardPlugin { return &BuiltinBoardPlugin{} }

func (p *BuiltinBoardPlugin) Name() string        { return "@board" }
func (p *BuiltinBoardPlugin) Description() string { return i18n.T("plugins.board.description") }
func (p *BuiltinBoardPlugin) Usage() string {
	return "@board create|list|show|move|assign|note|link|archive"
}
func (p *BuiltinBoardPlugin) Version() string { return "1.0.0" }
func (p *BuiltinBoardPlugin) Path() string    { return "[builtin]" }

// Schema documents the tool for the agent system prompt catalog.
func (p *BuiltinBoardPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON",
		"subcommands": []map[string]interface{}{
			{
				"name":        "create",
				"description": "Create a work card. Columns: backlog|doing|review|blocked|done (default backlog).",
				"flags": []map[string]interface{}{
					{"name": "title", "type": "string", "required": true, "description": "short card title"},
					{"name": "description", "type": "string", "required": false, "description": "full task description / acceptance criteria"},
					{"name": "assignee", "type": "string", "required": false, "description": "worker agent type (coder, reviewer, tester, …)"},
					{"name": "column", "type": "string", "required": false, "description": "initial column (default backlog)"},
				},
				"examples": []string{`{"cmd":"create","args":{"title":"Implement /foo","assignee":"coder"}}`},
			},
			{
				"name":        "list",
				"description": "List cards grouped by column. Optional column filter. Default subcommand.",
				"flags": []map[string]interface{}{
					{"name": "column", "type": "string", "required": false, "description": "filter: backlog|doing|review|blocked|done"},
				},
				"examples": []string{`{"cmd":"list"}`, `{"cmd":"list","args":{"column":"review"}}`},
			},
			{
				"name":        "show",
				"description": "Show one card in full: description, notes, history, linked runs/jobs.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "card ID, e.g. card-3"},
				},
				"examples": []string{`{"cmd":"show","args":{"id":"card-3"}}`},
			},
			{
				"name":        "move",
				"description": "Move a card to another column (records the transition).",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "card ID"},
					{"name": "to", "type": "string", "required": true, "description": "target column"},
				},
				"examples": []string{`{"cmd":"move","args":{"id":"card-3","to":"review"}}`},
			},
			{
				"name":        "assign",
				"description": "Set the card's assignee (worker agent type). Empty clears it.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "card ID"},
					{"name": "assignee", "type": "string", "required": true, "description": "worker agent type"},
				},
				"examples": []string{`{"cmd":"assign","args":{"id":"card-3","assignee":"reviewer"}}`},
			},
			{
				"name":        "note",
				"description": "Append a note to a card (review verdict, delivery summary, blocker reason).",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "card ID"},
					{"name": "text", "type": "string", "required": true, "description": "note text"},
					{"name": "author", "type": "string", "required": false, "description": "who wrote it (default orchestrator)"},
				},
				"examples": []string{`{"cmd":"note","args":{"id":"card-3","text":"review FAILED: tests missing"}}`},
			},
			{
				"name":        "link",
				"description": "Link an agent run and/or scheduler job to a card for traceability.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "card ID"},
					{"name": "run_id", "type": "string", "required": false, "description": "agent run ID, e.g. run-7"},
					{"name": "job_id", "type": "string", "required": false, "description": "scheduler job ID"},
				},
				"examples": []string{`{"cmd":"link","args":{"id":"card-3","run_id":"run-7"}}`},
			},
			{
				"name":        "archive",
				"description": "Remove done cards (optionally only those older than a duration like 24h).",
				"flags": []map[string]interface{}{
					{"name": "older_than", "type": "string", "required": false, "description": "Go duration; omit to archive all done cards"},
				},
				"examples": []string{`{"cmd":"archive"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute delegates to ExecuteWithStream.
func (p *BuiltinBoardPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs one @board subcommand.
func (p *BuiltinBoardPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentBoardAdapter()
	if adapter == nil {
		return "", errors.New("@board: no adapter wired (board not available)")
	}

	sub, payload, err := parseBoardInvocation(args)
	if err != nil {
		return "", err
	}

	get := func(keys ...string) string { return strings.TrimSpace(jsonString(payload, keys...)) }

	switch sub {
	case "create", "add", "new":
		title := get("title", "name")
		if title == "" {
			return "", errors.New(`@board create: missing "title"`)
		}
		return adapter.Create(title, get("description", "desc", "body"), get("assignee", "agent"), get("column", "col", "status"))
	case "list", "ls", "status":
		return adapter.List(get("column", "col"))
	case "show", "get", "info":
		id := boardCardID(payload)
		if id == "" {
			return "", errors.New(`@board show: missing "id" (e.g. {"cmd":"show","args":{"id":"card-3"}})`)
		}
		return adapter.Show(id)
	case "move", "transition":
		id := boardCardID(payload)
		to := get("to", "column", "col")
		if id == "" || to == "" {
			return "", errors.New(`@board move: requires "id" and "to" (e.g. {"cmd":"move","args":{"id":"card-3","to":"review"}})`)
		}
		return adapter.Move(id, to, get("by", "author"))
	case "assign":
		id := boardCardID(payload)
		if id == "" {
			return "", errors.New(`@board assign: missing "id"`)
		}
		return adapter.Assign(id, get("assignee", "agent", "to"))
	case "note", "comment":
		id := boardCardID(payload)
		text := get("text", "note", "message", "body")
		if id == "" || text == "" {
			return "", errors.New(`@board note: requires "id" and "text"`)
		}
		return adapter.Note(id, get("author", "by"), text)
	case "link":
		id := boardCardID(payload)
		runID := get("run_id", "runId", "run")
		jobID := get("job_id", "jobId", "job")
		if id == "" || (runID == "" && jobID == "") {
			return "", errors.New(`@board link: requires "id" and at least one of "run_id"/"job_id"`)
		}
		return adapter.Link(id, runID, jobID)
	case "archive", "gc", "clean":
		return adapter.Archive(get("older_than", "olderThan", "age"))
	default:
		return "", fmt.Errorf("@board: unknown subcommand %q (expected create|list|show|move|assign|note|link|archive)", sub)
	}
}

// boardCardID extracts the card ID, tolerating common aliases.
func boardCardID(payload map[string]json.RawMessage) string {
	return strings.TrimSpace(jsonString(payload, "id", "card_id", "cardId", "card"))
}

// parseBoardInvocation resolves subcommand + payload from the canonical JSON
// envelope or a lenient argv form ("show card-3", "move card-3 review").
// No args = list.
func parseBoardInvocation(args []string) (string, map[string]json.RawMessage, error) {
	if len(args) == 0 {
		return "list", nil, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(first), &top); err != nil {
			return "", nil, fmt.Errorf("@board: malformed JSON args: %w", err)
		}
		var sub string
		if cmd, ok := top["cmd"]; ok {
			_ = json.Unmarshal(cmd, &sub)
		}
		if sub == "" {
			sub = "list"
		}
		var inner map[string]json.RawMessage
		if rawInner, ok := top["args"]; ok {
			_ = json.Unmarshal(rawInner, &inner)
		}
		if inner == nil {
			inner = top
		}
		return sub, inner, nil
	}

	// argv fallback. Positional conventions per subcommand:
	//   show|assign|note|move|link <id> [...]; move <id> <to>; note <id> <text...>
	sub := first
	rest := args[1:]
	positional, innerJSON := argvToInnerJSON(rest, nil, nil)
	inner := map[string]json.RawMessage{}
	if innerJSON != "{}" {
		if err := json.Unmarshal([]byte(innerJSON), &inner); err != nil {
			inner = map[string]json.RawMessage{}
		}
	}
	setIfMissing := func(key, value string) {
		if value == "" {
			return
		}
		if _, ok := inner[key]; !ok {
			raw, _ := json.Marshal(value)
			inner[key] = raw
		}
	}
	// Split the positional blob into first token + remainder.
	firstTok, remainder := positional, ""
	if idx := strings.IndexByte(positional, ' '); idx > 0 {
		firstTok, remainder = positional[:idx], strings.TrimSpace(positional[idx+1:])
	}
	switch sub {
	case "create", "add", "new":
		setIfMissing("title", positional)
	case "move", "transition":
		setIfMissing("id", firstTok)
		setIfMissing("to", remainder)
	case "note", "comment":
		setIfMissing("id", firstTok)
		setIfMissing("text", remainder)
	case "assign":
		setIfMissing("id", firstTok)
		setIfMissing("assignee", remainder)
	case "link":
		setIfMissing("id", firstTok)
		setIfMissing("run_id", remainder)
	case "archive", "gc", "clean":
		setIfMissing("older_than", firstTok)
	default:
		setIfMissing("id", firstTok)
	}
	return sub, inner, nil
}
