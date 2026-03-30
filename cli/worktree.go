package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	prompt "github.com/c-bata/go-prompt"
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
			fmt.Println(colorize("  Uso: /worktree create <branch-name>", ColorYellow))
			return
		}
		cli.worktreeCreate(args[2])
	case "list", "ls":
		cli.worktreeList()
	case "remove", "rm", "delete":
		if len(args) < 3 {
			fmt.Println(colorize("  Uso: /worktree remove <path-or-branch>", ColorYellow))
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
		fmt.Println(colorize("  Erro: não estamos em um repositório git.", ColorRed))
		return
	}

	// Get repo root
	repoRoot := getGitRepoRoot()
	if repoRoot == "" {
		fmt.Println(colorize("  Erro: não foi possível determinar a raiz do repositório.", ColorRed))
		return
	}

	// Sanitize branch name
	branch = strings.TrimSpace(branch)
	safeName := strings.ReplaceAll(branch, "/", "-")

	// Create worktree path alongside the repo root
	worktreePath := filepath.Join(filepath.Dir(repoRoot), fmt.Sprintf("%s-worktree-%s", filepath.Base(repoRoot), safeName))

	// Check if branch already exists
	branchExists := false
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--list", branch).Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		branchExists = true
	}

	var cmd *exec.Cmd
	if branchExists {
		// Checkout existing branch in new worktree
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, branch)
	} else {
		// Create new branch in new worktree
		cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, worktreePath)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf("  Erro ao criar worktree: %s", strings.TrimSpace(string(output))), ColorRed))
		return
	}

	// Change to the worktree directory
	if err := os.Chdir(worktreePath); err != nil {
		fmt.Println(colorize(fmt.Sprintf("  Worktree criado em %s mas falhou ao mudar diretório: %v", worktreePath, err), ColorYellow))
		return
	}

	fmt.Println(colorize("  Worktree criado e ativo:", ColorGreen))
	fmt.Println(colorize(fmt.Sprintf("    Branch: %s", branch), ColorCyan))
	fmt.Println(colorize(fmt.Sprintf("    Path:   %s", worktreePath), ColorGray))
	fmt.Println()

	// Invalidate context builder cache since CWD changed
	if cli.contextBuilder != nil {
		cli.contextBuilder.InvalidateCache()
	}
}

func (cli *ChatCLI) worktreeList() {
	if !isGitRepo() {
		fmt.Println(colorize("  Não estamos em um repositório git.", ColorYellow))
		return
	}

	cmd := exec.Command("git", "worktree", "list")
	output, err := cmd.Output()
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf("  Erro: %v", err), ColorRed))
		return
	}

	fmt.Println()
	fmt.Println(colorize("  Git Worktrees", ColorCyan))
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
		fmt.Println(colorize("  Não estamos em um repositório git.", ColorYellow))
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
		fmt.Println(colorize("  Não é possível remover o worktree principal.", ColorRed))
		return
	}

	cwd, _ := os.Getwd()
	if cwd == removePath {
		// We're in the worktree being removed, cd to repo root first
		if err := os.Chdir(repoRoot); err != nil {
			fmt.Println(colorize(fmt.Sprintf("  Erro ao mudar diretório: %v", err), ColorRed))
			return
		}
		fmt.Println(colorize(fmt.Sprintf("  Voltando ao diretório principal: %s", repoRoot), ColorGray))
	}

	cmd := exec.Command("git", "worktree", "remove", removePath)
	_, err := cmd.CombinedOutput()
	if err != nil {
		// Try force remove
		cmd = exec.Command("git", "worktree", "remove", "--force", removePath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Println(colorize(fmt.Sprintf("  Erro ao remover worktree: %s", strings.TrimSpace(string(output))), ColorRed))
			return
		}
	}

	fmt.Println(colorize(fmt.Sprintf("  Worktree removido: %s", removePath), ColorGreen))

	if cli.contextBuilder != nil {
		cli.contextBuilder.InvalidateCache()
	}
}

func (cli *ChatCLI) worktreeStatus() {
	if !isGitRepo() {
		fmt.Println(colorize("  Não estamos em um repositório git.", ColorYellow))
		return
	}

	cwd, _ := os.Getwd()
	branch := getCurrentBranch()
	repoRoot := getGitRepoRoot()

	fmt.Println()
	fmt.Println(colorize("  Worktree Status", ColorCyan))
	fmt.Println(colorize("  "+strings.Repeat("─", 50), ColorGray))
	fmt.Printf("  CWD:    %s\n", cwd)
	fmt.Printf("  Branch: %s\n", colorize(branch, ColorGreen))
	fmt.Printf("  Repo:   %s\n", repoRoot)

	isWorktree := cwd != repoRoot
	if isWorktree {
		fmt.Printf("  Type:   %s\n", colorize("worktree (linked)", ColorCyan))
	} else {
		fmt.Printf("  Type:   %s\n", colorize("main worktree", ColorGray))
	}

	// Count worktrees
	cmd := exec.Command("git", "worktree", "list")
	if out, err := cmd.Output(); err == nil {
		count := len(strings.Split(strings.TrimSpace(string(out)), "\n"))
		fmt.Printf("  Total:  %d worktrees\n", count)
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
		{Text: "create", Description: "Cria um novo worktree com branch"},
		{Text: "list", Description: "Lista worktrees ativos"},
		{Text: "remove", Description: "Remove um worktree"},
		{Text: "status", Description: "Mostra informações do worktree atual"},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}
