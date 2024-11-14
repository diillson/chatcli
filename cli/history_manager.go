package cli

import (
	"bufio"
	"fmt"
	"go.uber.org/zap"
	"os"
)

type HistoryManager struct {
	historyFile    string
	commandHistory []string
	logger         *zap.Logger
}

func NewHistoryManager(logger *zap.Logger) *HistoryManager {
	return &HistoryManager{
		historyFile: ".chatcli_history",
		logger:      logger,
	}
}

func (hm *HistoryManager) LoadHistory() ([]string, error) {
	f, err := os.Open(hm.historyFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Nenhum histórico para carregar
		}
		hm.logger.Warn("Não foi possível carregar o histórico:", zap.Error(err))
		return nil, err
	}
	defer f.Close()

	var history []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		history = append(history, line)
	}

	if err := scanner.Err(); err != nil {
		hm.logger.Warn("Erro ao ler o histórico:", zap.Error(err))
		return nil, err
	}

	return history, nil
}

func (hm *HistoryManager) SaveHistory(commandHistory []string) error {
	f, err := os.OpenFile(hm.historyFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		hm.logger.Warn("Não foi possível salvar o histórico:", zap.Error(err))
		return err
	}
	defer f.Close()

	for _, cmd := range commandHistory {
		fmt.Fprintln(f, cmd)
	}

	return nil
}
