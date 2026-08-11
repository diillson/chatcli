package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Security (L4): Strict session name validation
var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-.]{0,254}$`)

// SessionData is an alias for the shared models.SessionData type.
// Kept for local convenience within the cli package.
type SessionData = models.SessionData

// SessionManager gerencia o salvamento e carregamento de sessões de conversa.
type SessionManager struct {
	sessionsDir string
	logger      *zap.Logger

	// Search-corpus cache (see searchCorpus): rebuilt only when the store's
	// directory signature changes, so per-turn session auto-recall doesn't
	// re-parse every saved session file.
	corpusMu sync.Mutex
	corpus   *sessionCorpus
}

// NewSessionManager cria uma nova instância do SessionManager.
func NewSessionManager(logger *zap.Logger) (*SessionManager, error) {
	homeDir, err := utils.GetHomeDir()
	if err != nil {
		return nil, fmt.Errorf("não foi possível obter o diretório home: %w", err)
	}

	sessionsDir := filepath.Join(homeDir, ".chatcli", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return nil, fmt.Errorf("não foi possível criar o diretório de sessões: %w", err)
	}

	return &SessionManager{
		sessionsDir: sessionsDir,
		logger:      logger,
	}, nil
}

// validateSessionName checks the session name against security rules (L4).
func validateSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	// Reject null bytes, control characters
	for _, c := range name {
		if c < 0x20 || c == 0x7f || c == 0x00 {
			return fmt.Errorf("session name contains invalid control characters")
		}
	}
	if !validSessionName.MatchString(name) {
		return fmt.Errorf("session name must be alphanumeric with dash/underscore/dot only (max 255 chars)")
	}
	return nil
}

// getSessionPath retorna o caminho completo para um arquivo de sessão.
func (sm *SessionManager) getSessionPath(name string) string {
	// Sanitiza o nome para evitar problemas com o sistema de arquivos
	safeName := strings.ReplaceAll(name, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")
	return filepath.Join(sm.sessionsDir, safeName+".json")
}

// atomicWriteSessionFile persists data via a same-directory temp file and an
// atomic rename (the same durability pattern as ctxmgr's context store), so a
// crash mid-write can never leave a torn session file behind.
func atomicWriteSessionFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	if err := os.WriteFile(tmpName, data, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// CleanExpiredSessions removes sessions older than the configured TTL (L6).
// Default TTL: 90 days, configurable via CHATCLI_SESSION_TTL (in days).
func (sm *SessionManager) CleanExpiredSessions() int {
	ttlDays := 90
	if v := os.Getenv("CHATCLI_SESSION_TTL"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 {
			ttlDays = n
		}
	}

	maxAge := time.Duration(ttlDays) * 24 * time.Hour
	cutoff := time.Now().Add(-maxAge)
	cleaned := 0

	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(sm.sessionsDir, entry.Name())
			if err := os.Remove(path); err == nil {
				sm.logger.Info("Expired session cleaned",
					zap.String("session", entry.Name()),
					zap.Time("modified", info.ModTime()))
				cleaned++
			}
		}
	}

	if cleaned > 0 {
		sm.logger.Info("Session cleanup completed", zap.Int("removed", cleaned), zap.Int("ttl_days", ttlDays))
	}
	return cleaned
}

// machineSessionPrefixes name the session files ChatCLI creates on its own
// (REPL autosave-on-exit and MCP per-session autosave). Lifecycle policies —
// prune-by-count, TTL expiry — apply ONLY to these: sessions the USER named
// are deliberate checkpoints and are never deleted automatically. The
// distilled layers (facts, episodes, rollups) survive any session cleanup,
// so pruning bounds disk and search cost without degrading recall.
var machineSessionPrefixes = []string{"autosave-", "mcp-"}

// isMachineSession reports whether name carries a machine prefix.
func isMachineSession(name string) bool {
	for _, p := range machineSessionPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// PruneSessionsByPrefix deletes the OLDEST sessions matching prefix beyond
// keep, ordered by file modification time (newest survive). Returns how many
// were removed. Safe on any cadence; missing dir is not an error.
func (sm *SessionManager) PruneSessionsByPrefix(prefix string, keep int) int {
	if keep < 0 {
		keep = 0
	}
	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		return 0
	}
	type aged struct {
		name string
		mod  time.Time
	}
	matches := make([]aged, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		matches = append(matches, aged{name: name, mod: info.ModTime()})
	}
	if len(matches) <= keep {
		return 0
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].mod.Before(matches[j].mod) })
	removed := 0
	for _, m := range matches[:len(matches)-keep] {
		if err := sm.DeleteSession(m.name); err == nil {
			removed++
		}
	}
	if removed > 0 {
		sm.logger.Info("Machine sessions pruned", zap.String("prefix", prefix), zap.Int("removed", removed))
	}
	return removed
}

// CleanExpiredMachineSessions applies the TTL (CHATCLI_SESSION_TTL, default
// 90 days; "0" disables expiry entirely, honoring the documented contract)
// to MACHINE-created sessions only — autosaves and MCP session mirrors.
// User-named sessions are never expired: a checkpoint someone saved on
// purpose must outlive any retention policy. This is the lifecycle hook the
// boot paths call; the broader CleanExpiredSessions remains available for
// operators who explicitly want full expiry.
func (sm *SessionManager) CleanExpiredMachineSessions() int {
	ttlDays := 90
	if v := os.Getenv("CHATCLI_SESSION_TTL"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil {
			if n == 0 {
				return 0 // documented opt-out: keep every session indefinitely
			}
			if n > 0 {
				ttlDays = n
			}
		}
	}
	cutoff := time.Now().Add(-time.Duration(ttlDays) * 24 * time.Hour)

	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		return 0
	}
	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if !isMachineSession(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(sm.sessionsDir, entry.Name())); err == nil {
			cleaned++
		}
	}
	if cleaned > 0 {
		sm.logger.Info("Expired machine sessions cleaned", zap.Int("removed", cleaned), zap.Int("ttl_days", ttlDays))
	}
	return cleaned
}

// SaveSession salva o histórico da conversa em um arquivo JSON.
// Mantém assinatura original para compatibilidade com remote client.
func (sm *SessionManager) SaveSession(name string, history []models.Message) error {
	return sm.SaveSessionV2(name, &SessionData{
		Version:     2,
		ChatHistory: history,
	})
}

// SaveSessionV2 salva uma sessão completa com históricos escopados.
func (sm *SessionManager) SaveSessionV2(name string, sd *SessionData) error {
	// Security (L4): Validate session name
	if err := validateSessionName(name); err != nil {
		return err
	}

	sd.Version = 2
	// Derive a topic title once at save time so machine names
	// (autosave-20260720-1504) stay recognizable in list/search/recall.
	// An explicitly set title always wins.
	if strings.TrimSpace(sd.Title) == "" {
		sd.Title = deriveSessionTitle(sd)
	}
	filePath := sm.getSessionPath(name)
	data, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		sm.logger.Error("Erro ao serializar a sessão para JSON", zap.String("session", name), zap.Error(err))
		return fmt.Errorf("erro ao serializar a sessão: %w", err)
	}

	// Atomic same-directory temp + rename: the REPL, the gateway daemon and
	// the MCP server all share this store, so a plain WriteFile interrupted
	// mid-write (or two processes saving concurrently) could leave a torn
	// session file. The unique temp name keeps concurrent savers from
	// clobbering each other's in-flight write; last rename wins whole-file.
	if err := atomicWriteSessionFile(filePath, data); err != nil {
		sm.logger.Error("Erro ao salvar o arquivo da sessão", zap.String("path", filePath), zap.Error(err))
		return fmt.Errorf("erro ao salvar o arquivo da sessão: %w", err)
	}

	sm.logger.Info("Sessão salva com sucesso", zap.String("session", name), zap.String("path", filePath))
	return nil
}

// SessionModTime returns the store file's last-modified time for a saved
// session. It is the freshness signal for cross-surface continuity: a bound
// surface (REPL, MCP/ACP session, gateway principal) compares it against its
// own last sync stamp to decide whether another surface has written the
// session since, and reloads before the next turn when it has.
// SessionExists reports whether a saved session file exists under name.
// Invalid names report false — callers treat them as "nothing to load" and
// surface the validation error on the write path instead.
func (sm *SessionManager) SessionExists(name string) bool {
	if err := validateSessionName(name); err != nil {
		return false
	}
	_, err := os.Stat(sm.getSessionPath(name))
	return err == nil
}

func (sm *SessionManager) SessionModTime(name string) (time.Time, error) {
	if err := validateSessionName(name); err != nil {
		return time.Time{}, err
	}
	info, err := os.Stat(sm.getSessionPath(name))
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// LoadSession carrega o histórico de uma conversa de um arquivo JSON.
// Mantém assinatura original para compatibilidade com remote client.
// Retorna apenas o chatHistory para uso legado.
func (sm *SessionManager) LoadSession(name string) ([]models.Message, error) {
	sd, err := sm.LoadSessionV2(name)
	if err != nil {
		return nil, err
	}
	return sd.ChatHistory, nil
}

// LoadSessionV2 carrega uma sessão completa com suporte a formato v2 e legacy.
func (sm *SessionManager) LoadSessionV2(name string) (*SessionData, error) {
	filePath := sm.getSessionPath(name)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("sessão '%s' não encontrada", name)
	}

	data, err := os.ReadFile(filePath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err != nil {
		sm.logger.Error("Erro ao ler o arquivo da sessão", zap.String("path", filePath), zap.Error(err))
		return nil, fmt.Errorf("erro ao ler o arquivo da sessão: %w", err)
	}

	// Try v2 format first
	var sd SessionData
	if err := json.Unmarshal(data, &sd); err == nil && sd.Version >= 2 {
		sm.logger.Info("Sessão v2 carregada com sucesso", zap.String("session", name))
		return &sd, nil
	}

	// Fallback: legacy format (plain []models.Message)
	var legacy []models.Message
	if err := json.Unmarshal(data, &legacy); err != nil {
		sm.logger.Error("Erro ao desserializar a sessão", zap.String("session", name), zap.Error(err))
		return nil, fmt.Errorf("arquivo de sessão corrompido: %w", err)
	}

	sm.logger.Info("Sessão legacy carregada com sucesso (migrada para v2)", zap.String("session", name))
	return &SessionData{
		Version:     2,
		ChatHistory: legacy,
	}, nil
}

// sessionTitleMaxRunes caps derived titles: long enough to state a topic,
// short enough for one list line.
const sessionTitleMaxRunes = 60

// deriveSessionTitle picks the first non-empty user message as the session's
// topic label, whitespace-collapsed and rune-capped. Returns "" for sessions
// with no user turn.
func deriveSessionTitle(sd *SessionData) string {
	for _, hist := range [][]models.Message{sd.ChatHistory, sd.AgentHistory, sd.CoderHistory} {
		for _, m := range hist {
			if m.Role != "user" {
				continue
			}
			t := strings.Join(strings.Fields(m.Content), " ")
			if t == "" {
				continue
			}
			r := []rune(t)
			if len(r) > sessionTitleMaxRunes {
				t = string(r[:sessionTitleMaxRunes]) + "…"
			}
			return t
		}
	}
	return ""
}

// LatestSessionInfo returns the newest saved session's name, save time and
// title, best-effort — a zero name means the store is empty or unreadable.
// Only the newest file is parsed, so this is cheap enough for the boot path.
func (sm *SessionManager) LatestSessionInfo() (name string, saved time.Time, title string) {
	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		return "", time.Time{}, ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if name == "" || info.ModTime().After(saved) {
			name = strings.TrimSuffix(e.Name(), ".json")
			saved = info.ModTime()
		}
	}
	if name == "" {
		return "", time.Time{}, ""
	}
	if sd, err := sm.LoadSessionV2(name); err == nil && sd != nil {
		title = sd.Title
	}
	return name, saved, title
}

// SessionTitles returns the stored title per session, best-effort ("" or a
// missing key means no title). Served from the cached search corpus, so
// callers can decorate listings without re-reading the store.
func (sm *SessionManager) SessionTitles() map[string]string {
	corpus, err := sm.searchCorpus()
	if err != nil || corpus == nil {
		return nil
	}
	return corpus.titles
}

// ListSessions lista todas as sessões salvas.
func (sm *SessionManager) ListSessions() ([]string, error) {
	entries, err := os.ReadDir(sm.sessionsDir)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler o diretório de sessões: %w", err)
	}

	var sessions []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			sessionName := strings.TrimSuffix(entry.Name(), ".json")
			sessions = append(sessions, sessionName)
		}
	}
	return sessions, nil
}

// SessionSearchHit is one session that matched a search, with the strongest
// snippets for context.
type SessionSearchHit struct {
	Session  string
	Matches  int
	Score    float64
	Snippets []string
	SavedAt  time.Time // session file mtime; zero when unknown
	Title    string    // stored session title; "" when absent
}

// sessionSearchDoc maps one BM25 document (a message) back to its session.
// norm is the normalized (lowercase, accent-folded) view used for ranking
// and anchor lookup; content keeps the original text for snippets.
type sessionSearchDoc struct {
	session string
	role    string
	content string
	norm    string
}

// sessionCorpus is the parsed, search-ready view of the session store. It is
// cached on the SessionManager and invalidated by a cheap directory signature
// (names + mtimes + sizes), so per-turn callers (session auto-recall) don't
// re-parse hundreds of JSON files on every agent turn.
// warmTitles returns the cached session-title map when the search corpus is
// already built, or nil — it NEVER triggers a corpus build (the first build
// parses every session JSON, a cost the graph derivation must not add to
// chat turns). The returned map is a read-only snapshot: corpus rebuilds
// swap in a fresh map rather than mutating this one.
func (sm *SessionManager) warmTitles() map[string]string {
	sm.corpusMu.Lock()
	defer sm.corpusMu.Unlock()
	if sm.corpus == nil {
		return nil
	}
	return sm.corpus.titles
}

type sessionCorpus struct {
	sig    string
	docs   []sessionSearchDoc
	text   map[string]string    // normalized (lowercase, accent-folded) full text per session
	saved  map[string]time.Time // session file mtime
	titles map[string]string    // stored session title ("" when absent)
	order  []string             // session names, newest first
}

// searchCorpus returns the cached corpus, rebuilding it only when the store
// changed. Unreadable sessions are skipped rather than aborting the search.
func (sm *SessionManager) searchCorpus() (*sessionCorpus, error) {
	names, err := sm.ListSessions()
	if err != nil {
		return nil, err
	}

	type fileInfo struct {
		name  string
		mtime time.Time
	}
	infos := make([]fileInfo, 0, len(names))
	var sig strings.Builder
	for _, name := range names {
		st, err := os.Stat(sm.getSessionPath(name))
		if err != nil {
			continue
		}
		infos = append(infos, fileInfo{name: name, mtime: st.ModTime()})
		fmt.Fprintf(&sig, "%s|%d|%d;", name, st.ModTime().UnixNano(), st.Size())
	}

	sm.corpusMu.Lock()
	defer sm.corpusMu.Unlock()
	if sm.corpus != nil && sm.corpus.sig == sig.String() {
		return sm.corpus, nil
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].mtime.After(infos[j].mtime) })

	c := &sessionCorpus{
		sig:    sig.String(),
		text:   make(map[string]string, len(infos)),
		saved:  make(map[string]time.Time, len(infos)),
		titles: make(map[string]string, len(infos)),
	}
	for _, fi := range infos {
		sd, err := sm.LoadSessionV2(fi.name)
		if err != nil || sd == nil {
			continue
		}
		var b strings.Builder
		count := 0
		for _, hist := range [][]models.Message{sd.ChatHistory, sd.AgentHistory, sd.CoderHistory, sd.SharedMemory} {
			for _, msg := range hist {
				if msg.Content == "" {
					continue
				}
				// System messages are stored prompts, not conversation: they
				// are near-identical across sessions and dense with generic
				// words, so they both pollute BM25 ranking and let framing
				// terms qualify every session. Recall searches conversation
				// only.
				if msg.Role == "system" {
					continue
				}
				norm := normalizeForSearch(msg.Content)
				c.docs = append(c.docs, sessionSearchDoc{session: fi.name, role: msg.Role, content: msg.Content, norm: norm})
				b.WriteString(norm)
				b.WriteByte('\n')
				count++
			}
		}
		if count == 0 {
			continue
		}
		c.text[fi.name] = b.String()
		c.saved[fi.name] = fi.mtime
		c.titles[fi.name] = sd.Title
		c.order = append(c.order, fi.name)
	}
	sm.corpus = c
	return c, nil
}

// recentSessionHitsCap bounds how many sessions a no-significant-terms
// query ("o que discutimos?") lists.
const recentSessionHitsCap = 5

// recentSessionHits lists the most recently saved in-scope sessions, newest
// first, with the head of their first messages as snippets. Used when the
// query carries no content-bearing terms and BM25 has nothing to rank on.
func (sm *SessionManager) recentSessionHits(corpus *sessionCorpus, inScope map[string]bool, maxSnippetsPerSession int) []SessionSearchHit {
	hits := make([]SessionSearchHit, 0, recentSessionHitsCap)
	for _, name := range corpus.order { // already newest first
		if !inScope[name] || len(hits) >= recentSessionHitsCap {
			continue
		}
		hit := SessionSearchHit{
			Session: name,
			SavedAt: corpus.saved[name],
			Title:   corpus.titles[name],
		}
		for _, d := range corpus.docs {
			if d.session != name {
				continue
			}
			hit.Matches++
			if len(hit.Snippets) < maxSnippetsPerSession {
				hit.Snippets = append(hit.Snippets, d.role+": "+snippetAround(d.content, d.norm, ""))
			}
		}
		hits = append(hits, hit)
	}
	return hits
}

// sessionRecencyBoost is a bounded multiplier favoring recent sessions when
// BM25 scores are close: at most +25% for a session saved right now, fading
// with a one-week half-life. Match density still dominates the ranking.
func sessionRecencyBoost(saved time.Time) float64 {
	if saved.IsZero() {
		return 1
	}
	ageWeeks := time.Since(saved).Hours() / (24 * 7)
	if ageWeeks < 0 {
		ageWeeks = 0
	}
	return 1 + 0.25/(1+ageWeeks)
}

// SearchSessions performs a ranked full-text search across all persisted
// sessions, reusing the existing JSON store (no separate index). Semantics,
// tuned for natural-language recall queries ("o que discutimos sobre X?"):
//
//   - Query terms are normalized (lowercase, accent-folded) and reduced to
//     SIGNIFICANT terms — PT/EN stopwords and recall-framing verbs
//     ("discutimos", "decided") carry no signal and used to disqualify
//     every session under the old raw AND filter.
//   - A session QUALIFIES when every significant term appears somewhere in
//     it — in ANY message, not necessarily the same one. When no session
//     qualifies (or the query was all stopwords), the filter relaxes and
//     BM25 ranks alone rather than returning nothing.
//   - Qualifying sessions RANK by BM25 over their individual messages (the
//     same keyless scorer the knowledge corpus uses), with a bounded
//     recency boost so yesterday's session outranks a months-old one when
//     match quality is comparable. Snippets come from each session's
//     top-scoring messages.
//
// maxSnippetsPerSession caps how many context snippets each hit carries.
func (sm *SessionManager) SearchSessions(query string, maxSnippetsPerSession int) ([]SessionSearchHit, error) {
	return sm.searchSessionsScoped(query, maxSnippetsPerSession, 0)
}

// searchSessionsScoped implements SearchSessions; recentOnly > 0 restricts
// the search to the N most recently saved sessions (used by the per-turn
// session auto-recall's AMBIENT mode, where ranking the whole store every
// turn would be wasteful). Explicit recall (referential/temporal questions)
// passes 0 and reaches the whole store.
func (sm *SessionManager) searchSessionsScoped(query string, maxSnippetsPerSession, recentOnly int) ([]SessionSearchHit, error) {
	return sm.searchSessionsFiltered(query, maxSnippetsPerSession, recentOnly, time.Time{}, time.Time{})
}

// searchSessionsFiltered additionally bounds hits to sessions saved within
// [from, to) when from is non-zero — the backing for temporal recall
// questions ("o que fizemos há 2 semanas?").
func (sm *SessionManager) searchSessionsFiltered(query string, maxSnippetsPerSession, recentOnly int, from, to time.Time) ([]SessionSearchHit, error) {
	normQuery := normalizeForSearch(strings.TrimSpace(query))
	rawTerms := strings.Fields(normQuery)
	if len(rawTerms) == 0 {
		return nil, fmt.Errorf("empty query")
	}
	terms := significantSearchTerms(rawTerms)
	if maxSnippetsPerSession <= 0 {
		maxSnippetsPerSession = 3
	}

	corpus, err := sm.searchCorpus()
	if err != nil {
		return nil, err
	}

	inScope := make(map[string]bool, len(corpus.order))
	for i, name := range corpus.order {
		if recentOnly > 0 && i >= recentOnly {
			break
		}
		if !from.IsZero() {
			saved := corpus.saved[name]
			if saved.Before(from) || (!to.IsZero() && !saved.Before(to)) {
				continue
			}
		}
		inScope[name] = true
	}

	var docs []sessionSearchDoc
	for _, d := range corpus.docs {
		if inScope[d.session] {
			docs = append(docs, d)
		}
	}
	if len(docs) == 0 {
		return nil, nil
	}

	// A query with no significant terms is pure recall framing ("o que
	// discutimos?") — BM25 has nothing to rank on, so list the most recent
	// sessions in scope directly, newest first.
	if len(terms) == 0 {
		return sm.recentSessionHits(corpus, inScope, maxSnippetsPerSession), nil
	}

	qualifies := qualifySessionsByTerms(corpus, inScope, terms)

	// Rank over the normalized view so accents don't break token matching;
	// indices still map 1:1 onto docs.
	contents := make([]string, len(docs))
	for i, d := range docs {
		contents[i] = d.norm
	}
	ranked := ctxmgr.RankDocsBM25(contents, normQuery, len(contents))

	return aggregateSessionHits(corpus, docs, ranked, qualifies, terms, maxSnippetsPerSession), nil
}

// qualifySessionsByTerms applies the session-level significant-term filter,
// widening when strictness would return nothing: AND (every term somewhere
// in the session) → OR (at least one term) when AND finds nothing. A query
// whose terms appear in NO session still qualifies nothing.
func qualifySessionsByTerms(corpus *sessionCorpus, inScope map[string]bool, terms []string) map[string]bool {
	qualifies := make(map[string]bool, len(inScope))
	anyQualified := false
	for name := range inScope {
		text := corpus.text[name]
		all := true
		for _, t := range terms {
			if !strings.Contains(text, t) {
				all = false
				break
			}
		}
		qualifies[name] = all
		anyQualified = anyQualified || all
	}
	if !anyQualified && len(terms) > 1 {
		for name := range inScope {
			for _, t := range terms {
				if strings.Contains(corpus.text[name], t) {
					qualifies[name] = true
					break
				}
			}
		}
	}
	return qualifies
}

// aggregateSessionHits folds ranked message hits into per-session results,
// preserving BM25 order so each session's snippets are its strongest
// messages, then orders sessions by recency-boosted aggregate score.
func aggregateSessionHits(corpus *sessionCorpus, docs []sessionSearchDoc, ranked []ctxmgr.DocHit, qualifies map[string]bool, terms []string, maxSnippetsPerSession int) []SessionSearchHit {
	bySession := make(map[string]*SessionSearchHit)
	order := make([]string, 0)
	for _, h := range ranked {
		d := docs[h.Index]
		if !qualifies[d.session] {
			continue
		}
		hit, ok := bySession[d.session]
		if !ok {
			hit = &SessionSearchHit{
				Session: d.session,
				SavedAt: corpus.saved[d.session],
				Title:   corpus.titles[d.session],
			}
			bySession[d.session] = hit
			order = append(order, d.session)
		}
		hit.Matches++
		hit.Score += h.Score
		if len(hit.Snippets) < maxSnippetsPerSession {
			// Anchor lookup happens on the normalized view; the byte index
			// only centers a cosmetic window, and snippetAround snaps slice
			// bounds to rune boundaries, so accent-folding length drift
			// cannot corrupt the output.
			anchor := terms[0]
			for _, t := range terms {
				if strings.Contains(d.norm, t) {
					anchor = t
					break
				}
			}
			hit.Snippets = append(hit.Snippets, d.role+": "+snippetAround(d.content, d.norm, anchor))
		}
	}

	hits := make([]SessionSearchHit, 0, len(order))
	for _, name := range order {
		hits = append(hits, *bySession[name])
	}
	sort.SliceStable(hits, func(i, j int) bool {
		si := hits[i].Score * sessionRecencyBoost(hits[i].SavedAt)
		sj := hits[j].Score * sessionRecencyBoost(hits[j].SavedAt)
		if si != sj {
			return si > sj
		}
		return hits[i].Session < hits[j].Session
	})
	return hits
}

// GetSessionMessages returns one page of a saved session's unified message
// stream plus the total count, so the @session tool can read an old
// conversation without loading it over the live one. offset is 0-based;
// limit <= 0 applies a default page size.
func (sm *SessionManager) GetSessionMessages(name string, offset, limit int) ([]models.Message, int, error) {
	sd, err := sm.LoadSessionV2(name)
	if err != nil {
		return nil, 0, err
	}
	if sd == nil {
		return nil, 0, fmt.Errorf("session %q not found", name)
	}

	// Unified stream in store order: chat history is the live format; the
	// legacy per-mode histories are appended for old files.
	var all []models.Message
	for _, hist := range [][]models.Message{sd.ChatHistory, sd.AgentHistory, sd.CoderHistory, sd.SharedMemory} {
		all = append(all, hist...)
	}

	total := len(all)
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}

// snippetAround returns a trimmed, single-line window of content centered on
// the first occurrence of term, capped to keep search output compact.
func snippetAround(content, lowerContent, term string) string {
	const window = 120
	idx := strings.Index(lowerContent, term)
	if idx < 0 {
		idx = 0
	}
	start := idx - window/2
	if start < 0 {
		start = 0
	}
	end := idx + window/2
	if end > len(content) {
		end = len(content)
	}
	// Snap to rune boundaries: the anchor index may come from a normalized
	// view whose byte offsets drift from the original, and a mid-rune slice
	// would emit invalid UTF-8 (which some downstream CLIs hard-reject).
	for start > 0 && start < len(content) && !utf8.RuneStart(content[start]) {
		start--
	}
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end++
	}
	if start > end {
		start = end
	}
	s := strings.TrimSpace(strings.ReplaceAll(content[start:end], "\n", " "))
	if start > 0 {
		s = "…" + s
	}
	if end < len(content) {
		s += "…"
	}
	return s
}

// ForkSession creates a copy of an existing session with a new name.
// The forked session is an independent copy — changes to either session don't affect the other.
func (sm *SessionManager) ForkSession(sourceName, newName string) error {
	if sourceName == "" || newName == "" {
		return fmt.Errorf("source and target session names required")
	}
	if sourceName == newName {
		return fmt.Errorf("source and target names must be different")
	}

	// Check target doesn't already exist
	targetPath := sm.getSessionPath(newName)
	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("sessão '%s' já existe, escolha outro nome", newName)
	}

	// Load source
	sd, err := sm.LoadSessionV2(sourceName)
	if err != nil {
		return fmt.Errorf("falha ao carregar sessão fonte: %w", err)
	}

	// Save as new name
	if err := sm.SaveSessionV2(newName, sd); err != nil {
		return fmt.Errorf("falha ao salvar sessão fork: %w", err)
	}

	sm.logger.Info("Session forked",
		zap.String("source", sourceName),
		zap.String("fork", newName))
	return nil
}

// ForkCurrentToNew creates a fork from in-memory session data (for forking unsaved sessions).
func (sm *SessionManager) ForkCurrentToNew(newName string, sd *SessionData) error {
	if newName == "" {
		return fmt.Errorf("target session name required")
	}

	targetPath := sm.getSessionPath(newName)
	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("sessão '%s' já existe", newName)
	}

	return sm.SaveSessionV2(newName, sd)
}

// DeleteSession apaga um arquivo de sessão.
func (sm *SessionManager) DeleteSession(name string) error {
	filePath := sm.getSessionPath(name)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("sessão '%s' não encontrada", name)
	}
	return os.Remove(filePath)
}
