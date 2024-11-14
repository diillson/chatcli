package cli

import (
	"bufio"
	"fmt"
	"go.uber.org/zap"
	"os"
	"strconv"
	"time"
)

const defaultMaxHistorySize = 50 * 1024 * 1024 // 50MB

type HistoryManager struct {
	historyFile    string
	commandHistory []string
	logger         *zap.Logger
	maxHistorySize int64
}

func NewHistoryManager(logger *zap.Logger) *HistoryManager {
	return &HistoryManager{
		historyFile:    ".chatcli_history",
		logger:         logger,
		maxHistorySize: getMaxHistorySizeFromEnv(),
	}
}

// getMaxHistorySizeFromEnv lê a variável de ambiente HISTORY_MAX_SIZE e retorna o valor em bytes.
// Se a variável não estiver definida ou for inválida, retorna o valor padrão (50MB).
func getMaxHistorySizeFromEnv() int64 {
	envValue := os.Getenv("HISTORY_MAX_SIZE")
	if envValue != "" {
		size, err := strconv.ParseInt(envValue, 10, 64)
		if err == nil && size > 0 {
			return size
		}
	}
	return defaultMaxHistorySize
}

// LoadHistory carrega o histórico do arquivo
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

// SaveHistory salva o histórico no arquivo e faz backup se o tamanho exceder o limite
func (hm *HistoryManager) SaveHistory(commandHistory []string) error {
	// Verificar o tamanho do arquivo de histórico
	fileInfo, err := os.Stat(hm.historyFile)
	if err == nil && fileInfo.Size() >= hm.maxHistorySize {
		// Fazer backup do arquivo de histórico
		backupFile := fmt.Sprintf("%s.bak-%d", hm.historyFile, time.Now().Unix())
		err := os.Rename(hm.historyFile, backupFile)
		if err != nil {
			hm.logger.Warn("Não foi possível fazer backup do histórico:", zap.Error(err))
			return err
		}
		hm.logger.Info("Backup do histórico criado:", zap.String("backupFile", backupFile))
	}

	// Abrir o arquivo de histórico para escrita
	f, err := os.OpenFile(hm.historyFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		hm.logger.Warn("Não foi possível salvar o histórico:", zap.Error(err))
		return err
	}
	defer f.Close()

	// Escrever o histórico no arquivo
	for _, cmd := range commandHistory {
		fmt.Fprintln(f, cmd)
	}

	return nil
}
