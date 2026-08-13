/*
 * ChatCLI - Slash command catalog (resolution + fingerprint cache)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// fileFormat selects the parser for a source directory.
type fileFormat int

const (
	formatMarkdown fileFormat = iota // *.md with optional YAML frontmatter
	formatTOML                       // *.toml with prompt/description keys (Gemini/Qwen)
	formatPromptMD                   // *.prompt.md (GitHub Copilot prompt files)
)

// dirSpec pairs a directory with the source family it belongs to and how
// its files parse.
type dirSpec struct {
	dir    string
	source Source
	format fileFormat
	// topLevelOnly skips namespace subdirectories — Codex's own contract
	// ("scans only top-level Markdown files").
	topLevelOnly bool
}

// Catalog loads and serves the slash-command set. Reads are served from an
// in-memory snapshot guarded by a stat-only fingerprint of the source dirs
// (same discipline as the session corpus cache): the walk+parse cost is paid
// only when a file actually changed, not per lookup — lookups happen on
// every REPL dispatch and every ACP/MCP listing.
type Catalog struct {
	mu     sync.Mutex
	logger *zap.Logger

	projectDir string
	globalDir  string
	// homeDir roots the global interop dirs (~/.codex/prompts etc.).
	homeDir string

	// isReserved reports whether a name would shadow a built-in slash
	// command. Injected by the CLI (derived from the live dispatch table)
	// so this package never hard-codes a second copy of that list.
	isReserved func(name string) bool

	snap map[string]*Command // key: InvocationName()
	sig  string
	// refused records name→path of commands rejected for shadowing a
	// built-in, for /config commands diagnostics.
	refused map[string]string
	// skipped records path→reason for files that failed to parse. Surfaced
	// in /config commands: a silently dropped file (broken frontmatter,
	// oversized, bad name) is otherwise invisible to the user.
	skipped map[string]string
}

// NewCatalog builds a catalog. globalDir is ~/.chatcli/commands (created
// lazily); projectDir may be empty when no project root was detected.
// isReserved may be nil (no shadowing protection — tests only).
func NewCatalog(projectDir, globalDir string, isReserved func(string) bool, logger *zap.Logger) *Catalog {
	if logger == nil {
		logger = zap.NewNop()
	}
	if isReserved == nil {
		isReserved = func(string) bool { return false }
	}
	home, _ := os.UserHomeDir()
	return &Catalog{
		logger:     logger,
		projectDir: projectDir,
		globalDir:  globalDir,
		homeDir:    home,
		isReserved: isReserved,
	}
}

// SetHomeDir re-roots the global interop directories (~/.codex/prompts,
// ~/.gemini/commands, …). Production uses the real home; hermetic tests
// point this at a temp dir so the developer's actual prompt libraries can
// never leak into assertions.
func (c *Catalog) SetHomeDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.homeDir != dir {
		c.homeDir = dir
		c.sig = ""
	}
}

// SetProjectDir re-roots the project-scoped directories (mirrors the skill
// loader's SetProjectDir; called when the CLI detects the project root).
func (c *Catalog) SetProjectDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.projectDir != dir {
		c.projectDir = dir
		c.sig = "" // force re-scan on next read
	}
}

// dirs returns the source directories in PRECEDENCE order: first hit for a
// name wins. Native beats interop; within each family, project beats
// personal. The interop order is deliberate and stable: agentic-CLI
// ecosystems whose files are most commonly committed to repos come first.
func (c *Catalog) sourceDirs() []dirSpec {
	var out []dirSpec
	if c.projectDir != "" {
		out = append(out, dirSpec{dir: filepath.Join(c.projectDir, ".chatcli", "commands"), source: SourceProject})
	}
	if c.globalDir != "" {
		out = append(out, dirSpec{dir: c.globalDir, source: SourceGlobal})
	}
	if c.projectDir != "" {
		out = append(out,
			dirSpec{dir: filepath.Join(c.projectDir, ".claude", "commands"), source: SourceClaude},
			dirSpec{dir: filepath.Join(c.projectDir, ".devin", "workflows"), source: SourceDevin},
			dirSpec{dir: filepath.Join(c.projectDir, ".windsurf", "workflows"), source: SourceWindsurf},
			dirSpec{dir: filepath.Join(c.projectDir, ".cursor", "commands"), source: SourceCursor},
			dirSpec{dir: filepath.Join(c.projectDir, ".opencode", "commands"), source: SourceOpencode},
			dirSpec{dir: filepath.Join(c.projectDir, ".gemini", "commands"), source: SourceGemini, format: formatTOML},
			dirSpec{dir: filepath.Join(c.projectDir, ".qwen", "commands"), source: SourceQwen, format: formatTOML},
			dirSpec{dir: filepath.Join(c.projectDir, ".github", "prompts"), source: SourceCopilot, format: formatPromptMD},
		)
	}
	if c.homeDir != "" {
		out = append(out,
			dirSpec{dir: filepath.Join(c.homeDir, ".codex", "prompts"), source: SourceCodex, topLevelOnly: true},
			dirSpec{dir: filepath.Join(c.homeDir, ".cursor", "commands"), source: SourceCursor},
			dirSpec{dir: filepath.Join(c.homeDir, ".config", "opencode", "commands"), source: SourceOpencode},
			dirSpec{dir: filepath.Join(c.homeDir, ".gemini", "commands"), source: SourceGemini, format: formatTOML},
			dirSpec{dir: filepath.Join(c.homeDir, ".qwen", "commands"), source: SourceQwen, format: formatTOML},
		)
	}
	return out
}

// Dirs exposes the scanned directories (existing or not) for /config
// commands and the knowledge-graph fingerprint.
func (c *Catalog) Dirs() []string {
	specs := c.sourceDirs()
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.dir)
	}
	return out
}

// Get resolves one command by invocation token ("name" or "ns:name").
// Returns nil when unknown.
func (c *Catalog) Get(token string) *Command {
	snap, _ := c.snapshot()
	return snap[strings.ToLower(strings.TrimSpace(token))]
}

// List returns every loaded command sorted by invocation name.
func (c *Catalog) List() []*Command {
	snap, _ := c.snapshot()
	out := make([]*Command, 0, len(snap))
	for _, cmd := range snap {
		out = append(out, cmd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InvocationName() < out[j].InvocationName() })
	return out
}

// Refused returns the name→path map of commands rejected for shadowing
// built-ins (diagnostics).
func (c *Catalog) Refused() map[string]string {
	_, refused := c.snapshot()
	return refused
}

// Skipped returns the path→reason map of files that failed to parse
// (diagnostics for /config commands).
func (c *Catalog) Skipped() map[string]string {
	c.snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skipped
}

// Invalidate forces a re-scan on the next read (wired to /reload).
func (c *Catalog) Invalidate() {
	c.mu.Lock()
	c.sig = ""
	c.mu.Unlock()
}

// snapshot serves the cached command set, re-scanning only when the
// stat-only signature of the source dirs changed.
func (c *Catalog) snapshot() (map[string]*Command, map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sig := c.fingerprintLocked()
	if c.snap != nil && sig == c.sig {
		return c.snap, c.refused
	}

	snap := make(map[string]*Command)
	refused := make(map[string]string)
	c.skipped = make(map[string]string)
	for _, spec := range c.sourceDirs() {
		c.loadDirLocked(snap, refused, spec)
	}
	c.snap, c.refused, c.sig = snap, refused, sig
	return snap, refused
}

// loadDirLocked walks one source dir (top level + one namespace level of
// subdirectories) merging into snap. First writer for a name wins — dirs()
// order IS the precedence.
func (c *Catalog) loadDirLocked(snap map[string]*Command, refused map[string]string, spec dirSpec) {
	entries, err := os.ReadDir(spec.dir)
	if err != nil {
		return // missing dir is the common case
	}
	for _, e := range entries {
		if e.IsDir() {
			if spec.topLevelOnly {
				continue
			}
			subEntries, err := os.ReadDir(filepath.Join(spec.dir, e.Name()))
			if err != nil {
				continue
			}
			for _, se := range subEntries {
				if !se.IsDir() && matchesFormat(se.Name(), spec.format) {
					c.mergeFileLocked(snap, refused, filepath.Join(spec.dir, e.Name(), se.Name()), spec, e.Name())
				}
			}
			continue
		}
		if matchesFormat(e.Name(), spec.format) {
			c.mergeFileLocked(snap, refused, filepath.Join(spec.dir, e.Name()), spec, "")
		}
	}
}

func (c *Catalog) mergeFileLocked(snap map[string]*Command, refused map[string]string, path string, spec dirSpec, namespace string) {
	cmd, err := parseCommandFileAs(path, spec, namespace)
	if err != nil {
		c.skipped[path] = err.Error()
		c.logger.Warn("slash command skipped", zap.String("path", path), zap.Error(err))
		return
	}
	token := cmd.InvocationName()
	// Never shadow a built-in: a project file must not be able to hijack
	// /session or /config. Namespaced names cannot collide (':' never
	// appears in built-ins) so only bare names are checked.
	if cmd.Namespace == "" && c.isReserved(cmd.Name) {
		refused[token] = path
		c.logger.Warn("slash command refused: shadows a built-in", zap.String("name", token), zap.String("path", path))
		return
	}
	if prev, dup := snap[token]; dup {
		c.logger.Debug("slash command overridden by higher-precedence source",
			zap.String("name", token), zap.String("winner", prev.Path), zap.String("loser", path))
		return
	}
	snap[token] = cmd
}

// fingerprintLocked stats every source dir (and one namespace level) into a
// cheap signature. No file is opened or parsed.
func (c *Catalog) fingerprintLocked() string {
	var b strings.Builder
	for _, spec := range c.sourceDirs() {
		entries, err := os.ReadDir(spec.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "%s/%s|%d|%d;", spec.dir, e.Name(), info.Size(), info.ModTime().UnixNano())
			if e.IsDir() {
				subEntries, err := os.ReadDir(filepath.Join(spec.dir, e.Name()))
				if err != nil {
					continue
				}
				for _, se := range subEntries {
					if si, err := se.Info(); err == nil {
						fmt.Fprintf(&b, "%s/%s/%s|%d|%d;", spec.dir, e.Name(), se.Name(), si.Size(), si.ModTime().UnixNano())
					}
				}
			}
		}
	}
	return b.String()
}
