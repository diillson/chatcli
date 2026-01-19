/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

type HistoryManager struct {
	historyFile    string
	logger         *zap.Logger
	maxHistorySize int64
}

func NewHistoryManager(logger *zap.Logger) *HistoryManager {
	rawPath := utils.GetEnvOrDefault("HISTORY_FILE", config.DefaultHistoryFile)

	finalPath, err := utils.ExpandPath(rawPath)
	if err != nil {
		logger.Warn("Falha ao expandir caminho do arquivo de historico",
			zap.String("path", rawPath),
			zap.Error(err))
		finalPath = rawPath
	}

	return &HistoryManager{
		historyFile:    finalPath,
		logger:         logger,
		maxHistorySize: getMaxHistorySizeFromEnv(),
	}
}

// GetHistoryFilePath retorna o caminho atual do arquivo de histórico
func (hm *HistoryManager) GetHistoryFilePath() string {
	return hm.historyFile
}

// getMaxHistorySizeFromEnv lê a variável de ambiente HISTORY_MAX_SIZE e retorna o valor em bytes.
// Agora aceita valores como "50MB", "100KB", "1GB", etc.
func getMaxHistorySizeFromEnv() int64 {
	envValue := os.Getenv("HISTORY_MAX_SIZE")
	if envValue != "" {
		// Agora chama a função centralizada
		size, err := utils.ParseSize(envValue)
		if err == nil && size > 0 {
			return size
		}
	}
	return config.DefaultMaxHistorySize
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

// AppendAndRotateHistory salva o histórico no arquivo e faz backup se o tamanho exceder o limite
func (hm *HistoryManager) AppendAndRotateHistory(newCommands []string) error {
	// 1. Anexar os novos comandos ao arquivo de histórico
	f, err := os.OpenFile(hm.historyFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		hm.logger.Warn("Não foi possível abrir o histórico para anexar comandos", zap.Error(err))
		return err
	}

	writer := bufio.NewWriter(f)
	for _, cmd := range newCommands {
		if _, err := fmt.Fprintln(writer, cmd); err != nil {
			// Ignorar erro de escrita individual, mas logar
			hm.logger.Warn("Erro ao escrever comando no histórico", zap.String("command", cmd), zap.Error(err))
		}
	}
	if err := writer.Flush(); err != nil {
		_ = f.Close()
		hm.logger.Error("Erro ao fazer flush no arquivo de histórico", zap.Error(err))
		return err
	}
	_ = f.Close()

	// 2. Verificar o tamanho do arquivo após anexar
	fileInfo, err := os.Stat(hm.historyFile)
	if err != nil {
		// Se não conseguirmos obter o status, não podemos rotacionar.
		hm.logger.Warn("Não foi possível obter o status do arquivo de histórico para rotação", zap.Error(err))
		return nil
	}

	if fileInfo.Size() < hm.maxHistorySize {
		// O arquivo está dentro do limite, trabalho concluído.
		return nil
	}

	// 3. O arquivo excedeu o limite, realizar a rotação e truncamento.
	hm.logger.Info("Arquivo de histórico excedeu o tamanho máximo, iniciando rotação.",
		zap.Int64("size", fileInfo.Size()),
		zap.Int64("max_size", hm.maxHistorySize),
	)

	// Criar backup
	backupFile := fmt.Sprintf("%s.bak-%d", hm.historyFile, time.Now().Unix())
	if err := os.Rename(hm.historyFile, backupFile); err != nil {
		hm.logger.Error("Falha ao criar backup do histórico", zap.Error(err))
		return err
	}
	hm.logger.Info("Backup do histórico criado", zap.String("backupFile", backupFile))

	// 4. Truncar o histórico: ler as últimas N linhas do backup e escrevê-las no novo arquivo
	linesToKeep := 5000 // Manter as últimas 5000 linhas como um bom ponto de partida

	// Ler todas as linhas do backup
	backupData, err := os.ReadFile(backupFile)
	if err != nil {
		hm.logger.Error("Falha ao ler o arquivo de backup para truncamento", zap.Error(err))
		// Recria um arquivo de histórico vazio para não perder o funcionamento
		return os.WriteFile(hm.historyFile, []byte{}, 0644)
	}

	lines := strings.Split(string(backupData), "\n")

	// Pegar as últimas `linesToKeep` linhas
	startIndex := 0
	if len(lines) > linesToKeep {
		startIndex = len(lines) - linesToKeep
	}

	recentHistory := lines[startIndex:]

	// Escrever as linhas recentes de volta no arquivo de histórico principal (agora vazio)
	return os.WriteFile(hm.historyFile, []byte(strings.Join(recentHistory, "\n")), 0644)
}
