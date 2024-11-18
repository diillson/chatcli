package cli

import (
	"os"
	"testing"

	"go.uber.org/zap"
)

func TestHistoryManager_LoadAndSaveHistory(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	hm := NewHistoryManager(logger)

	// Remover arquivo de histórico anterior
	os.Remove(hm.historyFile)

	commands := []string{"/help", "/exit"}
	err := hm.SaveHistory(commands)
	if err != nil {
		t.Fatalf("Erro ao salvar histórico: %v", err)
	}

	loadedCommands, err := hm.LoadHistory()
	if err != nil {
		t.Fatalf("Erro ao carregar histórico: %v", err)
	}

	if len(loadedCommands) != len(commands) {
		t.Errorf("Esperado %d comandos, obtido %d", len(commands), len(loadedCommands))
	}
}
