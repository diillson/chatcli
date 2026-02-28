package workers

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// DepsAgent is the specialized agent for dependency management and auditing.
type DepsAgent struct {
	skills *SkillSet
}

// NewDepsAgent creates a DepsAgent with its pre-built skills.
func NewDepsAgent() *DepsAgent {
	a := &DepsAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *DepsAgent) Type() AgentType  { return AgentTypeDeps }
func (a *DepsAgent) Name() string     { return "DepsAgent" }
func (a *DepsAgent) IsReadOnly() bool { return false }
func (a *DepsAgent) AllowedCommands() []string {
	return []string{"read", "exec", "search", "tree"}
}

func (a *DepsAgent) Description() string {
	return "Expert in dependency management, auditing, and vulnerability scanning. " +
		"Can analyze go.mod/go.sum, find outdated packages, audit for vulnerabilities, " +
		"and explain dependency chains. Understands Go modules deeply."
}

func (a *DepsAgent) SystemPrompt() string {
	return `You are a specialized DEPENDENCY MANAGEMENT agent in ChatCLI.
Your expertise: Go modules, dependency auditing, vulnerability scanning, version management.

## YOUR ROLE
- Analyze go.mod and go.sum for dependency health
- Find outdated dependencies and suggest updates
- Audit for known vulnerabilities using govulncheck
- Explain why a specific dependency exists (dependency chain)
- Detect unused dependencies and version conflicts

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read go.mod, go.sum, or source files. Args: {"file":"go.mod"}
- exec: Run dependency commands. Args: {"cmd":"go mod verify"}
- search: Find import usages. Args: {"term":"github.com/pkg","dir":".","glob":"*.go"}
- tree: List directory structure. Args: {"dir":"."}

IMPORTANT: The key for file path is "file", NOT "path".

## RULES
1. Always read go.mod first to understand the dependency landscape.
2. Run go mod verify before making any dependency changes.
3. Never run go get -u blindly — check what will change first with go list -m -u.
4. When explaining deps, use go mod why and go mod graph for evidence.
5. For vulnerability scanning, use govulncheck if available.
6. Be cautious with major version updates — flag breaking changes.

## RESPONSE FORMAT
1. Start with <reasoning> (dependency question and analysis plan)
2. Emit <tool_call> tags for dependency operations
3. Provide structured report:

### Dependency Status
Overview of current state.

### Issues Found
Specific problems with versions, vulnerabilities, or conflicts.

### Recommendations
Actionable steps, ordered by priority.`
}

func (a *DepsAgent) Skills() *SkillSet { return a.skills }

func (a *DepsAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
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

func (a *DepsAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "audit-deps",
		Description: "Run go mod verify and govulncheck (if available) to audit dependency health and vulnerabilities",
		Type:        SkillExecutable,
		Script:      auditDepsScript,
	})
	a.skills.Register(&Skill{
		Name:        "update-deps",
		Description: "List outdated dependencies with available updates (dry-run, no changes)",
		Type:        SkillExecutable,
		Script:      updateDepsScript,
	})
	a.skills.Register(&Skill{
		Name:        "why-dep",
		Description: "Explain why a specific dependency exists using go mod why and go mod graph",
		Type:        SkillExecutable,
		Script:      whyDepScript,
	})
	a.skills.Register(&Skill{
		Name:        "find-outdated",
		Description: "Find all dependencies that have newer versions available",
		Type:        SkillDescriptive,
	})
}

// auditDepsScript runs go mod verify and govulncheck.
func auditDepsScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	commands := []struct {
		name     string
		cmd      string
		optional bool
	}{
		{"go mod verify", fmt.Sprintf("cd %s && go mod verify 2>&1", dir), false},
		{"govulncheck", fmt.Sprintf("cd %s && govulncheck ./... 2>&1", dir), true},
	}

	var results strings.Builder
	results.WriteString("## Dependency Audit\n\n")

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

		if err != nil && c.optional && (strings.Contains(output, "not found") || strings.Contains(output, "not installed")) {
			fmt.Fprintf(&results, "### %s: NOT AVAILABLE\n", c.name)
			fmt.Fprintf(&results, "Install: `go install golang.org/x/vuln/cmd/govulncheck@latest`\n\n")
			continue
		}

		if err != nil {
			fmt.Fprintf(&results, "### %s: ISSUES FOUND\n```\n%s\n```\n\n", c.name, output)
		} else if output == "" || strings.Contains(output, "verified") || strings.Contains(output, "No vulnerabilities") {
			fmt.Fprintf(&results, "### %s: CLEAN\n\n", c.name)
		} else {
			fmt.Fprintf(&results, "### %s\n```\n%s\n```\n\n", c.name, output)
		}
	}

	return results.String(), nil
}

// updateDepsScript lists outdated dependencies without modifying anything.
func updateDepsScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	cmd := fmt.Sprintf("cd %s && go list -m -u -json all 2>&1 | grep -A2 '\"Update\"' | head -100", dir)

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
	err := eng.Execute(ctx, "exec", []string{"--cmd", cmd})
	outWriter.Flush()
	errWriter.Flush()

	var results strings.Builder
	results.WriteString("## Outdated Dependencies\n\n")

	output := strings.TrimSpace(buf.String())
	if output == "" && err == nil {
		results.WriteString("All dependencies are up to date!\n")
	} else if output == "" {
		results.WriteString("No update information available.\n")
	} else {
		results.WriteString("```json\n")
		results.WriteString(output)
		results.WriteString("\n```\n")
	}

	return results.String(), nil
}

// whyDepScript explains why a dependency exists.
func whyDepScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}
	module := input["module"]
	if module == "" {
		return "", fmt.Errorf("why-dep requires 'module' parameter (e.g., 'github.com/pkg/errors')")
	}

	commands := []struct {
		name string
		cmd  string
	}{
		{"go mod why", fmt.Sprintf("cd %s && go mod why -m %s 2>&1", dir, module)},
		{"go mod graph (filtered)", fmt.Sprintf("cd %s && go mod graph 2>&1 | grep %s | head -20", dir, module)},
	}

	var results strings.Builder
	fmt.Fprintf(&results, "## Why does %s exist?\n\n", module)

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
			fmt.Fprintf(&results, "### %s: Error\n```\n%s\n```\n\n", c.name, output)
		} else if output == "" {
			fmt.Fprintf(&results, "### %s: No results\n\n", c.name)
		} else {
			fmt.Fprintf(&results, "### %s\n```\n%s\n```\n\n", c.name, output)
		}
	}

	return results.String(), nil
}
