/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadFileContent lê o conteúdo de um arquivo, expandindo ~ para o diretório home,
// mostrando um indicador de progresso para arquivos grandes.
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
		return "", fmt.Errorf("o arquivo '%s' é muito grande (%.2f MB, limite de %.2f MB)",
			absPath, float64(info.Size())/1024/1024, float64(maxSize)/1024/1024)
	}

	// Para arquivos grandes, mostrar um indicador de progresso
	showProgress := info.Size() > 1024*1024 // Maior que 1MB

	var content string

	if showProgress {
		// Ler o conteúdo com indicador de progresso
		file, err := os.Open(absPath)
		if err != nil {
			return "", fmt.Errorf("erro ao abrir o arquivo: %w", err)
		}
		defer func() { _ = file.Close() }()

		var data strings.Builder
		buffer := make([]byte, 8192) // 8KB por vez
		totalRead := int64(0)

		for {
			n, err := file.Read(buffer)
			if err != nil && err != io.EOF {
				return "", fmt.Errorf("erro ao ler o arquivo: %w", err)
			}

			if n == 0 {
				break
			}

			data.Write(buffer[:n])
			totalRead += int64(n)

			// Atualizar progresso a cada 10%
			if totalRead%(info.Size()/10) < 8192 {
				percentComplete := int(float64(totalRead) / float64(info.Size()) * 100)
				fmt.Printf("\rLendo %s... %d%% completo", filepath.Base(absPath), percentComplete)
			}
		}

		fmt.Printf("\r\033[K") // Limpar a linha de progresso
		content = data.String()
	} else {
		// Ler normalmente para arquivos pequenos
		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", fmt.Errorf("erro ao ler o arquivo: %w", err)
		}
		content = string(data)
	}

	// Opcional: Tratar diferentes codificações ou formatos, se necessário
	// Por exemplo, remover caracteres nulos:
	content = strings.ReplaceAll(content, "\x00", "")

	return content, nil
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

		// Verifica se o segundo caractere é um separador de diretório.
		// Accept both '/' and '\' so that paths like "~/.chatcli" work on Windows
		// where filepath.Separator is '\'.
		if path[1] == '/' || path[1] == filepath.Separator {
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
