package workers

import (
	"context"
)

// CoderAgent is the specialized agent for writing, patching, and creating code.
type CoderAgent struct {
	skills *SkillSet
}

// NewCoderAgent creates a CoderAgent with its pre-built skills.
func NewCoderAgent() *CoderAgent {
	a := &CoderAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *CoderAgent) Type() AgentType  { return AgentTypeCoder }
func (a *CoderAgent) Name() string     { return "CoderAgent" }
func (a *CoderAgent) IsReadOnly() bool { return false }
func (a *CoderAgent) AllowedCommands() []string {
	return []string{"write", "patch", "read", "tree", "search", "exec"}
}

func (a *CoderAgent) Description() string {
	return "Expert in writing, patching, and creating code. " +
		"Can create new files, modify existing files with search/replace or unified diffs, " +
		"and generate boilerplate. Always reads before writing to verify context."
}

func (a *CoderAgent) SystemPrompt() string {
	return `You are a specialized CODE WRITING agent in ChatCLI.

## RULES
1. ALWAYS read a file before modifying it — never edit blind.
2. Use base64 encoding for multiline content in write/patch args.
3. Keep changes minimal and focused on the requested task.
4. Preserve existing code style and conventions.
5. Do NOT narrate your actions. No "Let me...", "I will...", "Now I'll...".
6. NEVER write narration before calling tools. ZERO narration between tool calls.
7. Only output text AFTER all operations are done — for the final result or if blocked.

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read file contents
- write: Create or overwrite a file
- patch: Apply search/replace to a file
- tree: List directory structure
- search: Search for text/regex in files
- exec: Execute shell commands

## WORKFLOW
1. Call tools directly — read files, make changes, verify.
2. Emit final text only when done with a concise summary of what changed.`
}

func (a *CoderAgent) Skills() *SkillSet { return a.skills }

func (a *CoderAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
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

func (a *CoderAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "write-file",
		Description: "Create or overwrite a file with new content",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "patch-file",
		Description: "Apply search/replace changes to an existing file",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "create-module",
		Description: "Generate a new Go package with boilerplate (package declaration, imports, main struct)",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "refactor",
		Description: "Rename symbols or restructure code across files",
		Type:        SkillDescriptive,
	})
}
