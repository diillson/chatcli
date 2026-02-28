package workers

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// FormatterAgent is the specialized agent for code formatting and style normalization.
type FormatterAgent struct {
	skills *SkillSet
}

// NewFormatterAgent creates a FormatterAgent with its pre-built skills.
func NewFormatterAgent() *FormatterAgent {
	a := &FormatterAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *FormatterAgent) Type() AgentType  { return AgentTypeFormatter }
func (a *FormatterAgent) Name() string     { return "FormatterAgent" }
func (a *FormatterAgent) IsReadOnly() bool { return false }
func (a *FormatterAgent) AllowedCommands() []string {
	return []string{"read", "patch", "exec", "tree"}
}

func (a *FormatterAgent) Description() string {
	return "Expert in code formatting, import organization, and style normalization. " +
		"Can run gofmt, goimports, and enforce consistent code style. " +
		"Mechanical and fast — minimal LLM reasoning needed."
}

func (a *FormatterAgent) SystemPrompt() string {
	return `You are a specialized CODE FORMATTER agent in ChatCLI.
Your expertise: code formatting, import organization, style normalization.

## YOUR ROLE
- Format Go code using gofmt and goimports
- Organize imports into standard/external/internal groups
- Fix minor style inconsistencies (trailing whitespace, blank lines)
- Normalize naming conventions when explicitly requested

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read file contents. Args: {"file":"path/to/file"}
- patch: Apply search/replace fixes. Args: {"file":"path","search":"old","replace":"new"}
- exec: Run formatters. Args: {"cmd":"gofmt -w file.go"}
- tree: List directory structure. Args: {"dir":"."}

IMPORTANT: The key for file path is "file", NOT "path".

## RULES
1. Prefer running standard tools (gofmt, goimports) over manual patching.
2. Never change code logic — only formatting and style.
3. Format entire files, not just snippets, for consistency.
4. If goimports is not available, fall back to gofmt.
5. Report what was changed and what was already clean.

## RESPONSE FORMAT
1. Start with <reasoning> (what needs formatting and which tool to use)
2. Emit <tool_call> tags for formatting operations
3. Report summary: files formatted, files already clean`
}

func (a *FormatterAgent) Skills() *SkillSet { return a.skills }

func (a *FormatterAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
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

func (a *FormatterAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "format-code",
		Description: "Run gofmt -w (or goimports -w if available) on Go files in a directory",
		Type:        SkillExecutable,
		Script:      formatCodeScript,
	})
	a.skills.Register(&Skill{
		Name:        "fix-imports",
		Description: "Run goimports to fix and organize imports in Go files",
		Type:        SkillExecutable,
		Script:      fixImportsScript,
	})
	a.skills.Register(&Skill{
		Name:        "normalize-style",
		Description: "Apply consistent naming and style conventions to code (LLM-driven)",
		Type:        SkillDescriptive,
	})
}

// formatCodeScript runs gofmt (or goimports) on Go files.
func formatCodeScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}
	file := input["file"]

	target := dir + "/..."
	if file != "" {
		target = file
	}

	// Try goimports first, fall back to gofmt
	formatters := []struct {
		name string
		cmd  string
	}{
		{"goimports", fmt.Sprintf("goimports -w %s 2>&1", target)},
		{"gofmt", fmt.Sprintf("gofmt -w %s 2>&1", target)},
	}

	var results strings.Builder
	results.WriteString("## Format Results\n\n")

	for _, f := range formatters {
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
		err := eng.Execute(ctx, "exec", []string{"--cmd", f.cmd})
		outWriter.Flush()
		errWriter.Flush()

		output := strings.TrimSpace(buf.String())
		if err == nil {
			if output == "" {
				fmt.Fprintf(&results, "### %s: Applied successfully (no warnings)\n\n", f.name)
			} else {
				fmt.Fprintf(&results, "### %s: Applied\n```\n%s\n```\n\n", f.name, output)
			}
			return results.String(), nil
		}

		// If goimports not found, try next
		if strings.Contains(output, "not found") || strings.Contains(output, "No such file") {
			fmt.Fprintf(&results, "### %s: not available, trying fallback...\n\n", f.name)
			continue
		}

		fmt.Fprintf(&results, "### %s: Error\n```\n%s\n```\n\n", f.name, output)
		return results.String(), err
	}

	results.WriteString("No formatter available. Install goimports: `go install golang.org/x/tools/cmd/goimports@latest`\n")
	return results.String(), nil
}

// fixImportsScript specifically runs goimports to organize imports.
func fixImportsScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}
	file := input["file"]

	var cmd string
	if file != "" {
		cmd = fmt.Sprintf("goimports -w %s 2>&1", file)
	} else {
		cmd = fmt.Sprintf("find %s -name '*.go' -not -path '*/vendor/*' -not -path '*/.git/*' -exec goimports -w {} + 2>&1", dir)
	}

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
	results.WriteString("## Import Fix Results\n\n")

	output := strings.TrimSpace(buf.String())
	if err != nil {
		if strings.Contains(output, "not found") {
			results.WriteString("goimports not installed. Install: `go install golang.org/x/tools/cmd/goimports@latest`\n")
		} else {
			fmt.Fprintf(&results, "Error: %s\n", output)
		}
	} else if output == "" {
		results.WriteString("Imports organized successfully.\n")
	} else {
		fmt.Fprintf(&results, "```\n%s\n```\n", output)
	}

	return results.String(), err
}
