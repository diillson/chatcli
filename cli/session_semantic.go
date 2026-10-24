/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * session_semantic.go — finding past conversations by meaning.
 *
 * The lexical search ranks what shares the query's words. This layer adds
 * the conversations that say the same thing in other words, through the
 * signal each installation actually has:
 *
 *   - embeddings, when an embedding provider is configured (Voyage, OpenAI,
 *     Google, Bedrock or a local Ollama): the user's own messages are
 *     embedded once into a sealed, persisted vector index next to the
 *     sessions and ranked by cosine similarity to every phrasing;
 *   - HyDE, when it is enabled (CHATCLI_QUALITY_HYDE_ENABLED): the model
 *     drafts what the user would have said, and its key terms run as one
 *     more phrasing.
 *
 * Results only ADD: lexical hits keep their order, and sessions found by
 * meaning follow them, bounded and labeled, so no search that works today
 * loses or reorders a hit. With neither signal configured the layer is a
 * no-op and the search is exactly the lexical one.
 */
package cli

import (
	"context"
	"crypto/sha1" // #nosec G505 -- content-addressing message ids for a cache, not security
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/llm/embedding/vindex"
	"go.uber.org/zap"
)

const (
	// maxRelatedSessions bounds the sessions a search adds by meaning.
	maxRelatedSessions = 3
	// relatedMinCosine is the similarity a message needs before its session
	// is offered as related (provider-agnostic floor; normalized vectors).
	relatedMinCosine = 0.35
	// sessionEmbedBudget bounds how many new messages one search embeds;
	// the rest are embedded by later searches (the index is incremental).
	sessionEmbedBudget = 2000
	// sessionEmbedTimeout bounds the embedding round-trips of one search.
	sessionEmbedTimeout = 20 * time.Second
	// sessionVectorMinRunes skips messages too short to carry meaning.
	sessionVectorMinRunes = 12
	// sessionVectorMaxBytes bounds the text embedded per message.
	sessionVectorMaxBytes = 4000
)

// sessionVectorIndex is one session store's vector index, rebuilt when the
// embedding provider changes (/config reload).
type sessionVectorIndex struct {
	mu       sync.Mutex
	provider string
	idx      *vindex.Index
	corpus   string // corpus signature the index was last pruned against
}

var (
	sessionVectorsMu sync.Mutex
	sessionVectors   = map[string]*sessionVectorIndex{} // sessions dir → index
)

// sessionVectorPath keeps the index beside (never inside) the sessions
// directory: every *.json in it is listed as a session.
func sessionVectorPath(sessionsDir string) string {
	return filepath.Join(filepath.Dir(sessionsDir), "session_vectors.json")
}

// vectorsFor returns the index of a session store for provider (nil when
// the provider is the null one).
func vectorsFor(sessionsDir string, provider embedding.Provider, logger *zap.Logger) *sessionVectorIndex {
	if embedding.IsNull(provider) {
		return nil
	}
	sessionVectorsMu.Lock()
	defer sessionVectorsMu.Unlock()
	v, ok := sessionVectors[sessionsDir]
	if !ok {
		v = &sessionVectorIndex{}
		sessionVectors[sessionsDir] = v
	}
	v.mu.Lock()
	if v.idx == nil || v.provider != provider.Name() {
		v.idx = vindex.New(sessionVectorPath(sessionsDir), provider, vindex.WithLogger(logger))
		v.provider = provider.Name()
		v.corpus = ""
	}
	v.mu.Unlock()
	return v
}

// sessionMessageID content-addresses a user message: an edited or resaved
// session re-embeds only what changed.
func sessionMessageID(session, content string) string {
	sum := sha1.Sum([]byte(content)) // #nosec G401 -- cache key, not security
	return session + "#" + hex.EncodeToString(sum[:10])
}

// relatedSessions returns up to maxRelatedSessions sessions in scope that
// relate to the phrasings by meaning and are not already among the lexical
// hits. It never errors: a missing provider, a provider failure or a
// timeout simply yields no related sessions.
func (cli *ChatCLI) relatedSessions(ctx context.Context, sm *SessionManager, phrasings []string, lexical []SessionSearchHit, from, to time.Time, maxSnippets int) []SessionSearchHit {
	if sm == nil || len(phrasings) == 0 {
		return nil
	}
	v := vectorsFor(sm.sessionsDir, cli.hydeProviderForSession(), cli.logger)
	if v == nil {
		return nil
	}
	corpus, err := sm.searchCorpus()
	if err != nil || corpus == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionEmbedTimeout)
	defer cancel()
	msgs := cli.indexUserMessages(ctx, v, corpus)

	listed := make(map[string]bool, len(lexical))
	for _, h := range lexical {
		listed[h.Session] = true
	}
	byID := make(map[string]sessionVecMsg, len(msgs))
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		saved := corpus.saved[m.session]
		inScope := (from.IsZero() || !saved.Before(from)) && (to.IsZero() || saved.Before(to))
		if listed[m.session] || !inScope {
			continue
		}
		ids = append(ids, m.id)
		byID[m.id] = m
	}
	if len(ids) == 0 {
		return nil
	}
	ranked := rankRelated(ctx, v.index(), phrasings, ids, byID, maxSnippets)
	out := make([]SessionSearchHit, 0, len(ranked))
	for _, c := range ranked {
		h := SessionSearchHit{
			Session:    c.session,
			SavedAt:    corpus.saved[c.session],
			Title:      corpus.titles[c.session],
			Related:    true,
			Similarity: c.score,
			Matches:    len(c.best),
		}
		for _, m := range c.best {
			h.Snippets = append(h.Snippets, "user: "+snippetAround(m.content, m.norm, ""))
		}
		out = append(out, h)
	}
	return out
}

