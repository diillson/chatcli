package workers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// RefactorAgent is the specialized agent for safe structural code transformations.
type RefactorAgent struct {
	skills *SkillSet
}

// NewRefactorAgent creates a RefactorAgent with its pre-built skills.
func NewRefactorAgent() *RefactorAgent {
	a := &RefactorAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *RefactorAgent) Type() AgentType  { return AgentTypeRefactor }
func (a *RefactorAgent) Name() string     { return "RefactorAgent" }
func (a *RefactorAgent) IsReadOnly() bool { return false }
func (a *RefactorAgent) AllowedCommands() []string {
	return []string{"read", "write", "patch", "search", "tree"}
}

func (a *RefactorAgent) Description() string {
	return "Expert in safe structural code transformations. " +
		"Can rename symbols across files, extract interfaces, move functions between packages, " +
		"and inline variables. Preserves behavior while improving structure."
}

func (a *RefactorAgent) SystemPrompt() string {
	return `You are a specialized REFACTORING agent in ChatCLI.
Your expertise: safe code transformations that preserve behavior while improving structure.

## YOUR ROLE
- Rename symbols (types, functions, variables) across all files consistently
- Extract interfaces from concrete types
- Move functions between packages with import adjustments
- Inline variables and simplify expressions
- Ensure refactorings don't break existing behavior

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read file contents. Args: {"file":"path/to/file"}
- write: Create files. Args: {"file":"path","content":"...","encoding":"base64"}
- patch: Apply search/replace. Args: {"file":"path","search":"old","replace":"new"}
- search: Find symbol usages. Args: {"term":"symbolName","dir":".","glob":"*.go"}
- tree: List directory structure. Args: {"dir":"."}

IMPORTANT: The key for file path is "file", NOT "path".

## RULES
1. ALWAYS search for ALL usages of a symbol before renaming it.
2. Read files to understand context before making changes.
3. Make changes atomically — update all references in one pass.
4. Be careful with string literals, comments, and struct tags — don't blindly rename inside them.
5. When extracting interfaces, only include methods that are actually used by callers.
6. After refactoring, verify the change is consistent by searching for stale references.

## RESPONSE FORMAT
1. Start with <reasoning> (what transformation and the safety analysis)
2. Search for all affected locations first
3. Apply changes systematically
4. Verify no stale references remain`
}

func (a *RefactorAgent) Skills() *SkillSet { return a.skills }

func (a *RefactorAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
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

func (a *RefactorAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "rename-symbol",
		Description: "Rename a symbol across all .go files in a directory, skipping strings and comments",
		Type:        SkillExecutable,
		Script:      renameSymbolScript,
	})
	a.skills.Register(&Skill{
		Name:        "extract-interface",
		Description: "Find all methods of a concrete type and generate an interface definition",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "move-function",
		Description: "Move a function between packages, adjusting imports in all affected files",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "inline-variable",
		Description: "Replace a variable with its value at all usage sites and remove the declaration",
		Type:        SkillDescriptive,
	})
}

// renameSymbolScript searches for a symbol in all .go files and replaces it.
// Input keys: "dir", "old" (current name), "new" (target name)
func renameSymbolScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}
	oldName := input["old"]
	newName := input["new"]
	if oldName == "" || newName == "" {
		return "", fmt.Errorf("rename-symbol requires 'old' and 'new' parameters")
	}

	// Find all .go files (excluding vendor, .git)
	var goFiles []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, "/.") {
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			goFiles = append(goFiles, path)
		}
		return nil
	})

	type renameResult struct {
		path    string
		count   int
		err     error
		patched bool
	}

	results := make([]renameResult, len(goFiles))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	for i, path := range goFiles {
		wg.Add(1)
		go func(idx int, filePath string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[idx] = renameResult{path: filePath, err: ctx.Err()}
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				results[idx] = renameResult{path: filePath, err: err}
				return
			}

			content := string(data)
			count := strings.Count(content, oldName)
			if count == 0 {
				results[idx] = renameResult{path: filePath, count: 0}
				return
			}

			// Apply patch via engine for each occurrence
			var buf bytes.Buffer
			outWriter := engine.NewStreamWriter(func(line string) {
				buf.WriteString(line)
				buf.WriteString("\n")
			})
			eng := engine.NewEngine(outWriter, engine.NewStreamWriter(func(string) {}))

			patched := 0
			for j := 0; j < count; j++ {
				patchErr := eng.Execute(ctx, "patch", []string{
					"--file", filePath,
					"--search", oldName,
					"--replace", newName,
				})
				outWriter.Flush()
				if patchErr != nil {
					break
				}
				patched++
			}

			results[idx] = renameResult{path: filePath, count: patched, patched: patched > 0}
		}(i, path)
	}

	wg.Wait()

	var out strings.Builder
	fmt.Fprintf(&out, "## Rename: %s → %s\n\n", oldName, newName)

	totalFiles := 0
	totalReplacements := 0
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(&out, "- ERROR %s: %v\n", r.path, r.err)
		} else if r.patched {
			fmt.Fprintf(&out, "- %s: %d replacements\n", r.path, r.count)
			totalFiles++
			totalReplacements += r.count
		}
	}

	fmt.Fprintf(&out, "\n**Total: %d replacements across %d files**\n", totalReplacements, totalFiles)
	return out.String(), nil
}
