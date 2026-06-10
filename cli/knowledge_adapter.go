/*
 * ChatCLI - Adapter binding the @knowledge tool to the live context manager.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.KnowledgeAdapter over the session's attached
 * knowledge-mode contexts: hybrid passage search (keyless BM25 floor +
 * embeddings when configured), paged document reads and TOC walks. Wired via
 * plugins.SetKnowledgeAdapter at startup. Also builds the knowledge block the
 * agent system prompt injects so the model knows the bases exist.
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
)

const (
	// knowledgeSearchMaxTopK caps a single search so one tool call stays a
	// bounded slice of the conversation budget.
	knowledgeSearchMaxTopK = 30

	// knowledgeOpTimeout bounds one tool operation, including the optional
	// embedding round-trip inside hybrid retrieval.
	knowledgeOpTimeout = 45 * time.Second
)

// knowledgePluginAdapter is the concrete plugins.KnowledgeAdapter.
type knowledgePluginAdapter struct {
	cli *ChatCLI
}

// sessionID resolves the manager session key, mirroring attachedContextParts.
func (a *knowledgePluginAdapter) sessionID() string {
	if a.cli.currentSessionName != "" {
		return a.cli.currentSessionName
	}
	return "default"
}

func (a *knowledgePluginAdapter) manager() *ctxmgr.Manager {
	if a.cli.contextHandler == nil {
		return nil
	}
	return a.cli.contextHandler.GetManager()
}

// Search runs hybrid retrieval and renders cited passages.
func (a *knowledgePluginAdapter) Search(query, kb string, topK int) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	if topK <= 0 {
		topK = ctxmgr.DefaultRetrievalTopK
	}
	if topK > knowledgeSearchMaxTopK {
		topK = knowledgeSearchMaxTopK
	}
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeOpTimeout)
	defer cancel()
	hits, err := mgr.KnowledgeSearch(ctx, a.sessionID(), kb, query, topK)
	if err != nil {
		return "", err
	}
	return ctxmgr.FormatKnowledgeHits(query, hits), nil
}

// Get returns one page of a document plus explicit pagination guidance.
func (a *knowledgePluginAdapter) Get(source, kb string, offset int) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	page, total, next, err := mgr.KnowledgeDocument(a.sessionID(), kb, source, offset)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📄 %s (chars %d-%d of %d)\n\n", source, offset, offset+len(page), total)
	b.WriteString(page)
	if next > 0 {
		fmt.Fprintf(&b, "\n\n[document continues — call get again with {\"source\":%q,\"offset\":%d}]", source, next)
	}
	return b.String(), nil
}

// TOC lists document paths, optionally narrowed by prefix.
func (a *knowledgePluginAdapter) TOC(kb, prefix string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	return mgr.KnowledgeTOC(a.sessionID(), kb, prefix)
}

// List describes the attached knowledge bases.
func (a *knowledgePluginAdapter) List() (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	kbs := mgr.AttachedKnowledge(a.sessionID())
	if len(kbs) == 0 {
		return "No knowledge base attached. Create one with /context create <name> <corpus.jsonl|dir> --mode knowledge, then /context attach <name>.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d knowledge base(s) attached:\n", len(kbs))
	for _, fc := range kbs {
		fmt.Fprintf(&b, "- %s — %d passage(s), %.2f MB indexed\n", fc.Name, fc.FileCount, float64(fc.TotalSize)/1024/1024)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// knowledgeAgentBlock renders the knowledge digests for the agent/coder
// system prompt: the index cards plus the pull instruction. Empty when no
// knowledge context is attached — the block then costs nothing. Deterministic
// for a given attachment set, so it can live in a cacheable prompt block.
func (cli *ChatCLI) knowledgeAgentBlock() string {
	if cli.contextHandler == nil {
		return ""
	}
	sessionID := cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}
	mgr := cli.contextHandler.GetManager()
	kbs := mgr.AttachedKnowledge(sessionID)
	if len(kbs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, fc := range kbs {
		b.WriteString(mgr.KnowledgeDigest(fc))
		b.WriteString("\n")
	}
	b.WriteString("Use the @knowledge tool to work with these bases: search passages, read full documents with get (paged), inspect coverage with toc. Ground answers in retrieved content and cite source paths.")
	return b.String()
}
