package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

// MockLLMClient para simular respostas da IA
type MockLLMClient struct {
	mock.Mock
}

func (m *MockLLMClient) GetModelName() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockLLMClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	args := m.Called(ctx, prompt, history, maxTokens)
	return args.String(0), args.Error(1)
}

// Helper para redirecionar Stdin durante o teste
func withStdin(t *testing.T, input string, f func()) {
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()

	r, w, err := os.Pipe()
	assert.NoError(t, err)

	os.Stdin = r

	go func() {
		defer w.Close()
		_, err := io.WriteString(w, input)
		assert.NoError(t, err)
	}()

	f()
}

func TestAgentMode_Run_ExtractsAndHandlesCommands(t *testing.T) {
	// Inicializar i18n para evitar panic nas mensagens
	i18n.Init()

	logger := zap.NewNop() // Usar Nop logger para não sujar o output do teste
	mockLLM := new(MockLLMClient)

	chatCLI := &ChatCLI{
		Client:    mockLLM,
		logger:    logger,
		animation: NewAnimationManager(),
		history:   []models.Message{},
		// PluginManager é nil aqui, o código deve lidar com isso graciosamente
	}
	agentMode := NewAgentMode(chatCLI, logger)

	// Resposta da IA simulando um bloco de código shell (fallback legacy)
	// Isso deve acionar o menu interativo
	aiResponse := "Vou listar os arquivos.\n" +
		"```execute:shell\n" +
		"ls -la\n" +
		"```"

	mockLLM.On("GetModelName").Return("MockGPT")
	// Configurar o mock para retornar a resposta definida
	mockLLM.On("SendPrompt",
		mock.Anything,
		mock.AnythingOfType("string"),
		mock.AnythingOfType("[]models.Message"),
		mock.AnythingOfType("int"),
	).Return(aiResponse, nil)

	// Capturar o bloco executado para asserção
	var executedBlock agent.CommandBlock

	// Mockar a função de execução interna para não rodar comandos reais no sistema
	agentMode.executeCommandsFunc = func(ctx context.Context, block agent.CommandBlock) (string, string) {
		executedBlock = block
		return "total 0", "" // Simula saída do ls
	}

	// Simular input do usuário no menu interativo:
	// "1" -> Escolhe executar o comando 1
	// "q" -> Sai do menu (necessário pois o menu entra em loop)
	userInput := "1\nq\n"

	withStdin(t, userInput, func() {
		// CORREÇÃO AQUI: Atualizada a assinatura para incluir o 4º argumento (systemPromptOverride)
		// Passamos "" para usar o prompt padrão
		err := agentMode.Run(context.Background(), "list files", "", "")
		assert.NoError(t, err)
	})

	mockLLM.AssertExpectations(t)

	// Verificar se o bloco foi capturado corretamente
	assert.NotNil(t, executedBlock, "O bloco de comando deveria ter sido executado")
	if len(executedBlock.Commands) > 0 {
		assert.Equal(t, "ls -la", strings.TrimSpace(executedBlock.Commands[0]))
		assert.Equal(t, "shell", executedBlock.Language)
	} else {
		t.Error("Nenhum comando foi extraído do bloco")
	}
}
