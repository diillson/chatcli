/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * BuiltinDevinPlugin — @devin as a native ReAct tool.
 *
 * Drives Cognition's Devin (the autonomous software engineer) from agent/
 * coder mode and from the /devin command: create sessions, follow their
 * progress, send follow-up messages, upload attachments and manage the org's
 * secrets, knowledge and playbooks.
 *
 * Both API generations are supported transparently via llm/devin: the
 * individual/Teams generation (v1, apk_user_/apk_ keys) and the
 * organizations/enterprise generation (v3, cog_ service users + DEVIN_ORG_ID).
 * DEVIN_API_VERSION forces a generation; the default auto-detects from the
 * credential shape.
 *
 * `run` returns immediately with the session id/URL — a Devin session runs
 * for minutes. Use `wait` for a bounded block until Devin's next turn
 * boundary, `status`/`messages` for cheap polling, or the /devin command's
 * durable watch (scheduler-backed) for fire-and-forget tracking.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/devin"
	"go.uber.org/zap"
)

// devinNewAPI builds the API client from the environment. Overridable in
// tests to point at an httptest server.
var devinNewAPI = func() (devin.API, error) {
	return devin.NewAPI(devin.ResolveAPIConfigFromEnv(zap.NewNop()))
}

// devinReadOnlyCmds are the subcommands that never mutate remote state.
var devinReadOnlyCmds = map[string]bool{
	"status":   true,
	"list":     true,
	"messages": true,
	"wait":     true,
	"info":     true,
}

// devinArgs is the typed view of @devin's JSON input.
type devinArgs struct {
	Cmd      string
	Action   string // secrets/knowledge/playbooks verb: list|get|create|update|delete
	Session  string
	Prompt   string
	Message  string
	Title    string
	Tags     []string
	Playbook string
	Snapshot string
	Mode     string
	Files    []string
	ID       string
	Name     string
	Body     string
	Trigger  string
	Key      string
	Value    string
	Type     string
	Note     string
	Limit    int
	Timeout  string
}

// BuiltinDevinPlugin is the @devin tool.
type BuiltinDevinPlugin struct{}

// NewBuiltinDevinPlugin returns a ready-to-register plugin.
func NewBuiltinDevinPlugin() *BuiltinDevinPlugin { return &BuiltinDevinPlugin{} }

// Name returns "@devin".
func (*BuiltinDevinPlugin) Name() string { return "@devin" }

// Description surfaces the tool in the catalog.
func (*BuiltinDevinPlugin) Description() string { return i18n.T("plugins.devin.description") }

// IsReadOnly reports true only for the query subcommands.
func (*BuiltinDevinPlugin) IsReadOnly(args []string) bool {
	parsed, err := parseDevinArgs(args)
	if err != nil {
		return false
	}
	if devinReadOnlyCmds[parsed.Cmd] {
		return true
	}
	switch parsed.Cmd {
	case "secrets", "knowledge", "playbooks":
		return parsed.Action == "" || parsed.Action == "list" || parsed.Action == "get"
	}
	return false
}

