package workers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// SearchAgent is the specialized agent for finding things across the codebase.
type SearchAgent struct {
	skills *SkillSet
}

// NewSearchAgent creates a SearchAgent with its pre-built skills.
func NewSearchAgent() *SearchAgent {
	a := &SearchAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *SearchAgent) Type() AgentType       { return AgentTypeSearch }
func (a *SearchAgent) Name() string           { return "SearchAgent" }
func (a *SearchAgent) IsReadOnly() bool       { return true }
func (a *SearchAgent) AllowedCommands() []string {
	return []string{"search", "tree", "read"}
}

func (a *SearchAgent) Description() string {
	return "Expert in finding things across the codebase. " +
		"Can search for symbols, patterns, usages, and definitions. " +
		"Can map project structure with annotations. READ-ONLY access only."
}

func (a *SearchAgent) SystemPrompt() string {
	return `You are a specialized CODE SEARCH agent in ChatCLI.
Your expertise: finding symbols, patterns, usages, and definitions across codebases.

## YOUR ROLE
- Search for function/type/variable usages across the project
- Find where symbols are defined
- Map project structure and annotate important files
- Locate dead code, unused imports, or orphaned files

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- search: Search for a regex pattern in files (with optional --dir and --glob)
- tree: List directory structure
- read: Read specific files to verify search results

## RULES
1. You are READ-ONLY. Never attempt any modifications.
2. Use specific search terms — avoid overly broad patterns.
3. When finding usages, search for the exact symbol name.
4. Provide structured results with file paths and line numbers.
5. If a search returns too many results, narrow with --glob or --dir.

## RESPONSE FORMAT
1. Start with <reasoning> (what you need to find and your search strategy)
2. Emit <tool_call> tags for search operations
3. After getting results, provide a structured summary with locations`
}

func (a *SearchAgent) Skills() *SkillSet { return a.skills }

func (a *SearchAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	config := WorkerReActConfig{
		MaxTurns:        DefaultWorkerMaxTurns,
		SystemPrompt:    a.SystemPrompt(),
		AllowedCommands: a.AllowedCommands(),
		ReadOnly:        true,
	}
	result, err := RunWorkerReAct(ctx, config, task, deps.LLMClient, deps.LockMgr, a.skills, deps.Logger)
	if result != nil {
		result.Agent = a.Type()
		result.Task = task
	}
	return result, err
}

func (a *SearchAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "find-usages",
		Description: "Find all files referencing a given symbol name",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "find-definition",
		Description: "Locate where a type, function, or variable is defined",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "map-project",
		Description: "Generate project tree + search for key patterns (interfaces, structs) in parallel",
		Type:        SkillExecutable,
		Script:      mapProjectScript,
	})
	a.skills.Register(&Skill{
		Name:        "find-dead-code",
		Description: "Find exported symbols that are never referenced outside their package",
		Type:        SkillDescriptive,
	})
}

// mapProjectScript generates a project overview with tree + parallel searches.
func mapProjectScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	type searchResult struct {
		label  string
		output string
	}

	searches := []struct {
		label string
		term  string
	}{
		{"Interfaces", `type \w+ interface`},
		{"Structs", `type \w+ struct`},
		{"Functions", `^func \w+`},
	}

	results := make([]searchResult, len(searches)+1) // +1 for tree
	var wg sync.WaitGroup

	// Tree
	wg.Add(1)
	go func() {
		defer wg.Done()
		var buf bytes.Buffer
		outWriter := engine.NewStreamWriter(func(line string) {
			buf.WriteString(line)
			buf.WriteString("\n")
		})
		eng := engine.NewEngine(outWriter, engine.NewStreamWriter(func(string) {}))
		_ = eng.Execute(ctx, "tree", []string{"--dir", dir, "--max-depth", "3"})
		outWriter.Flush()
		results[0] = searchResult{label: "Project Tree", output: buf.String()}
	}()

	// Parallel searches
	for i, s := range searches {
		wg.Add(1)
		go func(idx int, label, term string) {
			defer wg.Done()
			var buf bytes.Buffer
			outWriter := engine.NewStreamWriter(func(line string) {
				buf.WriteString(line)
				buf.WriteString("\n")
			})
			eng := engine.NewEngine(outWriter, engine.NewStreamWriter(func(string) {}))
			_ = eng.Execute(ctx, "search", []string{"--term", term, "--dir", dir, "--glob", "*.go"})
			outWriter.Flush()
			results[idx+1] = searchResult{label: label, output: buf.String()}
		}(i, s.label, s.term)
	}

	wg.Wait()

	var out strings.Builder
	fmt.Fprintf(&out, "## Project Map: %s\n\n", dir)
	for _, r := range results {
		fmt.Fprintf(&out, "### %s\n%s\n", r.label, r.output)
	}

	return out.String(), nil
}
