package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Helper para criar uma estrutura de diretório de teste
func createTestDirStructure(t *testing.T, baseDir string) {
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, "src/components"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, "node_modules/some-lib"), 0755))

	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "main.go"), []byte("package main"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "src/components/button.js"), []byte("export default Button"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "README.md"), []byte("# Test"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "image.jpg"), []byte("data"), 0644))                                     // Deve ser ignorado pela extensão
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "yarn.lock"), []byte("{}"), 0644))                                       // Deve ser ignorado pelo padrão
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "node_modules/some-lib/index.js"), []byte("module.exports = {}"), 0644)) // Deve ser ignorado
}

func BenchmarkProcessDirectory(b *testing.B) {
	// 1. Setup do ambiente de teste
	tempDir, err := os.MkdirTemp("", "benchmark-processdir-*")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	createTestDirStructure(nil, tempDir)
	logger, _ := zap.NewDevelopment()
	options := DefaultDirectoryScanOptions(logger)

	// 2. Rodar o benchmark
	b.ResetTimer() // Inicia a contagem de tempo
	for i := 0; i < b.N; i++ {
		// A função a ser benchmarkada
		_, err := ProcessDirectory(tempDir, options)
		if err != nil {
			b.Fatalf("ProcessDirectory failed: %v", err)
		}
	}
}

func TestProcessDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-processdir-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	createTestDirStructure(t, tempDir)
	logger, _ := zap.NewDevelopment()
	options := DefaultDirectoryScanOptions(logger)

	files, err := ProcessDirectory(tempDir, options)
	require.NoError(t, err)

	// Deve encontrar 3 arquivos: main.go, button.js, README.md
	assert.Len(t, files, 3)

	foundPaths := make(map[string]bool)
	for _, f := range files {
		// Normalizar o path para a comparação
		relPath, err := filepath.Rel(tempDir, f.Path)
		require.NoError(t, err)
		foundPaths[relPath] = true
	}

	assert.True(t, foundPaths["main.go"])
	assert.True(t, foundPaths[filepath.Join("src/components/button.js")])
	assert.True(t, foundPaths["README.md"])
}

func TestProcessDirectory_WithLimits(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-limits-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Criar mais arquivos do que o limite
	for i := 0; i < 5; i++ {
		fileName := fmt.Sprintf("file%d.go", i)
		content := fmt.Sprintf("package main // file %d", i)
		require.NoError(t, os.WriteFile(filepath.Join(tempDir, fileName), []byte(content), 0644))
	}

	logger, _ := zap.NewDevelopment()
	options := DefaultDirectoryScanOptions(logger)

	t.Run("MaxFiles limit", func(t *testing.T) {
		options.MaxFilesToProcess = 3
		files, err := ProcessDirectory(tempDir, options)
		require.NoError(t, err)
		assert.Len(t, files, 3, "Should process only up to the max files limit")
	})

	t.Run("MaxTotalSize limit", func(t *testing.T) {
		options.MaxFilesToProcess = 100 // Reset file limit
		options.MaxTotalSize = 30       // Limite de 30 bytes
		files, err := ProcessDirectory(tempDir, options)
		require.NoError(t, err)

		var totalSize int64
		for _, f := range files {
			totalSize += int64(len(f.Content))
		}
		// O número exato de arquivos pode variar com a concorrência,
		// mas o tamanho total não deve exceder muito o limite.
		assert.LessOrEqual(t, totalSize, options.MaxTotalSize, "Total size should be less than or equal to the limit")
		assert.NotEmpty(t, files, "Should have processed at least one file")
	})
}

func TestReadFileContent_Errors(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-readfile-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	t.Run("File not exist", func(t *testing.T) {
		_, err := ReadFileContent(filepath.Join(tempDir, "nonexistent.txt"), 1024)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "o arquivo não existe")
	})

	t.Run("Path is a directory", func(t *testing.T) {
		_, err := ReadFileContent(tempDir, 1024)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "não aponta para um arquivo regular")
	})

	t.Run("File too large", func(t *testing.T) {
		largeFilePath := filepath.Join(tempDir, "large.txt")
		content := "this is a large file content"
		require.NoError(t, os.WriteFile(largeFilePath, []byte(content), 0644))

		_, err := ReadFileContent(largeFilePath, 10) // Limite de 10 bytes
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "é muito grande")
	})
}
