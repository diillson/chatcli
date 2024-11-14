package cli

import (
	"bufio"
	"fmt"
	"github.com/diillson/chatcli/models"
	"github.com/peterh/liner"
	"go.uber.org/zap"
	"os"
)

// HistoryManager gerencia o histórico de comandos
type HistoryManager struct {
	historyFile string
	history     []models.Message
	logger      *zap.Logger
}

// NewHistoryManager cria uma nova instância de HistoryManager
func NewHistoryManager(historyFile string, logger *zap.Logger) *HistoryManager {
	return &HistoryManager{
		historyFile: historyFile,
		history:     []models.Message{},
		logger:      logger,
	}
}

// LoadHistory carrega o histórico do arquivo
func (hm *HistoryManager) LoadHistory(line *liner.State) {
	f, err := os.Open(hm.historyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return // Nenhum histórico para carregar
		}
		hm.logger.Warn("Não foi possível carregar o histórico:", zap.Error(err))
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineText := scanner.Text()
		hm.history = append(hm.history, models.Message{Role: "user", Content: lineText})
		line.AppendHistory(lineText) // Corrigido para usar liner.State.AppendHistory
	}

	if err := scanner.Err(); err != nil {
		hm.logger.Warn("Erro ao ler o histórico:", zap.Error(err))
	}
}

// SaveHistory salva o histórico no arquivo
func (hm *HistoryManager) SaveHistory(commandHistory []string) {
	f, err := os.OpenFile(hm.historyFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		hm.logger.Warn("Não foi possível salvar o histórico:", zap.Error(err))
		return
	}
	defer f.Close()

	for _, cmd := range commandHistory {
		fmt.Fprintln(f, cmd)
	}
}

// AddMessage adiciona uma mensagem ao histórico
func (hm *HistoryManager) AddMessage(role, content string) {
	hm.history = append(hm.history, models.Message{Role: role, Content: content})
}

// GetHistory retorna o histórico de mensagens
func (hm *HistoryManager) GetHistory() []models.Message {
	return hm.history
}
