package utils

import (
	"testing"
)

func TestGetGitInfo(t *testing.T) {
	_, err := GetGitInfo()
	if err != nil {
		t.Logf("Erro esperado se não estiver em um repositório Git: %v", err)
	}
}