// IsConcurrencySafe mirrors IsReadOnly: reads can fan out, mutations cannot.
func (p *BuiltinDevinPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// DescribeCall surfaces the subcommand and target in the spinner.
func (*BuiltinDevinPlugin) DescribeCall(args []string) string {
	parsed, err := parseDevinArgs(args)
	if err != nil {
		return i18n.T("plugins.devin.description")
	}
	switch parsed.Cmd {
	case "run":
		return i18n.T("plugins.devin.describe_run", truncateDevinText(parsed.Prompt, 60))
	case "message":
		return i18n.T("plugins.devin.describe_message", parsed.Session)
	case "status", "messages", "wait":
		return i18n.T("plugins.devin.describe_status", parsed.Session)
	case "attach":
		return i18n.T("plugins.devin.describe_attach", strings.Join(parsed.Files, ", "))
	}
	return i18n.T("plugins.devin.describe_generic", parsed.Cmd)
}

// Usage explains the canonical invocations (LLM-facing, English by design).
func (*BuiltinDevinPlugin) Usage() string {
	return `<tool_call name="@devin" args='{"cmd":"run","prompt":"Fix the flaky test in repo X and open a PR"}' />

Subcommands (flat JSON or {"cmd":"...","args":{...}} envelope):
  run        prompt (required), title, tags[], playbook, snapshot (v1), mode (v3: fast|lite|ultra|fusion), files[] (local paths uploaded as attachments)
  status     session (required) — state, tags, ACUs, PRs
  wait       session (required), timeout (e.g. "10m") — block until Devin's next turn boundary and return its reply
  message    session (required), message (required), files[] — send a follow-up
  messages   session (required), limit — conversation log
  list       limit, tags[] — recent sessions
  terminate  session (required) — permanently stop the session
  archive    session (required) — archive (v3 enterprise only)
  tags       session (required), tags[] — replace session tags
  attach     files[] (required) — upload files, returns URLs for run/message
  secrets    action=list|create|delete (+ key, value, type, note, id)
  knowledge  action=list|create|update|delete (+ name, body, trigger, id)
  playbooks  action=list|get|create|update|delete (+ title, body, id)
  info       — active API generation (v1 Teams / v3 enterprise) and endpoint

A Devin session runs for MINUTES. Prefer: run (returns session id) → continue
other work or wait/status. Requires DEVIN_API_KEY (and DEVIN_ORG_ID for v3).`
}

// Version is semver.
func (*BuiltinDevinPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinDevinPlugin) Path() string { return "" }

// Schema describes the tool for the LLM catalog.
func (*BuiltinDevinPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "flat JSON preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "run",
				"description": "Create a Devin session for a task. Returns the session id and URL immediately; the session keeps running for minutes.",
				"flags": []map[string]interface{}{
					{"name": "prompt", "type": "string", "required": true, "description": "Task instructions for Devin."},
					{"name": "title", "type": "string", "description": "Session title."},
					{"name": "tags", "type": "array", "description": "Session tags."},
					{"name": "playbook", "type": "string", "description": "Playbook id to apply."},
					{"name": "mode", "type": "string", "description": "v3 agent mode: fast|lite|ultra|fusion."},
					{"name": "files", "type": "array", "description": "Local file paths uploaded and attached to the prompt."},
				},
				"examples": []string{`{"cmd":"run","prompt":"Fix bug X in repo Y and open a PR","tags":["chatcli"]}`},
			},
			{
				"name":        "status",
				"description": "Session state, tags, ACUs consumed and pull requests.",
				"flags": []map[string]interface{}{
					{"name": "session", "type": "string", "required": true, "description": "Session id."},
				},
				"examples": []string{`{"cmd":"status","session":"devin-abc123"}`},
			},
			{
				"name":        "wait",
				"description": "Block (bounded) until Devin reaches its next turn boundary and return its reply.",
				"flags": []map[string]interface{}{
					{"name": "session", "type": "string", "required": true, "description": "Session id."},
					{"name": "timeout", "type": "string", "description": "Max wait, Go duration (default 10m)."},
				},
				"examples": []string{`{"cmd":"wait","session":"devin-abc123","timeout":"10m"}`},
			},
			{
				"name":        "message",
				"description": "Send a follow-up message to a session (auto-resumes a suspended one on v3).",
				"flags": []map[string]interface{}{
					{"name": "session", "type": "string", "required": true, "description": "Session id."},
					{"name": "message", "type": "string", "required": true, "description": "Follow-up instructions."},
					{"name": "files", "type": "array", "description": "Local file paths uploaded and attached."},
				},
				"examples": []string{`{"cmd":"message","session":"devin-abc123","message":"Also add tests"}`},
			},
			{
				"name":        "messages",
				"description": "Conversation log of a session.",
				"flags": []map[string]interface{}{
					{"name": "session", "type": "string", "required": true, "description": "Session id."},
					{"name": "limit", "type": "number", "description": "Show only the last N messages."},
				},
				"examples": []string{`{"cmd":"messages","session":"devin-abc123","limit":10}`},
			},
			{
				"name":        "list",
				"description": "Recent sessions, optionally filtered by tags.",
				"flags": []map[string]interface{}{
					{"name": "limit", "type": "number", "description": "Max sessions (default 20)."},
					{"name": "tags", "type": "array", "description": "Filter by tags."},
				},
				"examples": []string{`{"cmd":"list","limit":10}`},
			},
			{
				"name":        "terminate",
				"description": "Permanently stop a session.",
				"flags": []map[string]interface{}{
					{"name": "session", "type": "string", "required": true, "description": "Session id."},
				},
				"examples": []string{`{"cmd":"terminate","session":"devin-abc123"}`},
			},
			{
				"name":        "archive",
				"description": "Archive a session (v3 enterprise generation only).",
				"flags": []map[string]interface{}{
					{"name": "session", "type": "string", "required": true, "description": "Session id."},
				},
				"examples": []string{`{"cmd":"archive","session":"devin-abc123"}`},
			},
			{
				"name":        "tags",
				"description": "Replace a session's tags.",
				"flags": []map[string]interface{}{
					{"name": "session", "type": "string", "required": true, "description": "Session id."},
					{"name": "tags", "type": "array", "required": true, "description": "New tag set."},
				},
				"examples": []string{`{"cmd":"tags","session":"devin-abc123","tags":["backend","urgent"]}`},
			},
			{
				"name":        "attach",
				"description": "Upload local files; returns attachment URLs usable in run/message.",
				"flags": []map[string]interface{}{
					{"name": "files", "type": "array", "required": true, "description": "Local file paths."},
				},
				"examples": []string{`{"cmd":"attach","files":["./spec.md"]}`},
			},
			{
				"name":        "secrets",
				"description": "Manage org secrets: action=list|create|delete. Values are write-only.",
				"flags": []map[string]interface{}{
					{"name": "action", "type": "string", "required": true, "description": "list|create|delete."},
					{"name": "key", "type": "string", "description": "Secret key (create)."},
					{"name": "value", "type": "string", "description": "Secret value (create)."},
					{"name": "type", "type": "string", "description": "key-value|cookie|totp (default key-value)."},
					{"name": "note", "type": "string", "description": "Purpose note (create)."},
					{"name": "id", "type": "string", "description": "Secret id (delete)."},
				},
				"examples": []string{`{"cmd":"secrets","action":"list"}`},
			},
			{
				"name":        "knowledge",
				"description": "Manage the org knowledge base: action=list|create|update|delete.",
				"flags": []map[string]interface{}{
					{"name": "action", "type": "string", "required": true, "description": "list|create|update|delete."},
					{"name": "name", "type": "string", "description": "Entry name."},
					{"name": "body", "type": "string", "description": "Entry content."},
					{"name": "trigger", "type": "string", "description": "When Devin should use it."},
					{"name": "id", "type": "string", "description": "Entry id (update/delete)."},
				},
				"examples": []string{`{"cmd":"knowledge","action":"list"}`},
			},
			{
				"name":        "playbooks",
				"description": "Manage playbooks: action=list|get|create|update|delete.",
				"flags": []map[string]interface{}{
					{"name": "action", "type": "string", "required": true, "description": "list|get|create|update|delete."},
					{"name": "title", "type": "string", "description": "Playbook title."},
					{"name": "body", "type": "string", "description": "Playbook body."},
					{"name": "id", "type": "string", "description": "Playbook id (get/update/delete)."},
				},
				"examples": []string{`{"cmd":"playbooks","action":"list"}`},
			},
			{
				"name":        "info",
				"description": "Show the active API generation (v1 Teams / v3 enterprise) and configuration status.",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"info"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches to the subcommand.
func (p *BuiltinDevinPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs the subcommand. The streaming callback is unused —
// every call is a bounded request/poll cycle.
func (p *BuiltinDevinPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	parsed, err := parseDevinArgs(args)
	if err != nil {
		return "", fmt.Errorf("@devin: %w", err)
	}
	api, err := devinNewAPI()
	if err != nil {
		return "", fmt.Errorf("@devin: %s", i18n.T("plugins.devin.not_configured", config.DevinAPIKeyEnv, config.DevinOrgIDEnv))
	}
	out, err := runDevinCommand(ctx, api, parsed)
	if err != nil {
		return "", fmt.Errorf("@devin: %w", err)
	}
	return out, nil
}

// runDevinCommand dispatches a parsed command against the API.
func runDevinCommand(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	switch parsed.Cmd {
	case "run":
		return devinCmdRun(ctx, api, parsed)
	case "status":
		return devinCmdStatus(ctx, api, parsed)
	case "wait":
		return devinCmdWait(ctx, api, parsed)
	case "message":
		return devinCmdMessage(ctx, api, parsed)
	case "messages":
		return devinCmdMessages(ctx, api, parsed)
	case "list":
		return devinCmdList(ctx, api, parsed)
	case "terminate":
		if parsed.Session == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "session"))
		}
		if err := api.TerminateSession(ctx, parsed.Session); err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.session_terminated", parsed.Session), nil
	case "archive":
		if parsed.Session == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "session"))
		}
		if err := api.ArchiveSession(ctx, parsed.Session); err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.session_archived", parsed.Session), nil
	case "tags":
		if parsed.Session == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "session"))
		}
		if err := api.SetSessionTags(ctx, parsed.Session, parsed.Tags); err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.tags_updated", parsed.Session, strings.Join(parsed.Tags, ", ")), nil
	case "attach":
		return devinCmdAttach(ctx, api, parsed)
	case "secrets":
		return devinCmdSecrets(ctx, api, parsed)
	case "knowledge":
		return devinCmdKnowledge(ctx, api, parsed)
	case "playbooks":
		return devinCmdPlaybooks(ctx, api, parsed)
	case "info":
		return devinCmdInfo(api), nil
	default:
		return "", errors.New(i18n.T("plugins.devin.unknown_cmd", parsed.Cmd))
	}
}

