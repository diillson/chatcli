package engine

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// gitReadOnlyConfig neutralizes the repository settings that make a
// read-only git command run a program. The git-* subcommands are allowed
// without a prompt, and the repository they read may be one the workspace
// ships: an embedded bare repository whose config sets core.fsmonitor runs
// that program on every status. A command-line -c wins over every config
// file, and git ignores keys it does not know, so the list is safe on any
// version; safe.bareRepository needs git 2.38, and checkGitLayout covers
// the same case on older versions.
var gitReadOnlyConfig = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=" + os.DevNull,
	"-c", "safe.bareRepository=explicit",
	"-c", "log.showSignature=false",
	"-c", "core.pager=cat",
}

// gitReadOnlyEnv drops the system config, never prompts, and skips the
// optional index lock a status refresh would otherwise take and write.
var gitReadOnlyEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_OPTIONAL_LOCKS=0",
	"GIT_PAGER=cat",
}

// withExplicitBareRepos adds safe.bareRepository=explicit to the git
// configuration a shell line run by exec or test sees, through the
// GIT_CONFIG_COUNT environment protocol (git 2.31 and later; the key
// itself needs 2.38). A read-only git command such as git status is
// auto-allowed through exec, and run inside a bare repository the
// workspace ships it would load that repository's config, whose
// core.fsmonitor runs a program. An explicitly named repository
// (--git-dir, GIT_DIR) still works. Entries the user already set are
// kept: the new one is appended after them.
func withExplicitBareRepos(env []string) []string {
	count := 0
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				// git refuses a malformed count on its own; leave it be.
				return env
			}
			count = n
		}
	}
	out := make([]string, 0, len(env)+3)
	out = append(out, env...)
	return append(out,
		fmt.Sprintf("GIT_CONFIG_KEY_%d=safe.bareRepository", count),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=explicit", count),
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", count+1),
	)
}

// prepareGitDir expands and validates the --dir of a git-* subcommand and
// refuses a repository whose layout git only accepts implicitly.
func (e *Engine) prepareGitDir(ctx context.Context, dir string) (string, error) {
	dir = expandPath(dir)
	if dir == "" {
		dir = "."
	}
	if err := e.validatePath(dir); err != nil {
		return "", err
	}
	if err := checkGitLayout(ctx, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// checkGitLayout requires the repository found from dir to be an ordinary
// work tree: its git directory is <top>/.git, or <top>/.git is a gitfile
// (a linked worktree or a submodule). A bare repository the workspace
// ships, or one whose config points core.worktree back at the workspace,
// is refused even on a git too old to know safe.bareRepository.
// rev-parse runs no hook, filter or fsmonitor, so asking it is safe.
func checkGitLayout(ctx context.Context, dir string) error {
	out, err := runGitCommand(ctx, dir, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	if err != nil {
		// Not a repository, or git refused it: the real command reports it.
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return fmt.Errorf("git: refusing %q: cannot determine the repository layout", dir)
	}
	top, gitDir := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	dotGit := filepath.Join(top, ".git")
	info, statErr := os.Stat(dotGit)
	if statErr != nil {
		return fmt.Errorf("git: refusing %q: the work tree %q has no .git entry", dir, top)
	}
	if info.Mode().IsRegular() {
		return nil
	}
	if !info.IsDir() || !sameFile(dotGit, gitDir) {
		return fmt.Errorf("git: refusing %q: its git directory %q is not %q", dir, gitDir, dotGit)
	}
	return nil
}

func sameFile(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// runGitCommand runs one read-only git command with the hardened config.
func runGitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	full := make([]string, 0, len(gitReadOnlyConfig)+len(args))
	full = append(full, gitReadOnlyConfig...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...) //#nosec G204 -- fixed git binary; subcommand and flags are engine-built
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitReadOnlyEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *Engine) handleGitStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git-status", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	repo, err := e.prepareGitDir(ctx, *dir)
	if err != nil {
		return err
	}

	out, err := runGitCommand(ctx, repo, "status", "-sb")
	return e.printCommandOutput(out, err)
}

func (e *Engine) handleGitDiff(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git-diff", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	staged := fs.Bool("staged", false, "")
	nameOnly := fs.Bool("name-only", false, "")
	stat := fs.Bool("stat", false, "")
	path := fs.String("path", "", "")
	context := fs.Int("context", 3, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	repo, err := e.prepareGitDir(ctx, *dir)
	if err != nil {
		return err
	}

	// No external diff tool and no textconv driver: both run a program
	// the repository's config names.
	cmdArgs := []string{"diff", "--no-ext-diff", "--no-textconv", fmt.Sprintf("--unified=%d", *context)}
	if *staged {
		cmdArgs = append(cmdArgs, "--staged")
	}
	if *nameOnly {
		cmdArgs = append(cmdArgs, "--name-only")
	}
	if *stat {
		cmdArgs = append(cmdArgs, "--stat")
	}
	if *path != "" {
		cmdArgs = append(cmdArgs, "--", *path)
	}

	out, err := runGitCommand(ctx, repo, cmdArgs...)
	return e.printCommandOutput(out, err)
}

func (e *Engine) handleGitLog(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git-log", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	limit := fs.Int("limit", 20, "")
	path := fs.String("path", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	repo, err := e.prepareGitDir(ctx, *dir)
	if err != nil {
		return err
	}

	cmdArgs := []string{"log", "--oneline", fmt.Sprintf("-n%d", *limit)}
	if *path != "" {
		cmdArgs = append(cmdArgs, "--", *path)
	}

	out, err := runGitCommand(ctx, repo, cmdArgs...)
	return e.printCommandOutput(out, err)
}

func (e *Engine) handleGitChanged(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git-changed", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	repo, err := e.prepareGitDir(ctx, *dir)
	if err != nil {
		return err
	}

	out, err := runGitCommand(ctx, repo, "status", "--porcelain")
	if err != nil {
		return e.printCommandOutput(out, err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	files := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if len(l) > 3 {
			files = append(files, strings.TrimSpace(l[3:]))
		}
	}

	for _, f := range files {
		e.println(f)
	}
	return nil
}

func (e *Engine) handleGitBranch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git-branch", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	repo, err := e.prepareGitDir(ctx, *dir)
	if err != nil {
		return err
	}

	out, err := runGitCommand(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	return e.printCommandOutput(out, err)
}
