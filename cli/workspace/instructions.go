/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Instruction-file hierarchy and @imports.
 *
 * AGENTS.md is read from every directory between the project root and
 * the working directory, root first, so a package-level file refines the
 * repository-level one (the Codex layout). A directory without AGENTS.md
 * may carry CHATCLI.md or CLAUDE.md instead. Any instruction file can
 * pull another in with a line of the form "@path/to/file.md" (Claude
 * Code's import syntax) or "@import path", relative to the importing
 * file, up to four hops and never twice. The assembled text is capped at
 * 32 KiB, the same ceiling Codex applies to project docs.
 */
package workspace

import (
	"fmt"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// InstructionDocMaxBytes caps the assembled instruction hierarchy.
	InstructionDocMaxBytes = 32 * 1024
	// importMaxHops bounds nested @imports.
	importMaxHops = 4
)

// instructionFileNames are the per-directory candidates, in priority order.
var instructionFileNames = []string{"AGENTS.md", "CHATCLI.md", "CLAUDE.md"}

// instructionOverrideName replaces a directory's AGENTS.md when present
// (the Codex convention: AGENTS.override.md wins over AGENTS.md).
const instructionOverrideName = "AGENTS.override.md"

// instructionLocalSuffix names the per-machine companion of an instruction
// file (AGENTS.local.md, CLAUDE.local.md — gitignored, merged after it).
const instructionLocalSuffix = ".local.md"

// importFileMaxBytes caps ONE imported file; the whole hierarchy is still
// capped by InstructionDocMaxBytes after the join.
const importFileMaxBytes = 32 * 1024

// importLine matches "@path/file.md", "@./file.md", "@~/file.md" or
// "@import path" on a line of its own (Markdown files and plain text).
var importLine = regexp.MustCompile(`^\s*@(?:import\s+)?([~./\\A-Za-z0-9_][^\s]*\.(?:md|mdx|txt|markdown))\s*$`)

// SetWorkingDir records the directory the hierarchy walk ends at (the
// process cwd by default); no-op when it is outside the workspace.
func (bl *BootstrapLoader) SetWorkingDir(dir string) {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	bl.workingDir = dir
}

// instructionDirs returns workspaceDir and every directory below it down
// to the working directory, root first.
func (bl *BootstrapLoader) instructionDirs() []string {
	if bl.workspaceDir == "" {
		return nil
	}
	bl.mu.RLock()
	cwd := bl.workingDir
	bl.mu.RUnlock()
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	root := filepath.Clean(bl.workspaceDir)
	dirs := []string{root}
	if cwd == "" {
		return dirs
	}
	cwd = filepath.Clean(cwd)
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return dirs
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		dirs = append(dirs, cur)
	}
	return dirs
}

// instructionFileIn returns the first instruction file present in dir.
func (bl *BootstrapLoader) instructionFileIn(dir string) (string, string, bool) {
	// The override file replaces the directory's instructions outright.
	if p := filepath.Join(dir, instructionOverrideName); p != "" {
		if content, ok := bl.loadWithCache(p); ok {
			return p, bl.withLocalCompanion(dir, "AGENTS.md", content), true
		}
	}
	for _, name := range instructionFileNames {
		p := filepath.Join(dir, name)
		if content, ok := bl.loadWithCache(p); ok {
			return p, bl.withLocalCompanion(dir, name, content), true
		}
	}
	return "", "", false
}

// withLocalCompanion appends <name>.local.md (per-machine, gitignored
// additions) after the shared file's content when it exists.
func (bl *BootstrapLoader) withLocalCompanion(dir, name, content string) string {
	local := filepath.Join(dir, strings.TrimSuffix(name, ".md")+instructionLocalSuffix)
	extra, ok := bl.loadWithCache(local)
	if !ok || strings.TrimSpace(extra) == "" {
		return content
	}
	return content + "\n\n<!-- " + filepath.Base(local) + " -->\n" + extra
}