func (v *sessionVectorIndex) index() *vindex.Index {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.idx
}

// indexUserMessages brings the index up to date with the user's own
// messages (what they said about themselves is what a later question asks
// about), within the per-search embedding budget, and returns them.
func (cli *ChatCLI) indexUserMessages(ctx context.Context, v *sessionVectorIndex, corpus *sessionCorpus) []sessionVecMsg {
	var msgs []sessionVecMsg
	keep := make(map[string]struct{})
	for _, d := range corpus.docs {
		if d.role != "user" || len([]rune(strings.TrimSpace(d.content))) < sessionVectorMinRunes {
			continue
		}
		id := sessionMessageID(d.session, d.content)
		keep[id] = struct{}{}
		msgs = append(msgs, sessionVecMsg{id: id, session: d.session, content: d.content, norm: d.norm})
	}
	v.mu.Lock()
	idx := v.idx
	if v.corpus != corpus.sig {
		idx.Prune(keep) // deleted sessions and edited messages leave the index
		v.corpus = corpus.sig
	}
	v.mu.Unlock()
	missing := make(map[string]string)
	for _, m := range msgs {
		if len(missing) >= sessionEmbedBudget {
			break
		}
		if !idx.Has(m.id) {
			missing[m.id] = truncateRunesafe(m.content, sessionVectorMaxBytes)
		}
	}
	if len(missing) > 0 {
		if err := idx.Upsert(ctx, missing); err != nil {
			cli.logger.Debug("session vectors: embedding failed; related search uses what is indexed", zap.Error(err))
		}
	}
	return msgs
}

// relatedCand is one session found by meaning.
type relatedCand struct {
	session string
	score   float64
	best    []sessionVecMsg
}

// rankRelated scores the candidate messages against every phrasing and
// returns the best sessions, highest similarity first.
func rankRelated(ctx context.Context, idx *vindex.Index, phrasings, ids []string, byID map[string]sessionVecMsg, maxSnippets int) []*relatedCand {
	cands := make(map[string]*relatedCand)
	for _, p := range phrasings {
		qv, err := idx.EmbedQuery(ctx, p)
		if err != nil || len(qv) == 0 {
			continue
		}
		for _, h := range idx.ScoreAgainst(qv, ids, relatedMinCosine) {
			m := byID[h.ID]
			c, ok := cands[m.session]
			if !ok {
				c = &relatedCand{session: m.session}
				cands[m.session] = c
			}
			if float64(h.Score) > c.score {
				c.score = float64(h.Score)
			}
			if len(c.best) < maxSnippets && !containsMsg(c.best, m.id) {
				c.best = append(c.best, m)
			}
		}
	}
	ranked := make([]*relatedCand, 0, len(cands))
	for _, c := range cands {
		ranked = append(ranked, c)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].session < ranked[j].session
	})
	if len(ranked) > maxRelatedSessions {
		ranked = ranked[:maxRelatedSessions]
	}
	return ranked
}

// sessionVecMsg is one indexed user message.
type sessionVecMsg struct {
	id, session, content, norm string
}

func containsMsg(xs []sessionVecMsg, id string) bool {
	for _, x := range xs {
		if x.id == id {
			return true
		}
	}
	return false
}

// hydePhrasing returns the HyDE expansion of a question as one extra search
// phrasing (the key terms of what the user would plausibly have said), or
// "" when HyDE is off or yields nothing new.
func (cli *ChatCLI) hydePhrasing(ctx context.Context, question string) string {
	aug := cli.hydeAugmenter(quality.LoadFromEnv())
	if aug == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, sessionEmbedTimeout)
	defer cancel()
	original := strings.Fields(normalizeForSearch(question))
	terms := aug.Augment(ctx, question, nil)
	seen := make(map[string]bool, len(original))
	for _, t := range original {
		seen[t] = true
	}
	var extra []string
	for _, t := range terms {
		t = normalizeForSearch(t)
		if t != "" && !seen[t] {
			seen[t] = true
			extra = append(extra, t)
		}
	}
	return strings.Join(extra, " ")
}
