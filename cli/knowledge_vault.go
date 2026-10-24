/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * knowledge_vault.go — the knowledge graph projected as an Obsidian vault.
 *
 * Every note of the graph (fact, topic, project, the profile, episode,
 * session, skill, board card, context and context document) becomes one
 * Markdown file with YAML front matter (chatcli_id, kind, aliases, tags and
 * the source store's metadata) and a "Links" section of [[wikilinks]] — the
 * typed outgoing links; Obsidian derives the backlinks itself. Tags are
 * front-matter tags, not notes.
 *
 * The stores stay the source of truth (sealed at rest, deduplicated,
 * multi-process safe); the vault is a projection kept in sync in the
 * background: every graph invalidation schedules a debounced sync, which
 * pkg/vault applies incrementally — unchanged notes are not rewritten, notes
 * edited by hand are preserved, files the vault never wrote are untouched.
 *
 * Privacy: the vault is plaintext by design (it is meant to be opened in an
 * editor). Profile fields flagged sensitive — or whose key marks them as
 * private — are masked, and session transcripts never enter it (session
 * notes carry only title, date and links).
 */
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/pkg/knowledge"
	"github.com/diillson/chatcli/pkg/vault"
	"go.uber.org/zap"
)

const (
	// vaultDebounce is the quiet period after the last memory change
	// before the vault syncs; vaultMaxDelay bounds how long a steady
	// stream of changes can postpone it.
	vaultDebounce = 5 * time.Second
	vaultMaxDelay = 60 * time.Second
	// vaultInitialDelay defers the first sync of a session past startup.
	vaultInitialDelay = 10 * time.Second
	// vaultStopWait bounds how long shutdown waits for a running sync.
	vaultStopWait = 5 * time.Second
	// vaultSensitiveMask replaces private profile values in the vault.
	vaultSensitiveMask = "‹private›"
)

// vaultEnabled reports whether the vault is generated (default on).
func vaultEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(config.MemoryVaultEnv))) {
	case "off", "false", "0", "no", "disabled":
		return false
	}
	return true
}

// vaultDir is the vault of the store set currently installed: for the base
// set CHATCLI_MEMORY_VAULT_DIR, else "vault" next to the memory directory
// (~/.chatcli/vault, or under CHATCLI_STATE_DIR); for a gateway tenant
// always its own root's vault — one shared override directory would mix
// tenants. "" when memory is off.
func (cli *ChatCLI) vaultDir() string {
	if cli.activeTenant() != "" {
		return storeVaultDir(cli.memoryStore)
	}
	if d := strings.TrimSpace(os.Getenv(config.MemoryVaultDirEnv)); d != "" {
		if expanded, err := expandUserPath(d); err == nil {
			return expanded
		}
		return d
	}
	return storeVaultDir(cli.memoryStore)
}

// storeVaultDir is the default vault of a memory store: "vault" beside its
// memory directory.
func storeVaultDir(ms *workspace.MemoryStore) string {
	if ms == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(ms.Manager().MemoryDir()), "vault")
}

// vaultSyncer runs the background vault sync: debounced, single-flight,
// cancelable on shutdown.
type vaultSyncer struct {
	// src and dir are fixed for the syncer's lifetime: it never reads the
	// ChatCLI's (per-turn swapped) store fields.
	src    *graphSources
	dir    string
	logger *zap.Logger

	mu      sync.Mutex
	timer   *time.Timer
	since   time.Time // first change of the pending batch (zero = none)
	stopped bool

	runMu sync.Mutex // one sync at a time (background or /memory vault sync)
	ctx   context.Context
	stop  context.CancelFunc
	wg    sync.WaitGroup

	lastMu  sync.Mutex
	last    vault.Report
	lastErr error
	lastAt  time.Time
}

// wireVault starts the background vault sync for this session. A no-op
// when memory is off or CHATCLI_MEMORY_VAULT disables it.
func (cli *ChatCLI) wireVault() {
	if cli.memoryStore == nil || !vaultEnabled() || cli.vaultSync != nil {
		return
	}
	cli.vaultSync = cli.startVaultSyncer(cli.vaultDir())
}

// startVaultSyncer binds a syncer to the store set installed right now
// (base or tenant), hooks it to that memory's graph invalidations and
// schedules its first sync.
func (cli *ChatCLI) startVaultSyncer(dir string) *vaultSyncer {
	s := newVaultSyncer(cli.graphSources(), dir, cli.logger)
	cli.memoryStore.Manager().OnGraphDirty(s.schedule)
	s.scheduleIn(vaultInitialDelay)
	return s
}

