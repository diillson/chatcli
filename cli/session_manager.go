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
	"time"

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
// 90 days) to MACHINE-created sessions only — autosaves and MCP session
// mirrors. User-named sessions are never expired: a checkpoint someone saved
// on purpose must outlive any retention policy. This is the lifecycle hook
// the boot paths call; the broader CleanExpiredSessions remains available
// for operators who explicitly want full expiry.
func (sm *SessionManager) CleanExpiredMachineSessions() int {
	ttlDays := 90
	if v := os.Getenv("CHATCLI_SESSION_TTL"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 {
			ttlDays = n
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
}

// sessionSearchDoc maps one BM25 document (a message) back to its session.
type sessionSearchDoc struct {
	session string
	role    string
	content string
}

// SearchSessions performs a ranked full-text search across all persisted
// sessions, reusing the existing JSON store (no separate index). Two-level
// semantics balance recall and precision:
//
//   - A session QUALIFIES when every query term appears somewhere in it —
//     in ANY message, not necessarily the same one. The old per-message AND
//     returned nothing for exactly the queries recall exists for ("oauth
//     refresh decision" discussed across several turns).
//   - Qualifying sessions RANK by BM25 over their individual messages (the
//     same keyless scorer the knowledge corpus uses), so the session where
//     the terms are dense and rare outranks one that mentions them in
//     passing. Snippets come from each session's top-scoring messages.
//
// maxSnippetsPerSession caps how many context snippets each hit carries.
func (sm *SessionManager) SearchSessions(query string, maxSnippetsPerSession int) ([]SessionSearchHit, error) {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(terms) == 0 {
		return nil, fmt.Errorf("empty query")
	}
	if maxSnippetsPerSession <= 0 {
		maxSnippetsPerSession = 3
	}

	names, err := sm.ListSessions()
	if err != nil {
		return nil, err
	}

	var docs []sessionSearchDoc
	sessionText := make(map[string]*strings.Builder)
	for _, name := range names {
		sd, err := sm.LoadSessionV2(name)
		if err != nil || sd == nil {
			continue // skip unreadable sessions rather than abort the search
		}
		for _, hist := range [][]models.Message{sd.ChatHistory, sd.AgentHistory, sd.CoderHistory, sd.SharedMemory} {
			for _, msg := range hist {
				if msg.Content == "" {
					continue
				}
				docs = append(docs, sessionSearchDoc{session: name, role: msg.Role, content: msg.Content})
				b, ok := sessionText[name]
				if !ok {
					b = &strings.Builder{}
					sessionText[name] = b
				}
				b.WriteString(strings.ToLower(msg.Content))
				b.WriteByte('\n')
			}
		}
	}
	if len(docs) == 0 {
		return nil, nil
	}

	// Session-level AND filter: every term somewhere in the conversation.
	qualifies := make(map[string]bool, len(sessionText))
	for name, b := range sessionText {
		text := b.String()
		all := true
		for _, t := range terms {
			if !strings.Contains(text, t) {
				all = false
				break
			}
		}
		qualifies[name] = all
	}

	contents := make([]string, len(docs))
	for i, d := range docs {
		contents[i] = d.content
	}
	ranked := ctxmgr.RankDocsBM25(contents, query, len(contents))

	// Aggregate message hits per session, preserving BM25 order so each
	// session's snippets are its strongest messages.
	bySession := make(map[string]*SessionSearchHit)
	order := make([]string, 0)
	for _, h := range ranked {
		d := docs[h.Index]
		if !qualifies[d.session] {
			continue
		}
		hit, ok := bySession[d.session]
		if !ok {
			hit = &SessionSearchHit{Session: d.session}
			bySession[d.session] = hit
			order = append(order, d.session)
		}
		hit.Matches++
		hit.Score += h.Score
		if len(hit.Snippets) < maxSnippetsPerSession {
			lower := strings.ToLower(d.content)
			anchor := terms[0]
			for _, t := range terms {
				if strings.Contains(lower, t) {
					anchor = t
					break
				}
			}
			hit.Snippets = append(hit.Snippets, d.role+": "+snippetAround(d.content, lower, anchor))
		}
	}

	hits := make([]SessionSearchHit, 0, len(order))
	for _, name := range order {
		hits = append(hits, *bySession[name])
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Session < hits[j].Session
	})
	return hits, nil
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
