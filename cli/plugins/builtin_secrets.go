/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * BuiltinSecretsPlugin — leaked-credential scanner as the @secrets ReAct
 * tool: what a reviewer runs before a commit. It walks a tree (or the git
 * index, or one inline text) against a curated rule table plus a Shannon
 * entropy check, and reports every hit with the matched value REDACTED —
 * the model sees where a secret is, never the secret itself. Pure Go, no
 * network, bounded to the workspace by the engine's path policy.
 */
package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/coder/engine"
)

const (
	secretsMaxFileBytes     = 1 << 20
	secretsDefaultMaxFiles  = 5000
	secretsMaxReportedLines = 500
	scanIgnoreFile          = ".secretsignore"
	scanInlineMarker        = "secrets:ignore"
	secretsMinEntropy       = 4.0
)

// secretRule is one detector: a regexp whose capture group (group 0 when
// none) is the secret value to redact.
type secretRule struct {
	id, desc, severity string
	re                 *regexp.Regexp
	group              int
}

var secretRules = []secretRule{
	{"aws-access-key-id", "AWS access key id", "high", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"aws-secret-key", "AWS secret access key assignment", "critical", regexp.MustCompile(`(?i)aws[\w\-]*secret[\w\-]*\s*[:=]\s*['"]?([A-Za-z0-9/+=]{40})\b`), 1},
	{"github-token", "GitHub token (ghp/gho/ghu/ghs/ghr/github_pat)", "critical", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})\b`), 0},
	{"gitlab-pat", "GitLab personal access token", "critical", regexp.MustCompile(`\bglpat-[A-Za-z0-9\-_]{20,}\b`), 0},
	{"slack-token", "Slack token", "critical", regexp.MustCompile(`\bxox[abporse]-[A-Za-z0-9\-]{10,}\b`), 0},
	{"slack-webhook", "Slack incoming webhook URL", "high", regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Za-z0-9]+/B[A-Za-z0-9]+/[A-Za-z0-9]{20,}`), 0},
	{"google-api-key", "Google API key", "high", regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`), 0},
	{"stripe-live-key", "Stripe live secret/restricted key", "critical", regexp.MustCompile(`\b[sr]k_live_[0-9a-zA-Z]{20,}\b`), 0},
	{"twilio-api-key", "Twilio API key SID", "high", regexp.MustCompile(`\bSK[0-9a-fA-F]{32}\b`), 0},
	{"sendgrid-key", "SendGrid API key", "critical", regexp.MustCompile(`\bSG\.[A-Za-z0-9_\-]{16,}\.[A-Za-z0-9_\-]{16,}\b`), 0},
	{"sk-api-key", "OpenAI/Anthropic-style sk- API key", "critical", regexp.MustCompile(`\bsk-(?:proj-|ant-[a-z0-9]+-)?[A-Za-z0-9_\-]{20,}\b`), 0},
	{"jwt", "JSON Web Token", "medium", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`), 0},
	{"private-key", "Private key PEM block", "critical", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP |ENCRYPTED )?PRIVATE KEY(?: BLOCK)?-----`), 0},
	{"connection-string", "Connection string with embedded password", "high", regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|amqps?|rediss?|mssql|jdbc:[a-z]+)://[^\s:/@'"]+:([^\s@'"]{3,})@`), 1},
	{"basic-auth-url", "URL with basic-auth credentials", "high", regexp.MustCompile(`(?i)\bhttps?://[^\s:/@'"]+:([^\s@'"]{3,})@[^\s'"]+`), 1},
	{"generic-assignment", "Password/secret/token/api key assigned a literal", "medium", regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_\-]?key|access[_\-]?key)[\w\-]*\s*[:=]\s*['"]([^'"\s]{8,})['"]`), 1},
	{"high-entropy-secret", "High-entropy value assigned to a secret-like name", "high", regexp.MustCompile(`(?i)\b(?:password|passwd|secret|token|api[_\-]?key|access[_\-]?key|private[_\-]?key|credential)[\w\-]*\s*[:=]\s*['"]?([A-Za-z0-9+/=_\-]{20,})`), 1},
}

// secretsPlaceholder recognizes values that only look like secrets:
// templates, env references, masked examples and documentation fillers.
var secretsPlaceholder = regexp.MustCompile(`(?i)^(?:[x*.\-#]+|\$\{.*|\$[A-Z_].*|<.*|\{\{.*|%.*%)$|example|changeme|placeholder|your[_\-]?|redacted|dummy|sample`)

var secretsSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true}

type secretFinding struct {
	path, rule, severity, preview string
	line                          int
}

// BuiltinSecretsPlugin is the @secrets tool.
type BuiltinSecretsPlugin struct{}

// NewBuiltinSecretsPlugin returns a ready-to-register plugin.
func NewBuiltinSecretsPlugin() *BuiltinSecretsPlugin { return &BuiltinSecretsPlugin{} }

// Name returns "@secrets".
func (*BuiltinSecretsPlugin) Name() string { return "@secrets" }

// Description is i18n-resolved for the catalog.
func (*BuiltinSecretsPlugin) Description() string { return i18n.T("plugins.secrets.description") }

// Usage explains the verbs.
func (*BuiltinSecretsPlugin) Usage() string {
	return `<tool_call name="@secrets" args='{"cmd":"scan","args":{"path":"."}}' />

Subcommands (cmd + args):
  scan {path?, include?, max_files?, staged?}
        scan a file or tree for leaked credentials. path defaults to ".";
        include is a glob (e.g. "**/*.env"); staged:true scans only the files
        in the git index (git diff --cached). Skips binaries, files > 1 MiB,
        .git/node_modules/vendor/dist/build; honors .secretsignore (one glob or
        rule id per line) and an inline "secrets:ignore" marker on a line.
  check {text}         scan inline text
  rules {}             list the rule ids with severity
Every finding is printed as "path:line rule severity <redacted preview>";
the matched value is never echoed in full.`
}

// Version is semver.
func (*BuiltinSecretsPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinSecretsPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinSecretsPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "scan",
				"description": "Scan a file or directory tree for leaked credentials; values are redacted in the report.",
				"flags": []map[string]interface{}{
					{"name": "path", "type": "string", "required": false, "description": "File or directory inside the workspace. Default: current directory."},
					{"name": "include", "type": "string", "required": false, "description": "Glob restricting the files scanned (basename or **/ path glob)."},
					{"name": "max_files", "type": "integer", "required": false, "description": "Stop after this many files. Default 5000."},
					{"name": "staged", "type": "boolean", "required": false, "description": "Scan only the files staged in git (git diff --cached --name-only)."},
				},
				"examples": []string{`{"cmd":"scan","args":{"path":"."}}`, `{"cmd":"scan","args":{"staged":true}}`, `{"cmd":"scan","args":{"path":"config","include":"**/*.yaml"}}`},
			},
			{
				"name":        "check",
				"description": "Scan an inline text (a diff, a config snippet) for credentials.",
				"flags": []map[string]interface{}{
					{"name": "text", "type": "string", "required": true, "description": "Text to scan."},
				},
				"examples": []string{`{"cmd":"check","args":{"text":"export TOKEN=..."}}`},
			},
			{
				"name":        "rules",
				"description": "List the detection rule ids, severities and descriptions.",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"rules"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinSecretsPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinSecretsPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	cmd, in := "scan", map[string]json.RawMessage{}
	if len(args) > 0 {
		var err error
		if cmd, in, err = parseSecretsInvocation(args); err != nil {
			return "", fmt.Errorf("@secrets: %w", err)
		}
	}
	switch cmd {
	case "rules":
		var b strings.Builder
		for _, r := range secretRules {
			fmt.Fprintf(&b, "%-22s %-8s %s\n", r.id, r.severity, r.desc)
		}
		return b.String(), nil
	case "check":
		text := jsonString(in, "text", "content", "input")
		if text == "" {
			return "", errors.New(`@secrets check: "text" is required`)
		}
		return renderSecretFindings(scanSecretText("<text>", []byte(text), nil), 1, 0), nil
	}
	return scanSecretsPath(ctx, in)
}

