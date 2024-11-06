// utils/path.go
package utils

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileContent lê o conteúdo de um arquivo, expandindo ~ para o diretório home.
// O limite de tamanho do arquivo pode ser configurado via o parâmetro maxSize (em bytes).
// Retorna o conteúdo do arquivo como string e um erro, se houver.
func ReadFileContent(filePath string, maxSize int64) (string, error) {
	// Definir um limite de tamanho padrão (1MB) se maxSize não for especificado
	if maxSize == 0 {
		maxSize = 1 * 1024 * 1024 // 1MB
	}

	// Expandir ~ para o diretório home
	expandedPath, err := ExpandPath(filePath)
	if err != nil {
		return "", err
	}

	// Tornar o caminho absoluto
	absPath, err := filepath.Abs(expandedPath)
	if err != nil {
		return "", fmt.Errorf("não foi possível determinar o caminho absoluto: %w", err)
	}

	// Verificar se o arquivo existe
	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("o arquivo não existe: %s", absPath)
	}
	if err != nil {
		return "", fmt.Errorf("erro ao acessar o arquivo: %w", err)
	}

	// Verificar se é um arquivo regular
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("o caminho não aponta para um arquivo regular: %s", absPath)
	}

	// Verificar o tamanho do arquivo
	if info.Size() > maxSize {
		return "", fmt.Errorf("o arquivo é muito grande (limite de %d bytes)", maxSize)
	}

	// Ler o conteúdo do arquivo
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("erro ao ler o arquivo: %w", err)
	}

	content := string(data)

	// Opcional: Tratar diferentes codificações ou formatos, se necessário
	// Por exemplo, remover caracteres nulos:
	content = strings.ReplaceAll(content, "\x00", "")

	return content, nil
}

// IsTemporaryError verifica se o erro é temporário e pode ser retryado.
func IsTemporaryError(err error) bool {
	if ne, ok := err.(net.Error); ok {
		return ne.Temporary() || ne.Timeout()
	}
	return false
}

// ExpandPath expande o caractere ~ no início de um caminho para o diretório home do usuário.
// Se o caminho não começar com ~, ele é retornado sem modificações.
// A função não suporta a expansão de ~username, retornando um erro nesse caso.
func ExpandPath(path string) (string, error) {
	// Verifica se o caminho começa com ~
	if strings.HasPrefix(path, "~") {
		// Obtém o diretório home do usuário
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("não foi possível obter o diretório home: %w", err)
		}

		// Se o caminho for apenas ~, retorna o diretório home
		if len(path) == 1 {
			return home, nil
		}

		// Verifica se o segundo caractere é um separador de diretório
		if path[1] == filepath.Separator {
			// Constrói o caminho completo a partir do diretório home
			path = filepath.Join(home, path[2:])
		} else {
			// Expansão de ~username não é suportada
			return "", fmt.Errorf("expansão de ~username não é suportada, apenas ~ para o diretório home do usuário atual")
		}
	}

	// Retorna o caminho original se não começar com ~
	return path, nil
}
