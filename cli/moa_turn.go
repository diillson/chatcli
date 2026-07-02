/*
 * ChatCLI - Tool-aware turn executor for Mixture-of-Agents.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * A MoA run is a panel of experts, and each expert should be as capable as a
 * regular conversation turn: able to pull from the attached knowledge bases
 * and to expand "<<ccr:KEY>>" compression markers back into their originals.
 * This file builds the moa.Turn executor that grants exactly those two
 * sanctioned READ-ONLY exceptions to every participant — proposers and
 * aggregator alike.
 *
 * Deliberately excluded: ask_user (participants run concurrently and
 * unattended — N models racing to open interactive overlays is not a panel,
 * it's a mob) and graphview (a side-effecting artifact writer). No exec, file
 * or search tools, ever — the same rule chat mode enforces.
 *
 * Participants run in parallel goroutines, so everything here is UI-free
 * (no spinner/prompt coupling) and never mutates ChatCLI state; the only
 * shared sinks are the mutex-protected cost tracker and the read-only
 * knowledge/CCR stores.
 */
package cli

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// moaToolMaxRounds bounds how many tool pulls one participant may chain
// before it must answer — enough for search → get → recall without ever
// looping unbounded across N parallel participants.
const moaToolMaxRounds = 4

// moaToolset is the set of read-only capabilities granted to every
// participant of one MoA run. Computed once per run, shared by all turns.
type moaToolset struct {
	knowledge bool // attached knowledge bases may be queried
	recall    bool // <<ccr:KEY>> markers may be expanded
}

func (t moaToolset) any() bool { return t.knowledge || t.recall }

// moaToolsetLabel renders the granted capabilities for the run status line —
// tool identifiers, not translatable prose ("" when none apply).
func moaToolsetLabel(t moaToolset) string {
	var names []string
	if t.knowledge {
		names = append(names, "knowledge")
	}
	if t.recall {
		names = append(names, "recall")
	}
	return strings.Join(names, ", ")
}

// moaToolsetForRun decides the capabilities for a run: knowledge follows the
// same gate as the chat exception (enabled + a base attached to the session);
// recall is granted only when the compression layer is wired AND the shared
// history actually carries a CCR marker — offering it otherwise would be a
// dead tool in every participant's prompt.
func (cli *ChatCLI) moaToolsetForRun(history []models.Message) moaToolset {
	ts := moaToolset{knowledge: cli.chatKnowledgeActive()}
	if cli.compressionLayer != nil && historyHasCCRMarkers(history) {
		ts.recall = true
	}
	return ts
}

// historyHasCCRMarkers reports whether any message carries a "<<ccr:KEY>>"
// compression marker, using the compress package's own key scanner.
func historyHasCCRMarkers(history []models.Message) bool {
	for _, m := range history {
		if len(compress.ExtractKeys(m.Content)) > 0 {
			return true
		}
	}
	return false
}

// moaTurn returns the executor a MoA run uses for every participant: resolve
// an authenticated client for the ref (reusing the live session client and
// its OAuth/forwarded-token auth when it matches) and run one buffered
// exchange, with bounded tool rounds when the toolset grants any.
func (cli *ChatCLI) moaTurn(ts moaToolset) moa.Turn {
	return func(ctx context.Context, ref moa.Ref, prompt string, history []models.Message) (string, error) {
		c, err := cli.moaClientFor(ref.Provider, ref.Model)
		if err != nil {
			return "", err
		}
		if !ts.any() {
			return c.SendPrompt(ctx, prompt, history, 0)
		}
		if tac, ok := client.AsToolAware(c); ok && tac.SupportsNativeTools() {
			return cli.runMoaTurnNative(ctx, tac, ref, prompt, history, ts)
		}
		return cli.runMoaTurnXML(ctx, c, prompt, history, ts)
	}
}

// runMoaTurnNative is the buffered decision loop for native tool-use
// providers: offer the granted read-only tools, execute at most
// moaToolMaxRounds pulls, then the buffered content is the answer.
func (cli *ChatCLI) runMoaTurnNative(
	ctx context.Context,
	tac client.ToolAwareClient,
	ref moa.Ref,
	prompt string,
	history []models.Message,
	ts moaToolset,
) (string, error) {
	var tools []models.ToolDefinition
	if ts.knowledge {
		tools = append(tools, knowledgeToolDefinition())
	}
	if ts.recall {
		tools = append(tools, recallToolDefinition())
	}

	for round := 0; ; round++ {
		resp, err := tac.SendPromptWithTools(ctx, prompt, history, tools, 0)
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", nil
		}
		if resp.Usage != nil && cli.costTracker != nil {
			cli.costTracker.RecordRealUsage(ref.Provider, ref.Model, resp.Usage)
		}

		var kbArgs, rcArgs string
		for _, tc := range resp.ToolCalls {
			switch {
			case ts.knowledge && isKnowledgeToolName(tc.Name) && kbArgs == "":
				kbArgs = tc.ArgumentsJSON()
			case ts.recall && plugins.IsRecallTool(tc.Name) && rcArgs == "":
				rcArgs = tc.ArgumentsJSON()
			}
		}

		if round < moaToolMaxRounds {
			if kbArgs != "" {
				history, prompt = appendMoaToolRound(history, prompt, "knowledge", kbArgs, cli.runChatKnowledge(ctx, kbArgs))
				continue
			}
			if rcArgs != "" {
				history, prompt = appendMoaToolRound(history, prompt, "recall", rcArgs, runMoaRecall(ctx, rcArgs))
				continue
			}
		}
		return resp.Content, nil
	}
}

