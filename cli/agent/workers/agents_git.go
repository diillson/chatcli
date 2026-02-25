package workers

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// GitAgent is the specialized agent for version control operations.
type GitAgent struct {
	skills *SkillSet
}

// NewGitAgent creates a GitAgent with its pre-built skills.
func NewGitAgent() *GitAgent {
	a := &GitAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *GitAgent) Type() AgentType  { return AgentTypeGit }
func (a *GitAgent) Name() string     { return "GitAgent" }
func (a *GitAgent) IsReadOnly() bool { return false }
func (a *GitAgent) AllowedCommands() []string {
	return []string{"git-status", "git-diff", "git-log", "git-changed", "git-branch", "exec"}
}

func (a *GitAgent) Description() string {
	return "Expert in version control operations. " +
		"Can check status, view diffs, browse history, manage branches, and create commits. " +
		"The exec command is restricted to git-only operations."
}

func (a *GitAgent) SystemPrompt() string {
	return `You are a specialized GIT agent in ChatCLI.
Your expertise: version control, commits, branches, diffs, history.

## YOUR ROLE
- Check repository status and changed files
- View diffs (staged and unstaged)
- Browse commit history
- Create branches and commits
- Analyze changes for code review

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- git-status: Show repository status
- git-diff: Show file changes (with optional --staged flag)
- git-log: Show commit history (with optional --limit flag)
- git-changed: List changed files
- git-branch: Show/manage branches
- exec: Execute git commands ONLY (e.g., git add, git commit)

## RULES
1. The exec command MUST only run git commands. No other executables allowed.
2. Never force-push or use destructive git operations (reset --hard, clean -f)
3. Always check git status before committing
4. Write descriptive commit messages that explain the "why"
5. When reviewing changes, provide structured analysis

## RESPONSE FORMAT
1. Start with <reasoning> (what git operations you need and why)
2. Emit <tool_call> tags for git operations
3. After execution, summarize the repository state`
}

func (a *GitAgent) Skills() *SkillSet { return a.skills }

func (a *GitAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	config := WorkerReActConfig{
		MaxTurns:        DefaultWorkerMaxTurns,
		SystemPrompt:    a.SystemPrompt(),
		AllowedCommands: a.AllowedCommands(),
		ReadOnly:        false,
	}
	result, err := RunWorkerReAct(ctx, config, task, deps.LLMClient, deps.LockMgr, a.skills, deps.Logger)
	if result != nil {
		result.Agent = a.Type()
		result.Task = task
	}
	return result, err
}

func (a *GitAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "smart-commit",
		Description: "Run git status + git diff, generate commit message, and commit",
		Type:        SkillExecutable,
		Script:      smartCommitScript,
	})
	a.skills.Register(&Skill{
		Name:        "review-changes",
		Description: "Show comprehensive diff summary with file-by-file analysis",
		Type:        SkillExecutable,
		Script:      reviewChangesScript,
	})
	a.skills.Register(&Skill{
		Name:        "create-branch",
		Description: "Create and switch to a new feature branch",
		Type:        SkillDescriptive,
	})
}

// smartCommitScript runs git status and diff to prepare for a commit.
func smartCommitScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	var results strings.Builder

	commands := []struct {
		name string
		cmd  string
		args []string
	}{
		{"Status", "git-status", []string{}},
		{"Staged Diff", "git-diff", []string{"--staged", "true"}},
	}

	for _, c := range commands {
		var buf bytes.Buffer
		outWriter := engine.NewStreamWriter(func(line string) {
			buf.WriteString(line)
			buf.WriteString("\n")
		})
		errWriter := engine.NewStreamWriter(func(line string) {
			buf.WriteString(line)
			buf.WriteString("\n")
		})

		eng := engine.NewEngine(outWriter, errWriter)
		err := eng.Execute(ctx, c.cmd, c.args)
		outWriter.Flush()
		errWriter.Flush()

		fmt.Fprintf(&results, "### %s\n%s\n", c.name, buf.String())
		if err != nil {
			fmt.Fprintf(&results, "Error: %v\n", err)
		}
	}

	return results.String(), nil
}

// reviewChangesScript provides a comprehensive diff review.
func reviewChangesScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	var results strings.Builder

	commands := []struct {
		name string
		cmd  string
		args []string
	}{
		{"Changed Files", "git-changed", []string{}},
		{"Full Diff", "git-diff", []string{}},
		{"Recent Commits", "git-log", []string{"--limit", "5"}},
	}

	for _, c := range commands {
		var buf bytes.Buffer
		outWriter := engine.NewStreamWriter(func(line string) {
			buf.WriteString(line)
			buf.WriteString("\n")
		})
		errWriter := engine.NewStreamWriter(func(line string) {
			buf.WriteString(line)
			buf.WriteString("\n")
		})

		eng := engine.NewEngine(outWriter, errWriter)
		err := eng.Execute(ctx, c.cmd, c.args)
		outWriter.Flush()
		errWriter.Flush()

		fmt.Fprintf(&results, "### %s\n%s\n", c.name, buf.String())
		if err != nil {
			fmt.Fprintf(&results, "Error: %v\n", err)
		}
	}

	return results.String(), nil
}
