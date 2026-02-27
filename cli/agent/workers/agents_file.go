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

// FileAgent is the specialized agent for reading, analyzing, and understanding code.
type FileAgent struct {
	skills *SkillSet
}

// NewFileAgent creates a FileAgent with its pre-built skills.
func NewFileAgent() *FileAgent {
	a := &FileAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *FileAgent) Type() AgentType  { return AgentTypeFile }
func (a *FileAgent) Name() string     { return "FileAgent" }
func (a *FileAgent) IsReadOnly() bool { return true }
func (a *FileAgent) AllowedCommands() []string {
	return []string{"read", "tree", "search"}
}

func (a *FileAgent) Description() string {
	return "Expert in reading, analyzing, and understanding code files. " +
		"Can read multiple files in parallel, map project structure, find patterns, and trace dependencies. " +
		"NEVER modifies files — read-only access only."
}

func (a *FileAgent) SystemPrompt() string {
	return `You are a specialized FILE READING agent in ChatCLI.
Your expertise: reading files, analyzing code structure, mapping dependencies.

## YOUR ROLE
- Read and analyze source code files
- Map project structure and directory trees
- Find patterns, interfaces, and type definitions
- Trace import chains and dependencies
- Summarize file contents for other agents

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read file contents. Args: {"file":"path/to/file"} (optionally "start" and "end" for line range)
- tree: List directory structure. Args: {"dir":"path/to/dir"}
- search: Search for patterns in files. Args: {"term":"pattern","dir":"path/to/dir"}

IMPORTANT: The key for file path is "file", NOT "path".

## RULES
1. You are READ-ONLY. Never attempt write, patch, exec, or any modification.
2. Be thorough — read all relevant files, not just the first one.
3. Provide structured summaries with file paths and line numbers.
4. When analyzing structure, list types, functions, and interfaces found.
5. Batch multiple read calls in one response when possible.

## RESPONSE FORMAT
1. Start with <reasoning> (what you need to read and why)
2. Emit <tool_call> tags for reading operations
3. After getting results, provide a clear structured summary`
}

func (a *FileAgent) Skills() *SkillSet { return a.skills }

func (a *FileAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
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

func (a *FileAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "batch-read",
		Description: "Read multiple files in parallel using goroutines (no LLM needed)",
		Type:        SkillExecutable,
		Script:      batchReadScript,
	})
	a.skills.Register(&Skill{
		Name:        "find-pattern",
		Description: "Search for a regex pattern across all files in a directory",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "analyze-structure",
		Description: "Map code structure: types, functions, interfaces in a directory",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "map-deps",
		Description: "Trace import/dependency chains for a package",
		Type:        SkillDescriptive,
	})
}

// batchReadScript reads multiple files in parallel using goroutines.
// Input keys: "dir" (directory path), "glob" (optional glob pattern, default "*.go")
func batchReadScript(ctx context.Context, input map[string]string, eng *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}
	globPattern := input["glob"]
	if globPattern == "" {
		globPattern = "*.go"
	}

	// Find files matching the pattern
	matches, err := filepath.Glob(filepath.Join(dir, globPattern))
	if err != nil {
		return "", fmt.Errorf("glob failed: %w", err)
	}
	if len(matches) == 0 {
		// Try recursive
		err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip errors
			}
			if !info.IsDir() {
				matched, _ := filepath.Match(globPattern, info.Name())
				if matched {
					matches = append(matches, path)
				}
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("walk failed: %w", err)
		}
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No files matching %q found in %q", globPattern, dir), nil
	}

	// Read files in parallel
	type fileResult struct {
		path    string
		content string
		err     error
	}

	results := make([]fileResult, len(matches))
	var wg sync.WaitGroup

	// Limit concurrency to 8 goroutines
	sem := make(chan struct{}, 8)

	for i, path := range matches {
		wg.Add(1)
		go func(idx int, filePath string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[idx] = fileResult{path: filePath, err: ctx.Err()}
				return
			case sem <- struct{}{}:
				defer func() { <-sem }()
			}

			var buf bytes.Buffer
			outWriter := engine.NewStreamWriter(func(line string) {
				buf.WriteString(line)
				buf.WriteString("\n")
			})
			errWriter := engine.NewStreamWriter(func(string) {}) // discard errors

			eng := engine.NewEngine(outWriter, errWriter)
			err := eng.Execute(ctx, "read", []string{"--file", filePath})
			outWriter.Flush()

			results[idx] = fileResult{path: filePath, content: buf.String(), err: err}
		}(i, path)
	}

	wg.Wait()

	// Aggregate results
	var out strings.Builder
	fmt.Fprintf(&out, "## Batch Read: %d files from %s\n\n", len(matches), dir)
	for _, r := range results {
		fmt.Fprintf(&out, "### %s\n", r.path)
		if r.err != nil {
			fmt.Fprintf(&out, "ERROR: %v\n\n", r.err)
		} else {
			out.WriteString(r.content)
			out.WriteString("\n")
		}
	}

	return out.String(), nil
}
