/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Knowledge rerank wiring. CHATCLI_KNOWLEDGE_RERANK picks the optional
 * stage applied to every hybrid knowledge retrieval:
 *
 *   off  (default) — fused BM25+vector order, unchanged behavior;
 *   mmr             — keyless diversity rerank (maximal marginal relevance);
 *   llm             — listwise rerank by the compaction model
 *                     (CHATCLI_COMPACT_MODEL) or, failing that, the session
 *                     client, bounded by a short timeout.
 *
 * The stage is attached to the context manager, so it survives embedding
 * provider rebuilds and applies to tenant store sets alike.
 */
package cli

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// KnowledgeRerankEnv selects the knowledge rerank stage (off|mmr|llm).
const KnowledgeRerankEnv = "CHATCLI_KNOWLEDGE_RERANK"

const knowledgeRerankLLMTimeout = 8 * time.Second

// knowledgeRerankMode normalizes the env value.
func knowledgeRerankMode() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(KnowledgeRerankEnv))); v {
	case "mmr", "llm":
		return v
	default:
		return "off"
	}
}

// knowledgeReranker builds the configured stage, nil for off.
func (cli *ChatCLI) knowledgeReranker() ctxmgr.Reranker {
	switch knowledgeRerankMode() {
	case "mmr":
		return ctxmgr.MMRReranker{Lambda: 0.7}
	case "llm":
		return ctxmgr.LLMReranker{Call: cli.rerankPromptFunc(), Timeout: knowledgeRerankLLMTimeout}
	}
	return nil
}

// rerankUserTurn is the fixed user message of a rerank call (model-facing).
const rerankUserTurn = "Reply with the ranking now."

// rerankPromptFunc binds the listwise reranker to the cheapest model the
// session has: the compaction summarizer when configured, else the
// session client. Resolved per call so @model switches are honored.
func (cli *ChatCLI) rerankPromptFunc() ctxmgr.PromptFunc {
	return func(ctx context.Context, prompt string) (string, error) {
		c := cli.compactSummarizerClient()
		provider, model := cli.compactSummarizerProvider, cli.compactSummarizerModel
		if c == nil {
			c, provider, model = cli.Client, cli.Provider, cli.Model
		}
		if c == nil {
			return "", ctxmgr.ErrRerankUnavailable
		}
		// The instruction rides once, as the system message; the user turn
		// only asks for the answer (the prompt used to be sent twice).
		out, err := c.SendPrompt(ctx, rerankUserTurn, []models.Message{{Role: "system", Content: prompt}, {Role: "user", Content: rerankUserTurn}}, 256)
		if err == nil && cli.costTracker != nil {
			// Background spend, tagged like the memory worker's calls.
			cli.costTracker.RecordMemoryUsage(provider, model, client.GetUsageOrEstimate(c, len(prompt)+len(rerankUserTurn), len(out)))
		}
		return out, err
	}
}

// attachKnowledgeReranker installs the configured stage on a context
// manager (base or tenant). Safe to call repeatedly; nil clears.
func (cli *ChatCLI) attachKnowledgeReranker(mgr *ctxmgr.Manager) {
	if mgr == nil {
		return
	}
	mgr.AttachReranker(cli.knowledgeReranker())
}
