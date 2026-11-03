/*
 * ChatCLI - Tool-free closing round for the chat/MoA exception loops.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * The chat exception loops (knowledge, memory, graphview, context_pull) and
 * the MoA participant loop offer read-only tools for a bounded number of
 * rounds and then treat the buffered content as the answer. A model that
 * keeps calling a tool past its budget — or that answers a pull with a tool
 * call only, or with nothing at all — used to leave that buffered content
 * EMPTY: the tool call was dropped (or its XML markup stripped) and the user
 * got a blank reply. The closing round below gives the model one tool-free
 * chance to answer from the material already gathered.
 */
package cli

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	client "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// toolFreeAnswerDirective closes the turn: no tools remain, answer now. It is
// an instruction to the model, so it stays in English like the other
// exception directives.
const toolFreeAnswerDirective = "\n\n[No more tool calls are available for this turn.] " +
	"Answer the user's question now, in natural language, using only the material already gathered above. " +
	"Do not emit any tool call, tag or JSON call. If the material is not enough, say so plainly and state what is missing."

// promptSender is the plain-text transport every provider implements
// (client.LLMClient and moa.Client both satisfy it).
type promptSender interface {
	SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error)
}

// stripToolCallMarkup removes any parsed tool-call markup from a buffered
// reply and trims it, so a reply that was only a tag becomes "".
func stripToolCallMarkup(text string) string {
	calls, _ := agent.ParseToolCalls(text)
	for _, tc := range calls {
		if tc.Raw != "" {
			text = strings.ReplaceAll(text, tc.Raw, "")
		}
	}
	return strings.TrimSpace(text)
}

// toolFreeAnswer runs the single closing round: the pending prompt (which
// carries the last tool result, if any) plus the directive, over the plain
// tool-less transport so the model cannot answer with another call. Any
// stray markup is stripped; the result may still be "" — the caller's
// renderer reports that explicitly instead of drawing an empty reply.
func toolFreeAnswer(ctx context.Context, c promptSender, prompt string, history []models.Message, maxTokens int, retryOnAuth func(error) bool) (string, string, error) {
	sent := prompt + toolFreeAnswerDirective
	out, err := c.SendPrompt(ctx, sent, history, maxTokens)
	if err != nil && retryOnAuth != nil && retryOnAuth(err) {
		out, err = c.SendPrompt(ctx, sent, history, maxTokens)
	}
	if err != nil {
		return "", sent, err
	}
	return stripToolCallMarkup(out), sent, nil
}

// chatToolFreeAnswer is toolFreeAnswer for the chat loops: it books the
// closing round's usage itself, because the exception loop already flagged
// the turn as recorded and handleChatTurnResult will not book it again.
func (cli *ChatCLI) chatToolFreeAnswer(ctx context.Context, c client.LLMClient, prompt string, history []models.Message, maxTokens int, resolution SkillClientResolution) (string, error) {
	out, sent, err := toolFreeAnswer(ctx, c, prompt, history, maxTokens, cli.refreshClientOnAuthError)
	if err != nil {
		return "", err
	}
	if cli.costTracker != nil {
		cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model,
			client.GetUsageOrEstimate(c, len(sent), len(out)))
		cli.turnUsageRecorded = true
	}
	return out, nil
}

// chatToolBudget counts the rounds each chat exception has consumed in one
// turn. Budgets are PER TOOL: a single shared round index let one tool's
// rounds eat another's (four knowledge pulls left a context_pull with no
// round at all), and that refused call was what reached the user as an
// empty reply. The sum of the caps bounds the loop.
type chatToolBudget struct {
	kb, mem, pull int
	gvDone        bool
}

func (b *chatToolBudget) takeKnowledge() bool {
	return takeRound(&b.kb, chatKnowledgeMaxRounds)
}

func (b *chatToolBudget) takeMemory() bool {
	return takeRound(&b.mem, chatMemoryMaxRounds)
}

func (b *chatToolBudget) takeContextPull() bool {
	return takeRound(&b.pull, chatContextPullMaxRounds)
}

// takeGraphView allows the one-shot render once per turn.
func (b *chatToolBudget) takeGraphView() bool {
	if b.gvDone {
		return false
	}
	b.gvDone = true
	return true
}

func takeRound(used *int, limit int) bool {
	if *used >= limit {
		return false
	}
	*used++
	return true
}