// newVaultSyncer returns an idle syncer for src writing to dir.
func newVaultSyncer(src *graphSources, dir string, logger *zap.Logger) *vaultSyncer {
	if logger == nil {
		logger = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &vaultSyncer{src: src, dir: dir, logger: logger, ctx: ctx, stop: cancel}
}

// currentVaultSyncer is the syncer of the store set installed now: the
// active gateway tenant's, else the base one. nil when none runs.
func (cli *ChatCLI) currentVaultSyncer() *vaultSyncer {
	if p := cli.activeTenant(); p != "" && cli.tenants != nil {
		cli.tenants.mu.Lock()
		defer cli.tenants.mu.Unlock()
		if ts, ok := cli.tenants.items[p]; ok {
			return ts.vaultSync
		}
		return nil
	}
	return cli.vaultSync
}

// schedule (re)arms the debounce timer. Called from MarkDirty — possibly
// under a store lock — so it only touches the timer.
func (s *vaultSyncer) schedule() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	now := time.Now()
	if s.since.IsZero() {
		s.since = now
	}
	delay := vaultDebounce
	if waited := now.Sub(s.since); waited+delay > vaultMaxDelay {
		delay = vaultMaxDelay - waited
		if delay < 0 {
			delay = 0
		}
	}
	s.armLocked(delay)
}

func (s *vaultSyncer) scheduleIn(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if s.since.IsZero() {
		s.since = time.Now()
	}
	s.armLocked(d)
}

func (s *vaultSyncer) armLocked(d time.Duration) {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(d, s.fire)
}

func (s *vaultSyncer) fire() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.since = time.Time{}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if _, err := s.run(false); err != nil && s.ctx.Err() == nil {
		s.logger.Warn("memory vault: background sync failed", zap.Error(err))
	}
}

// run performs one sync (serialized with any other).
func (s *vaultSyncer) run(force bool) (vault.Report, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	dir := s.dir
	if dir == "" || s.src == nil || s.src.memoryStore == nil {
		return vault.Report{}, fmt.Errorf("memory vault: memory is disabled")
	}
	notes := s.src.buildVaultNotes(s.src.knowledgeGraph())
	rep, err := vault.Sync(dir, notes, vault.Options{Force: force, Context: s.ctx})
	s.lastMu.Lock()
	s.last, s.lastErr, s.lastAt = rep, err, time.Now()
	s.lastMu.Unlock()
	if err == nil {
		s.logger.Debug("memory vault synced", zap.String("dir", dir),
			zap.Int("written", rep.Written), zap.Int("unchanged", rep.Unchanged),
			zap.Int("removed", rep.Removed), zap.Int("kept", len(rep.Kept)),
			zap.Duration("took", rep.Duration))
	}
	return rep, err
}

// status returns the last sync's outcome.
func (s *vaultSyncer) status() (rep vault.Report, at time.Time, err error) {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	return s.last, s.lastAt, s.lastErr
}

// shutdown stops scheduling, cancels a running sync and waits (bounded)
// for it to record what it wrote.
func (s *vaultSyncer) shutdown() {
	s.mu.Lock()
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	s.stop()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(vaultStopWait):
	}
}

// knowledgeGraph is the bound-store twin of ChatCLI.knowledgeGraph: the
// cached snapshot when the persisted graph is live, else a fresh build.
func (src *graphSources) knowledgeGraph() *knowledge.Graph {
	if memoryGraphEnabled() && src.memoryStore != nil {
		if g := src.memoryStore.Manager().KnowledgeGraph(); g != nil {
			return g
		}
	}
	return src.build()
}

// shutdownVaultSyncers stops the base syncer and every resident tenant's.
func (cli *ChatCLI) shutdownVaultSyncers() {
	if cli.vaultSync != nil {
		cli.vaultSync.shutdown()
	}
	if cli.tenants == nil {
		return
	}
	cli.tenants.mu.Lock()
	var syncers []*vaultSyncer
	for _, ts := range cli.tenants.items {
		if ts.vaultSync != nil {
			syncers = append(syncers, ts.vaultSync)
		}
	}
	cli.tenants.mu.Unlock()
	for _, s := range syncers {
		s.shutdown()
	}
}

// --- rendering ---

// vaultBuild carries the lookups one rendering pass joins against.
type vaultBuild struct {
	g         *knowledge.Graph
	paths     map[string]string // node id → note path
	facts     map[string]*memory.Fact
	topics    map[string]memory.Topic
	projects  map[string]memory.Project
	episodes  map[string]*memory.Episode
	cards     map[string]board.Card
	contexts  map[string]*ctxmgr.FileContext
	docSource map[string][2]string // doc node id → {context id, source}
	userNotes map[[3]string]string // (from, to, rel) → note of an explicit link
	journal   map[string]string    // journal note id → full text
	// docNames resolves a [[wikilink]] inside a document against its own
	// context first: context id → normalized title/file name → doc node.
	docNames map[string]map[string]string
	profile  memory.UserProfile
	ctxMgr   *ctxmgr.Manager
}

