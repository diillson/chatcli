/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinOsvPlugin — dependency vulnerability scanning as an @osv ReAct tool.
 *
 * It reads a project's dependency manifest (go.mod, requirements.txt,
 * package-lock.json, Cargo.lock) and checks every pinned dependency against the
 * free, keyless OSV.dev database (https://osv.dev). It can also check a single
 * package@version directly. Inspired by hermes-agent's osv_check, implemented
 * natively in Go and self-contained — no API key, no external CLI.
 */
package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// osvBaseURL is the OSV.dev API root. Overridable in tests.
var osvBaseURL = "https://api.osv.dev"

// osvHTTPClient is the HTTP client used for OSV queries. Overridable in tests.
var osvHTTPClient = &http.Client{Timeout: 30 * time.Second}

// BuiltinOsvPlugin is the @osv tool.
type BuiltinOsvPlugin struct{}

// NewBuiltinOsvPlugin returns a ready-to-register plugin.
func NewBuiltinOsvPlugin() *BuiltinOsvPlugin { return &BuiltinOsvPlugin{} }

// Name returns "@osv".
func (*BuiltinOsvPlugin) Name() string { return "@osv" }

// Description surfaces the tool in the catalog.
func (*BuiltinOsvPlugin) Description() string {
	return "Scan project dependencies for known vulnerabilities using the free, keyless OSV.dev database. Reads go.mod / requirements.txt / package-lock.json / Cargo.lock, or checks a single package@version."
}

// Usage explains the canonical invocation.
func (*BuiltinOsvPlugin) Usage() string {
	return `<tool_call name="@osv" args='{"cmd":"scan","args":{"path":"go.mod"}}' />

Subcommands (cmd + args):
  scan {path}                  scan a manifest file (go.mod, requirements.txt,
                               package-lock.json, Cargo.lock) or a directory
                               containing one. Defaults to the current directory.
  check {ecosystem, package, version}
                               check one dependency. ecosystem: Go|PyPI|npm|crates.io`
}

// Version is semver.
func (*BuiltinOsvPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinOsvPlugin) Path() string { return "" }

// IsConcurrencySafe lets the orchestrator fan @osv out in parallel — it only
// reads files and queries a read-only API.
func (*BuiltinOsvPlugin) IsConcurrencySafe() bool { return true }

// Schema describes the subcommands.
func (*BuiltinOsvPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "scan",
				"description": "Scan a dependency manifest (or a directory containing one) against OSV.dev.",
				"flags": []map[string]interface{}{
					{"name": "path", "type": "string", "required": false, "description": "Manifest file or directory. Default: current directory."},
				},
				"examples": []string{`{"cmd":"scan","args":{"path":"go.mod"}}`, `{"cmd":"scan"}`},
			},
			{
				"name":        "check",
				"description": "Check a single package@version.",
				"flags": []map[string]interface{}{
					{"name": "ecosystem", "type": "string", "required": true, "description": "Go | PyPI | npm | crates.io | Maven | RubyGems | NuGet | Packagist"},
					{"name": "package", "type": "string", "required": true, "description": "Package name."},
					{"name": "version", "type": "string", "required": true, "description": "Exact version."},
				},
				"examples": []string{`{"cmd":"check","args":{"ecosystem":"PyPI","package":"requests","version":"2.19.0"}}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinOsvPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinOsvPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		// Default: scan the current directory.
		return p.scan(ctx, ".")
	}
	cmd, inner, err := parseOsvInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@osv: %w", err)
	}
	switch cmd {
	case "scan":
		var in struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Path) == "" {
			in.Path = "."
		}
		return p.scan(ctx, in.Path)
	case "check":
		var in struct {
			Ecosystem string `json:"ecosystem"`
			Package   string `json:"package"`
			Version   string `json:"version"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if in.Ecosystem == "" || in.Package == "" || in.Version == "" {
			return "", errors.New(`@osv check: "ecosystem", "package" and "version" are all required`)
		}
		vulns, err := osvQuery(ctx, in.Ecosystem, in.Package, in.Version)
		if err != nil {
			return "", fmt.Errorf("@osv: %w", err)
		}
		return formatOsvOne(in.Ecosystem, in.Package, in.Version, vulns), nil
	default:
		return "", fmt.Errorf("@osv: unknown cmd %q (valid: scan|check)", cmd)
	}
}

// osvDep is one resolved dependency.
type osvDep struct {
	Ecosystem string
	Name      string
	Version   string
}

// osvVuln is the subset of an OSV record we report.
type osvVuln struct {
	ID       string   `json:"id"`
	Summary  string   `json:"summary"`
	Aliases  []string `json:"aliases"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
}

func (p *BuiltinOsvPlugin) scan(ctx context.Context, path string) (string, error) {
	manifest, err := resolveManifest(path)
	if err != nil {
		return "", fmt.Errorf("@osv: %w", err)
	}
	deps, err := parseManifest(manifest)
	if err != nil {
		return "", fmt.Errorf("@osv: %w", err)
	}
	if len(deps) == 0 {
		return fmt.Sprintf("@osv: no pinned dependencies found in %s", manifest), nil
	}

	type finding struct {
		dep   osvDep
		vulns []osvVuln
	}
	var findings []finding
	scanned := 0
	for _, d := range deps {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		vulns, err := osvQuery(ctx, d.Ecosystem, d.Name, d.Version)
		if err != nil {
			continue // skip transient failures; don't abort the whole scan
		}
		scanned++
		if len(vulns) > 0 {
			findings = append(findings, finding{dep: d, vulns: vulns})
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "@osv scan of %s — %d dependencies checked\n", manifest, scanned)
	if len(findings) == 0 {
		b.WriteString("\n✅ No known vulnerabilities found.")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "\n⚠️  %d vulnerable dependenc(ies):\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "\n• %s@%s (%s)\n", f.dep.Name, f.dep.Version, f.dep.Ecosystem)
		for _, v := range f.vulns {
			b.WriteString("    " + formatVulnLine(v) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func formatOsvOne(ecosystem, pkg, version string, vulns []osvVuln) string {
	if len(vulns) == 0 {
		return fmt.Sprintf("✅ %s@%s (%s): no known vulnerabilities.", pkg, version, ecosystem)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️  %s@%s (%s): %d vulnerabilit(ies):\n", pkg, version, ecosystem, len(vulns))
	for _, v := range vulns {
		b.WriteString("  " + formatVulnLine(v) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatVulnLine(v osvVuln) string {
	line := v.ID
	if len(v.Aliases) > 0 {
		line += " (" + strings.Join(v.Aliases, ", ") + ")"
	}
	for _, s := range v.Severity {
		if s.Score != "" {
			line += " [" + s.Type + ":" + s.Score + "]"
			break
		}
	}
	if v.Summary != "" {
		line += " — " + v.Summary
	}
	return line
}

// osvQuery hits POST /v1/query for one package@version.
func osvQuery(ctx context.Context, ecosystem, name, version string) ([]osvVuln, error) {
	reqBody := map[string]interface{}{
		"version": version,
		"package": map[string]string{"ecosystem": ecosystem, "name": name},
	}
	data, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, osvBaseURL+"/v1/query", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := osvHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OSV API status %d", resp.StatusCode)
	}
	var out struct {
		Vulns []osvVuln `json:"vulns"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Vulns, nil
}

// resolveManifest turns a path into a concrete manifest file.
func resolveManifest(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	for _, name := range []string{"go.mod", "requirements.txt", "package-lock.json", "Cargo.lock"} {
		candidate := filepath.Join(path, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no supported manifest in %s (looked for go.mod, requirements.txt, package-lock.json, Cargo.lock)", path)
}

// parseManifest dispatches on filename.
func parseManifest(path string) ([]osvDep, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path supplied by the user/agent for scanning
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "go.mod":
		return parseGoMod(data), nil
	case "requirements.txt":
		return parseRequirements(data), nil
	case "package-lock.json":
		return parsePackageLock(data)
	case "cargo.lock":
		return parseCargoLock(data), nil
	default:
		return nil, fmt.Errorf("unsupported manifest %q", filepath.Base(path))
	}
}

func parseGoMod(data []byte) []osvDep {
	var deps []osvDep
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "require ")
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "module ") ||
			strings.HasPrefix(line, "go ") || strings.HasPrefix(line, "require") ||
			strings.HasPrefix(line, ")") || strings.HasPrefix(line, "replace") ||
			strings.HasPrefix(line, "exclude") || strings.HasPrefix(line, "toolchain") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "v") {
			ver := strings.TrimSuffix(fields[1], "+incompatible")
			deps = append(deps, osvDep{Ecosystem: "Go", Name: fields[0], Version: ver})
		}
	}
	return deps
}

