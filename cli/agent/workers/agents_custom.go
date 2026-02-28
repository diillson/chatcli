package workers

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diillson/chatcli/pkg/coder/engine"
	"github.com/diillson/chatcli/pkg/persona"
)

// builtinAgentTypes lists the reserved agent type names that cannot be overridden by custom agents.
var builtinAgentTypes = map[AgentType]bool{
	AgentTypeFile:        true,
	AgentTypeCoder:       true,
	AgentTypeShell:       true,
	AgentTypeGit:         true,
	AgentTypeSearch:      true,
	AgentTypePlanner:     true,
	AgentTypeReviewer:    true,
	AgentTypeTester:      true,
	AgentTypeRefactor:    true,
	AgentTypeDiagnostics: true,
	AgentTypeFormatter:   true,
	AgentTypeDeps:        true,
}

// CustomAgent adapts a persona.Agent into a WorkerAgent for multi-agent orchestration.
// It wraps persona expertise content + skills into a full worker with ReAct loop access.
type CustomAgent struct {
	agentType    AgentType
	name         string
	description  string
	systemPrompt string
	commands     []string
	readOnly     bool
	skills       *SkillSet
}

// NewCustomAgent creates a CustomAgent from a persona Agent and its resolved skills.
func NewCustomAgent(pa *persona.Agent, personaSkills []*persona.Skill) *CustomAgent {
	commands := MapToolsToCommands(pa.Tools)
	if len(commands) == 0 {
		// Default: read-only if no tools specified
		commands = []string{"read", "search", "tree"}
	}

	readOnly := isReadOnlyToolSet(pa.Tools)
	systemPrompt := buildCustomSystemPrompt(pa, personaSkills, commands, readOnly)
	skillSet := buildCustomSkillSet(personaSkills)

	return &CustomAgent{
		agentType:    AgentType(strings.ToLower(pa.Name)),
		name:         pa.Name,
		description:  pa.Description,
		systemPrompt: systemPrompt,
		commands:     commands,
		readOnly:     readOnly,
		skills:       skillSet,
	}
}

func (a *CustomAgent) Type() AgentType           { return a.agentType }
func (a *CustomAgent) Name() string              { return a.name }
func (a *CustomAgent) Description() string       { return a.description }
func (a *CustomAgent) SystemPrompt() string      { return a.systemPrompt }
func (a *CustomAgent) Skills() *SkillSet         { return a.skills }
func (a *CustomAgent) AllowedCommands() []string { return a.commands }
func (a *CustomAgent) IsReadOnly() bool          { return a.readOnly }

func (a *CustomAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	config := WorkerReActConfig{
		MaxTurns:        DefaultWorkerMaxTurns,
		SystemPrompt:    a.systemPrompt,
		AllowedCommands: a.commands,
		ReadOnly:        a.readOnly,
	}
	result, err := RunWorkerReAct(ctx, config, task, deps.LLMClient, deps.LockMgr, a.skills, deps.PolicyChecker, deps.Logger)
	if result != nil {
		result.Agent = a.agentType
		result.Task = task
	}
	return result, err
}

// MapToolsToCommands converts persona frontmatter tool names (Claude Code-style)
// to @coder subcommand names used by the worker ReAct loop.
//
// Mapping:
//
//	Read  → read
//	Grep  → search
//	Glob  → tree
//	Bash  → exec, test + git commands
//	Write → write
//	Edit  → patch
//	Agent → ignored (meta-tool)
func MapToolsToCommands(tools []string) []string {
	cmdSet := make(map[string]bool)
	hasBash := false

	for _, tool := range tools {
		switch strings.TrimSpace(tool) {
		case "Read":
			cmdSet["read"] = true
		case "Grep":
			cmdSet["search"] = true
		case "Glob":
			cmdSet["tree"] = true
		case "Bash":
			cmdSet["exec"] = true
			cmdSet["test"] = true
			hasBash = true
		case "Write":
			cmdSet["write"] = true
		case "Edit":
			cmdSet["patch"] = true
		case "Agent":
			// Meta-tool, ignored for worker commands
		}
	}

	if hasBash {
		for _, gc := range []string{"git-status", "git-diff", "git-log", "git-changed", "git-branch"} {
			cmdSet[gc] = true
		}
	}

	result := make([]string, 0, len(cmdSet))
	for cmd := range cmdSet {
		result = append(result, cmd)
	}
	sort.Strings(result)
	return result
}

// isReadOnlyToolSet returns true if the tools list contains only read-related tools.
func isReadOnlyToolSet(tools []string) bool {
	for _, tool := range tools {
		switch strings.TrimSpace(tool) {
		case "Write", "Edit", "Bash":
			return false
		}
	}
	return true
}

