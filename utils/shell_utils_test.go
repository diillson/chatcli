package utils

import (
	"testing"
)

func TestGetUserShell(t *testing.T) {
	shell := GetUserShell()
	if shell == "" {
		t.Error("Shell do usuário não encontrado")
	}
}

func TestGetShellHistory(t *testing.T) {
	_, err := GetShellHistory()
	if err != nil {
		t.Logf("Erro esperado se o shell não for suportado: %v", err)
	}
}