// devinCmdRun uploads any local files and creates the session.
func devinCmdRun(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	if strings.TrimSpace(parsed.Prompt) == "" {
		return "", errors.New(i18n.T("plugins.devin.missing_arg", "prompt"))
	}
	urls, err := devinUploadFiles(ctx, api, parsed.Files)
	if err != nil {
		return "", err
	}
	session, err := api.CreateSession(ctx, devin.CreateSessionRequest{
		Prompt:         parsed.Prompt,
		Title:          parsed.Title,
		Tags:           parsed.Tags,
		PlaybookID:     parsed.Playbook,
		SnapshotID:     parsed.Snapshot,
		Mode:           parsed.Mode,
		AttachmentURLs: urls,
	})
	if err != nil {
		return "", err
	}
	return i18n.T("plugins.devin.session_created", session.ID, firstNonEmptyDevin(session.URL, session.ID)), nil
}

// devinCmdStatus renders one session's snapshot.
func devinCmdStatus(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	if parsed.Session == "" {
		return "", errors.New(i18n.T("plugins.devin.missing_arg", "session"))
	}
	session, err := api.GetSession(ctx, parsed.Session)
	if err != nil {
		return "", err
	}
	return renderDevinSession(session, true), nil
}

// devinCmdWait blocks (bounded) until the session's next turn boundary.
func devinCmdWait(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	if parsed.Session == "" {
		return "", errors.New(i18n.T("plugins.devin.missing_arg", "session"))
	}
	timeout := 10 * time.Minute
	if parsed.Timeout != "" {
		d, err := time.ParseDuration(parsed.Timeout)
		if err != nil || d <= 0 {
			return "", errors.New(i18n.T("plugins.devin.invalid_timeout", parsed.Timeout))
		}
		timeout = d
	}
	reply, session, err := devin.WaitTurn(ctx, api, parsed.Session, 0, timeout, nil)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(renderDevinSession(session, false))
	if reply != "" {
		b.WriteString("\n\n")
		b.WriteString(i18n.T("plugins.devin.reply_header"))
		b.WriteString("\n")
		b.WriteString(reply)
	}
	return b.String(), nil
}

