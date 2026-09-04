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
	for _, name := range instructionFileNames {
		p := filepath.Join(dir, name)
		if content, ok := bl.loadWithCache(p); ok {
			return p, content, true
		}
	}
	return "", "", false
}

// loadInstructionHierarchy assembles the project instruction files (root
// → cwd), falling back to the global AGENTS.md when the project has none.
// Each file has its @imports expanded; the result is capped.
func (bl *BootstrapLoader) loadInstructionHierarchy() (string, bool) {
	parts := make([]string, 0, 4)
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
	if len(parts) == 0 {
		if bl.globalDir == "" {
			return "", false
		}
		path, content, ok := bl.instructionFileIn(bl.globalDir)
		if !ok {
			return "", false
		}
		parts = append(parts, bl.expandImports(content, filepath.Dir(path), 1, map[string]bool{path: true}))
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
		imported, ok := bl.loadWithCache(target)
		if !ok {
			out = append(out, line)
			continue
		}
		visited[target] = true
		out = append(out, fmt.Sprintf("<!-- import %s -->", filepath.Base(target)))
		out = append(out, bl.expandImports(imported, filepath.Dir(target), hop+1, visited))
	}
	return strings.Join(out, "\n")
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
