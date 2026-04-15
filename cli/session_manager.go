package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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

	if err := os.WriteFile(filePath, data, 0o600); err != nil {
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