// scanSecretsPath walks the requested tree (or the git index) and reports.
func scanSecretsPath(ctx context.Context, in map[string]json.RawMessage) (string, error) {
	root := jsonString(in, "path", "dir", "file")
	if root == "" {
		root = "."
	}
	if err := engine.ValidateReadPath(root); err != nil {
		return "", fmt.Errorf("@secrets scan: %w", err)
	}
	maxFiles := jsonInt(in, "max_files", "maxFiles")
	if maxFiles <= 0 {
		maxFiles = secretsDefaultMaxFiles
	}
	include := jsonString(in, "include", "glob")
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("@secrets scan: %w", err)
	}
	dir := root
	if !info.IsDir() {
		dir = filepath.Dir(root)
	}
	ignoreGlobs, disabled := loadSecretsIgnore(dir)
	var files []string
	switch {
	case jsonBool(in, "staged"):
		if files, err = gitStagedFiles(ctx, dir); err != nil {
			return "", fmt.Errorf("@secrets scan: %w", err)
		}
	case !info.IsDir():
		files = []string{root}
	default:
		files, err = walkSecretsTree(ctx, root, maxFiles)
		if err != nil {
			return "", fmt.Errorf("@secrets scan: %w", err)
		}
	}
	var findings []secretFinding
	scanned, skipped := 0, 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		rel := secretsRel(dir, f)
		if (include != "" && !docsFlattenGlobAny(rel, []string{include})) || docsFlattenGlobAny(rel, ignoreGlobs) {
			skipped++
			continue
		}
		data, ok := readSecretsFile(f)
		if !ok {
			skipped++
			continue
		}
		scanned++
		findings = append(findings, scanSecretText(rel, data, disabled)...)
	}
	out := renderSecretFindings(findings, scanned, skipped)
	if info.IsDir() && len(files) >= maxFiles {
		// A walk cut short must never read as a complete scan.
		out += fmt.Sprintf("note: stopped at max_files=%d; raise it to scan the whole tree\n", maxFiles)
	}
	return out, nil
}

// walkSecretsTree collects regular files under root, skipping the noisy
// directories and never following symlinks (which could leave the tree).
func walkSecretsTree(ctx context.Context, root string, maxFiles int) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, keep scanning
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if path != root && secretsSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if len(files) >= maxFiles {
			return filepath.SkipAll
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

// gitStagedFiles lists the paths in the git index of dir. git runs with
// fixed arguments — nothing from the model reaches a shell.
func gitStagedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z").Output() // #nosec G204 -- fixed git verb and flags; dir validated by engine.ValidateReadPath
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}
	top, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output() // #nosec G204 -- fixed git verb; dir validated above
	if err != nil {
		return nil, fmt.Errorf("git rev-parse: %w", err)
	}
	root := strings.TrimSpace(string(top))
	var files []string
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) > 0 {
			files = append(files, filepath.Join(root, filepath.FromSlash(string(name))))
		}
	}
	return files, nil
}

// readSecretsFile returns the content of a scannable text file: regular,
// at most 1 MiB and with no NUL byte in its first 8 KiB.
func readSecretsFile(path string) ([]byte, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > secretsMaxFileBytes {
		return nil, false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from the walked tree under a path validated by engine.ValidateReadPath
	if err != nil {
		return nil, false
	}
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, false
	}
	return data, true
}

// loadSecretsIgnore reads .secretsignore at dir: a line that names a rule
// id disables that rule; any other line is a glob of paths to skip.
func loadSecretsIgnore(dir string) (globs []string, disabled map[string]bool) {
	disabled = map[string]bool{}
	ids := map[string]bool{}
	for _, r := range secretRules {
		ids[r.id] = true
	}
	data, err := os.ReadFile(filepath.Join(dir, scanIgnoreFile)) // #nosec G304 -- fixed file name under the validated scan root
	if err != nil {
		return nil, disabled
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case ids[line]:
			disabled[line] = true
		default:
			globs = append(globs, line)
		}
	}
	return globs, disabled
}

// scanSecretText runs every enabled rule over each line; a given value
// span is reported once (first rule wins) and lines carrying the inline
// marker are skipped.
func scanSecretText(name string, data []byte, disabled map[string]bool) []secretFinding {
	var out []secretFinding
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), secretsMaxFileBytes+1)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.Contains(line, scanInlineMarker) {
			continue
		}
		seen := map[int]bool{}
		for _, r := range secretRules {
			if disabled[r.id] {
				continue
			}
			for _, m := range r.re.FindAllStringSubmatchIndex(line, -1) {
				start, end := m[2*r.group], m[2*r.group+1]
				if start < 0 || seen[start] {
					continue
				}
				val := line[start:end]
				if secretsPlaceholder.MatchString(val) || (r.id == "high-entropy-secret" && shannonEntropy(val) < secretsMinEntropy) {
					continue
				}
				seen[start] = true
				out = append(out, secretFinding{path: name, line: n, rule: r.id, severity: r.severity, preview: redactSecret(val)})
			}
		}
	}
	return out
}

