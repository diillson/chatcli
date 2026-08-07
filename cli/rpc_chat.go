/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * rpc_chat.go
 *
 * Full-pipeline chat turns for the MCP/ACP servers. The interactive REPL turn
 * (processLLMRequest) and this headless variant share every enrichment stage —
 * assembleChatSystemPrompt (workspace memory, /context attach, pinned + auto
 * skills, MCP catalog, RAG), buildChatTempHistory, skill model routing, effort
 * hints, token-aware history compaction, hub mirroring and the memory worker
 * nudge — so an MCP client gets the SAME ChatCLI experience as the terminal,
 * by construction rather than by reimplementation.
 *
 * Concurrency: the pipeline reads and writes shared ChatCLI state
 * (cli.history, cli.currentSessionName), so turns run under the same
 * process-wide serialization as the captured agent/coder runs (rpcStdoutSem via
 * captureRPCStdout). The backend owns the per-session histories; this method
 * swaps one in for the duration of the turn and always restores the previous
 * state, even on error.
 */
package cli

import (
	"context"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// RPCChatOpts parametrizes a headless chat turn. Provider/Model are the
// per-call routing overrides from the MCP tool arguments; when set they win
// over any skill model hint (explicit beats implicit).
type RPCChatOpts struct {
	Provider string
	Model    string
}

// RPCChatTurn is the result of a headless chat turn: the assistant reply and
// the updated session history the backend should store.
type RPCChatTurn struct {
	Reply   string
	History []models.Message
}

// rpcDefaultSessionID names the live MCP session used when the client did not
// pass one. Deliberately aligned with nothing REPL-side: the REPL default
// session for /context attachments is "default", reachable from MCP by
// passing session="default" explicitly.
const rpcDefaultSessionID = "mcp"

// RunChatTurnRPC runs ONE chat turn through the full interactive pipeline,
// headless. history is the session history owned by the caller; the returned
// RPCChatTurn.History is the compacted/extended history to store back.
func (cli *ChatCLI) RunChatTurnRPC(
	ctx context.Context, sessionID, userInput string,
	history []models.Message, o RPCChatOpts,
) (RPCChatTurn, error) {
	var turn RPCChatTurn
	_, err := captureRPCStdout(ctx, func() error {
		var innerErr error
		turn, innerErr = cli.runChatTurnSerialized(ctx, sessionID, userInput, history, o)
		return innerErr
	})
	return turn, err
}

// runChatTurnSerialized is the turn body. The caller holds rpcStdoutSem (via
// captureRPCStdout), so the shared-state swap below cannot interleave with
// another captured run.
func (cli *ChatCLI) runChatTurnSerialized(
	ctx context.Context, sessionID, userInput string,
	history []models.Message, o RPCChatOpts,
) (RPCChatTurn, error) {
	if sessionID == "" {
		sessionID = rpcDefaultSessionID
	}

	// Swap in the session state; ALWAYS restore, error included — the REPL
	// invariants (and any later run) depend on it.
	prevHistory, prevSession := cli.history, cli.currentSessionName
	cli.history = append([]models.Message(nil), history...)
	cli.currentSessionName = sessionID
	defer func() {
		cli.history = prevHistory
		cli.currentSessionName = prevSession
	}()

	cli.fireUserPromptSubmitHook(ctx, userInput)

	// Same pre-flight as the REPL turn: @file/@git/… special contexts, vision
	// gating, cross-channel hub pull, token-aware compaction (this replaces
	// the old hard 30-message cap).
	input, additionalContext, images := cli.processSpecialCommands(ctx, userInput)
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc
	cli.syncHubContext(ctx)
	cli.compactHistoryIfNeeded(ctx)

	assembly := cli.assembleChatSystemPrompt(ctx, input, additionalContext)
	tempHistory := cli.buildChatTempHistory(assembly.parts, input, additionalContext, images)

	activeClient, resProvider, resModel, err := cli.resolveRPCChatClient(assembly.modelHint, o)
	if err != nil {
		return RPCChatTurn{}, err
	}

	ctx = cli.applyChatEffortHint(ctx, routeEffortForPrompt(input, assembly.effort))
	maxTokens := cli.getMaxTokensForCurrentLLM()

	reply, err := activeClient.SendPrompt(ctx, input+additionalContext, tempHistory, maxTokens)
	if cli.refreshClientOnAuthError(err) {
		reply, err = activeClient.SendPrompt(ctx, input+additionalContext, tempHistory, maxTokens)
	}
	if err != nil {
		return RPCChatTurn{}, err
	}

	userMessage := models.Message{Role: "user", Content: input + additionalContext, Images: images}
	cli.history = append(cli.history, userMessage, models.Message{Role: "assistant", Content: reply})
	cli.mirrorHubTurn(ctx, userMessage.Content, reply)

	if cli.costTracker != nil {
		usage := client.GetUsageOrEstimate(activeClient, len(input+additionalContext), len(reply))
		cli.costTracker.RecordRealUsage(resProvider, resModel, usage)
	}
	// Memory extraction + skill self-evolution ride the same worker the REPL
	// uses. The turn is handed over as an owned segment (WAL queue) because
	// cli.history is restored the moment this function returns — the async
	// live-history path would never see it.
	if cli.memWorker != nil {
		cli.memWorker.nudgeSegment(ctx, []models.Message{userMessage, {Role: "assistant", Content: reply}})
	}

	return RPCChatTurn{
		Reply:   reply,
		History: append([]models.Message(nil), cli.history...),
	}, nil
}

// resolveRPCChatClient picks the client for a headless chat turn. An explicit
// per-call provider/model override wins over the skill model hint; with no
// override the turn goes through the same skill routing as the REPL
// (resolveSkillClient), logged instead of printed.
func (cli *ChatCLI) resolveRPCChatClient(
	modelHint string, o RPCChatOpts,
) (client.LLMClient, string, string, error) {
	if o.Provider != "" || o.Model != "" {
		provider := o.Provider
		if provider == "" {
			provider = cli.Provider
		}
		c, err := cli.manager.GetClient(provider, o.Model)
		if err != nil {
			return nil, "", "", err
		}
		return c, provider, o.Model, nil
	}
	resolution := cli.resolveSkillClient(modelHint)
	if resolution.Changed {
		cli.logger.Info("rpc chat: skill model hint honored",
			zap.String("provider", resolution.Provider),
			zap.String("model", resolution.Model))
	}
	return resolution.Client, resolution.Provider, resolution.Model, nil
}
