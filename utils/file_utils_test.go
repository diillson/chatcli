package utils

import (
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