// buildVaultNotes renders every note of g plus the Home map of content.
func (src *graphSources) buildVaultNotes(g *knowledge.Graph) []vault.Note {
	if g == nil {
		return nil
	}
	b := src.newVaultBuild(g)
	b.assignPaths()
	notes := make([]vault.Note, 0, len(b.paths)+1)
	for _, n := range g.Nodes() {
		p, ok := b.paths[n.ID]
		if !ok {
			continue
		}
		notes = append(notes, b.note(n, p))
	}
	notes = append(notes, b.home())
	return notes
}

func (src *graphSources) newVaultBuild(g *knowledge.Graph) *vaultBuild {
	b := &vaultBuild{
		g:         g,
		paths:     make(map[string]string),
		facts:     make(map[string]*memory.Fact),
		topics:    make(map[string]memory.Topic),
		projects:  make(map[string]memory.Project),
		episodes:  make(map[string]*memory.Episode),
		cards:     make(map[string]board.Card),
		contexts:  make(map[string]*ctxmgr.FileContext),
		docSource: make(map[string][2]string),
		userNotes: make(map[[3]string]string),
		journal:   make(map[string]string),
		docNames:  make(map[string]map[string]string),
	}
	if src.memoryStore != nil {
		m := src.memoryStore.Manager()
		for _, f := range m.Facts.GetAll() {
			b.facts["fact:"+f.ID] = f
		}
		for _, t := range m.Topics.GetAll() {
			b.topics["topic:"+graphSlug(t.Name)] = t
		}
		for _, p := range m.Projects.GetAll() {
			b.projects["project:"+graphSlug(p.Name)] = p
		}
		for _, e := range m.Episodes.Range(zeroTime, zeroTime, "", "", graphMaxEpisodeNodes) {
			b.episodes["episode:"+e.ID] = e
		}
		if !m.Profile.IsEmpty() {
			b.profile = m.Profile.Get()
		}
		if m.Daily != nil {
			for _, dn := range m.Daily.GetRecentDailyNotes(m.GetConfig().DailyNoteRetention) {
				b.journal["note:daily:"+dn.Date.Format("2006-01-02")] = dn.Content
			}
		}
		if m.Rollups != nil {
			for _, d := range m.Rollups.Digests() {
				b.journal["note:"+d.Period+":"+d.Label] = d.Content
			}
		}
		if m.Links != nil {
			for _, l := range m.Links.All() {
				if l.Note != "" {
					b.userNotes[[3]string{l.From, l.To, l.Rel}] = l.Note
				}
			}
		}
	}
	if cards, err := boardStore().List(""); err == nil {
		for _, c := range cards {
			b.cards["card:"+c.ID] = c
		}
	}
	if src.contextHandler != nil {
		if mgr := src.contextHandler.GetManager(); mgr != nil {
			b.ctxMgr = mgr
			if ctxs := mgr.SnapshotContexts(); len(ctxs) > 0 {
				for i := range ctxs {
					fc := &ctxs[i]
					b.contexts[contextNodeID(fc)] = fc
					names := make(map[string]string)
					for _, d := range mgr.Documents(fc.ID) {
						did := docNodeID(fc.ID, d.Source)
						b.docSource[did] = [2]string{fc.ID, d.Source}
						for _, name := range []string{d.Title, docAlias(d.Source)} {
							if k := knowledge.NormalizeRef(name); k != "" {
								if _, taken := names[k]; !taken {
									names[k] = did
								}
							}
						}
					}
					b.docNames[fc.ID] = names
				}
			}
		}
	}
	return b
}

