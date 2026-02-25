package engine

import (
	"flag"
	"fmt"
	"strings"
)

func (e *Engine) handleGitStatus(args []string) error {
	fs := flag.NewFlagSet("git-status", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	out, err := runCommand(*dir, "git", "status", "-sb")
	return e.printCommandOutput(out, err)
}

func (e *Engine) handleGitDiff(args []string) error {
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

	cmdArgs := []string{"diff", fmt.Sprintf("--unified=%d", *context)}
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

	out, err := runCommand(*dir, "git", cmdArgs...)
	return e.printCommandOutput(out, err)
}

func (e *Engine) handleGitLog(args []string) error {
	fs := flag.NewFlagSet("git-log", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	limit := fs.Int("limit", 20, "")
	path := fs.String("path", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	cmdArgs := []string{"log", "--oneline", fmt.Sprintf("-n%d", *limit)}
	if *path != "" {
		cmdArgs = append(cmdArgs, "--", *path)
	}

	out, err := runCommand(*dir, "git", cmdArgs...)
	return e.printCommandOutput(out, err)
}

func (e *Engine) handleGitChanged(args []string) error {
	fs := flag.NewFlagSet("git-changed", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	out, err := runCommand(*dir, "git", "status", "--porcelain")
	if err != nil {
		return e.printCommandOutput(out, err)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var files []string
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

func (e *Engine) handleGitBranch(args []string) error {
	fs := flag.NewFlagSet("git-branch", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	out, err := runCommand(*dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	return e.printCommandOutput(out, err)
}
