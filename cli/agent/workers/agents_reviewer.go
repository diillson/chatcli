package workers

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// ReviewerAgent is the specialized agent for code quality analysis and review.
type ReviewerAgent struct {
	skills *SkillSet
}

// NewReviewerAgent creates a ReviewerAgent with its pre-built skills.
func NewReviewerAgent() *ReviewerAgent {
	a := &ReviewerAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *ReviewerAgent) Type() AgentType  { return AgentTypeReviewer }
func (a *ReviewerAgent) Name() string     { return "ReviewerAgent" }
func (a *ReviewerAgent) IsReadOnly() bool { return true }
func (a *ReviewerAgent) AllowedCommands() []string {
	return []string{"read", "search", "tree"}
}

func (a *ReviewerAgent) Description() string {
	return "Expert in code review, quality analysis, and security auditing. " +
		"Can review files for bugs, code smells, SOLID violations, and security issues. " +
		"Can review staged git diffs and run lint checks. READ-ONLY access only."
}

func (a *ReviewerAgent) SystemPrompt() string {
	return `You are a specialized CODE REVIEWER agent in ChatCLI.
Your expertise: code quality, bug detection, security analysis, best practices enforcement.

## YOUR ROLE
- Review source code for bugs, logic errors, and edge cases
- Identify code smells, SOLID violations, and anti-patterns
- Detect security vulnerabilities (injection, XSS, auth flaws, secrets in code)
- Assess readability, maintainability, and testability
- Provide actionable, prioritized feedback

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read file contents. Args: {"file":"path/to/file"} (optionally "start" and "end" for line range)
- search: Search for patterns in files. Args: {"term":"pattern","dir":"path/to/dir"}
- tree: List directory structure. Args: {"dir":"path/to/dir"}

IMPORTANT: The key for file path is "file", NOT "path".

## RULES
1. You are READ-ONLY. Never attempt write, patch, exec, or any modification.
2. Be specific — reference file paths, line numbers, and exact code snippets.
3. Prioritize findings: CRITICAL > HIGH > MEDIUM > LOW.
4. For each issue, explain WHY it's a problem and suggest a concrete fix.
5. Don't nitpick style — focus on correctness, security, and maintainability.
6. Acknowledge good patterns when you see them.

## RESPONSE FORMAT
1. Start with <reasoning> (what you plan to review and why)
2. Emit <tool_call> tags for reading operations
3. Provide a structured review:

### Summary
Overall assessment (1-2 sentences).

### Critical Issues
- [CRITICAL] file:line — description and fix suggestion

### Improvements
- [HIGH/MEDIUM/LOW] file:line — description and fix suggestion

### Positives
- Good patterns observed`
}

func (a *ReviewerAgent) Skills() *SkillSet { return a.skills }

func (a *ReviewerAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	config := WorkerReActConfig{
		MaxTurns:        DefaultWorkerMaxTurns,
		SystemPrompt:    a.SystemPrompt(),
		AllowedCommands: a.AllowedCommands(),
		ReadOnly:        true,
	}
	result, err := RunWorkerReAct(ctx, config, task, deps.LLMClient, deps.LockMgr, a.skills, deps.PolicyChecker, deps.Logger)
	if result != nil {
		result.Agent = a.Type()
		result.Task = task
	}
	return result, err
}

func (a *ReviewerAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "review-file",
		Description: "Analyze a file for bugs, code smells, SOLID violations, and security issues",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "diff-review",
		Description: "Review staged git changes — runs git-diff and git-changed to produce structured review",
		Type:        SkillExecutable,
		Script:      diffReviewScript,
	})
	a.skills.Register(&Skill{
		Name:        "scan-lint",
		Description: "Run go vet and staticcheck (if available) and parse output into categorized issues",
		Type:        SkillExecutable,
		Script:      scanLintScript,
	})
}

// diffReviewScript fetches staged diff and changed files for review.
func diffReviewScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	commands := []struct {
		name string
		cmd  string
		args []string
	}{
		{"Changed Files", "git-changed", []string{"--dir", dir}},
		{"Staged Diff", "git-diff", []string{"--staged", "true", "--dir", dir}},
		{"Unstaged Diff", "git-diff", []string{"--dir", dir}},
	}

	var results strings.Builder
	results.WriteString("## Diff Review Data\n\n")

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

// scanLintScript runs go vet (and staticcheck if available) to find issues.
func scanLintScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	linters := []struct {
		name string
		cmd  string
	}{
		{"go vet", fmt.Sprintf("cd %s && go vet ./... 2>&1", dir)},
		{"staticcheck", fmt.Sprintf("cd %s && staticcheck ./... 2>&1", dir)},
	}

	var results strings.Builder
	results.WriteString("## Lint Scan Results\n\n")

	for _, l := range linters {
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
		err := eng.Execute(ctx, "exec", []string{"--cmd", l.cmd})
		outWriter.Flush()
		errWriter.Flush()

		output := strings.TrimSpace(buf.String())
		if output == "" && err == nil {
			fmt.Fprintf(&results, "### %s: CLEAN (no issues)\n\n", l.name)
		} else {
			status := "ISSUES FOUND"
			if err != nil && strings.Contains(err.Error(), "not found") {
				status = "NOT AVAILABLE"
			}
			fmt.Fprintf(&results, "### %s: %s\n```\n%s\n```\n\n", l.name, status, output)
		}
	}

	return results.String(), nil
}
