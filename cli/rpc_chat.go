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
	"strings"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// RPCChatOpts parametrizes a headless chat turn. Provider/Model are the
// per-call routing overrides from the MCP tool arguments; when set they win
// over any skill model hint (explicit beats implicit).
type RPCChatOpts struct {
	Provider string
	Model    string
	// Attachments carries per-turn inputs a remote surface collected (images
	// a browser attached). A pointer keeps the options comparable.
	Attachments *TurnAttachments
	// Stream, when set, receives the reply as the provider produces it and
	// the turn uses the provider's streaming path when it has one. The
	// full reply is still returned. An interface keeps the options
	// comparable.
	Stream ChunkSink
}

// TurnAttachments are the binary inputs of one turn.
type TurnAttachments struct {
	Images []models.ImageContent
}

// ChunkSink receives streamed reply text.
type ChunkSink interface {
	Chunk(text string)
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
) (turn RPCChatTurn, err error) {
	if sessionID == "" {
		sessionID = rpcDefaultSessionID
	}
	// One node per chat turn on the live dashboard, as the REPL turn has:
	// a turn served over MCP, ACP, the gateway or the web UI is a turn of
	// this process too. The outcome is the turn's error, if any.
	span := pulse.Begin(pulse.KindTurn, pulseTurnChat, "")
	defer func() { span.EndErr(err) }()

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

	// Slash-command expansion first: an MCP/ACP chat turn invoking
	// "/review-pr 12" gets the same template expansion as the REPL, with
	// the pre-exec gate resolving through policy (non-interactive).
	if expanded, isCmd := cli.expandSlashCommandInput(ctx, userInput, false); isCmd {
		userInput = expanded
	}

	// Same pre-flight as the REPL turn: @file/@git/… special contexts, vision
	// gating, cross-channel hub pull, token-aware compaction (this replaces
	// the old hard 30-message cap).
	input, additionalContext, images := cli.processSpecialCommands(ctx, userInput)
	if o.Attachments != nil {
		images = append(images, o.Attachments.Images...)
	}
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc
	cli.syncHubContext(ctx)
	cli.compactHistoryIfNeeded(ctx)

	assembly := cli.assembleChatSystemPrompt(ctx, input, additionalContext)
	tempHistory := cli.buildChatTempHistoryWithContext(assembly.parts, assembly.turnContext, input, additionalContext, images)
	turnCtx := turnContextText(assembly.turnContext)

	activeClient, resProvider, resModel, err := cli.resolveRPCChatClient(assembly.modelHint, o)
	if err != nil {
		return RPCChatTurn{}, err
	}

	ctx = cli.applyChatEffortHint(ctx, routeEffortForPrompt(input, assembly.effort))
	maxTokens := cli.getMaxTokensForCurrentLLM()

	// Budget hard stop applies to gateway/RPC surfaces too — long-lived
	// unattended sessions are exactly where a runaway spend hurts most.
	if err := cli.budgetBlockedErr(); err != nil {
		return RPCChatTurn{}, err
	}

	send := func() (string, *models.UsageInfo, error) {
		return cli.rpcSend(ctx, activeClient, input+additionalContext, tempHistory, maxTokens, o.Stream)
	}
	reply, usage, err := send()
	if cli.refreshClientOnAuthError(err) {
		// The refresh rebuilt cli.Client; the routed client above still
		// holds the expired credential, so resolve the route again.
		if activeClient, resProvider, resModel, err = cli.resolveRPCChatClient(assembly.modelHint, o); err != nil {
			return RPCChatTurn{}, err
		}
		reply, usage, err = send()
	}
	// Overflow recovery (bounded): the unattended surfaces used to fail
	// the turn outright where the REPL agent loop recovered.
	rec := cli.newOverflowRecovery("rpc", nil)
	for err != nil && cli.recoverOverflow(ctx, rec, err) {
		tempHistory = cli.buildChatTempHistoryWithContext(assembly.parts, assembly.turnContext, input, additionalContext, images)
		reply, usage, err = send()
	}
	if err != nil {
		return RPCChatTurn{}, err
	}
	// Headless: nobody can confirm a coder handoff here, so a stray tag is
	// dropped from the reply instead of reaching the client.
	if task, cleaned := extractCoderHandoff(reply); task != "" {
		cli.logger.Debug("rpc chat: dropping coder handoff proposal", zap.String("task", task))
		reply = cleaned
	}

	userMessage := models.Message{Role: "user", Content: input + additionalContext, Images: images}
	if turnCtx != "" {
		cli.history = append(cli.history, models.TurnContextMessage(turnCtx))
	}
	cli.history = append(cli.history, userMessage, models.Message{Role: "assistant", Content: reply})
	cli.mirrorHubTurn(ctx, userMessage.Content, reply)

	if cli.costTracker != nil {
		if usage == nil {
			usage = client.GetUsageOrEstimate(activeClient, len(input+additionalContext), len(reply))
		}
		cli.costTracker.RecordRealUsage(resProvider, resModel, usage)
		// The same context-window projection the REPL footer reports, on
		// the session node of this process; the history is still the
		// session's here, before the deferred restore.
		if pct, _, window := cli.contextWindowUsage(resProvider, resModel, usage); window > 0 {
			pulseContextWindow(roundPct(pct), window)
		}
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
		// A call that names the provider only is served by the session's
		// model on the session's provider, else by that provider's default
		// model, and the turn is recorded under that id. An empty name here
		// used to file the usage under "provider:" — unpriced, so the turn
		// cost nothing on /cost — and to open a second, nameless model node
		// on the dashboard. A provider with no known default keeps the
		// client's own name as a last resort.
		model := o.Model
		if model == "" {
			if provider == cli.Provider {
				model = cli.Model
			} else {
				model = cli.providerDefaultModel(provider)
			}
		}
		c, err := cli.manager.GetClient(provider, model)
		if err != nil {
			return nil, "", "", err
		}
		if model == "" {
			model = c.GetModelName()
		}
		cli.pulseNoteRoute(provider, model, pulseRouteSession, "rpc chat turn")
		return c, provider, model, nil
	}
	resolution := cli.resolveSkillClient(modelHint)
	if resolution.Changed {
		cli.logger.Info("rpc chat: skill model hint honored",
			zap.String("provider", resolution.Provider),
			zap.String("model", resolution.Model))
	}
	cli.pulseNoteResolvedRoute(resolution, pulseRouteSkill, "rpc chat turn")
	return resolution.Client, resolution.Provider, resolution.Model, nil
}

