/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinSkillPlugin — self-authoring skills as an @skill ReAct tool.
 *
 * It lets the agent CREATE and EVOLVE its own skills at runtime: when it learns
 * a reusable procedure, a project convention, or a workflow the user repeats,
 * it writes a SKILL.md into the user's global skills directory, where the loader
 * auto-discovers it on the next turn (and on every future session). This is the
 * "skills that get better over time" capability — inspired by hermes-agent's
 * skill authoring/management, implemented natively against ChatCLI's own skill
 * format.
 *
 * Division of labor with @memory: @memory stores FACTS ("the user prefers X");
 * @skill stores reusable PROCEDURES/KNOWLEDGE with triggers ("how to deploy
 * this project"). The skill is activated automatically when its triggers match
 * a future request.
 *
 * Self-contained: it writes to ~/.chatcli/skills (the same global directory the
 * loader scans and builtin.Seed populates), so no adapter wiring is required.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/persona/usage"
)

// skillNameRe constrains skill names to a safe slug (no path traversal).
var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// skillsDirOverride lets tests redirect the skills directory.
var skillsDirOverride string

// BuiltinSkillPlugin is the @skill tool.
type BuiltinSkillPlugin struct{}

// NewBuiltinSkillPlugin returns a ready-to-register plugin.
func NewBuiltinSkillPlugin() *BuiltinSkillPlugin { return &BuiltinSkillPlugin{} }

// Name returns "@skill".
func (*BuiltinSkillPlugin) Name() string { return "@skill" }

// Description surfaces the tool.
func (*BuiltinSkillPlugin) Description() string {
	return "Author and evolve your own skills. When you learn a reusable procedure, a project convention, or a workflow the user repeats, save it as a skill so it auto-activates in future sessions. Use create/update/list/show/remove."
}

// Usage explains the canonical invocation.
func (*BuiltinSkillPlugin) Usage() string {
	return `<tool_call name="@skill" args='{"cmd":"create","args":{"name":"deploy-this-project","description":"How to deploy this project. Triggers on deploy requests.","triggers":["deploy","ship it"],"content":"# Deploy\n\nRun make release then ..."}}' />

Subcommands (cmd + args):
  create {name, description, content, triggers?, allowed_tools?}
       name           kebab-case slug (a-z, 0-9, dash)
       description    one line; this is what decides relevance — make it good
       content        the skill body (markdown)
       triggers       optional keywords that auto-activate the skill
       allowed_tools  optional tool/capability list
  update {name, ...}   same fields; the skill must already exist
  list                 list saved skills
  show {name}          print a skill's content
  remove {name}        delete a skill
  stats                show activation analytics (which skills earn their keep)
  export {names?, out} bundle skills into a shareable JSON pack
  import {path}        install skills from a pack (overwrite optional)`
}

