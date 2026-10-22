/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * commands_more.go — the verbs an engineer reaches for between reading
 * and patching: git-show (one commit or a file at a ref), git-blame (who
 * wrote these lines), find (files by name pattern, the glob the search
 * verb never had), move and delete (with a backup, like every other
 * mutation here).
 */
package engine

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// findDefaultLimit bounds a find listing.
const findDefaultLimit = 200

// handleGitShow prints one commit (message and patch, or --stat) or a file
// as it was at a ref.
func (e *Engine) handleGitShow(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git-show", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	ref := fs.String("ref", "HEAD", "")
	path := fs.String("path", "", "")
	stat := fs.Bool("stat", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	repo, err := e.prepareGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	r := strings.TrimSpace(*ref)
	if r == "" {
		r = "HEAD"
	}
	if strings.HasPrefix(r, "-") {
		return fmt.Errorf("git-show: ref %q must not start with a dash", r)
	}
	var cmdArgs []string
	switch {
	case strings.TrimSpace(*path) != "":
		// The file's content at that ref, no commit header.
		cmdArgs = []string{"show", "--no-ext-diff", "--no-textconv", r + ":" + strings.TrimSpace(*path)}
	case *stat:
		cmdArgs = []string{"show", "--no-ext-diff", "--no-textconv", "--stat", r}
	default:
		cmdArgs = []string{"show", "--no-ext-diff", "--no-textconv", r}
	}
	out, err := runGitCommand(ctx, repo, cmdArgs...)
	return e.printCommandOutput(out, err)
}

// handleGitBlame annotates a file (or a line range of it) with the commit,
// author and date of each line.
func (e *Engine) handleGitBlame(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git-blame", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	file := fs.String("file", "", "")
	start := fs.Int("start", 0, "")
	end := fs.Int("end", 0, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*file) == "" {
		return errors.New("git-blame needs --file (and optionally --start/--end lines)")
	}
	repo, err := e.prepareGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	cmdArgs := []string{"blame", "--date=short"}
	if *start > 0 || *end > 0 {
		s, en := *start, *end
		if s <= 0 {
			s = 1
		}
		if en <= 0 {
			en = s
		}
		if en < s {
			return fmt.Errorf("git-blame: --end %d is before --start %d", en, s)
		}
		cmdArgs = append(cmdArgs, fmt.Sprintf("-L%d,%d", s, en))
	}
	cmdArgs = append(cmdArgs, "--", strings.TrimSpace(*file))
	out, err := runGitCommand(ctx, repo, cmdArgs...)
	return e.printCommandOutput(out, err)
}

// handleFind lists files whose NAME matches a glob (or whose path contains
// a substring with --contains), under dir, skipping the usual noise.
func (e *Engine) handleFind(args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	pattern := fs.String("pattern", "", "")
	contains := fs.String("contains", "", "")
	limit := fs.Int("limit", findDefaultLimit, "")
	dirsToo := fs.Bool("dirs", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	pat := strings.TrimSpace(*pattern)
	sub := strings.ToLower(strings.TrimSpace(*contains))
	if pat == "" && sub == "" {
		return errors.New(`find needs --pattern (a name glob such as "*_test.go") or --contains (a path substring)`)
	}
	if pat != "" {
		if _, err := filepath.Match(pat, "x"); err != nil {
			return fmt.Errorf("find: invalid pattern %q: %w", pat, err)
		}
	}
	*dir = expandPath(*dir)
	if err := e.validatePath(*dir); err != nil {
		return err
	}
	if *limit <= 0 {
		*limit = findDefaultLimit
	}
	skip := map[string]bool{".git": true, "node_modules": true, "vendor": true}
	var hits []string
	total := 0
	_ = filepath.Walk(*dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() && p != *dir && (skip[name] || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		if info.IsDir() && !*dirsToo {
			return nil
		}
		if p == *dir {
			return nil
		}
		rel, _ := filepath.Rel(*dir, p)
		matched := true
		if pat != "" {
			if ok, _ := filepath.Match(pat, name); !ok {
				matched = false
			}
		}
		if matched && sub != "" && !strings.Contains(strings.ToLower(rel), sub) {
			matched = false
		}
		if !matched {
			return nil
		}
		total++
		if len(hits) < *limit {
			hits = append(hits, rel)
		}
		return nil
	})
	if total == 0 {
		e.printf("No file matches %s under %s.\n", describeFind(pat, sub), *dir)
		return nil
	}
	sort.Strings(hits)
	for _, h := range hits {
		e.println(h)
	}
	if total > len(hits) {
		e.printf("... [%d of %d shown; raise --limit or narrow the pattern] ...\n", len(hits), total)
	}
	return nil
}

// describeFind names a find query for its messages.
func describeFind(pat, sub string) string {
	switch {
	case pat != "" && sub != "":
		return fmt.Sprintf("%q containing %q", pat, sub)
	case pat != "":
		return fmt.Sprintf("%q", pat)
	default:
		return fmt.Sprintf("path containing %q", sub)
	}
}

// handleMove renames or moves a file inside the workspace. The source is
// backed up first (.bak beside it), the destination's directories are
// created, and an existing destination is refused unless --force.
func (e *Engine) handleMove(args []string) error {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	from := fs.String("from", "", "")
	to := fs.String("to", "", "")
	force := fs.Bool("force", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	src, dst := strings.TrimSpace(*from), strings.TrimSpace(*to)
	if src == "" || dst == "" {
		return errors.New("move needs --from and --to")
	}
	if err := e.validateWritePath(src); err != nil {
		return err
	}
	if err := e.validateWritePath(dst); err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("move: %w", err)
	}
	if _, err := os.Stat(dst); err == nil && !*force {
		return fmt.Errorf("move: %s exists; pass --force to replace it", dst)
	}
	if !info.IsDir() {
		if err := createBackup(src); err != nil {
			e.errorf("WARN: backup failed for %s: %v\n", src, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("move: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("move: %w", err)
	}
	e.printf("✅ Moved '%s' to '%s'.\n", src, dst)
	return nil
}

// handleDelete removes a file (never a directory) inside the workspace,
// keeping a .bak copy beside where it was so rollback can bring it back.
func (e *Engine) handleDelete(args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	file := fs.String("file", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	f := strings.TrimSpace(*file)
	if f == "" {
		return errors.New("delete needs --file")
	}
	if err := e.validateWritePath(f); err != nil {
		return err
	}
	info, err := os.Stat(f)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("delete: %s is a directory; this verb removes files only (use exec for directories, deliberately)", f)
	}
	if err := createBackup(f); err != nil {
		return fmt.Errorf("delete: backup failed, nothing removed: %w", err)
	}
	if err := os.Remove(f); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	e.printf("✅ Deleted '%s' (backup at '%s.bak'; rollback restores it).\n", f, f)
	return nil
}