// devinCmdMessage sends a follow-up (uploading local files first).
func devinCmdMessage(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	if parsed.Session == "" {
		return "", errors.New(i18n.T("plugins.devin.missing_arg", "session"))
	}
	if strings.TrimSpace(parsed.Message) == "" {
		return "", errors.New(i18n.T("plugins.devin.missing_arg", "message"))
	}
	urls, err := devinUploadFiles(ctx, api, parsed.Files)
	if err != nil {
		return "", err
	}
	if err := api.SendMessage(ctx, parsed.Session, parsed.Message, urls); err != nil {
		return "", err
	}
	return i18n.T("plugins.devin.message_sent", parsed.Session), nil
}

// devinCmdMessages renders the session conversation (optionally the tail).
func devinCmdMessages(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	if parsed.Session == "" {
		return "", errors.New(i18n.T("plugins.devin.missing_arg", "session"))
	}
	messages, err := api.ListMessages(ctx, parsed.Session)
	if err != nil {
		return "", err
	}
	if len(messages) == 0 {
		return i18n.T("plugins.devin.no_messages", parsed.Session), nil
	}
	if parsed.Limit > 0 && len(messages) > parsed.Limit {
		messages = messages[len(messages)-parsed.Limit:]
	}
	var b strings.Builder
	b.WriteString(i18n.T("plugins.devin.messages_header", parsed.Session))
	for _, m := range messages {
		author := "devin"
		if m.FromUser {
			author = "user"
		}
		stamp := ""
		if !m.CreatedAt.IsZero() {
			stamp = m.CreatedAt.Format("2006-01-02 15:04") + " "
		}
		fmt.Fprintf(&b, "\n[%s%s] %s", stamp, author, strings.TrimSpace(m.Text))
	}
	return b.String(), nil
}

