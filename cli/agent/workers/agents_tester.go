package workers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// TesterAgent is the specialized agent for test generation, execution, and coverage analysis.
type TesterAgent struct {
	skills *SkillSet
}

// NewTesterAgent creates a TesterAgent with its pre-built skills.
func NewTesterAgent() *TesterAgent {
	a := &TesterAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *TesterAgent) Type() AgentType  { return AgentTypeTester }
func (a *TesterAgent) Name() string     { return "TesterAgent" }
func (a *TesterAgent) IsReadOnly() bool { return false }
func (a *TesterAgent) AllowedCommands() []string {
	return []string{"read", "write", "patch", "exec", "test", "search", "tree"}
}

func (a *TesterAgent) Description() string {
	return "Expert in test generation, execution, and coverage analysis. " +
		"Can write new tests, run test suites with coverage, find untested functions, " +
		"and generate table-driven tests. Understands Go testing patterns deeply."
}

func (a *TesterAgent) SystemPrompt() string {
	return `You are a specialized TESTING agent in ChatCLI.
Your expertise: test generation, coverage analysis, TDD, Go testing patterns.

## YOUR ROLE
- Generate comprehensive tests for functions and packages
- Run tests and analyze results, failures, and coverage
- Find untested code and suggest test strategies
- Write table-driven tests following Go conventions
- Ensure edge cases and error paths are covered

## AVAILABLE COMMANDS
Use <tool_call name="@coder" args='{"cmd":"COMMAND","args":{...}}' /> syntax.

- read: Read file contents. Args: {"file":"path/to/file"}
- write: Create test files. Args: {"file":"path_test.go","content":"...","encoding":"base64"}
- patch: Modify existing test files. Args: {"file":"path_test.go","search":"old","replace":"new"}
- exec: Execute commands (e.g., go test with flags). Args: {"cmd":"go test -v ./pkg/..."}
- test: Run the project's test suite. Args: {"dir":"."}
- search: Find patterns. Args: {"term":"func Test","dir":".","glob":"*_test.go"}
- tree: List directory structure. Args: {"dir":"."}

IMPORTANT: The key for file path is "file", NOT "path".

## RULES
1. ALWAYS read the source code before writing tests for it.
2. Follow Go naming conventions: TestXxx, test file = xxx_test.go.
3. Use table-driven tests when testing multiple cases for the same function.
4. Test both happy paths and error paths.
5. Use testify or standard library assertions — check what the project uses.
6. Run tests after writing them to verify they compile and pass.
7. Keep tests focused — one behavior per test case.

## RESPONSE FORMAT
1. Start with <reasoning> (what to test and your testing strategy)
2. Emit <tool_call> tags for reading source, writing tests, running tests
3. Provide summary of coverage and test results`
}

func (a *TesterAgent) Skills() *SkillSet { return a.skills }

func (a *TesterAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
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

func (a *TesterAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "generate-tests",
		Description: "Generate comprehensive tests for a function or package (LLM-driven)",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "run-coverage",
		Description: "Run go test -coverprofile and parse coverage per function",
		Type:        SkillExecutable,
		Script:      runCoverageScript,
	})
	a.skills.Register(&Skill{
		Name:        "find-untested",
		Description: "Find exported functions that have no corresponding test",
		Type:        SkillExecutable,
		Script:      findUntestedScript,
	})
	a.skills.Register(&Skill{
		Name:        "generate-table-test",
		Description: "Generate an idiomatic Go table-driven test for a specific function",
		Type:        SkillDescriptive,
	})
}