// buildCustomSystemPrompt assembles the system prompt for a custom persona agent
// operating as a worker in the multi-agent orchestration system.
func buildCustomSystemPrompt(
	pa *persona.Agent,
	personaSkills []*persona.Skill,
	commands []string,
	readOnly bool,
) string {
	var b strings.Builder

	// Identity
	fmt.Fprintf(&b, "You are a specialized %s agent in ChatCLI.\n", strings.ToUpper(pa.Name))
	if pa.Description != "" {
		fmt.Fprintf(&b, "Your expertise: %s\n", pa.Description)
	}
	b.WriteString("\n")

	// Domain expertise (the agent's markdown body)
	if pa.Content != "" {
		b.WriteString("## YOUR SPECIALIZED KNOWLEDGE\n\n")
		b.WriteString(pa.Content)
		b.WriteString("\n\n")
	}

	// Tool call syntax and available commands
	b.WriteString("## AVAILABLE COMMANDS\n")
	b.WriteString("Use <tool_call name=\"@coder\" args='{\"cmd\":\"COMMAND\",\"args\":{...}}' /> syntax.\n\n")

	cmdDescriptions := map[string]string{
		"read":        "Read file contents. Args: {\"file\":\"path/to/file\"}",
		"write":       "Create/overwrite file. Args: {\"file\":\"path\",\"content\":\"BASE64\",\"encoding\":\"base64\"}",
		"patch":       "Search/replace edit. Args: {\"file\":\"path\",\"search\":\"old\",\"replace\":\"new\"}",
		"tree":        "Directory listing. Args: {\"dir\":\".\",\"max-depth\":3}",
		"search":      "Search patterns in files. Args: {\"term\":\"pattern\",\"dir\":\".\"}",
		"exec":        "Execute command. Args: {\"cmd\":\"go build ./...\"}",
		"test":        "Run tests. Args: {\"dir\":\".\"}",
		"git-status":  "Git status. Args: {\"dir\":\".\"}",
		"git-diff":    "Git diff. Args: {\"staged\":true}",
		"git-log":     "Git log. Args: {\"limit\":10}",
		"git-changed": "Git changed files. Args: {}",
		"git-branch":  "Git branch operations. Args: {}",
	}

	for _, cmd := range commands {
		if desc, ok := cmdDescriptions[cmd]; ok {
			fmt.Fprintf(&b, "- **%s**: %s\n", cmd, desc)
		} else {
			fmt.Fprintf(&b, "- %s\n", cmd)
		}
	}
	b.WriteString("\nIMPORTANT: The key for file path is \"file\", NOT \"path\".\n\n")

	// Read-only constraint
	if readOnly {
		b.WriteString("## CONSTRAINT: READ-ONLY\n")
		b.WriteString("You are READ-ONLY. Never attempt write, patch, exec, or any modification.\n\n")
	}

	// Loaded skills content
	if len(personaSkills) > 0 {
		b.WriteString("## KNOWLEDGE SKILLS\n\n")
		for _, skill := range personaSkills {
			fmt.Fprintf(&b, "### Skill: %s\n", skill.Name)
			if skill.Description != "" {
				fmt.Fprintf(&b, "%s\n\n", skill.Description)
			}
			if skill.Content != "" {
				b.WriteString(skill.Content)
				b.WriteString("\n\n")
			}

			// Subskill paths
			if len(skill.Subskills) > 0 {
				b.WriteString("Available knowledge documents (read with 'read' command if needed):\n")
				keys := sortedKeys(skill.Subskills)
				for _, name := range keys {
					fmt.Fprintf(&b, "- %s: %s\n", name, skill.Subskills[name])
				}
				b.WriteString("\n")
			}

			// Script paths
			if len(skill.Scripts) > 0 {
				b.WriteString("Available scripts (execute with 'exec' command if needed):\n")
				keys := sortedKeys(skill.Scripts)
				for _, name := range keys {
					cmd := persona.InferExecutionCommand(filepath.Base(name), skill.Scripts[name])
					fmt.Fprintf(&b, "- %s: `%s`\n", name, cmd)
				}
				b.WriteString("\n")
			}
		}
	}

	// Response format
	b.WriteString("## RULES\n")
	b.WriteString("1. Start with <reasoning> (what you plan to do and why)\n")
	b.WriteString("2. Emit <tool_call> tags for operations\n")
	b.WriteString("3. After getting results, provide a clear structured summary\n")
	b.WriteString("4. Be thorough — use all relevant tools, not just one\n")
	b.WriteString("5. Batch multiple tool calls in one response when possible\n")

	return b.String()
}

// buildCustomSkillSet creates a SkillSet for a custom agent from its persona skills.
func buildCustomSkillSet(personaSkills []*persona.Skill) *SkillSet {
	ss := NewSkillSet()

	for _, skill := range personaSkills {
		// Register each skill as descriptive (LLM-driven resolution)
		ss.Register(&Skill{
			Name:        skill.Name,
			Description: skill.Description,
			Type:        SkillDescriptive,
		})

		// Scripts become executable skills
		for scriptName, scriptPath := range skill.Scripts {
			ss.Register(&Skill{
				Name:        fmt.Sprintf("%s/%s", skill.Name, scriptName),
				Description: fmt.Sprintf("Execute script %s from skill %s", scriptName, skill.Name),
				Type:        SkillExecutable,
				Script:      makeScriptRunner(scriptPath),
			})
		}
	}

	return ss
}

// makeScriptRunner creates a SkillFunc that executes a script file via the engine's exec command.
func makeScriptRunner(scriptPath string) SkillFunc {
	return func(ctx context.Context, input map[string]string, eng *engine.Engine) (string, error) {
		cmd := persona.InferExecutionCommand(filepath.Base(scriptPath), scriptPath)
		var outBuf strings.Builder
		outWriter := engine.NewStreamWriter(func(line string) {
			outBuf.WriteString(line)
			outBuf.WriteString("\n")
		})
		errWriter := engine.NewStreamWriter(func(line string) {
			outBuf.WriteString("ERR: ")
			outBuf.WriteString(line)
			outBuf.WriteString("\n")
		})

		execEng := engine.NewEngine(outWriter, errWriter)
		err := execEng.Execute(ctx, "exec", []string{"--cmd", cmd})
		outWriter.Flush()
		errWriter.Flush()
		return outBuf.String(), err
	}
}

// sortedKeys returns the keys of a map sorted alphabetically.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
