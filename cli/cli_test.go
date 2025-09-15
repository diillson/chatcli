package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/diillson/chatcli/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

// Mock para a função de checagem de versão
type mockVersionChecker struct {
	mock.Mock
}

func (m *mockVersionChecker) Check(ctx context.Context) (string, bool, error) {
	args := m.Called(ctx)
	return args.String(0), args.Bool(1), args.Error(2)
}

// stripANSI remove códigos ANSI de uma string
func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(s, "")
}

// normalizeSpaces remove espaços extras para asserções flexíveis
func normalizeSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestHandleVersionCommand(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	cliInstance := &ChatCLI{logger: logger} // Instância mínima para o handler
	handler := NewCommandHandler(cliInstance)

	// Salva as implementações originais
	originalCheckImpl := version.CheckLatestVersionImpl
	originalBuildImpl := version.GetBuildInfoImpl

	// Cria um mock para a checagem
	mockChecker := new(mockVersionChecker)

	// Temporariamente override as variáveis exportadas
	version.CheckLatestVersionImpl = func(ctx context.Context) (string, bool, error) {
		return mockChecker.Check(ctx)
	}
	version.GetBuildInfoImpl = func() (string, string, string) {
		// Retorna versão mockada para testes consistentes (não "dev")
		return "1.25.0", "abc1234", "2024-09-15"
	}
	defer func() {
		version.CheckLatestVersionImpl = originalCheckImpl
		version.GetBuildInfoImpl = originalBuildImpl
	}()

	testCases := []struct {
		name       string
		mockLatest string
		mockUpdate bool
		mockErr    error
		expectOut  string
	}{
		{"Update available", "1.26.0", true, nil, "Disponível! Atualize"},
		{"No update", "1.25.0", false, nil, "Você está na versão mais recente."},
		{"With error", "", false, errors.New("network error"), "Não foi possível verificar: network error"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Configura o mock para esta iteração
			mockChecker.On("Check", mock.Anything).Return(tc.mockLatest, tc.mockUpdate, tc.mockErr).Once()

			// Redireciona stdout para capturar saída
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w
			defer func() { os.Stdout = oldStdout }()

			handler.handleVersionCommand()

			w.Close()
			out, _ := io.ReadAll(r)

			// Limpa e normaliza a saída para asserts flexíveis
			cleanOut := stripANSI(string(out))
			normalized := normalizeSpaces(cleanOut)

			assert.Contains(t, normalized, tc.expectOut)
			mockChecker.AssertExpectations(t)
		})
	}
}
