package workers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// PlannerAgent is a pure reasoning agent with NO tool access.
// It uses an LLM to analyze tasks, create execution plans, and decompose complex tasks.
//
// Default effort: "high" — planning is the agent we most want to think hard
// (no tool actions to pay for, all value comes from the quality of the
// decomposition). Override with CHATCLI_AGENT_PLANNER_{MODEL,EFFORT}.
//
// Output mode: by default the planner produces the markdown plan
// described in SystemPrompt(). When the task is prefixed with the
// PlannerStructuredOutputDirective marker, it switches to a strict
// JSON schema (see plan.Plan) so quality.PlanRunner can execute it.
type PlannerAgent struct {
	BuiltinAgentMeta
	skills *SkillSet
}

// PlannerStructuredOutputDirective is the leading marker that, when
// found in the task string, instructs the PlannerAgent to emit a
// strictly-formatted JSON plan instead of the default markdown.
//
// quality.RunStructuredPlan prepends this so the contract stays
// inside this file — callers don't need to know the exact prompt.
const PlannerStructuredOutputDirective = "[STRUCTURED_PLAN_OUTPUT]"

// NewPlannerAgent creates a PlannerAgent with its pre-built skills.
func NewPlannerAgent() *PlannerAgent {
	a := &PlannerAgent{
		BuiltinAgentMeta: NewBuiltinAgentMeta("PLANNER", "", "high"),
		skills:           NewSkillSet(),
	}
	a.registerSkills()
	return a
}

func (a *PlannerAgent) Type() AgentType  { return AgentTypePlanner }
func (a *PlannerAgent) Name() string     { return "PlannerAgent" }
func (a *PlannerAgent) IsReadOnly() bool { return true }
func (a *PlannerAgent) AllowedCommands() []string {
	return []string{} // no tool access
}

func (a *PlannerAgent) Description() string {
	return "Expert in analyzing tasks and creating execution plans. " +
		"Has NO direct file or shell access — pure reasoning only. " +
		"Use this agent to decompose complex tasks into ordered subtasks before dispatching to other agents."
}

// structuredPlanSystemPrompt is used when the task carries
// PlannerStructuredOutputDirective. The model is instructed to emit
// JSON that matches quality.Plan exactly. Steps reference each other
// via #E1, #E2 placeholders (ReWOO).
const structuredPlanSystemPrompt = `You are a specialized PLANNING agent in ChatCLI.
Your sole output for this turn is a single JSON object that matches the schema below.
Do NOT include prose, markdown headers, or commentary outside the JSON.
Wrap nothing — emit raw JSON.

## Schema

{
  "task_summary": "<one-sentence summary of the user goal>",
  "steps": [
    {
      "id": "E1",
      "agent": "<one of: file|coder|shell|git|search|planner|reviewer|tester|refactor|diagnostics|formatter|deps>",
      "task": "<imperative task. May reference earlier steps with #E1, #E1.summary, #E1.head=200>",
      "deps": []
    }
  ],
  "parallel_groups": [["E1"], ["E2","E3"]]
}

## Rules
- Step ids are E1, E2, E3, … in declaration order.
- Each step's deps must reference only earlier ids.
- Use #E<n> in task text to inject a previous output (resolved at runtime).
- Use #E<n>.head=N to inject only the first N chars; .summary for the first non-empty line.
- Prefer the smallest plan that solves the task. 1-step plans are valid for trivial tasks.
- "agent" must be one of the listed types. Do not invent agent names.
- "parallel_groups" is optional — when present, ids in the same inner array can run together.

## Output

Return ONLY the JSON object. No backticks, no headings, no explanations.`

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
//
// When task starts with PlannerStructuredOutputDirective the agent
// switches to structuredPlanSystemPrompt and the directive is stripped
// from the user message so the model only sees the actual task.
func (a *PlannerAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	startTime := time.Now()
	callID := nextCallID()

	systemPrompt := a.SystemPrompt()
	userTask := task
	if strings.HasPrefix(task, PlannerStructuredOutputDirective) {
		systemPrompt = structuredPlanSystemPrompt
		userTask = strings.TrimSpace(strings.TrimPrefix(task, PlannerStructuredOutputDirective))
	}

	history := []models.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userTask},
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