// assignPaths gives every non-tag node a stable, readable note path.
func (b *vaultBuild) assignPaths() {
	ctxDir := make(map[string]string) // context id → folder
	docRoot := make(map[string]string)
	for id, fc := range b.contexts {
		ctxDir[fc.ID] = "Contexts/" + vault.FileName(fc.Name)
		_ = id
	}
	// Documents keep their folder structure under the context folder,
	// relative to the context's common source directory.
	srcByCtx := make(map[string][]string)
	for _, cs := range b.docSource {
		srcByCtx[cs[0]] = append(srcByCtx[cs[0]], cs[1])
	}
	for cid, srcs := range srcByCtx {
		docRoot[cid] = commonSourceDir(srcs)
	}
	for _, n := range b.g.Nodes() {
		title := strings.TrimSpace(n.Title)
		if title == "" {
			title = n.ID
		}
		var p string
		switch n.Kind {
		case knowledge.KindTag:
			continue
		case knowledge.KindProfile:
			p = "Profile/" + vault.FileName(b.profileTitle(title)) + ".md"
		case knowledge.KindProject:
			p = "Projects/" + vault.FileName(title) + ".md"
		case knowledge.KindTopic:
			p = "Topics/" + vault.FileName(title) + ".md"
		case knowledge.KindFact:
			cat := "general"
			if f, ok := b.facts[n.ID]; ok && strings.TrimSpace(f.Category) != "" {
				cat = f.Category
			}
			p = "Facts/" + vault.FileName(cat) + "/" + vault.FileName(title) + " (" + shortID(n.ID) + ").md"
		case knowledge.KindEpisode:
			year := "undated"
			if e, ok := b.episodes[n.ID]; ok && !e.Date.IsZero() {
				year = e.Date.Format("2006")
			}
			p = "Episodes/" + year + "/" + vault.FileName(title) + " (" + shortID(n.ID) + ").md"
		case knowledge.KindSession:
			p = "Sessions/" + vault.FileName(strings.TrimPrefix(n.ID, "session:")) + ".md"
		case knowledge.KindNote:
			// note:<period>:<label> → Journal/Daily/2026-10-24.md, the
			// file names Obsidian's daily-notes core plugin uses.
			parts := strings.SplitN(n.ID, ":", 3)
			if len(parts) != 3 {
				continue
			}
			folder := map[string]string{"daily": "Daily", "weekly": "Weekly", "monthly": "Monthly"}[parts[1]]
			if folder == "" {
				folder = vault.FileName(parts[1])
			}
			p = "Journal/" + folder + "/" + vault.FileName(parts[2]) + ".md"
		case knowledge.KindSkill:
			p = "Skills/" + vault.FileName(title) + ".md"
		case knowledge.KindCard:
			col := "board"
			if c, ok := b.cards[n.ID]; ok {
				col = string(c.Column)
			}
			name := title
			if c, ok := b.cards[n.ID]; ok && c.Title != "" {
				name = c.Title
			}
			p = "Board/" + vault.FileName(col) + "/" + vault.FileName(name) + " (" + shortID(n.ID) + ").md"
		case knowledge.KindKB, knowledge.KindContext:
			fc, ok := b.contexts[n.ID]
			if !ok {
				continue
			}
			dir := ctxDir[fc.ID]
			p = dir + "/" + path.Base(dir) + ".md"
		case knowledge.KindDoc:
			cs, ok := b.docSource[n.ID]
			if !ok {
				continue
			}
			p = ctxDir[cs[0]] + "/" + docVaultPath(cs[1], docRoot[cs[0]])
		default:
			p = "Notes/" + vault.FileName(string(n.Kind)) + "/" + vault.FileName(title) + ".md"
		}
		b.paths[n.ID] = p
	}
	b.dedupePaths()
}