// devinCmdList renders recent sessions.
func devinCmdList(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	limit := parsed.Limit
	if limit <= 0 {
		limit = 20
	}
	sessions, err := api.ListSessions(ctx, devin.ListSessionsOptions{Limit: limit, Tags: parsed.Tags})
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return i18n.T("plugins.devin.no_sessions"), nil
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	var b strings.Builder
	b.WriteString(i18n.T("plugins.devin.sessions_header", len(sessions)))
	for i := range sessions {
		b.WriteString("\n")
		b.WriteString(renderDevinSession(&sessions[i], false))
	}
	return b.String(), nil
}

// devinCmdAttach uploads local files and lists the resulting URLs.
func devinCmdAttach(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	if len(parsed.Files) == 0 {
		return "", errors.New(i18n.T("plugins.devin.missing_arg", "files"))
	}
	urls, err := devinUploadFiles(ctx, api, parsed.Files)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, u := range urls {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(i18n.T("plugins.devin.attachment_uploaded", filepath.Base(parsed.Files[i]), u))
	}
	return b.String(), nil
}

// devinCmdSecrets manages org secrets (values are write-only by API design).
func devinCmdSecrets(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	switch parsed.Action {
	case "", "list":
		secrets, err := api.ListSecrets(ctx)
		if err != nil {
			return "", err
		}
		if len(secrets) == 0 {
			return i18n.T("plugins.devin.no_secrets"), nil
		}
		var b strings.Builder
		b.WriteString(i18n.T("plugins.devin.secrets_header", len(secrets)))
		for _, s := range secrets {
			fmt.Fprintf(&b, "\n- %s  key=%s type=%s", s.ID, s.Key, s.Type)
			if s.Note != "" {
				fmt.Fprintf(&b, "  (%s)", s.Note)
			}
		}
		return b.String(), nil
	case "create":
		if parsed.Key == "" || parsed.Value == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "key/value"))
		}
		secretType := parsed.Type
		if secretType == "" {
			secretType = "key-value"
		}
		secret, err := api.CreateSecret(ctx, devin.CreateSecretRequest{
			Type: secretType, Key: parsed.Key, Value: parsed.Value, Sensitive: true, Note: parsed.Note,
		})
		if err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.secret_created", secret.ID, secret.Key), nil
	case "delete":
		if parsed.ID == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "id"))
		}
		if err := api.DeleteSecret(ctx, parsed.ID); err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.secret_deleted", parsed.ID), nil
	}
	return "", errors.New(i18n.T("plugins.devin.unknown_action", parsed.Action))
}