// redactSecret keeps the first four characters so a reviewer can locate
// the value, then the length — never the secret itself.
func redactSecret(v string) string {
	r := []rune(v)
	if len(r) > 4 {
		r = r[:4]
	}
	return fmt.Sprintf("%s…(%d chars)", string(r), len([]rune(v)))
}

// shannonEntropy is the bits-per-character entropy of s.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]float64{}
	for _, c := range s {
		freq[c]++
	}
	var h float64
	n := float64(len([]rune(s)))
	for _, f := range freq {
		p := f / n
		h -= p * math.Log2(p)
	}
	return h
}

// secretsRel reports path relative to dir, both symlink-resolved so a git
// toplevel (real path) and a walked path (as given) agree.
func secretsRel(dir, path string) string {
	resolve := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			return r
		}
		return abs
	}
	if rel, err := filepath.Rel(resolve(dir), resolve(path)); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

// renderSecretFindings prints the severity summary and one line per hit.
func renderSecretFindings(findings []secretFinding, scanned, skipped int) string {
	var b strings.Builder
	if len(findings) == 0 {
		fmt.Fprintf(&b, "no secrets found (%d file(s) scanned, %d skipped)\n", scanned, skipped)
		return b.String()
	}
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.severity]++
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].path != findings[j].path {
			return findings[i].path < findings[j].path
		}
		return findings[i].line < findings[j].line
	})
	fmt.Fprintf(&b, "%d finding(s): critical=%d high=%d medium=%d (%d file(s) scanned, %d skipped)\n",
		len(findings), counts["critical"], counts["high"], counts["medium"], scanned, skipped)
	for i, f := range findings {
		if i >= secretsMaxReportedLines {
			fmt.Fprintf(&b, "… and %d more\n", len(findings)-i)
			break
		}
		fmt.Fprintf(&b, "%s:%d %s %s %s\n", f.path, f.line, f.rule, f.severity, f.preview)
	}
	return b.String()
}

// parseSecretsInvocation accepts the {cmd,args} envelope, a flat native
// object and the flattened argv form.
func parseSecretsInvocation(args []string) (string, map[string]json.RawMessage, error) {
	return parseVerbEnvelope(args, canonicalSecretsCmd, func(raw map[string]json.RawMessage) string {
		if _, ok := raw["text"]; ok {
			return "check"
		}
		return "scan"
	}, func(cmd string) string {
		if cmd == "check" {
			return "text"
		}
		return "path"
	}, nil, nil)
}

func canonicalSecretsCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "scan", "audit", "detect":
		return "scan"
	case "check", "text", "inline":
		return "check"
	case "rules", "list":
		return "rules"
	}
	return ""
}

// parseVerbEnvelope is the envelope/argv parser shared by the small
// verb-style builtins: the {cmd,args} envelope, a flat native object (cmd
// inferred by inferFlat) and the flattened argv form ("verb --k v", bare
// flags as booleans, the leading positional mapped to primary(cmd)).
func parseVerbEnvelope(args []string, canon func(string) string, inferFlat func(map[string]json.RawMessage) string,
	primary func(string) string, arrayKeys, intKeys map[string]bool) (string, map[string]json.RawMessage, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", nil, fmt.Errorf("parse envelope: %w", err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		cmd := canon(cmdStr)
		if cmd == "" {
			if !isFlatArgs(raw) {
				return "", nil, fmt.Errorf("missing or unknown cmd %q", cmdStr)
			}
			cmd = inferFlat(raw)
		}
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner := map[string]json.RawMessage{}
			if err := json.Unmarshal(rargs, &inner); err != nil {
				return "", nil, fmt.Errorf("parse args: %w", err)
			}
			return cmd, inner, nil
		}
		delete(raw, "cmd")
		return cmd, raw, nil
	}
	if len(args) == 0 {
		return "", nil, errors.New("empty args")
	}
	cmd := canon(args[0])
	if cmd == "" {
		return "", nil, fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	inner := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(argvInner(args[1:], primary(cmd), arrayKeys, intKeys)), &inner)
	return cmd, inner, nil
}
