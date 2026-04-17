package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

// handleWorktreeCommand manages git worktrees for isolated work.
// Usage:
//
//	/worktree create <branch>  - create a new worktree + branch and cd into it
//	/worktree list             - list active worktrees
//	/worktree remove <path>    - remove a worktree
//	/worktree status           - show current worktree info
func (cli *ChatCLI) handleWorktreeCommand(userInput string) {
	args := strings.Fields(userInput)

	if len(args) < 2 {
		cli.worktreeStatus()
		return
	}

	switch args[1] {
	case "create", "new":
		if len(args) < 3 {
			fmt.Println(colorize("  "+i18n.T("wt.cmd.usage_create"), ColorYellow))
			return
		}
		cli.worktreeCreate(args[2])
	case "list", "ls":
		cli.worktreeList()
	case "remove", "rm", "delete":
		if len(args) < 3 {
			fmt.Println(colorize("  "+i18n.T("wt.cmd.usage_remove"), ColorYellow))
			return
		}
		cli.worktreeRemove(args[2])
	case "status":
		cli.worktreeStatus()
	default:
		// Treat as branch name: /worktree feature-x
		cli.worktreeCreate(args[1])
	}
}

func (cli *ChatCLI) worktreeCreate(branch string) {
	// Check if we're in a git repo
	if !isGitRepo() {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.err_not_git_repo"), ColorRed))
		return
	}

	// Get repo root
	repoRoot := getGitRepoRoot()
	if repoRoot == "" {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.err_repo_root"), ColorRed))
		return
	}

	// Sanitize branch name
	branch = strings.TrimSpace(branch)
	safeName := strings.ReplaceAll(branch, "/", "-")

	// Create worktree path alongside the repo root
	worktreePath := filepath.Join(filepath.Dir(repoRoot), fmt.Sprintf("%s-worktree-%s", filepath.Base(repoRoot), safeName))

	// Check if branch already exists
	branchExists := false
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--list", branch).Output() //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	if err == nil && strings.TrimSpace(string(out)) != "" {
		branchExists = true
	}

	var cmd *exec.Cmd
	if branchExists {
		// Checkout existing branch in new worktree
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, branch) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	} else {
		// Create new branch in new worktree
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, worktreePath) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.err_create", strings.TrimSpace(string(output))), ColorRed))
		return
	}

	// Change to the worktree directory
	if err := os.Chdir(worktreePath); err != nil {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.warn_chdir_after_create", worktreePath, err), ColorYellow))
		return
	}

	fmt.Println(colorize("  "+i18n.T("wt.cmd.created_active"), ColorGreen))
	fmt.Println(colorize("    "+i18n.T("wt.cmd.kv_branch", branch), ColorCyan))
	fmt.Println(colorize("    "+i18n.T("wt.cmd.kv_path", worktreePath), ColorGray))
	fmt.Println()

	// Invalidate context builder cache since CWD changed
	if cli.contextBuilder != nil {
		cli.contextBuilder.InvalidateCache()
	}
}

func (cli *ChatCLI) worktreeList() {
	if !isGitRepo() {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.not_in_git_repo"), ColorYellow))
		return
	}

	cmd := exec.Command("git", "worktree", "list")
	output, err := cmd.Output()
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.err_generic", err), ColorRed))
		return
	}

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("wt.cmd.title_list"), ColorCyan))
	fmt.Println(colorize("  "+strings.Repeat("─", 50), ColorGray))

	cwd, _ := os.Getwd()
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indicator := "  "
		parts := strings.Fields(line)
		if len(parts) > 0 && parts[0] == cwd {
			indicator = colorize("→ ", ColorGreen)
		}
		fmt.Printf("  %s%s\n", indicator, line)
	}
	fmt.Println()
}

