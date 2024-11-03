// utils/path.go
package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandPath expande o ~ no caminho do arquivo para o diretório home do usuário
func ExpandPath(path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("não foi possível obter o diretório home: %w", err)
		}
		// Suporta ~ ou ~/caminho
		if len(path) == 1 {
			return home, nil
		}
		if path[1] == '/' || path[1] == '\\' {
			path = filepath.Join(home, path[2:])
		} else {
			return "", fmt.Errorf("expansão de ~username não é suportada")
		}
	}
	return path, nil
}
