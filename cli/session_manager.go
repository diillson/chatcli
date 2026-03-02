package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// SessionData is the v2 session format that supports scoped histories.
// It is backward-compatible with the legacy format (plain []models.Message).
type SessionData struct {
	Version      int              `json:"version"`                  // 2 for the new format
	ChatHistory  []models.Message `json:"chat_history"`
	AgentHistory []models.Message `json:"agent_history,omitempty"`
	CoderHistory []models.Message `json:"coder_history,omitempty"`
	SharedMemory []models.Message `json:"shared_memory,omitempty"`
}

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

// getSessionPath retorna o caminho completo para um arquivo de sessão.
func (sm *SessionManager) getSessionPath(name string) string {
	// Sanitiza o nome para evitar problemas com o sistema de arquivos
	safeName := strings.ReplaceAll(name, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")
	return filepath.Join(sm.sessionsDir, safeName+".json")
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
	if name == "" {
		return fmt.Errorf("o nome da sessão não pode ser vazio")
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

	data, err := os.ReadFile(filePath)
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

// DeleteSession apaga um arquivo de sessão.
func (sm *SessionManager) DeleteSession(name string) error {
	filePath := sm.getSessionPath(name)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("sessão '%s' não encontrada", name)
	}
	return os.Remove(filePath)
}