// dedupePaths gives every note a distinct path, compared case-insensitively
// (macOS and Windows fold case), BEFORE any link is rendered — so a
// [[link]] always lands on the note it names. Nodes keep their path in id
// order; a later colliding node gets its short id appended.
func (b *vaultBuild) dedupePaths() {
	ids := make([]string, 0, len(b.paths))
	for id := range b.paths {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	taken := make(map[string]bool, len(ids))
	for _, id := range ids {
		p := b.paths[id]
		key := strings.ToLower(p)
		if !taken[key] {
			taken[key] = true
			continue
		}
		base := strings.TrimSuffix(p, ".md")
		for i := 0; ; i++ {
			cand := base + " (" + shortID(id) + ").md"
			if i > 0 {
				cand = base + " (" + shortID(id) + "-" + strconv.Itoa(i) + ").md"
			}
			if !taken[strings.ToLower(cand)] {
				taken[strings.ToLower(cand)] = true
				b.paths[id] = cand
				break
			}
		}
	}
}

// commonSourceDir is the longest common directory of a context's document
// sources ('/'-normalized), so a scanned "/home/me/repo/docs" corpus lands
// as "docs/…" rather than the absolute path.
func commonSourceDir(srcs []string) string {
	if len(srcs) == 0 {
		return ""
	}
	split := func(s string) []string {
		return strings.Split(path.Dir(strings.ReplaceAll(s, "\\", "/")), "/")
	}
	common := split(srcs[0])
	for _, s := range srcs[1:] {
		parts := split(s)
		i := 0
		for i < len(common) && i < len(parts) && common[i] == parts[i] {
			i++
		}
		common = common[:i]
	}
	dir := strings.Join(common, "/")
	if dir == "." {
		return ""
	}
	return dir
}

// docVaultPath maps a document source to its note path inside the context
// folder: relative to root, each segment sanitized, ".md" appended unless
// the document already is Markdown.
func docVaultPath(source, root string) string {
	rel := strings.ReplaceAll(source, "\\", "/")
	if root != "" {
		rel = strings.TrimPrefix(strings.TrimPrefix(rel, root), "/")
	}
	segs := strings.Split(rel, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		out = append(out, vault.FileName(seg))
	}
	if len(out) == 0 {
		out = []string{"document"}
	}
	last := out[len(out)-1]
	if !strings.HasSuffix(strings.ToLower(last), ".md") {
		out[len(out)-1] = last + ".md"
	}
	return strings.Join(out, "/")
}

func shortID(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	id = vault.FileName(id)
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

// note renders one graph node.
func (b *vaultBuild) note(n *knowledge.Node, notePath string) vault.Note {
	title := plainTitle(n.Title)
	if n.Kind == knowledge.KindProfile {
		title = b.profileTitle(title)
	}
	if title == "" {
		title = n.ID
	}
	front := make([]vault.Field, 0, 12)
	front = append(front,
		vault.Field{Key: "chatcli_id", Value: n.ID},
		vault.Field{Key: "kind", Value: string(n.Kind)},
	)
	aliases := b.g.Aliases(n.ID)
	if base := strings.TrimSuffix(path.Base(notePath), ".md"); base != title {
		aliases = append([]string{title}, aliases...)
	}
	front = append(front, vault.Field{Key: "aliases", IsList: true, List: dedupeStrings(aliases)})
	front = append(front, vault.Field{Key: "tags", IsList: true, List: b.tagsOf(n.ID)})
	front = append(front, b.kindFields(n)...)

	var body strings.Builder
	body.WriteString("# " + title + "\n\n")
	switch n.Kind {
	case knowledge.KindProfile:
		body.WriteString(b.profileBody())
	case knowledge.KindNote:
		// The journal note's own text (the graph keeps only a summary).
		if text := strings.TrimSpace(b.journal[n.ID]); text != "" {
			body.WriteString(b.rewriteWikilinks(redactSecretsForLLM(text), "") + "\n")
		} else if s := strings.TrimSpace(n.Summary); s != "" {
			body.WriteString(b.rewriteWikilinks(s, "") + "\n")
		}
	case knowledge.KindDoc:
		// Documents carry their content; assembled only when the note
		// has to be rendered (Key below short-circuits unchanged ones).
		cs := b.docSource[n.ID]
		links := b.linksSection(n.ID)
		mgr := b.ctxMgr
		return vault.Note{
			Path:  notePath,
			Key:   b.docKey(n.ID, cs, links),
			Front: front,
			BodyFunc: func() string {
				content := ""
				if mgr != nil {
					// Secrets masked like everything else that leaves the
					// sealed stores; wikilinks rewritten only in Markdown
					// (in code, "[[ -f x ]]" is shell, not a link).
					content = redactSecretsForLLM(mgr.DocumentContent(cs[0], cs[1]))
					if isMarkdownSource(cs[1]) {
						content = b.rewriteWikilinks(content, cs[0])
					}
				}
				return docNoteBody(title, cs[1], content, links)
			},
		}
	default:
		if s := strings.TrimSpace(n.Summary); s != "" && s != title {
			body.WriteString(b.rewriteWikilinks(s, "") + "\n")
		}
		body.WriteString(b.kindBody(n))
	}
	body.WriteString(b.linksSection(n.ID))
	return vault.Note{Path: notePath, Front: front, Body: body.String()}
}

// docKey is the revision of a document note: the context revision, the
// source and the rendered links — unchanged means the note is current.
func (b *vaultBuild) docKey(id string, cs [2]string, links string) string {
	h := sha256.New()
	fp := ""
	if b.ctxMgr != nil {
		fp = b.ctxMgr.DocumentsFingerprint(cs[0])
	}
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", id, fp, cs[1], links, strings.Join(b.tagsOf(id), ","))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func docNoteBody(title, source, content, links string) string {
	var b strings.Builder
	b.WriteString("> Source: `" + strings.ReplaceAll(source, "`", "'") + "`\n\n")
	content = strings.TrimSpace(content)
	switch {
	case content == "":
	case isMarkdownSource(source):
		// The document's own Markdown, minus a leading H1 that would
		// duplicate the note title.
		if first, rest, ok := strings.Cut(content, "\n"); ok && strings.TrimSpace(strings.TrimLeft(first, "#")) == title && strings.HasPrefix(first, "# ") {
			content = strings.TrimSpace(rest)
		}
		b.WriteString(content + "\n")
	default:
		lang := strings.TrimPrefix(strings.ToLower(filepath.Ext(source)), ".")
		fence := "```"
		for strings.Contains(content, fence) {
			fence += "`"
		}
		b.WriteString(fence + lang + "\n" + content + "\n" + fence + "\n")
	}
	b.WriteString(links)
	return "# " + title + "\n\n" + b.String()
}

func isMarkdownSource(source string) bool {
	switch strings.ToLower(filepath.Ext(source)) {
	case ".md", ".mdx", ".markdown", ".txt", "":
		return true
	}
	return false
}

// profileTitle is the profile note's display name: the user's name, unless
// it is marked private — then the neutral "user", everywhere the vault
// would show it (file name, heading, links, Home).
func (b *vaultBuild) profileTitle(title string) string {
	if b.profile.FieldSensitive("name") {
		return "user"
	}
	return title
}

// tagsOf returns the Obsidian tags of a node: the tag notes it links to.
// The profile's skill/goal tags are dropped when those lists are private.
func (b *vaultBuild) tagsOf(id string) []string {
	var tags []string
	for _, l := range b.g.Outlinks(id) {
		if id == "profile:user" && l.Rel == knowledge.RelHas &&
			(b.profile.FieldSensitive("skills") || b.profile.FieldSensitive("goals")) {
			continue
		}
		if t, ok := b.g.Node(l.To); ok && t.Kind == knowledge.KindTag {
			if tag := vault.Tag(t.Title); tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	sort.Strings(tags)
	return dedupeStrings(tags)
}

// linksSection renders the typed outgoing links of a node as wikilinks,
// grouped by relation. Tag links are front-matter tags, not listed here.
func (b *vaultBuild) linksSection(id string) string {
	type line struct{ rel, text string }
	var lines []line
	for _, l := range b.g.Outlinks(id) {
		p, ok := b.paths[l.To]
		if !ok {
			continue // tags and nodes without a note
		}
		t, _ := b.g.Node(l.To)
		label := l.To
		if t != nil && strings.TrimSpace(t.Title) != "" {
			label = plainTitle(t.Title)
			if t.Kind == knowledge.KindProfile {
				label = b.profileTitle(label)
			}
		}
		text := vault.Link(p, label)
		if l.Origin == knowledge.OriginUser {
			text += " · linked by you"
			if note := b.userNotes[[3]string{l.From, l.To, string(l.Rel)}]; note != "" {
				text += " — " + note
			}
		}
		lines = append(lines, line{rel: string(l.Rel), text: text})
	}
	if len(lines) == 0 {
		return ""
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].rel != lines[j].rel {
			return lines[i].rel < lines[j].rel
		}
		return lines[i].text < lines[j].text
	})
	var s strings.Builder
	s.WriteString("\n## Links\n\n")
	for _, ln := range lines {
		s.WriteString("- " + strings.ReplaceAll(ln.rel, "_", " ") + " → " + ln.text + "\n")
	}
	return s.String()
}

// kindFields adds the source store's metadata to the front matter.
func (b *vaultBuild) kindFields(n *knowledge.Node) []vault.Field {
	var out []vault.Field
	add := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			out = append(out, vault.Field{Key: k, Value: v})
		}
	}
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	switch n.Kind {
	case knowledge.KindFact:
		if f, ok := b.facts[n.ID]; ok {
			add("category", f.Category)
			if f.Confidence > 0 {
				add("confidence", strconv.FormatFloat(f.Confidence, 'f', 2, 64))
			}
			add("provenance", f.Provenance)
			add("source_project", f.SourceProject)
			add("created", stamp(f.CreatedAt))
			add("last_accessed", stamp(f.LastAccessed))
		}
	case knowledge.KindTopic:
		if t, ok := b.topics[n.ID]; ok {
			add("mentions", strconv.Itoa(t.Mentions))
			add("first_seen", stamp(t.FirstSeen))
			add("last_seen", stamp(t.LastSeen))
		}
	case knowledge.KindProject:
		if p, ok := b.projects[n.ID]; ok {
			add("path", p.Path)
			add("status", p.Status)
			add("last_active", stamp(p.LastActive))
			if len(p.Technologies) > 0 {
				out = append(out, vault.Field{Key: "technologies", IsList: true, List: p.Technologies})
			}
		}
	case knowledge.KindEpisode:
		if e, ok := b.episodes[n.ID]; ok {
			add("date", e.Date.Format("2006-01-02"))
			add("project", e.Project)
			add("source", e.Source)
		}
	case knowledge.KindCard:
		if c, ok := b.cards[n.ID]; ok {
			add("column", string(c.Column))
			add("assignee", c.Assignee)
			add("created", stamp(c.CreatedAt))
			add("updated", stamp(c.UpdatedAt))
		}
	case knowledge.KindKB, knowledge.KindContext:
		if fc, ok := b.contexts[n.ID]; ok {
			add("mode", string(fc.Mode))
			add("files", strconv.Itoa(fc.FileCount))
			add("updated", stamp(fc.UpdatedAt))
		}
	case knowledge.KindDoc:
		if cs, ok := b.docSource[n.ID]; ok {
			add("source", cs[1])
			if fc, ok := b.contexts["kb:"+cs[0]]; ok {
				add("context", fc.Name)
			} else if fc, ok := b.contexts["ctx:"+cs[0]]; ok {
				add("context", fc.Name)
			}
		}
	}
	return out
}