// devinCmdKnowledge manages the org knowledge base.
func devinCmdKnowledge(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	switch parsed.Action {
	case "", "list":
		notes, err := api.ListKnowledge(ctx)
		if err != nil {
			return "", err
		}
		if len(notes) == 0 {
			return i18n.T("plugins.devin.no_knowledge"), nil
		}
		var b strings.Builder
		b.WriteString(i18n.T("plugins.devin.knowledge_header", len(notes)))
		for _, n := range notes {
			fmt.Fprintf(&b, "\n- %s  %s", n.ID, n.Name)
			if n.Trigger != "" {
				fmt.Fprintf(&b, " — %s", truncateDevinText(n.Trigger, 80))
			}
		}
		return b.String(), nil
	case "create":
		if parsed.Name == "" || parsed.Body == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "name/body"))
		}
		trigger := parsed.Trigger
		if trigger == "" {
			trigger = parsed.Name
		}
		note, err := api.CreateKnowledge(ctx, devin.KnowledgeNoteRequest{Name: parsed.Name, Body: parsed.Body, Trigger: trigger})
		if err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.knowledge_created", note.ID, note.Name), nil
	case "update":
		if parsed.ID == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "id"))
		}
		note, err := api.UpdateKnowledge(ctx, parsed.ID, devin.KnowledgeNoteRequest{Name: parsed.Name, Body: parsed.Body, Trigger: parsed.Trigger})
		if err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.knowledge_updated", note.ID), nil
	case "delete":
		if parsed.ID == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "id"))
		}
		if err := api.DeleteKnowledge(ctx, parsed.ID); err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.knowledge_deleted", parsed.ID), nil
	}
	return "", errors.New(i18n.T("plugins.devin.unknown_action", parsed.Action))
}

// devinCmdPlaybooks manages playbooks.
func devinCmdPlaybooks(ctx context.Context, api devin.API, parsed devinArgs) (string, error) {
	switch parsed.Action {
	case "", "list":
		playbooks, err := api.ListPlaybooks(ctx)
		if err != nil {
			return "", err
		}
		if len(playbooks) == 0 {
			return i18n.T("plugins.devin.no_playbooks"), nil
		}
		var b strings.Builder
		b.WriteString(i18n.T("plugins.devin.playbooks_header", len(playbooks)))
		for _, p := range playbooks {
			fmt.Fprintf(&b, "\n- %s  %s", p.ID, p.Title)
		}
		return b.String(), nil
	case "get":
		if parsed.ID == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "id"))
		}
		p, err := api.GetPlaybook(ctx, parsed.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s — %s\n\n%s", p.ID, p.Title, p.Body), nil
	case "create":
		if parsed.Title == "" || parsed.Body == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "title/body"))
		}
		p, err := api.CreatePlaybook(ctx, devin.PlaybookRequest{Title: parsed.Title, Body: parsed.Body})
		if err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.playbook_created", p.ID, p.Title), nil
	case "update":
		if parsed.ID == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "id"))
		}
		p, err := api.UpdatePlaybook(ctx, parsed.ID, devin.PlaybookRequest{Title: parsed.Title, Body: parsed.Body})
		if err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.playbook_updated", p.ID), nil
	case "delete":
		if parsed.ID == "" {
			return "", errors.New(i18n.T("plugins.devin.missing_arg", "id"))
		}
		if err := api.DeletePlaybook(ctx, parsed.ID); err != nil {
			return "", err
		}
		return i18n.T("plugins.devin.playbook_deleted", parsed.ID), nil
	}
	return "", errors.New(i18n.T("plugins.devin.unknown_action", parsed.Action))
}