// Version is semver.
func (*BuiltinSkillPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinSkillPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinSkillPlugin) Schema() string {
	field := func(name, typ string, req bool, desc string) map[string]interface{} {
		return map[string]interface{}{"name": name, "type": typ, "required": req, "description": desc}
	}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "create",
				"description": "Create a new skill that auto-activates on its triggers in future sessions.",
				"flags": []map[string]interface{}{
					field("name", "string", true, "kebab-case slug."),
					field("description", "string", true, "One line; decides relevance."),
					field("content", "string", true, "Skill body (markdown)."),
					field("triggers", "array", false, "Keywords that auto-activate it."),
					field("allowed_tools", "array", false, "Tools the skill may use."),
				},
				"examples": []string{`{"cmd":"create","args":{"name":"deploy-x","description":"How to deploy project X","content":"# Deploy\n...","triggers":["deploy x"]}}`},
			},
			{"name": "update", "description": "Update an existing skill (same fields as create).", "examples": []string{`{"cmd":"update","args":{"name":"deploy-x","content":"# Deploy (v2)\n..."}}`}},
			{"name": "list", "description": "List saved skills.", "examples": []string{`{"cmd":"list"}`}},
			{"name": "show", "description": "Print a skill's content.", "examples": []string{`{"cmd":"show","args":{"name":"deploy-x"}}`}},
			{"name": "remove", "description": "Delete a skill.", "examples": []string{`{"cmd":"remove","args":{"name":"deploy-x"}}`}},
			{"name": "stats", "description": "Show skill activation analytics.", "examples": []string{`{"cmd":"stats"}`}},
			{
				"name":        "export",
				"description": "Bundle skills into a shareable JSON pack.",
				"flags": []map[string]interface{}{
					field("names", "array", false, "Skills to export (empty = all)."),
					field("out", "string", false, "Output pack path (default temp)."),
				},
				"examples": []string{`{"cmd":"export","args":{"names":["deploy-x"],"out":"team-pack.json"}}`},
			},
			{
				"name":        "import",
				"description": "Install skills from a pack.",
				"flags": []map[string]interface{}{
					field("path", "string", true, "Pack file to import."),
					field("overwrite", "boolean", false, "Overwrite existing skills."),
				},
				"examples": []string{`{"cmd":"import","args":{"path":"team-pack.json"}}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinSkillPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

type skillInput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Content      string   `json:"content"`
	Triggers     []string `json:"triggers"`
	AllowedTools []string `json:"allowed_tools"`
	// pack operations
	Names     []string `json:"names"`     // export: which skills (empty = all)
	Out       string   `json:"out"`       // export: output pack path
	Path      string   `json:"path"`      // import: pack path to read
	Overwrite bool     `json:"overwrite"` // import: overwrite existing skills
}

// skillPack is the export/import bundle: each entry carries the raw SKILL.md so
// the round-trip is lossless (no frontmatter re-parsing).
type skillPack struct {
	Version int             `json:"version"`
	Skills  []skillPackItem `json:"skills"`
}

type skillPackItem struct {
	Name    string `json:"name"`
	Content string `json:"content"` // raw SKILL.md
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinSkillPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@skill: empty args. Example: <tool_call name="@skill" args='{"cmd":"list"}' />`)
	}
	cmd, inner, err := parseSkillInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@skill: %w", err)
	}
	dir, err := resolveSkillsDir()
	if err != nil {
		return "", fmt.Errorf("@skill: %w", err)
	}

	switch cmd {
	case "create", "update":
		var in skillInput
		_ = json.Unmarshal([]byte(inner), &in)
		return writeSkill(dir, cmd == "update", in)
	case "list":
		return listSkills(dir)
	case "show":
		var in skillInput
		_ = json.Unmarshal([]byte(inner), &in)
		return showSkill(dir, in.Name)
	case "remove":
		var in skillInput
		_ = json.Unmarshal([]byte(inner), &in)
		return removeSkill(dir, in.Name)
	case "stats":
		return statsSkills(dir)
	case "export":
		var in skillInput
		_ = json.Unmarshal([]byte(inner), &in)
		return exportSkills(dir, in.Names, in.Out)
	case "import":
		var in skillInput
		_ = json.Unmarshal([]byte(inner), &in)
		return importSkills(dir, in.Path, in.Overwrite)
	default:
		return "", fmt.Errorf("@skill: unknown cmd %q (valid: create|update|list|show|remove|stats|export|import)", cmd)
	}
}

