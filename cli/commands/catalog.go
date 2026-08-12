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

// dirSpec pairs a directory with the source family it belongs to.
type dirSpec struct {
	dir    string
	source Source
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

	// isReserved reports whether a name would shadow a built-in slash
	// command. Injected by the CLI (derived from the live dispatch table)
	// so this package never hard-codes a second copy of that list.
	isReserved func(name string) bool

	snap map[string]*Command // key: InvocationName()
	sig  string
	// refused records name→path of commands rejected for shadowing a
	// built-in, for /config commands diagnostics.
	refused map[string]string
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
	return &Catalog{
		logger:     logger,
		projectDir: projectDir,
		globalDir:  globalDir,
		isReserved: isReserved,
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
// name wins. Project beats personal; native beats interop.
func (c *Catalog) sourceDirs() []dirSpec {
	var out []dirSpec
	if c.projectDir != "" {
		out = append(out, dirSpec{filepath.Join(c.projectDir, ".chatcli", "commands"), SourceProject})
	}
	if c.globalDir != "" {
		out = append(out, dirSpec{c.globalDir, SourceGlobal})
	}
	if c.projectDir != "" {
		out = append(out,
			dirSpec{filepath.Join(c.projectDir, ".claude", "commands"), SourceClaude},
			dirSpec{filepath.Join(c.projectDir, ".devin", "workflows"), SourceDevin},
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
			subEntries, err := os.ReadDir(filepath.Join(spec.dir, e.Name()))
			if err != nil {
				continue
			}
			for _, se := range subEntries {
				if !se.IsDir() && strings.HasSuffix(se.Name(), ".md") {
					c.mergeFileLocked(snap, refused, filepath.Join(spec.dir, e.Name(), se.Name()), spec.source, e.Name())
				}
			}
			continue
		}
		if strings.HasSuffix(e.Name(), ".md") {
			c.mergeFileLocked(snap, refused, filepath.Join(spec.dir, e.Name()), spec.source, "")
		}
	}
}

func (c *Catalog) mergeFileLocked(snap map[string]*Command, refused map[string]string, path string, source Source, namespace string) {
	cmd, err := parseCommandFile(path, source, namespace)
	if err != nil {
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
