package workers

import (
	"context"
	"fmt"
	"time"

	"github.com/diillson/chatcli/models"
)

// PlannerAgent is a pure reasoning agent with NO tool access.
// It uses an LLM to analyze tasks, create execution plans, and decompose complex tasks.
type PlannerAgent struct {
	skills *SkillSet
}

// NewPlannerAgent creates a PlannerAgent with its pre-built skills.
func NewPlannerAgent() *PlannerAgent {
	a := &PlannerAgent{skills: NewSkillSet()}
	a.registerSkills()
	return a
}

func (a *PlannerAgent) Type() AgentType       { return AgentTypePlanner }
func (a *PlannerAgent) Name() string           { return "PlannerAgent" }
func (a *PlannerAgent) IsReadOnly() bool       { return true }
func (a *PlannerAgent) AllowedCommands() []string {
	return []string{} // no tool access
}

func (a *PlannerAgent) Description() string {
	return "Expert in analyzing tasks and creating execution plans. " +
		"Has NO direct file or shell access — pure reasoning only. " +
		"Use this agent to decompose complex tasks into ordered subtasks before dispatching to other agents."
}

func (a *PlannerAgent) SystemPrompt() string {
	return `You are a specialized PLANNING agent in ChatCLI.
Your expertise: task analysis, decomposition, and execution planning.

## YOUR ROLE
- Analyze complex tasks and break them into ordered subtasks
- Identify dependencies between subtasks
- Determine which specialized agent should handle each subtask
- Assess risks and suggest safe execution order
- Create clear, actionable plans

## IMPORTANT
You have NO access to files, commands, or any tools.
You can only reason and produce a structured plan as text output.

## RESPONSE FORMAT
Always produce a structured plan:

### Task Analysis
Brief summary of what needs to be done and why.

### Execution Plan
1. [agent:file] Read and analyze relevant files
2. [agent:search] Find all usages of X
3. [agent:coder] Modify file Y
4. [agent:shell] Run tests to verify
...

### Dependencies
- Step 3 depends on steps 1 and 2 (needs context)
- Step 4 depends on step 3 (verify changes)

### Risks
- Potential issues and mitigation strategies

### Parallel Opportunities
- Steps 1 and 2 can run in parallel
- Step 3 must wait for both`
}

func (a *PlannerAgent) Skills() *SkillSet { return a.skills }

// Execute runs the PlannerAgent — a single LLM call with no tool execution.
func (a *PlannerAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	startTime := time.Now()
	callID := nextCallID()

	history := []models.Message{
		{Role: "system", Content: a.SystemPrompt()},
		{Role: "user", Content: task},
	}

	response, err := deps.LLMClient.SendPrompt(ctx, "", history, 0)
	if err != nil {
		return &AgentResult{
			CallID:   callID,
			Agent:    a.Type(),
			Task:     task,
			Error:    fmt.Errorf("planner LLM call failed: %w", err),
			Duration: time.Since(startTime),
		}, err
	}

	return &AgentResult{
		CallID:   callID,
		Agent:    a.Type(),
		Task:     task,
		Output:   response,
		Duration: time.Since(startTime),
	}, nil
}

func (a *PlannerAgent) registerSkills() {
	a.skills.Register(&Skill{
		Name:        "analyze-task",
		Description: "Break down a complex task into subtasks with agent assignments",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "create-plan",
		Description: "Design an ordered execution plan with dependencies",
		Type:        SkillDescriptive,
	})
	a.skills.Register(&Skill{
		Name:        "decompose",
		Description: "Split a monolithic change into safe incremental steps",
		Type:        SkillDescriptive,
	})
}