// rpcSend performs one model call for an RPC chat turn. With a sink and a
// provider that streams, the reply is forwarded chunk by chunk and the
// usage the stream reports comes back with it; otherwise the buffered call
// is made and usage is left for the caller to estimate.
func (cli *ChatCLI) rpcSend(ctx context.Context, active client.LLMClient, prompt string, history []models.Message, maxTokens int, sink ChunkSink) (string, *models.UsageInfo, error) {
	if sink == nil {
		reply, err := active.SendPrompt(ctx, prompt, history, maxTokens)
		return reply, nil, err
	}
	sc, ok := client.AsStreamingClient(active)
	if !ok {
		reply, err := active.SendPrompt(ctx, prompt, history, maxTokens)
		if err == nil && reply != "" {
			sink.Chunk(reply)
		}
		return reply, nil, err
	}
	chunks, err := sc.SendPromptStream(ctx, prompt, history, maxTokens)
	if err != nil {
		return "", nil, err
	}
	var (
		text  strings.Builder
		usage *models.UsageInfo
	)
	for chunk := range chunks {
		if chunk.Error != nil {
			return text.String(), usage, chunk.Error
		}
		if chunk.Text != "" {
			text.WriteString(chunk.Text)
			sink.Chunk(chunk.Text)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.Done {
			break
		}
	}
	if err := ctx.Err(); err != nil && text.Len() == 0 {
		return "", usage, err
	}
	return text.String(), usage, nil
}