// runCoverageScript runs go test with coverage and parses the results.
func runCoverageScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}
	pkg := input["pkg"]
	if pkg == "" {
		pkg = "./..."
	}

	coverFile := filepath.Join(os.TempDir(), "chatcli_cover.out")
	cmd := fmt.Sprintf("cd %s && go test -coverprofile=%s %s 2>&1", dir, coverFile, pkg)

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
	testErr := eng.Execute(ctx, "exec", []string{"--cmd", cmd})
	outWriter.Flush()
	errWriter.Flush()

	var results strings.Builder
	results.WriteString("## Coverage Report\n\n")

	if testErr != nil {
		results.WriteString("### Test Execution: FAILED\n")
	} else {
		results.WriteString("### Test Execution: PASSED\n")
	}
	results.WriteString("```\n")
	results.WriteString(buf.String())
	results.WriteString("```\n\n")

	// Try to get per-function coverage
	funcCmd := fmt.Sprintf("go tool cover -func=%s 2>&1", coverFile)
	var funcBuf bytes.Buffer
	funcOut := engine.NewStreamWriter(func(line string) {
		funcBuf.WriteString(line)
		funcBuf.WriteString("\n")
	})
	funcEng := engine.NewEngine(funcOut, engine.NewStreamWriter(func(string) {}))
	funcErr := funcEng.Execute(ctx, "exec", []string{"--cmd", funcCmd})
	funcOut.Flush()

	if funcErr == nil && funcBuf.Len() > 0 {
		results.WriteString("### Per-Function Coverage\n```\n")
		results.WriteString(funcBuf.String())
		results.WriteString("```\n")
	}

	// Clean up temp file
	os.Remove(coverFile)

	return results.String(), testErr
}

// findUntestedScript finds exported functions without corresponding tests.
func findUntestedScript(ctx context.Context, input map[string]string, _ *engine.Engine) (string, error) {
	dir := input["dir"]
	if dir == "" {
		dir = "."
	}

	// Regex patterns for exported Go functions
	funcPattern := regexp.MustCompile(`^func\s+([A-Z]\w*)\s*\(`)
	methodPattern := regexp.MustCompile(`^func\s+\(\w+\s+\*?\w+\)\s+([A-Z]\w*)\s*\(`)
	testPattern := regexp.MustCompile(`^func\s+Test(\w+)`)

	type fileResult struct {
		path  string
		funcs []string
	}

	var sourceFiles, testFiles []string

	// Walk directory to find .go files
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip vendor and hidden dirs
		if strings.Contains(path, "vendor/") || strings.Contains(path, "/.") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			testFiles = append(testFiles, path)
		} else {
			sourceFiles = append(sourceFiles, path)
		}
		return nil
	})

	// Collect exported functions from source files
	exportedFuncs := make(map[string]string) // funcName -> file:line
	sem := make(chan struct{}, 8)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, sf := range sourceFiles {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := os.ReadFile(path)
			if err != nil {
				return
			}
			lines := strings.Split(string(data), "\n")
			for i, line := range lines {
				if m := funcPattern.FindStringSubmatch(line); len(m) > 1 {
					mu.Lock()
					exportedFuncs[m[1]] = fmt.Sprintf("%s:%d", path, i+1)
					mu.Unlock()
				}
				if m := methodPattern.FindStringSubmatch(line); len(m) > 1 {
					mu.Lock()
					exportedFuncs[m[1]] = fmt.Sprintf("%s:%d", path, i+1)
					mu.Unlock()
				}
			}
		}(sf)
	}

	// Collect test function names
	testedNames := make(map[string]bool)
	for _, tf := range testFiles {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := os.ReadFile(path)
			if err != nil {
				return
			}
			for _, line := range strings.Split(string(data), "\n") {
				if m := testPattern.FindStringSubmatch(line); len(m) > 1 {
					mu.Lock()
					testedNames[m[1]] = true
					mu.Unlock()
				}
			}
		}(tf)
	}

	wg.Wait()

	// Find untested functions
	var untested []string
	for name, location := range exportedFuncs {
		// Check if any test name contains this function name
		found := false
		for testName := range testedNames {
			if strings.Contains(testName, name) {
				found = true
				break
			}
		}
		if !found {
			untested = append(untested, fmt.Sprintf("- %s (%s)", name, location))
		}
	}

	var results strings.Builder
	fmt.Fprintf(&results, "## Untested Functions Report\n\n")
	fmt.Fprintf(&results, "- Source files scanned: %d\n", len(sourceFiles))
	fmt.Fprintf(&results, "- Test files found: %d\n", len(testFiles))
	fmt.Fprintf(&results, "- Exported functions: %d\n", len(exportedFuncs))
	fmt.Fprintf(&results, "- Untested functions: %d\n\n", len(untested))

	if len(untested) > 0 {
		results.WriteString("### Functions Without Tests\n")
		// Sort for stable output
		for _, line := range untested {
			results.WriteString(line)
			results.WriteString("\n")
		}
	} else {
		results.WriteString("All exported functions have corresponding tests!\n")
	}

	return results.String(), nil
}
