/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"fmt"
	"github.com/diillson/chatcli/config"
	"go.uber.org/zap"
	"os"
	"strconv"
	"strings"
	"time"
)

type HistoryManager struct {
	historyFile    string
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
// Agora aceita valores como "50MB", "100KB", "1GB", etc.
func getMaxHistorySizeFromEnv() int64 {
	envValue := os.Getenv("HISTORY_MAX_SIZE")
	if envValue != "" {
		size, err := parseSize(envValue)
		if err == nil && size > 0 {
			return size
		}
	}
	return config.DefaultMaxHistorySize
}

// parseSize converte uma string de tamanho legível (como "50MB", "100KB", "1GB") para bytes.
func parseSize(sizeStr string) (int64, error) {
	sizeStr = strings.TrimSpace(sizeStr)
	unit := "B" // Padrão para bytes
	var multiplier int64 = 1

	// Verificar se a string termina com uma unidade de medida
	if strings.HasSuffix(sizeStr, "KB") {
		unit = "KB"
		multiplier = 1024
	} else if strings.HasSuffix(sizeStr, "MB") {
		unit = "MB"
		multiplier = 1024 * 1024
	} else if strings.HasSuffix(sizeStr, "GB") {
		unit = "GB"
		multiplier = 1024 * 1024 * 1024
	}

	// Remover a unidade da string para obter apenas o número
	sizeStr = strings.TrimSuffix(sizeStr, unit)
	sizeStr = strings.TrimSpace(sizeStr)

	// Converter o número para int64
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tamanho inválido: %s", sizeStr)
	}

	return size * multiplier, nil
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
	defer func() { _ = f.Close() }()

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
	defer func() { _ = f.Close() }()

	// Escrever o histórico no arquivo
	for _, cmd := range commandHistory {
		_, _ = fmt.Fprintln(f, cmd)
	}

	return nil
}