// runMoaTurnXML is the same loop for providers WITHOUT native tools: the
// granted tool formats are pinned via injected instructions, calls are parsed
// from the text, and stray markup is stripped from the final answer.
func (cli *ChatCLI) runMoaTurnXML(
	ctx context.Context,
	c moa.Client,
	prompt string,
	history []models.Message,
	ts moaToolset,
) (string, error) {
	instruction := ""
	if ts.knowledge {
		instruction += chatKnowledgeXMLInstruction()
	}
	if ts.recall {
		instruction += moaRecallXMLInstruction()
	}
	prompt += instruction

	for round := 0; ; round++ {
		resp, err := c.SendPrompt(ctx, prompt, history, 0)
		if err != nil {
			return "", err
		}

		calls, _ := agent.ParseToolCalls(resp)
		var kbArgs, rcArgs string
		for _, tc := range calls {
			switch {
			case ts.knowledge && isKnowledgeToolName(tc.Name) && kbArgs == "":
				kbArgs = tc.Args
			case ts.recall && plugins.IsRecallTool(tc.Name) && rcArgs == "":
				rcArgs = tc.Args
			}
		}

		// The continuation prompt re-pins the call formats for the next round.
		if round < moaToolMaxRounds {
			if kbArgs != "" {
				history, prompt = appendMoaToolRound(history, prompt, "knowledge", kbArgs, cli.runChatKnowledge(ctx, kbArgs))
				prompt += instruction
				continue
			}
			if rcArgs != "" {
				history, prompt = appendMoaToolRound(history, prompt, "recall", rcArgs, runMoaRecall(ctx, rcArgs))
				prompt += instruction
				continue
			}
		}

		clean := resp
		for _, tc := range calls {
			if tc.Raw != "" {
				clean = strings.ReplaceAll(clean, tc.Raw, "")
			}
		}
		return strings.TrimSpace(clean), nil
	}
}

// appendMoaToolRound folds one tool pull into the conversation being built
// for the participant's next call: the pending prompt becomes a user turn
// (skipped when the history already ends with it — the shared enriched
// history closes with the user's request), the call is acknowledged, and the
// result becomes the new pending prompt.
func appendMoaToolRound(history []models.Message, prompt, toolName, callJSON, result string) ([]models.Message, string) {
	next := make([]models.Message, 0, len(history)+2)
	next = append(next, history...)
	if n := len(history); n == 0 || history[n-1].Role != "user" || history[n-1].Content != prompt {
		next = append(next, models.Message{Role: "user", Content: prompt})
	}
	next = append(next, models.Message{Role: "assistant", Content: "[" + toolName + " call] " + callJSON})
	followup := toolName + " result:\n" + result +
		"\n\nUse this material to answer the user's request. " +
		"Call a tool again only if something essential is still missing."
	return next, followup
}

// runMoaRecall executes one CCR recall through the same builtin the agent
// uses. Errors come back as a tool-result string so the participant can
// correct the call instead of its whole turn failing.
func runMoaRecall(ctx context.Context, argsJSON string) string {
	out, err := plugins.NewBuiltinRecallPlugin().Execute(ctx, []string{argsJSON})
	if err != nil {
		return "recall error: " + err.Error()
	}
	return out
}

// recallToolDefinition is the native tool-use definition for CCR recall
// offered to MoA participants. The arguments mirror the @recall JSON
// envelope, so the call feeds the builtin executor unchanged.
func recallToolDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: "recall",
			Description: "Retrieve the full original of content that was compressed earlier in this conversation (read-only). " +
				"A '<<ccr:KEY>>' marker means detail was offloaded to save context; call recall with that key to read the complete original verbatim.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key": map[string]interface{}{
						"type":        "string",
						"description": "The CCR key from a '<<ccr:KEY>>' marker (bare key or full marker both accepted).",
					},
				},
				"required": []string{"key"},
			},
		},
	}
}

// moaRecallXMLInstruction pins the recall call format for providers WITHOUT
// native tools, mirroring chatKnowledgeXMLInstruction.
func moaRecallXMLInstruction() string {
	return "\n\n[Panel exception — CCR recall is ENABLED for this turn]\n" +
		"Parts of this conversation were compressed: a '<<ccr:KEY>>' marker stands in for offloaded content. " +
		"You normally have no tools, but for THIS turn you MAY expand a marker (read-only), and the call WILL be executed. " +
		"If — and only if — you need the full original behind a marker, reply with EXACTLY one tag and nothing else:\n" +
		`<tool_call name="@recall" args='{"key":"<the KEY from the marker>"}' />` + "\n" +
		"You will receive the original verbatim and may then answer. If you do not need it, just answer normally."
}