func (cli *ChatCLI) worktreeRemove(target string) {
	if !isGitRepo() {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.not_in_git_repo"), ColorYellow))
		return
	}

	// Try as path first, then as branch name
	removePath := target
	if !filepath.IsAbs(removePath) {
		// Try to find by branch name in worktree list
		cmd := exec.Command("git", "worktree", "list", "--porcelain")
		output, err := cmd.Output()
		if err == nil {
			lines := strings.Split(string(output), "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "branch refs/heads/"+target) {
					// Found the branch, get the path from the previous "worktree" line
					for j := i - 1; j >= 0; j-- {
						if strings.HasPrefix(lines[j], "worktree ") {
							removePath = strings.TrimPrefix(lines[j], "worktree ")
							break
						}
					}
					break
				}
			}
		}
	}

	// Don't remove the main worktree
	repoRoot := getGitRepoRoot()
	if removePath == repoRoot {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.err_remove_main"), ColorRed))
		return
	}

	cwd, _ := os.Getwd()
	if cwd == removePath {
		// We're in the worktree being removed, cd to repo root first
		if err := os.Chdir(repoRoot); err != nil {
			fmt.Println(colorize("  "+i18n.T("wt.cmd.err_chdir", err), ColorRed))
			return
		}
		fmt.Println(colorize("  "+i18n.T("wt.cmd.back_to_main", repoRoot), ColorGray))
	}

	cmd := exec.Command("git", "worktree", "remove", removePath) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	_, err := cmd.CombinedOutput()
	if err != nil {
		// Try force remove
		cmd = exec.Command("git", "worktree", "remove", "--force", removePath) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Println(colorize("  "+i18n.T("wt.cmd.err_remove", strings.TrimSpace(string(output))), ColorRed))
			return
		}
	}

	fmt.Println(colorize("  "+i18n.T("wt.cmd.removed", removePath), ColorGreen))

	if cli.contextBuilder != nil {
		cli.contextBuilder.InvalidateCache()
	}
}

func (cli *ChatCLI) worktreeStatus() {
	if !isGitRepo() {
		fmt.Println(colorize("  "+i18n.T("wt.cmd.not_in_git_repo"), ColorYellow))
		return
	}

	cwd, _ := os.Getwd()
	branch := getCurrentBranch()
	repoRoot := getGitRepoRoot()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("wt.cmd.title_status"), ColorCyan))
	fmt.Println(colorize("  "+strings.Repeat("─", 50), ColorGray))
	fmt.Printf("  %s    %s\n", i18n.T("wt.cmd.label_cwd"), cwd)
	fmt.Printf("  %s %s\n", i18n.T("wt.cmd.label_branch"), colorize(branch, ColorGreen))
	fmt.Printf("  %s   %s\n", i18n.T("wt.cmd.label_repo"), repoRoot)

	isWorktree := cwd != repoRoot
	if isWorktree {
		fmt.Printf("  %s   %s\n", i18n.T("wt.cmd.label_type"), colorize(i18n.T("wt.cmd.type_linked"), ColorCyan))
	} else {
		fmt.Printf("  %s   %s\n", i18n.T("wt.cmd.label_type"), colorize(i18n.T("wt.cmd.type_main"), ColorGray))
	}

	// Count worktrees
	cmd := exec.Command("git", "worktree", "list")
	if out, err := cmd.Output(); err == nil {
		count := len(strings.Split(strings.TrimSpace(string(out)), "\n"))
		fmt.Printf("  %s  %s\n", i18n.T("wt.cmd.label_total"), i18n.T("wt.cmd.total_count", count))
	}
	fmt.Println()
}

// --- git helpers ---

func isGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func getGitRepoRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getCurrentBranch() string {
	cmd := exec.Command("git", "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return "detached"
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "detached"
	}
	return branch
}

// getWorktreeSuggestions returns autocomplete suggestions for /worktree.
func (cli *ChatCLI) getWorktreeSuggestions(d prompt.Document) []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "create", Description: i18n.T("wt.cmd.sugg_create")},
		{Text: "list", Description: i18n.T("wt.cmd.sugg_list")},
		{Text: "remove", Description: i18n.T("wt.cmd.sugg_remove")},
		{Text: "status", Description: i18n.T("wt.cmd.sugg_status")},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}