// loadInstructionHierarchy assembles the project instruction files (root
// → cwd), falling back to the global AGENTS.md when the project has none.
// Each file has its @imports expanded; the result is capped.
func (bl *BootstrapLoader) loadInstructionHierarchy() (string, bool) {
	parts := make([]string, 0, 4)
	// The global file is always merged first (Claude Code merges global +
	// project); it used to be a fallback only when the project had none.
	globalUsed := false
	if bl.globalDir != "" {
		if path, content, ok := bl.instructionFileIn(bl.globalDir); ok {
			parts = append(parts, "<!-- global "+filepath.Base(path)+" -->\n"+bl.expandImports(content, filepath.Dir(path), 1, map[string]bool{path: true}))
			globalUsed = true
		}
	}
	for _, dir := range bl.instructionDirs() {
		path, content, ok := bl.instructionFileIn(dir)
		if !ok {
			continue
		}
		content = strings.TrimSpace(bl.expandImports(content, filepath.Dir(path), 1, map[string]bool{path: true}))
		if content == "" {
			continue
		}
		if dir != filepath.Clean(bl.workspaceDir) {
			rel, _ := filepath.Rel(bl.workspaceDir, path)
			content = "<!-- " + filepath.ToSlash(rel) + " -->\n" + content
		}
		parts = append(parts, content)
	}
	_ = globalUsed
	if len(parts) == 0 {
		return "", false
	}
	return capInstructionDoc(strings.Join(parts, "\n\n"), InstructionDocMaxBytes), true
}

// expandImports replaces import lines with the imported file's content,
// resolved relative to baseDir (absolute and ~ paths accepted), nested up
// to importMaxHops and never the same file twice. A missing or unreadable
// target leaves the line untouched.
func (bl *BootstrapLoader) expandImports(content, baseDir string, hop int, visited map[string]bool) string {
	if hop > importMaxHops || !strings.Contains(content, "@") {
		return content
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		m := importLine.FindStringSubmatch(line)
		if inFence || m == nil {
			out = append(out, line)
			continue
		}
		target := m[1]
		if strings.HasPrefix(target, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				target = filepath.Join(home, target[2:])
			}
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(baseDir, target)
		}
		target = filepath.Clean(target)
		if visited[target] {
			out = append(out, line)
			continue
		}
		// Import boundary: symlinks resolved, then the file must live under
		// the workspace or the global instruction directory. A cloned
		// repository's instruction file cannot pull arbitrary files from
		// the user's disk into the prompt.
		if !bl.importAllowed(target) {
			if bl.logger != nil {
				bl.logger.Warn("instruction @import outside the workspace skipped", zap.String("target", target))
			}
			out = append(out, line)
			continue
		}
		imported, ok := bl.loadWithCache(target)
		if !ok {
			out = append(out, line)
			continue
		}
		if len(imported) > importFileMaxBytes {
			imported = capInstructionDoc(imported, importFileMaxBytes)
		}
		visited[target] = true
		out = append(out, fmt.Sprintf("<!-- import %s -->", filepath.Base(target)))
		out = append(out, bl.expandImports(imported, filepath.Dir(target), hop+1, visited))
	}
	return strings.Join(out, "\n")
}

// importAllowed reports whether an @import target, symlinks resolved,
// lives under the workspace or the global instruction directory.
func (bl *BootstrapLoader) importAllowed(target string) bool {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	for _, root := range []string{bl.workspaceDir, bl.globalDir} {
		if root == "" {
			continue
		}
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			r = filepath.Clean(root)
		}
		if rel, err := filepath.Rel(r, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// capInstructionDoc truncates at a line boundary with a visible note.
func capInstructionDoc(doc string, limit int) string {
	if len(doc) <= limit {
		return doc
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(doc[cut]) {
		cut-- // never split a multi-byte rune
	}
	if i := strings.LastIndexByte(doc[:cut], '\n'); i > limit/2 {
		cut = i
	}
	return doc[:cut] + fmt.Sprintf("\n\n<!-- instruction files truncated at %d KiB -->", limit/1024)
}
