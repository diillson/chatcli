package utils

import (
	"os"
	"testing"
)

func TestReadFileContent(t *testing.T) {
	content, err := ReadFileContent("path.go", 0)
	if err != nil {
		t.Fatalf("Erro ao ler arquivo: %v", err)
	}
	if content == "" {
		t.Error("Conteúdo do arquivo está vazio")
	}
}

func TestExpandPath(t *testing.T) {
	homeDir, _ := os.UserHomeDir()
	path, err := ExpandPath("~/test")
	if err != nil {
		t.Fatalf("Erro ao expandir caminho: %v", err)
	}
	if path != homeDir+"/test" {
		t.Errorf("Caminho expandido incorretamente: %s", path)
	}
}
