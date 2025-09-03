package utils

import (
	"os"
	"os/user"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupShellTest Mocks das funções de OS para os testes de shell
func setupShellTest(t *testing.T, shell, historyContent string) {
	// Salva as funções originais
	originalGetenv := osGetenv
	originalUserCurrent := userCurrent
	originalReadFile := osReadFile
	originalStat := osStat

	// Adiciona cleanup para restaurar as funções originais após o teste
	t.Cleanup(func() {
		osGetenv = originalGetenv
		userCurrent = originalUserCurrent
		osReadFile = originalReadFile
		osStat = originalStat
	})

	// Mock os.Getenv
	osGetenv = func(key string) string {
		if key == "SHELL" {
			return "/bin/" + shell
		}
		return os.Getenv(key)
	}

	// Mock user.Current
	userCurrent = func() (*user.User, error) {
		return &user.User{HomeDir: "/home/testuser"}, nil
	}

	// Mock os.ReadFile
	osReadFile = func(name string) ([]byte, error) {
		return []byte(historyContent), nil
	}

	// Mock os.Stat
	osStat = func(name string) (os.FileInfo, error) {
		return nil, nil // Retorna sucesso, indicando que o arquivo existe
	}
}

func TestGetShellHistory(t *testing.T) {
	testCases := []struct {
		name            string
		shell           string
		historyContent  string
		expectedContent string
		expectError     bool
	}{
		{
			name:            "Bash History",
			shell:           "bash",
			historyContent:  "ls -la\ngit status",
			expectedContent: "ls -la\ngit status",
			expectError:     false,
		},
		{
			name:            "Zsh History",
			shell:           "zsh",
			historyContent:  ": 1663200000:0;ls -la\n: 1663200001:0;git status",
			expectedContent: "ls -la\ngit status",
			expectError:     false,
		},
		{
			name:            "Fish History",
			shell:           "fish",
			historyContent:  "- cmd: ls -la\n- cmd: git status",
			expectedContent: "- cmd: ls -la\n- cmd: git status", // fish não tem processamento especial
			expectError:     false,
		},
		{
			name:        "Unsupported Shell",
			shell:       "csh",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setupShellTest(t, tc.shell, tc.historyContent)

			content, err := GetShellHistory()

			if tc.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedContent, content)
			}
		})
	}
}

func TestGetShellHistory_FileNotExist(t *testing.T) {
	setupShellTest(t, "bash", "")

	// Sobrescreve o mock de os.Stat para simular arquivo não encontrado
	originalStat := osStat
	osStat = func(name string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { osStat = originalStat })

	_, err := GetShellHistory()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "arquivo de histórico não encontrado")
}