// statsSkills reports activation analytics, most-used first, and flags authored
// skills that have never activated (candidates to evolve or remove).
func statsSkills(dir string) (string, error) {
	// The usage file is a sibling of the skills dir (~/.chatcli/skill-usage.json
	// when skills live in ~/.chatcli/skills), so this matches what the manager
	// records to in production and stays isolated under a test override.
	ranking := usage.New(filepath.Join(filepath.Dir(dir), "skill-usage.json")).Ranking()
	var b strings.Builder
	if len(ranking) == 0 {
		b.WriteString("No skill activations recorded yet.")
	} else {
		b.WriteString("Skill activations (most used first):\n")
		used := map[string]bool{}
		for _, r := range ranking {
			used[r.Name] = true
			last := r.LastUsed
			if last == "" {
				last = "—"
			}
			fmt.Fprintf(&b, "  • %s — %d (last: %s)\n", r.Name, r.Count, last)
		}
		// Authored skills with zero activations.
		var unused []string
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if _, statErr := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); statErr == nil && !used[e.Name()] {
					unused = append(unused, e.Name())
				}
			}
		}
		if len(unused) > 0 {
			b.WriteString("\nNever activated (consider evolving or removing):\n  • " + strings.Join(unused, "\n  • "))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// exportSkills bundles the named skills (or all) into a JSON pack at out (or a
// temp file), carrying each raw SKILL.md for a lossless round-trip.
func exportSkills(dir string, names []string, out string) (string, error) {
	var selected []string
	if len(names) > 0 {
		selected = names
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", fmt.Errorf("@skill export: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				if _, statErr := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); statErr == nil {
					selected = append(selected, e.Name())
				}
			}
		}
	}
	if len(selected) == 0 {
		return "", errors.New("@skill export: no skills to export")
	}

	pack := skillPack{Version: 1}
	for _, name := range selected {
		if !skillNameRe.MatchString(strings.TrimSpace(name)) {
			return "", fmt.Errorf("@skill export: invalid name %q", name)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md")) // #nosec G304 -- name slug-validated
		if err != nil {
			return "", fmt.Errorf("@skill export: %q: %w", name, err)
		}
		pack.Skills = append(pack.Skills, skillPackItem{Name: name, Content: string(raw)})
	}

	data, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return "", err
	}
	path := out
	if strings.TrimSpace(path) == "" {
		f, ferr := os.CreateTemp("", "chatcli-skillpack-*.json")
		if ferr != nil {
			return "", ferr
		}
		path = f.Name()
		_ = f.Close()
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	return i18n.T("skill.tool.exported", len(pack.Skills), abs), nil
}

// importSkills installs skills from a JSON pack. Existing skills are skipped
// unless overwrite is set.
func importSkills(dir, path string, overwrite bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New(`@skill import: "path" is required`)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- user-supplied pack path
	if err != nil {
		return "", fmt.Errorf("@skill import: %w", err)
	}
	var pack skillPack
	if err := json.Unmarshal(raw, &pack); err != nil {
		return "", fmt.Errorf("@skill import: invalid pack: %w", err)
	}
	if len(pack.Skills) == 0 {
		return "", errors.New("@skill import: pack contains no skills")
	}

	installed := make([]string, 0, len(pack.Skills))
	var skipped []string
	for _, item := range pack.Skills {
		name := strings.TrimSpace(item.Name)
		if !skillNameRe.MatchString(name) || strings.TrimSpace(item.Content) == "" {
			skipped = append(skipped, item.Name+" (invalid)")
			continue
		}
		file := filepath.Join(dir, name, "SKILL.md")
		if _, statErr := os.Stat(file); statErr == nil && !overwrite {
			skipped = append(skipped, name+" (exists)")
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(file, []byte(item.Content), 0o600); err != nil {
			return "", err
		}
		installed = append(installed, name)
	}
	msg := i18n.T("skill.tool.imported", len(installed))
	if len(installed) > 0 {
		msg += ": " + strings.Join(installed, ", ")
	}
	if len(skipped) > 0 {
		msg += fmt.Sprintf("; skipped %d (%s)", len(skipped), strings.Join(skipped, ", "))
	}
	return msg, nil
}

func writeSkill(dir string, mustExist bool, in skillInput) (string, error) {
	name := strings.TrimSpace(in.Name)
	if !skillNameRe.MatchString(name) {
		return "", fmt.Errorf("@skill: invalid name %q (use kebab-case: a-z, 0-9, dash)", in.Name)
	}
	if strings.TrimSpace(in.Description) == "" {
		return "", errors.New(`@skill: "description" is required`)
	}
	if strings.TrimSpace(in.Content) == "" {
		return "", errors.New(`@skill: "content" is required`)
	}
	skillDir := filepath.Join(dir, name)
	file := filepath.Join(skillDir, "SKILL.md")
	_, statErr := os.Stat(file)
	exists := statErr == nil
	if mustExist && !exists {
		return "", fmt.Errorf("@skill update: %q does not exist (use create)", name)
	}
	if !mustExist && exists {
		return "", fmt.Errorf("@skill create: %q already exists (use update to change it)", name)
	}

	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(renderSkill(name, in)), 0o600); err != nil {
		return "", err
	}
	key := "skill.tool.created"
	if mustExist {
		key = "skill.tool.updated"
	}
	return i18n.T(key, name, file), nil
}