// devinCmdInfo reports the active generation and endpoint.
func devinCmdInfo(api devin.API) string {
	return i18n.T("plugins.devin.info", api.Version())
}

// devinUploadFiles uploads each local path and returns the attachment URLs
// (order preserved).
func devinUploadFiles(ctx context.Context, api devin.API, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	urls := make([]string, 0, len(files))
	for _, path := range files {
		expanded := expandDevinPath(path)
		f, err := os.Open(expanded) //#nosec G304 -- user-provided local path, same trust model as @read
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("plugins.devin.attach_open_failed", path), err)
		}
		att, err := api.UploadAttachment(ctx, filepath.Base(expanded), f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		urls = append(urls, att.URL)
	}
	return urls, nil
}

// renderDevinSession renders one session as compact text. verbose adds the
// structured output when present.
func renderDevinSession(s *devin.Session, verbose bool) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s]", s.ID, s.State)
	if s.Title != "" {
		fmt.Fprintf(&b, " %s", s.Title)
	}
	if s.URL != "" {
		fmt.Fprintf(&b, " — %s", s.URL)
	}
	var details []string
	if len(s.Tags) > 0 {
		details = append(details, "tags: "+strings.Join(s.Tags, ","))
	}
	if s.ACUsConsumed > 0 {
		details = append(details, fmt.Sprintf("ACUs: %.1f", s.ACUsConsumed))
	}
	if !s.UpdatedAt.IsZero() {
		details = append(details, "updated: "+s.UpdatedAt.Format("2006-01-02 15:04"))
	}
	if len(details) > 0 {
		b.WriteString("\n  ")
		b.WriteString(strings.Join(details, " | "))
	}
	for _, pr := range s.PullRequests {
		b.WriteString("\n  PR: " + pr.URL)
		if pr.State != "" {
			b.WriteString(" (" + pr.State + ")")
		}
	}
	if verbose && len(s.StructuredOutput) > 0 && string(s.StructuredOutput) != "null" {
		b.WriteString("\n  structured_output: ")
		b.Write(s.StructuredOutput)
	}
	return b.String()
}

