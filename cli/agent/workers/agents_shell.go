package workers

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// ShellAgent is the specialized agent for executing commands, building, and testing.
type ShellAgent struct {
	skills *SkillSet
}

// NewShellAgent creates a ShellAgent with its pre-built skills.
func NewShellAgent() *ShellAgent {
	a := &ShellAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *ShellAgent) Type() AgentType       { return AgentTypeShell }
func (a *ShellAgent) Name() string           { return "ShellAgent" }
func (a *ShellAgent) IsReadOnly() bool       { return false }
func (a *ShellAgent) AllowedCommands() []string {
	return []string{"exec", "test"}
}

func (a *ShellAgent) Description() string {
	return "Expert in executing shell commands, building projects, and running tests. " +
		"Can run build commands, test suites, linters, and other CLI tools. " +
		"Respects security policies — never runs destructive commands."
}

func (a *ShellAgent) SystemPrompt() string {
	return `You are a specialized SHELL EXECUTION agent in ChatCLI.
Your expertise: running commands, building projects, executing test suites, linting.

## YOUR ROLE
- Execute build commands (go build, npm build, make, etc.)
- Run test suites and analyze results
- Execute linters and code quality tools
- Run any safe CLI command needed for the task

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- exec: Execute a shell command
- test: Run the project's test suite

## RULES
1. NEVER run destructive commands (rm -rf, dd, mkfs, etc.)
2. NEVER run commands that modify system configuration
3. Prefer specific, targeted commands over broad ones
4. Always include the working directory in exec args when relevant
5. Capture and analyze command output — don't just report success/failure
6. If a build or test fails, analyze the error output and provide actionable feedback

## RESPONSE FORMAT
1. Start with <reasoning> (what you need to execute and why)
2. Emit <tool_call> tags for commands
3. After execution, analyze the output and provide a clear summary`
}

func (a *ShellAgent) Skills() *SkillSet { return a.skills }

func (a *ShellAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
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

func (a *ShellAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "run-tests",
		Description: "Run go test ./... -json and parse results into structured summary",
		Type:        SkillExecutable,
		Script:      runTestsScript,
	})
	a.skills.Register(&Skill{
		Name:        "build-check",
		Description: "Run go build ./... && go vet ./... and report errors",
		Type:        SkillExecutable,
		Script:      buildCheckScript,
	})
	a.skills.Register(&Skill{
		Name:        "lint-fix",
		Description: "Run linter and analyze issues",
		Type:        SkillDescriptive,
	})
}

// runTestsScript executes go test and returns structured output.
func runTestsScript(ctx context.Context, input map[string]string, eng *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	var buf bytes.Buffer
	outWriter := engine.NewStreamWriter(func(line string) {
		buf.WriteString(line)
		buf.WriteString("\n")
	})
	errWriter := engine.NewStreamWriter(func(line string) {
		buf.WriteString("ERR: ")
		buf.WriteString(line)
		buf.WriteString("\n")
	})

	eng = engine.NewEngine(outWriter, errWriter)
	err := eng.Execute(ctx, "test", []string{"--dir", dir})
	outWriter.Flush()
	errWriter.Flush()

	output := buf.String()
	var summary strings.Builder
	summary.WriteString("## Test Results\n\n")

	if err != nil {
		summary.WriteString("Status: FAILED\n\n")
	} else {
		summary.WriteString("Status: PASSED\n\n")
	}
	summary.WriteString("```\n")
	summary.WriteString(output)
	summary.WriteString("```\n")

	return summary.String(), err
}

// buildCheckScript runs go build and go vet.
func buildCheckScript(ctx context.Context, input map[string]string, eng *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	var results strings.Builder

	// Run go build
	commands := []struct {
		name string
		cmd  string
	}{
		{"Build", fmt.Sprintf("cd %s && go build ./...", dir)},
		{"Vet", fmt.Sprintf("cd %s && go vet ./...", dir)},
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

		eng = engine.NewEngine(outWriter, errWriter)
		err := eng.Execute(ctx, "exec", []string{"--cmd", c.cmd})
		outWriter.Flush()
		errWriter.Flush()

		output := buf.String()
		if err != nil {
			fmt.Fprintf(&results, "### %s: FAILED\n%s\n", c.name, output)
			return results.String(), err
		}
		fmt.Fprintf(&results, "### %s: OK\n", c.name)
		if strings.TrimSpace(output) != "" {
			results.WriteString(output)
			results.WriteString("\n")
		}
	}

	return results.String(), nil
}
