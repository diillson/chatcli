/*
 * ChatCLI - Adapter binding the @context tool to the live context manager.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.ContextAdapter over the session: create / attach / detach
 * / list / status / delete of context (knowledge) bases, so the agent can
 * build and wire its own documentation autonomously. Wired via
 * plugins.SetContextAdapter at startup, right after the context manager exists.
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
)

// contextOpTimeout bounds one @context operation. Create can scan a directory
// or ingest a corpus, so it gets the most headroom.
const contextOpTimeout = 120 * time.Second

// contextDefaultAttachPriority orders an attachment when the model omits it.
const contextDefaultAttachPriority = 100

// contextAutoRagTopK is the top-K applied when the model attaches a NON-knowledge
// context without asking for rag while embeddings are configured — it upgrades a
// whole-content dump into semantic retrieval instead.
const contextAutoRagTopK = 8

// contextPluginAdapter is the concrete plugins.ContextAdapter.
type contextPluginAdapter struct {
	cli *ChatCLI
}

// sessionID resolves the manager session key, mirroring knowledgePluginAdapter.
func (a *contextPluginAdapter) sessionID() string {
	if a.cli.currentSessionName != "" {
		return a.cli.currentSessionName
	}
	return "default"
}

func (a *contextPluginAdapter) manager() *ctxmgr.Manager {
	if a.cli.contextHandler == nil {
		return nil
	}
	return a.cli.contextHandler.GetManager()
}

// validContextMode keeps the model from creating a context in a bogus mode.
func validContextMode(mode string) (ctxmgr.ProcessingMode, bool) {
	switch ctxmgr.ProcessingMode(strings.ToLower(strings.TrimSpace(mode))) {
	case ctxmgr.ModeFull:
		return ctxmgr.ModeFull, true
	case ctxmgr.ModeSummary:
		return ctxmgr.ModeSummary, true
	case ctxmgr.ModeChunked:
		return ctxmgr.ModeChunked, true
	case ctxmgr.ModeSmart:
		return ctxmgr.ModeSmart, true
	case ctxmgr.ModeKnowledge, "":
		return ctxmgr.ModeKnowledge, true
	default:
		return "", false
	}
}

// Create builds and persists a context from the given sources.
func (a *contextPluginAdapter) Create(name, mode string, paths []string, description string, force bool) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	pmode, ok := validContextMode(mode)
	if !ok {
		return "", fmt.Errorf("invalid mode %q (valid: knowledge|full|summary|chunked|smart)", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextOpTimeout)
	defer cancel()
	fc, err := mgr.CreateContext(ctx, name, description, paths, pmode, nil, force)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Created context %q (mode=%s) — %d unit(s), %s.",
		fc.Name, fc.Mode, fc.FileCount, humanMB(fc.TotalSize))
	fmt.Fprintf(&b, "\nAttach it to use: @context attach {\"name\":%q}", fc.Name)
	if pmode == ctxmgr.ModeKnowledge {
		b.WriteString(" — then query with @knowledge (search/get).")
	}
	return b.String(), nil
}

// Attach attaches a named context to the session, applying RAG semantics.
func (a *contextPluginAdapter) Attach(name string, ragTopK, priority int) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.GetContextByName(name)
	if err != nil {
		return "", fmt.Errorf("context %q not found — create it first with @context create", name)
	}
	if priority <= 0 {
		priority = contextDefaultAttachPriority
	}

	embeddings := mgr.RetrievalEnabled()
	// Auto-RAG: a non-knowledge context attached without an explicit rag size,
	// while embeddings are configured, becomes retrieval-first instead of a
	// full-content dump. Knowledge mode already retrieves per turn, so it needs
	// no override.
	if ragTopK == 0 && embeddings && fc.Mode != ctxmgr.ModeKnowledge {
		ragTopK = contextAutoRagTopK
	}

	if err := mgr.AttachContextWithOptions(a.sessionID(), fc.ID, ctxmgr.AttachOptions{
		Priority:      priority,
		RetrievalTopK: ragTopK,
	}); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Attached %q (mode=%s).", fc.Name, fc.Mode)
	switch {
	case fc.Mode == ctxmgr.ModeKnowledge:
		if embeddings {
			b.WriteString(" Retrieval: hybrid (keyless BM25 + embeddings).")
		} else {
			b.WriteString(" Retrieval: keyless BM25 (embeddings not configured).")
		}
	case ragTopK > 0:
		fmt.Fprintf(&b, " Retrieval: semantic top-%d%s.", ragTopK, embeddingsNote(embeddings))
	default:
		b.WriteString(" Injected as full content.")
	}
	if fc.Mode == ctxmgr.ModeKnowledge {
		if digest := strings.TrimSpace(mgr.KnowledgeDigest(fc)); digest != "" {
			b.WriteString("\n\n")
			b.WriteString(digest)
		}
		b.WriteString("\nUse @knowledge to search/read it.")
	}
	return b.String(), nil
}

// embeddingsNote annotates whether semantic retrieval has a vector backend.
func embeddingsNote(embeddings bool) string {
	if embeddings {
		return " (embeddings on)"
	}
	return " (embeddings off — falls back to keyless lexical)"
}

// Detach removes a context from the session.
func (a *contextPluginAdapter) Detach(name string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.GetContextByName(name)
	if err != nil {
		return "", fmt.Errorf("context %q not found", name)
	}
	if err := mgr.DetachContext(a.sessionID(), fc.ID); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Detached %q from this session (still on disk; re-attach anytime).", fc.Name)
	return b.String(), nil
}

// List describes every available context.
func (a *contextPluginAdapter) List() (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	all, err := mgr.ListContexts(nil)
	if err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "No contexts yet. Build one with @context create {\"name\":...,\"paths\":[...],\"mode\":\"knowledge\"}.", nil
	}
	attached := a.attachedIDs()
	var b strings.Builder
	fmt.Fprintf(&b, "%d context(s):\n", len(all))
	for _, fc := range all {
		mark := " "
		if attached[fc.ID] {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %s — mode=%s, %d unit(s), %s\n", mark, fc.Name, fc.Mode, fc.FileCount, humanMB(fc.TotalSize))
	}
	b.WriteString("(* = attached to this session)")
	return b.String(), nil
}

// Status describes what is attached to the current session.
func (a *contextPluginAdapter) Status() (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	attached, err := mgr.GetAttachedContexts(a.sessionID())
	if err != nil {
		return "", err
	}
	if len(attached) == 0 {
		return "Nothing attached to this session.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d context(s) attached to this session:\n", len(attached))
	for _, fc := range attached {
		fmt.Fprintf(&b, "- %s — mode=%s, %d unit(s), %s\n", fc.Name, fc.Mode, fc.FileCount, humanMB(fc.TotalSize))
	}
	if mgr.RetrievalEnabled() {
		b.WriteString("Embeddings: configured (semantic retrieval active).")
	} else {
		b.WriteString("Embeddings: not configured (keyless lexical retrieval).")
	}
	return b.String(), nil
}

// Delete permanently removes a context.
func (a *contextPluginAdapter) Delete(name string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.GetContextByName(name)
	if err != nil {
		return "", fmt.Errorf("context %q not found", name)
	}
	if err := mgr.DeleteContext(fc.ID); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Deleted context %q permanently.", fc.Name)
	return b.String(), nil
}

// attachedIDs returns the set of context IDs attached to the session.
func (a *contextPluginAdapter) attachedIDs() map[string]bool {
	out := map[string]bool{}
	mgr := a.manager()
	if mgr == nil {
		return out
	}
	attached, err := mgr.GetAttachedContexts(a.sessionID())
	if err != nil {
		return out
	}
	for _, fc := range attached {
		out[fc.ID] = true
	}
	return out
}

// humanMB renders a byte count as MB with two decimals.
func humanMB(bytes int64) string {
	return fmt.Sprintf("%.2f MB", float64(bytes)/1024/1024)
}

// contextPipelineHint is the system-prompt guidance that gives the agent the
// autonomous documentation power: when it lacks knowledge and nothing is
// attached, it can build a base itself rather than guessing or stalling. Kept
// short and deterministic so it lives in the cacheable prompt prefix.
func contextPipelineHint() string {
	return strings.TrimSpace(`
## Building knowledge you lack
When you lack documentation for a library/framework/API and no knowledge base covers it, build one yourself instead of guessing or asking the user:
1. Locate the source — @websearch for the official docs (prefer the project's Markdown repo); or use a path/repo/URL the user gave.
2. Flatten it — @docs-flatten with root=<dir>, repo=<git-url>, or url=<docs-site> → produces a corpus.jsonl.
3. @context create {"name":"<topic>","paths":["<corpus.jsonl>"],"mode":"knowledge"} → then @context attach {"name":"<topic>"}.
4. Query it with @knowledge (search/get) and ground your answer in the retrieved passages.
Use @context list / @context status to see what is attached, and @context detach when you are done. Prefer authoritative sources; do not build a base from an unrelated or low-quality page.`)
}
