package workers

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// DiagnosticsAgent is the specialized agent for troubleshooting and investigating issues.
type DiagnosticsAgent struct {
	skills *SkillSet
}

// NewDiagnosticsAgent creates a DiagnosticsAgent with its pre-built skills.
func NewDiagnosticsAgent() *DiagnosticsAgent {
	a := &DiagnosticsAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *DiagnosticsAgent) Type() AgentType  { return AgentTypeDiagnostics }
func (a *DiagnosticsAgent) Name() string     { return "DiagnosticsAgent" }
func (a *DiagnosticsAgent) IsReadOnly() bool { return false }
func (a *DiagnosticsAgent) AllowedCommands() []string {
	return []string{"read", "search", "tree", "exec"}
}

func (a *DiagnosticsAgent) Description() string {
	return "Expert in troubleshooting, root cause analysis, and system diagnostics. " +
		"Can analyze errors, stack traces, dependency issues, and performance bottlenecks. " +
		"Combines code reading with command execution for investigative workflows."
}

func (a *DiagnosticsAgent) SystemPrompt() string {
	return `You are a specialized DIAGNOSTICS agent in ChatCLI.
Your expertise: troubleshooting, root cause analysis, error investigation, dependency auditing.

## YOUR ROLE
- Analyze error messages, stack traces, and log output to identify root causes
- Run diagnostic commands to gather system and project state
- Check dependency health (go mod, package versions, conflicts)
- Profile performance issues and identify bottlenecks
- Trace execution paths through code to find where things go wrong

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read file contents. Args: {"file":"path/to/file"} (optionally "start" and "end" for line range)
- search: Search for patterns. Args: {"term":"pattern","dir":"."}
- tree: List directory structure. Args: {"dir":"."}
- exec: Execute diagnostic commands. Args: {"cmd":"go mod verify"}

IMPORTANT: The key for file path is "file", NOT "path".

## RULES
1. Start with the error/symptom and work backwards to find the root cause.
2. Gather evidence before making conclusions — read files, run commands, search patterns.
3. Check the obvious first: typos, missing imports, nil pointers, wrong types.
4. For dependency issues, always run go mod verify before deeper investigation.
5. Provide clear chain of reasoning: symptom → evidence → root cause → fix.
6. Never run destructive commands — diagnostics only.

## RESPONSE FORMAT
1. Start with <reasoning> (symptom analysis and investigation plan)
2. Emit <tool_call> tags for diagnostic operations
3. Provide structured diagnosis:

### Symptom
What was reported.

### Investigation
Steps taken and evidence found.

### Root Cause
The actual problem, with file:line references.

### Recommended Fix
Specific, actionable steps to resolve.`
}

func (a *DiagnosticsAgent) Skills() *SkillSet { return a.skills }

func (a *DiagnosticsAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	config := WorkerReActConfig{
		MaxTurns:        DefaultWorkerMaxTurns,
		SystemPrompt:    a.SystemPrompt(),
		AllowedCommands: a.AllowedCommands(),
		ReadOnly:        false,
	}
	result, err := RunWorkerReAct(ctx, config, task, deps.LLMClient, deps.LockMgr, a.skills, deps.PolicyChecker, deps.Logger)
	if result != nil {
		result.Agent = a.Type()
		result.Task = task
	}
	return result, err
}

func (a *DiagnosticsAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "analyze-error",
		Description: "Parse an error message or stack trace and map it to source code locations",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "check-deps",
		Description: "Run go mod tidy, go mod verify, and check for dependency issues",
		Type:        SkillExecutable,
		Script:      checkDepsScript,
	})
	a.skills.Register(&Skill{
		Name:        "bisect-bug",
		Description: "Guide investigation to find the commit that introduced a bug using git history",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "profile-bottleneck",
		Description: "Run benchmarks or pprof and analyze performance hotspots",
		Type:        SkillDescriptive,
	})
}

// checkDepsScript runs go mod diagnostics to check dependency health.
func checkDepsScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	commands := []struct {
		name string
		cmd  string
	}{
		{"go mod verify", fmt.Sprintf("cd %s && go mod verify 2>&1", dir)},
		{"go mod tidy (check)", fmt.Sprintf("cd %s && go mod tidy -v 2>&1", dir)},
		{"go list (missing)", fmt.Sprintf("cd %s && go list -m -mod=mod all 2>&1 | head -50", dir)},
	}

	var results strings.Builder
	results.WriteString("## Dependency Health Check\n\n")

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
		err := eng.Execute(ctx, "exec", []string{"--cmd", c.cmd})
		outWriter.Flush()
		errWriter.Flush()

		output := strings.TrimSpace(buf.String())
		if err != nil {
			fmt.Fprintf(&results, "### %s: ISSUES FOUND\n```\n%s\n```\n\n", c.name, output)
		} else if output == "" || output == "all modules verified" {
			fmt.Fprintf(&results, "### %s: OK\n\n", c.name)
		} else {
			fmt.Fprintf(&results, "### %s\n```\n%s\n```\n\n", c.name, output)
		}
	}

	return results.String(), nil
}
