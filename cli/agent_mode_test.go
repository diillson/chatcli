package cli

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/diillson/chatcli/cli/agent" // Importar o novo pacote
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"

	"github.com/diillson/chatcli/models"
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
	logger, _ := zap.NewDevelopment()
	mockLLM := new(MockLLMClient)

	chatCLI := &ChatCLI{
		Client:    mockLLM,
		logger:    logger,
		animation: NewAnimationManager(),
		history:   []models.Message{},
	}
	agentMode := NewAgentMode(chatCLI, logger)

	aiResponse := `
                ` + "```" + `execute:shell
                ls -la
                ` + "```" + `
                `

	mockLLM.On("GetModelName").Return("MockGPT")
	mockLLM.On("SendPrompt", mock.Anything, mock.AnythingOfType("string"), mock.AnythingOfType("[]models.Message"), mock.AnythingOfType("int")).Return(aiResponse, nil)

	var executedBlock agent.CommandBlock
	agentMode.executeCommandsFunc = func(ctx context.Context, block agent.CommandBlock) (string, string) {
		executedBlock = block
		return "total 0", ""
	}

	userInput := "1\nq\n"
	withStdin(t, userInput, func() {
		err := agentMode.Run(context.Background(), "list files", "")
		assert.NoError(t, err)
	})

	mockLLM.AssertExpectations(t)
	assert.NotNil(t, executedBlock)
	assert.Len(t, executedBlock.Commands, 1)
	assert.Equal(t, "ls -la", executedBlock.Commands[0])
}
