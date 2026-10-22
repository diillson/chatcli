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
// It shares the proxy-aware transport so OSV lookups authenticate against a
// corporate proxy just like @webfetch/@websearch (see builtin_web_httpclient.go).
var osvHTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: newWebRoundTripper(), CheckRedirect: validateRedirect}

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
  scan {path, recursive?}      scan a manifest file (go.mod, requirements.txt,
                               package-lock.json, yarn.lock, pnpm-lock.yaml,
                               poetry.lock, Pipfile.lock, Gemfile.lock,
                               composer.lock, pom.xml, Cargo.lock) or a directory
                               containing one; recursive:true scans every
                               manifest below the directory (a monorepo in one
                               call). Defaults to the current directory.
  check {ecosystem, package, version}
                               check one dependency. ecosystem: Go|PyPI|npm|crates.io`
}

// Version is semver.
func (*BuiltinOsvPlugin) Version() string { return "1.1.0" }

// Path is empty for builtin plugins.
func (*BuiltinOsvPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinOsvPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "scan",
				"description": "Scan a dependency manifest (or a directory containing one) against OSV.dev. Manifests: go.mod, requirements.txt, package-lock.json, yarn.lock, pnpm-lock.yaml, poetry.lock, Pipfile.lock, Gemfile.lock, composer.lock, pom.xml, Cargo.lock.",
				"flags": []map[string]interface{}{
					{"name": "path", "type": "string", "required": false, "description": "Manifest file or directory. Default: current directory."},
					{"name": "recursive", "type": "boolean", "required": false, "description": "Scan every manifest found below the directory (monorepos), up to 25."},
				},
				"examples": []string{`{"cmd":"scan","args":{"path":"go.mod"}}`, `{"cmd":"scan"}`, `{"cmd":"scan","args":{"path":".","recursive":true}}`},
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
		// A raw map: the flattened argv form carries recursive as the string
		// "true", which a typed bool field would reject along with the path.
		var in map[string]json.RawMessage
		_ = json.Unmarshal([]byte(inner), &in)
		path := strings.TrimSpace(jsonString(in, "path", "dir", "file"))
		if path == "" {
			path = "."
		}
		if jsonBool(in, "recursive", "all", "tree") {
			return p.scanTree(ctx, path)
		}
		return p.scan(ctx, path)
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

// scanTree scans every manifest below dir and joins the reports.
func (p *BuiltinOsvPlugin) scanTree(ctx context.Context, dir string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("@osv: %w", err)
	}
	if !info.IsDir() {
		return p.scan(ctx, dir)
	}
	manifests := findManifests(dir)
	if len(manifests) == 0 {
		return fmt.Sprintf("@osv: no supported manifest below %s (looked for %s)", dir, strings.Join(osvManifestNames, ", ")), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "@osv recursive scan of %s — %d manifest(s)\n", dir, len(manifests))
	for _, m := range manifests {
		out, err := p.scan(ctx, m)
		if err != nil {
			fmt.Fprintf(&b, "\n%s: %v\n", m, err)
			continue
		}
		b.WriteString("\n" + out + "\n")
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
	// Send a stable, non-browser User-Agent so the request is predictable behind
	// TLS-intercepting corporate gateways. This is an API call (no CDN
	// bot-blocking to dodge), so it needs no browser UA / fallback dance.
	req.Header.Set("User-Agent", fallbackUserAgent)
	resp, err := osvHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("osv.dev API returned status %d", resp.StatusCode)
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
	for _, name := range osvManifestNames {
		candidate := filepath.Join(path, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no supported manifest in %s (looked for %s); pass recursive:true to search below it", path, strings.Join(osvManifestNames, ", "))
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
	case "yarn.lock":
		return parseYarnLock(data), nil
	case "pnpm-lock.yaml":
		return parsePnpmLock(data), nil
	case "poetry.lock":
		return parsePoetryLock(data), nil
	case "pipfile.lock":
		return parsePipfileLock(data)
	case "gemfile.lock":
		return parseGemfileLock(data), nil
	case "composer.lock":
		return parseComposerLock(data)
	case "pom.xml":
		return parsePomXML(data)
	default:
		return nil, fmt.Errorf("unsupported manifest %q (supported: %s)", filepath.Base(path), strings.Join(osvManifestNames, ", "))
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
			// Only name needs resetting; version is always set by its own line
			// before it is read in the append below.
			name = ""
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
			if !isFlatArgs(raw) {
				return "", "", fmt.Errorf("missing or unknown cmd %q (valid: scan|check)", cmdStr)
			}
			// flat native args: {"package":...} → check; {"path":...} or {} → scan
			if _, ok := raw["package"]; ok {
				canon = "check"
			} else {
				canon = "scan"
			}
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
	canon := canonicalOsvCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	primary := "path"
	if canon == "check" {
		primary = "package"
	}
	return canon, argvInner(args[1:], primary, nil, nil), nil
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