// renderSkill builds the SKILL.md text. Scalars are JSON-encoded, which is valid
// YAML for double-quoted strings — safe for descriptions with colons/quotes.
func renderSkill(name string, in skillInput) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + skillScalar(name) + "\n")
	b.WriteString("description: " + skillScalar(in.Description) + "\n")
	if len(in.AllowedTools) > 0 {
		b.WriteString("allowed-tools: " + skillFlowList(in.AllowedTools) + "\n")
	}
	if len(in.Triggers) > 0 {
		b.WriteString("triggers:\n")
		for _, t := range in.Triggers {
			if strings.TrimSpace(t) == "" {
				continue
			}
			b.WriteString("  - " + skillScalar(t) + "\n")
		}
	}
	b.WriteString("---\n\n")
	body := strings.TrimRight(in.Content, "\n")
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

func listSkills(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "No skills saved yet.", nil
		}
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "No skills saved yet.", nil
	}
	return "Saved skills:\n  • " + strings.Join(names, "\n  • "), nil
}

func showSkill(dir, name string) (string, error) {
	if !skillNameRe.MatchString(strings.TrimSpace(name)) {
		return "", fmt.Errorf("@skill show: invalid name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md")) // #nosec G304 -- name validated against slug regex
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("@skill show: %q not found", name)
		}
		return "", err
	}
	return string(data), nil
}

func removeSkill(dir, name string) (string, error) {
	if !skillNameRe.MatchString(strings.TrimSpace(name)) {
		return "", fmt.Errorf("@skill remove: invalid name %q", name)
	}
	skillDir := filepath.Join(dir, name)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return "", fmt.Errorf("@skill remove: %q not found", name)
	}
	if err := os.RemoveAll(skillDir); err != nil {
		return "", err
	}
	return i18n.T("skill.tool.removed", name), nil
}

// resolveSkillsDir returns the global skills directory (~/.chatcli/skills),
// matching the persona loader and builtin.Seed.
func resolveSkillsDir() (string, error) {
	if skillsDirOverride != "" {
		return skillsDirOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home directory: %w", err)
	}
	return filepath.Join(home, ".chatcli", "skills"), nil
}

func skillScalar(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func skillFlowList(items []string) string {
	b, _ := json.Marshal(items)
	return string(b)
}

func parseSkillInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf("parse envelope: %w", err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalSkillCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: create|update|list|show|remove)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, inner, nil
	}
	if len(args) == 0 {
		return "", "", fmt.Errorf("empty args")
	}
	canon := canonicalSkillCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	// argv form (flattened by the agent): "<cmd> --key value ..." with
	// triggers/allowed_tools/names as repeated flags → arrays.
	primary := "name"
	if canon == "import" {
		primary = "path"
	}
	inner := argvInner(args[1:], primary, map[string]bool{"triggers": true, "allowed_tools": true, "names": true}, nil)
	return canon, inner, nil
}

func canonicalSkillCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "create", "new", "add", "author", "learn":
		return "create"
	case "update", "edit", "evolve":
		return "update"
	case "list", "skills":
		return "list"
	case "show", "view", "get":
		return "show"
	case "remove", "delete", "rm":
		return "remove"
	case "stats", "usage", "analytics":
		return "stats"
	case "export", "pack":
		return "export"
	case "import", "install":
		return "import"
	}
	return ""
}