// kindBody adds kind-specific body text after the summary.
func (b *vaultBuild) kindBody(n *knowledge.Node) string {
	switch n.Kind {
	case knowledge.KindEpisode:
		if e, ok := b.episodes[n.ID]; ok && len(e.Refs) > 0 {
			return "\nReferences: " + strings.Join(e.Refs, ", ") + "\n"
		}
	case knowledge.KindCard:
		if c, ok := b.cards[n.ID]; ok && len(c.Notes) > 0 {
			var s strings.Builder
			s.WriteString("\n## Notes\n\n")
			for _, note := range c.Notes {
				s.WriteString("- " + strings.TrimSpace(note.Text) + "\n")
			}
			return s.String()
		}
	case knowledge.KindKB, knowledge.KindContext:
		return b.contextIndex(n.ID)
	}
	return ""
}

// contextIndex lists a context's documents (its folder note).
func (b *vaultBuild) contextIndex(ctxNode string) string {
	type entry struct{ path, title string }
	var docs []entry
	for _, l := range b.g.Backlinks(ctxNode) {
		if l.Rel != knowledge.RelPartOf {
			continue
		}
		if p, ok := b.paths[l.From]; ok {
			t, _ := b.g.Node(l.From)
			docs = append(docs, entry{p, plainTitle(t.Title)})
		}
	}
	if len(docs) == 0 {
		return ""
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].path < docs[j].path })
	var s strings.Builder
	s.WriteString("\n## Documents\n\n")
	for _, d := range docs {
		s.WriteString("- " + vault.Link(d.path, d.title) + "\n")
	}
	return s.String()
}