// parseDevinArgs supports flat JSON, the {"cmd","args"} envelope and
// `subcmd --flag value` argv form.
func parseDevinArgs(args []string) (devinArgs, error) {
	out := devinArgs{}
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &top); err != nil {
			return out, fmt.Errorf("malformed JSON args: %w", err)
		}
		merged := top
		if inner, ok := top["args"]; ok {
			var innerMap map[string]json.RawMessage
			if err := json.Unmarshal(inner, &innerMap); err == nil {
				merged = make(map[string]json.RawMessage, len(top)+len(innerMap))
				for k, v := range top {
					merged[k] = v
				}
				for k, v := range innerMap {
					merged[k] = v
				}
			}
		}
		out.Cmd = strings.ToLower(jsonString(merged, "cmd", "command", "subcommand"))
		out.Action = strings.ToLower(jsonString(merged, "action", "op"))
		out.Session = jsonString(merged, "session", "session_id", "sessionid", "devin_id")
		out.Prompt = jsonString(merged, "prompt", "task")
		out.Message = jsonString(merged, "message", "msg", "text")
		out.Title = jsonString(merged, "title")
		out.Playbook = jsonString(merged, "playbook", "playbook_id")
		out.Snapshot = jsonString(merged, "snapshot", "snapshot_id")
		out.Mode = strings.ToLower(jsonString(merged, "mode", "devin_mode"))
		out.ID = jsonString(merged, "id", "note_id", "secret_id")
		out.Name = jsonString(merged, "name")
		out.Body = jsonString(merged, "body", "content")
		out.Trigger = jsonString(merged, "trigger", "trigger_description")
		out.Key = jsonString(merged, "key")
		out.Value = jsonString(merged, "value")
		out.Type = strings.ToLower(jsonString(merged, "type", "secret_type"))
		out.Note = jsonString(merged, "note")
		out.Timeout = jsonString(merged, "timeout", "deadline")
		out.Limit = jsonInt(merged, "limit", "n", "max")
		out.Tags = jsonStringList(merged, "tags")
		out.Files = jsonStringList(merged, "files", "file", "paths", "path")
		return finalizeDevinArgs(out)
	}

	// Bare argv: the first token that is neither a flag nor a flag's value
	// is the subcommand.
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			// "--flag value" consumes the next token as the value so it is
			// never mistaken for the subcommand.
			if !strings.Contains(a, "=") && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
			continue
		}
		if out.Cmd == "" && strings.TrimSpace(a) != "" {
			out.Cmd = strings.ToLower(strings.TrimSpace(a))
			continue
		}
		rest = append(rest, a)
	}
	out.Action = strings.ToLower(stringFromFlagArgs(rest, []string{"action", "op"}))
	out.Session = stringFromFlagArgs(rest, []string{"session", "session-id", "id"})
	out.Prompt = stringFromFlagArgs(rest, []string{"prompt", "task"})
	out.Message = stringFromFlagArgs(rest, []string{"message", "msg", "text"})
	out.Title = stringFromFlagArgs(rest, []string{"title"})
	out.Playbook = stringFromFlagArgs(rest, []string{"playbook", "playbook-id"})
	out.Snapshot = stringFromFlagArgs(rest, []string{"snapshot", "snapshot-id"})
	out.Mode = strings.ToLower(stringFromFlagArgs(rest, []string{"mode"}))
	out.ID = stringFromFlagArgs(rest, []string{"id", "note-id", "secret-id"})
	out.Name = stringFromFlagArgs(rest, []string{"name"})
	out.Body = stringFromFlagArgs(rest, []string{"body", "content"})
	out.Trigger = stringFromFlagArgs(rest, []string{"trigger"})
	out.Key = stringFromFlagArgs(rest, []string{"key"})
	out.Value = stringFromFlagArgs(rest, []string{"value"})
	out.Type = strings.ToLower(stringFromFlagArgs(rest, []string{"type"}))
	out.Note = stringFromFlagArgs(rest, []string{"note"})
	out.Timeout = stringFromFlagArgs(rest, []string{"timeout", "deadline"})
	if v := stringFromFlagArgs(rest, []string{"limit", "n", "max"}); v != "" {
		fmt.Sscanf(v, "%d", &out.Limit) //nolint:errcheck // zero on parse failure is the documented default
	}
	if v := stringFromFlagArgs(rest, []string{"tags"}); v != "" {
		out.Tags = splitDevinCSV(v)
	}
	if v := stringFromFlagArgs(rest, []string{"files", "file", "path"}); v != "" {
		out.Files = splitDevinCSV(v)
	}
	return finalizeDevinArgs(out)
}

// finalizeDevinArgs validates the parsed command.
func finalizeDevinArgs(out devinArgs) (devinArgs, error) {
	if out.Cmd == "" {
		return out, fmt.Errorf(`provide "cmd" (run|status|wait|message|messages|list|terminate|archive|tags|attach|secrets|knowledge|playbooks|info)`)
	}
	return out, nil
}

// jsonStringList accepts a JSON array of strings or a CSV string under any
// of the given keys.
func jsonStringList(raw map[string]json.RawMessage, keys ...string) []string {
	for _, k := range keys {
		val, ok := raw[k]
		if !ok {
			continue
		}
		var list []string
		if err := json.Unmarshal(val, &list); err == nil {
			out := make([]string, 0, len(list))
			for _, item := range list {
				if s := strings.TrimSpace(item); s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
			continue
		}
		var s string
		if err := json.Unmarshal(val, &s); err == nil && strings.TrimSpace(s) != "" {
			return splitDevinCSV(s)
		}
	}
	return nil
}

// splitDevinCSV splits a comma-separated value, trimming blanks.
func splitDevinCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// expandDevinPath expands a leading ~ to the user home directory.
func expandDevinPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
		}
	}
	return path
}

// truncateDevinText caps a string for one-line display.
func truncateDevinText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// firstNonEmptyDevin returns the first non-empty string.
func firstNonEmptyDevin(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
