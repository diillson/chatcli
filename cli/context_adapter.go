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
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

// compile-time assertion: the adapter serves the maintenance verbs too.
var _ plugins.ContextMaintainer = (*contextPluginAdapter)(nil)

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
	return a.CreateTagged(name, mode, paths, description, nil, force)
}

// CreateTagged implements plugins.ContextMaintainer: Create plus tags.
func (a *contextPluginAdapter) CreateTagged(name, mode string, paths []string, description string, tags []string, force bool) (string, error) {
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
	fc, err := mgr.CreateContext(ctx, name, description, paths, pmode, tags, force)
	if err != nil {
		return "", err
	}
	// Tool-path mutation: the AI created a context without going through the
	// /context command dispatch, so the graph cache must be marked here.
	a.cli.markGraphDirty()
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
	return a.AttachWith(name, plugins.ContextAttachOptions{RagTopK: ragTopK, Priority: priority})
}

// checkAttachOptions applies the rules /context attach enforces before it
// touches the session, so the tool cannot attach a shape the command refuses.
func checkAttachOptions(fc *ctxmgr.FileContext, opts plugins.ContextAttachOptions) error {
	if opts.Weight < 0 || opts.Weight > 10 {
		return fmt.Errorf("%s", i18n.T("context.attach.error.invalid_weight"))
	}
	if opts.RagTopK > 0 && len(opts.Chunks) > 0 {
		return fmt.Errorf("%s", i18n.T("context.io.error.rag_with_chunks"))
	}
	if opts.Full && opts.RagTopK > 0 {
		return fmt.Errorf("%s", i18n.T("context.io.error.full_with_rag"))
	}
	return validateSelectedChunks(fc, opts.Chunks)
}

// AttachWith implements plugins.ContextMaintainer: Attach with the option set
// of /context attach (chunk selection, weight, full).
func (a *contextPluginAdapter) AttachWith(name string, opts plugins.ContextAttachOptions) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.GetContextByName(name)
	if err != nil {
		return "", fmt.Errorf("context %q not found — create it first with @context create", name)
	}
	if err := checkAttachOptions(fc, opts); err != nil {
		return "", err
	}
	priority, ragTopK := opts.Priority, opts.RagTopK
	if priority <= 0 {
		priority = contextDefaultAttachPriority
	}

	embeddings := mgr.RetrievalEnabled()
	// Auto-RAG: a non-knowledge context attached without an explicit rag size,
	// while embeddings are configured, becomes retrieval-first instead of a
	// full-content dump. Knowledge mode already retrieves per turn, so it needs
	// no override. Asking for the whole content (full) or for specific chunks
	// is an explicit injection choice and is left alone.
	if ragTopK == 0 && !opts.Full && len(opts.Chunks) == 0 && embeddings && fc.Mode != ctxmgr.ModeKnowledge {
		ragTopK = contextAutoRagTopK
	}

	if err := mgr.AttachContextWithOptions(a.sessionID(), fc.ID, ctxmgr.AttachOptions{
		Priority:       priority,
		SelectedChunks: opts.Chunks,
		RetrievalTopK:  ragTopK,
		Weight:         opts.Weight,
	}); err != nil {
		return "", err
	}
	// Attached contexts are part of the stable prompt prefix: without this
	// the next cache write is booked as unexplained churn (/context does it).
	a.cli.notePrefixChanged("context attach")

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
	case len(opts.Chunks) > 0:
		fmt.Fprintf(&b, " Injected chunks %v of %d.", opts.Chunks, len(fc.Chunks))
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
	a.cli.notePrefixChanged("context detach")
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
	// Tool-path mutation — same rationale as Create.
	a.cli.markGraphDirty()
	var b strings.Builder
	fmt.Fprintf(&b, "Deleted context %q permanently.", fc.Name)
	return b.String(), nil
}