func parseRequirements(data []byte) []osvDep {
	var deps []osvDep
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		// only handle exact pins (name==version)
		if i := strings.Index(line, "=="); i > 0 {
			name := strings.TrimSpace(line[:i])
			rest := strings.TrimSpace(line[i+2:])
			ver := strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == ';' || r == '#' })
			if name != "" && len(ver) > 0 {
				deps = append(deps, osvDep{Ecosystem: "PyPI", Name: name, Version: ver[0]})
			}
		}
	}
	return deps
}

func parsePackageLock(data []byte) ([]osvDep, error) {
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	var deps []osvDep
	seen := map[string]bool{}
	add := func(name, ver string) {
		if name == "" || ver == "" {
			return
		}
		key := name + "@" + ver
		if seen[key] {
			return
		}
		seen[key] = true
		deps = append(deps, osvDep{Ecosystem: "npm", Name: name, Version: ver})
	}
	// lockfile v2+: "packages" keyed by "node_modules/<name>"
	for path, pkg := range lock.Packages {
		if path == "" {
			continue
		}
		name := path
		if i := strings.LastIndex(path, "node_modules/"); i >= 0 {
			name = path[i+len("node_modules/"):]
		}
		add(name, pkg.Version)
	}
	// lockfile v1: "dependencies"
	for name, dep := range lock.Dependencies {
		add(name, dep.Version)
	}
	return deps, nil
}

func parseCargoLock(data []byte) []osvDep {
	var deps []osvDep
	var name, version string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "[[package]]":
			name, version = "", ""
		case strings.HasPrefix(line, "name = "):
			name = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
		case strings.HasPrefix(line, "version = "):
			version = strings.Trim(strings.TrimPrefix(line, "version = "), `"`)
			if name != "" && version != "" {
				deps = append(deps, osvDep{Ecosystem: "crates.io", Name: name, Version: version})
			}
		}
	}
	return deps
}

// parseOsvInvocation mirrors the other builtins' envelope/argv parsing.
func parseOsvInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(`parse envelope: %w`, err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalOsvCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: scan|check)", cmdStr)
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
	canon := canonicalOsvCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	if canon == "scan" {
		rest := strings.TrimSpace(strings.TrimPrefix(payload, args[0]))
		b, _ := json.Marshal(map[string]string{"path": rest})
		return canon, string(b), nil
	}
	return canon, "{}", nil
}

func canonicalOsvCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "scan", "audit":
		return "scan"
	case "check", "query":
		return "check"
	}
	return ""
}