// profileBody renders the user profile with private values masked.
func (b *vaultBuild) profileBody() string {
	p := b.profile
	var s strings.Builder
	scalar := func(key, label, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		if p.FieldSensitive(key) {
			val = vaultSensitiveMask
		}
		s.WriteString("- **" + label + ":** " + val + "\n")
	}
	scalar("name", "Name", p.Name)
	scalar("role", "Role", p.Role)
	scalar("expertise_level", "Expertise", p.ExpertiseLevel)
	scalar("preferred_language", "Language", p.PreferredLang)
	scalar("communication_style", "Communication style", p.CommStyle)
	scalar("company", "Company", p.Company)
	scalar("location", "Location", p.Location)
	list := func(key, label string, vals []string) {
		if len(vals) == 0 {
			return
		}
		s.WriteString("\n## " + label + "\n\n")
		if p.FieldSensitive(key) {
			s.WriteString("- " + vaultSensitiveMask + "\n")
			return
		}
		for _, v := range vals {
			s.WriteString("- " + v + "\n")
		}
	}
	list("certifications", "Certifications", p.Certifications)
	list("skills", "Skills", p.Skills)
	list("goals", "Goals", p.Goals)
	list("interests", "Interests", p.Interests)
	list("directives", "Directives", p.Directives)
	if len(p.Stances) > 0 {
		s.WriteString("\n## Stances\n\n")
		for _, st := range p.Stances {
			line := st.Position
			if st.Reason != "" {
				line += " — " + st.Reason
			}
			if p.FieldSensitive("stances") {
				line = vaultSensitiveMask
			}
			s.WriteString("- " + line + "\n")
		}
	}
	if len(p.Milestones) > 0 {
		s.WriteString("\n## Milestones\n\n")
		for _, m := range p.Milestones {
			text := m.Text
			if p.FieldSensitive("milestones") {
				text = vaultSensitiveMask
			}
			s.WriteString("- " + m.Date.Format("2006-01-02") + " — " + text + "\n")
		}
	}
	kvSection := func(label, prefix string, m map[string]string) {
		if len(m) == 0 {
			return
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		s.WriteString("\n## " + label + "\n\n")
		for _, k := range keys {
			v := m[k]
			if p.FieldSensitive(prefix + k) {
				v = vaultSensitiveMask
			}
			s.WriteString("- **" + k + ":** " + v + "\n")
		}
	}
	kvSection("Environment", "env:", p.Environment)
	kvSection("Preferences", "pref:", p.Preferences)
	return s.String()
}

// home renders the vault's map of content.
func (b *vaultBuild) home() vault.Note {
	var s strings.Builder
	s.WriteString("# chatcli memory\n\n")
	s.WriteString("Generated by chatcli from its memory, contexts and sessions. ")
	s.WriteString("Notes you edit here are kept (not overwritten); `/memory vault sync --force` regenerates them.\n\n")
	if card := b.g.IndexCard(graphIndexMaxHubs); card != "" {
		// The card lists hubs by title; a private profile name stays out
		// of the vault like everywhere else in it.
		if prof, ok := b.g.Node("profile:user"); ok && b.profile.FieldSensitive("name") && strings.TrimSpace(prof.Title) != "" {
			card = strings.ReplaceAll(card, strings.TrimSpace(prof.Title), "user")
		}
		s.WriteString("> " + b.rewriteWikilinks(card, "") + "\n\n")
	}
	sections := []struct {
		kind  knowledge.Kind
		label string
	}{
		{knowledge.KindProfile, "Profile"}, {knowledge.KindProject, "Projects"},
		{knowledge.KindTopic, "Topics"}, {knowledge.KindKB, "Knowledge bases"},
		{knowledge.KindContext, "Contexts"}, {knowledge.KindSkill, "Skills"},
		{knowledge.KindCard, "Board"}, {knowledge.KindNote, "Journal"},
		{knowledge.KindSession, "Recent sessions"},
	}
	for _, sec := range sections {
		var items []string
		for _, n := range b.g.Nodes() {
			if n.Kind != sec.kind {
				continue
			}
			if p, ok := b.paths[n.ID]; ok {
				label := plainTitle(n.Title)
				if n.Kind == knowledge.KindProfile {
					label = b.profileTitle(label)
				}
				items = append(items, "- "+vault.Link(p, label))
			}
		}
		if len(items) == 0 {
			continue
		}
		sort.Strings(items)
		s.WriteString("## " + sec.label + "\n\n" + strings.Join(items, "\n") + "\n\n")
	}
	counts := b.g.CountByKind()
	s.WriteString("## Everything else\n\n")
	fmt.Fprintf(&s, "- Facts: %d (folder `Facts/`)\n- Episodes: %d (folder `Episodes/`)\n- Documents: %d (inside `Contexts/`)\n",
		counts[knowledge.KindFact], counts[knowledge.KindEpisode], counts[knowledge.KindDoc])
	return vault.Note{
		Path:  "Home.md",
		Front: []vault.Field{{Key: "chatcli_id", Value: "home"}, {Key: "kind", Value: "index"}},
		Body:  s.String(),
	}
}

// rewriteWikilinks points every [[target]] in text at the exact vault note
// the graph resolves it to — [[target#heading|label]] becomes
// [[Folder/Note#heading|label]] — so links work in Obsidian even when the
// target is an alias, a title that differs from its file name, or a note in
// another folder. ctxID scopes resolution to a document's own context
// first, exactly like the graph does. Unresolvable targets and anything
// inside fenced code blocks are left untouched.
func (b *vaultBuild) rewriteWikilinks(text, ctxID string) string {
	if !strings.Contains(text, "[[") {
		return text
	}
	lines := strings.Split(text, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.Contains(line, "[[") {
			continue
		}
		lines[i] = b.rewriteLine(line, ctxID)
	}
	return strings.Join(lines, "\n")
}

func (b *vaultBuild) rewriteLine(line, ctxID string) string {
	var out strings.Builder
	for {
		start := strings.Index(line, "[[")
		if start < 0 {
			out.WriteString(line)
			return out.String()
		}
		end := strings.Index(line[start+2:], "]]")
		if end < 0 {
			out.WriteString(line)
			return out.String()
		}
		inner := line[start+2 : start+2+end]
		out.WriteString(line[:start])
		out.WriteString(b.rewriteLink(inner, ctxID))
		line = line[start+2+end+2:]
	}
}

func (b *vaultBuild) rewriteLink(inner, ctxID string) string {
	orig := "[[" + inner + "]]"
	target, label := inner, ""
	if i := strings.IndexByte(target, '|'); i >= 0 {
		target, label = target[:i], target[i+1:]
	}
	anchor := ""
	if i := strings.IndexAny(target, "#^"); i >= 0 {
		target, anchor = target[:i], target[i:]
	}
	target = strings.TrimSpace(target)
	if target == "" || strings.Contains(target, "/") {
		return orig // empty, same-note anchor, or already a path
	}
	var id string
	if ctxID != "" {
		id = b.docNames[ctxID][knowledge.NormalizeRef(target)]
	}
	if id == "" {
		if n, ok := b.g.Resolve(target); ok {
			id = n.ID
		}
	}
	p, ok := b.paths[id]
	if !ok {
		return orig
	}
	if label == "" {
		label = target
	}
	return vault.Link(strings.TrimSuffix(p, ".md")+anchor+".md", label)
}

// plainTitle renders a note title for display: [[target|label]] markup
// (a fact whose first line links somewhere) collapses to its visible text.
func plainTitle(title string) string {
	rest := strings.TrimSpace(title)
	var out strings.Builder
	for {
		start := strings.Index(rest, "[[")
		if start < 0 {
			out.WriteString(rest)
			return out.String()
		}
		end := strings.Index(rest[start:], "]]")
		if end < 0 {
			out.WriteString(rest)
			return out.String()
		}
		inner := rest[start+2 : start+end]
		if i := strings.LastIndexByte(inner, '|'); i >= 0 {
			inner = inner[i+1:]
		} else if i := strings.IndexAny(inner, "#^"); i >= 0 {
			inner = inner[:i]
		}
		out.WriteString(rest[:start])
		out.WriteString(inner)
		rest = rest[start+end+2:]
	}
}

func dedupeStrings(xs []string) []string {
	seen := make(map[string]bool, len(xs))
	out := xs[:0:0]
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}