// Update re-ingests or modifies an existing context. Empty fields keep the
// current value (mirrors /context update).
func (a *contextPluginAdapter) Update(name string, paths []string, mode, description string, tags []string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	if mode != "" {
		if _, ok := validContextMode(mode); !ok {
			return "", fmt.Errorf("invalid mode %q (valid: knowledge|full|summary|chunked|smart)", mode)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextOpTimeout)
	defer cancel()
	fc, err := mgr.UpdateContext(ctx, name, paths, ctxmgr.ProcessingMode(mode), tags, description)
	if err != nil {
		return "", err
	}
	a.cli.markGraphDirty()
	var b strings.Builder
	fmt.Fprintf(&b, "Updated context %q (mode=%s) — %d unit(s), %s.", fc.Name, fc.Mode, fc.FileCount, humanMB(fc.TotalSize))
	return b.String(), nil
}

// Show renders one context's metadata.
func (a *contextPluginAdapter) Show(name string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.GetContextByName(name)
	if err != nil {
		return "", fmt.Errorf("context %q not found", name)
	}
	return renderContextInfo(fc, a.attachedIDs()[fc.ID]), nil
}

// Inspect renders a deeper view: the documents/chunks, and one chunk's content
// when chunk > 0.
func (a *contextPluginAdapter) Inspect(name string, chunk int) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.GetContextByName(name)
	if err != nil {
		return "", fmt.Errorf("context %q not found", name)
	}
	var b strings.Builder
	b.WriteString(renderContextInfo(fc, a.attachedIDs()[fc.ID]))
	if chunk > 0 && fc.IsChunked {
		if chunk > len(fc.Chunks) {
			fmt.Fprintf(&b, "\nchunk %d out of range (have %d)", chunk, len(fc.Chunks))
			return b.String(), nil
		}
		ch := fc.Chunks[chunk-1]
		fmt.Fprintf(&b, "\n\n--- chunk %d/%d: %s (%d file(s)) ---", chunk, len(fc.Chunks), ch.Description, len(ch.Files))
		for _, f := range ch.Files {
			fmt.Fprintf(&b, "\n\n# %s\n%s", f.Path, f.Content)
		}
		return b.String(), nil
	}
	const maxList = 25
	b.WriteString("\n  sources:")
	for i, f := range fc.Files {
		if i >= maxList {
			fmt.Fprintf(&b, "\n    … and %d more", len(fc.Files)-maxList)
			break
		}
		fmt.Fprintf(&b, "\n    - %s", f.Path)
	}
	return b.String(), nil
}

// Merge combines source contexts into a new one (deduplicated).
func (a *contextPluginAdapter) Merge(name string, sources []string, description string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	ids := make([]string, 0, len(sources))
	for _, s := range sources {
		fc, err := mgr.GetContextByName(s)
		if err != nil {
			return "", fmt.Errorf("source context %q not found", s)
		}
		ids = append(ids, fc.ID)
	}
	merged, err := mgr.MergeContexts(name, description, ids, contextMergeDefaults())
	if err != nil {
		return "", err
	}
	a.cli.markGraphDirty()
	var b strings.Builder
	fmt.Fprintf(&b, "Merged %d context(s) into %q — %d unit(s), %s. Attach it with @context attach.",
		len(sources), merged.Name, merged.FileCount, humanMB(merged.TotalSize))
	return b.String(), nil
}

// Export writes a context to a portable file.
func (a *contextPluginAdapter) Export(name, path string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.GetContextByName(name)
	if err != nil {
		return "", fmt.Errorf("context %q not found", name)
	}
	if err := mgr.Storage.ExportContext(fc, path); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Exported context %q to %s.", fc.Name, path)
	return b.String(), nil
}

// Import loads a context from a file.
func (a *contextPluginAdapter) Import(path string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	fc, err := mgr.Storage.ImportContext(path)
	if err != nil {
		return "", err
	}
	a.cli.markGraphDirty()
	var b strings.Builder
	fmt.Fprintf(&b, "Imported context %q (mode=%s) — %d unit(s), %s. Attach it with @context attach.",
		fc.Name, fc.Mode, fc.FileCount, humanMB(fc.TotalSize))
	return b.String(), nil
}

