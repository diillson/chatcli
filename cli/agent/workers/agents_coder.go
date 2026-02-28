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
	return []string{"write", "patch", "read", "tree"}
}

func (a *CoderAgent) Description() string {
	return "Expert in writing, patching, and creating code. " +
		"Can create new files, modify existing files with search/replace or unified diffs, " +
		"and generate boilerplate. Always reads before writing to verify context."
}

func (a *CoderAgent) SystemPrompt() string {
	return `You are a specialized CODE WRITING agent in ChatCLI.
Your expertise: creating files, modifying code, applying patches, generating boilerplate.

## YOUR ROLE
- Write new files with proper structure and formatting
- Patch existing files using search/replace or unified diffs
- Read files first to understand context before modifying
- Generate boilerplate code for new packages/modules

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- write: Create or overwrite a file (use base64 for multiline content)
- patch: Apply search/replace or unified diff to a file (use base64 for multiline)
- read: Read file contents (always read before patching)
- tree: List directory structure

## RULES
1. ALWAYS read a file before patching it — never patch blind.
2. Use base64 encoding for multiline content in write/patch args.
3. Keep changes minimal and focused on the requested task.
4. Preserve existing code style and conventions.
5. Do not add unnecessary comments, docstrings, or error handling beyond what's needed.
6. Batch read + write in the same response only if you already know the file contents.

## RESPONSE FORMAT
1. Start with <reasoning> (what you plan to write/modify and why)
2. Emit <tool_call> tags for operations
3. After writing, verify by reading the result if the change is critical`
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
