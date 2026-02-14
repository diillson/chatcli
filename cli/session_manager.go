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
func (sm *SessionManager) SaveSession(name string, history []models.Message) error {
	if name == "" {
		return fmt.Errorf("o nome da sessão não pode ser vazio")
	}

	filePath := sm.getSessionPath(name)
	data, err := json.MarshalIndent(history, "", "  ")
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
func (sm *SessionManager) LoadSession(name string) ([]models.Message, error) {
	filePath := sm.getSessionPath(name)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("sessão '%s' não encontrada", name)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		sm.logger.Error("Erro ao ler o arquivo da sessão", zap.String("path", filePath), zap.Error(err))
		return nil, fmt.Errorf("erro ao ler o arquivo da sessão: %w", err)
	}

	var history []models.Message
	if err := json.Unmarshal(data, &history); err != nil {
		sm.logger.Error("Erro ao desserializar a sessão", zap.String("session", name), zap.Error(err))
		return nil, fmt.Errorf("arquivo de sessão corrompido: %w", err)
	}

	sm.logger.Info("Sessão carregada com sucesso", zap.String("session", name))
	return history, nil
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