// Refresh implements plugins.ContextMaintainer (/context refresh).
func (a *contextPluginAdapter) Refresh(ctx context.Context, name string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	if _, err := mgr.GetContextByName(name); err != nil {
		return "", contextSourceErr(name, ctxmgr.ErrContextNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, contextOpTimeout)
	defer cancel()
	_, rep, err := mgr.RefreshContext(ctx, name)
	if err != nil {
		return "", contextSourceErr(name, err)
	}
	if !rep.Dirty() {
		return fmt.Sprintf("context %q is up to date (%d unit(s) unchanged).", name, rep.Unchanged), nil
	}
	a.cli.markGraphDirty()
	out := fmt.Sprintf("refreshed context %q: %d changed, %d added, %d removed.", name, rep.Changed, rep.Added, rep.Removed)
	if rep.Resealed {
		out += " It was also re-indexed onto the current index format."
	}
	return out, nil
}

// contextSourceErr explains the two ways a context cannot be re-scanned.
func contextSourceErr(name string, err error) error {
	switch {
	case errors.Is(err, ctxmgr.ErrContextNotFound):
		return fmt.Errorf("context %q not found", name)
	case errors.Is(err, ctxmgr.ErrNoSourcePaths):
		return fmt.Errorf("context %q has no recorded source paths (imported or merged); rebuild it with @context update and explicit paths", name)
	}
	return err
}

// Watch implements plugins.ContextMaintainer; an empty name lists the watched.
func (a *contextPluginAdapter) Watch(name string) (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	w := a.cli.contextHandler.contextWatcher()
	if name == "" {
		names := w.Watching()
		if len(names) == 0 {
			return "No context is being watched.", nil
		}
		return "Watching: " + strings.Join(names, ", "), nil
	}
	if err := w.Watch(name); err != nil {
		return "", contextSourceErr(name, err)
	}
	paths, _ := mgr.SourcePathsOf(name)
	return fmt.Sprintf("watching %q: it is re-indexed whenever these sources change, until unwatch or the end of the session: %s",
		name, strings.Join(paths, ", ")), nil
}

// Unwatch implements plugins.ContextMaintainer (/context unwatch).
func (a *contextPluginAdapter) Unwatch(name string) (string, error) {
	if a.manager() == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	if !a.cli.contextHandler.contextWatcher().Unwatch(name) {
		return "", fmt.Errorf("context %q is not being watched", name)
	}
	return fmt.Sprintf("stopped watching %q.", name), nil
}

// Metrics summarizes the whole context store.
func (a *contextPluginAdapter) Metrics() (string, error) {
	mgr := a.manager()
	if mgr == nil {
		return "", fmt.Errorf("context manager unavailable in this session")
	}
	mt := mgr.GetMetrics()
	if mt == nil {
		return "No metrics available.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Context store: %d context(s), %d attached, %d file(s), %s total.",
		mt.TotalContexts, mt.AttachedContexts, mt.TotalFiles, humanMB(mt.TotalSizeBytes))
	if len(mt.ContextsByMode) > 0 {
		b.WriteString("\nBy mode:")
		for _, mode := range ctxSortedKeys(mt.ContextsByMode) {
			fmt.Fprintf(&b, " %s=%d", mode, mt.ContextsByMode[mode])
		}
	}
	return b.String(), nil
}

// renderContextInfo formats a FileContext for show/inspect (deterministic).
func renderContextInfo(fc *ctxmgr.FileContext, attached bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Context %q\n", fc.Name)
	fmt.Fprintf(&b, "  mode: %s | units: %d | size: %s | attached: %v\n", fc.Mode, fc.FileCount, humanMB(fc.TotalSize), attached)
	if fc.Description != "" {
		fmt.Fprintf(&b, "  description: %s\n", fc.Description)
	}
	if len(fc.Tags) > 0 {
		fmt.Fprintf(&b, "  tags: %s\n", strings.Join(fc.Tags, ", "))
	}
	fmt.Fprintf(&b, "  created: %s | updated: %s", fc.CreatedAt.Format(time.RFC3339), fc.UpdatedAt.Format(time.RFC3339))
	if len(fc.Metadata) > 0 {
		b.WriteString("\n  metadata:")
		for _, k := range ctxSortedKeys(fc.Metadata) {
			fmt.Fprintf(&b, "\n    %s: %s", k, fc.Metadata[k])
		}
	}
	return b.String()
}

// ctxSortedKeys returns the map keys in deterministic order.
func ctxSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
When you lack documentation for a library/framework/API, OR need to reason across a code/infra repository (app code, Terraform, Kubernetes/Argo GitOps), and no knowledge base covers it, build one yourself instead of guessing or asking the user:
1. Locate the source — @websearch for the official docs (prefer the project's Markdown repo); or use a path/repo/URL the user gave.
2. Flatten it — @docs-flatten with root=<dir>, repo=<git-url>, or url=<docs-site> → produces a corpus.jsonl. For a SOURCE/INFRA repo (it contains .go/.py/.ts/.java/.tf/.yaml/.sh, not just Markdown), add kind=code so functions, Terraform resources and K8s/Argo manifests are chunked by structure. Build one base per layer (app, infra, gitops) and attach them together so you can connect across layers.
3. @context create {"name":"<topic>","paths":["<corpus.jsonl>"],"mode":"knowledge"} → then @context attach {"name":"<topic>"}.
4. Query it with @knowledge (search/get) and ground your answer in the retrieved passages.
Use @context list / @context status to see what is attached, and @context detach when you are done. Prefer authoritative sources; do not build a base from an unrelated or low-quality page.`)
}
