/*
 * ChatCLI - Auto-RAG upgrade for /context attach
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A plain `/context attach <name>` injects the whole context verbatim into
 * the system prompt every turn. For large contexts that is the single
 * biggest per-turn payload in chat mode — while the agent-side @context tool
 * (context_adapter.go) has attached with semantic retrieval by default since
 * PR #1058. This file closes the asymmetry for the manual command: large
 * contexts upgrade to --rag semantics automatically, announced to the user,
 * with --full as the per-call opt-out and CHATCLI_ATTACH_AUTO_RAG as the
 * global kill switch.
 */
package cli

import (
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
)

// attachAutoRagEnvVar is the global kill switch for the automatic RAG
// upgrade on `/context attach`. On by default; set 0|false|off|no to always
// attach whole content unless --rag is passed explicitly.
const attachAutoRagEnvVar = "CHATCLI_ATTACH_AUTO_RAG"

// attachAutoRagMinBytes is the size floor for the automatic upgrade.
// Below it the whole content is cheaper than the retrieval machinery and
// carries zero recall risk, so small contexts always stay verbatim.
// Deliberately a constant: the env surface stays minimal, and 32 KiB
// (~8k tokens/turn) is where verbatim injection starts to dominate the
// chat payload. Note the agent-side @context tool upgrades with no
// threshold at all — the manual path is the conservative one.
const attachAutoRagMinBytes = 32 * 1024

// attachAutoRagEnabled reads the kill switch, live per call so /config
// flips apply immediately (same contract as skillInjectBudget).
func attachAutoRagEnabled() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv(attachAutoRagEnvVar))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// shouldAutoRag decides whether a manual attach with no explicit injection
// choice gets the semantic-retrieval upgrade. Every guard is a reason to
// keep today's verbatim behavior:
//   - the user already chose (--full, --rag, --chunk/--chunks);
//   - knowledge-mode contexts have their own index-card economics;
//   - the kill switch is off;
//   - no embedding provider (retrieval would silently degrade anyway);
//   - the context is small enough that verbatim is the better deal.
func (h *ContextHandler) shouldAutoRag(ctx *ctxmgr.FileContext, flags attachFlags) bool {
	if flags.full || flags.retrievalTopK > 0 || len(flags.selectedChunks) > 0 {
		return false
	}
	if ctx.Mode == ctxmgr.ModeKnowledge {
		return false
	}
	if !attachAutoRagEnabled() {
		return false
	}
	if !h.manager.RetrievalEnabled() {
		return false
	}
	return ctx.TotalSize >= attachAutoRagMinBytes
}
