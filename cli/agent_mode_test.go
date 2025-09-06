package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- Mocks ---

type MockLLMClient struct {
	mock.Mock
}

func (m *MockLLMClient) GetModelName() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockLLMClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	args := m.Called(ctx, prompt, history)
	return args.String(0), args.Error(1)
}

// --- Testes Unitários de Lógica Interna ---

func TestExtractCommandBlocks(t *testing.T) {
	mockLLM := new(MockLLMClient)
	chatCLI := &ChatCLI{Client: mockLLM}
	agentMode := NewAgentMode(chatCLI, zap.NewNop())

	testCases := []struct {
		name             string
		response         string
		expectedBlocks   int
		expectedCommand  string
		expectedLanguage string
		expectedIsScript bool
	}{
		{
			name:             "Single Shell Command",
			response:         "Aqui está o comando:\n" + "```execute:shell\nls -la\n```",
			expectedBlocks:   1,
			expectedCommand:  "ls -la",
			expectedLanguage: "shell",
			expectedIsScript: false,
		},
		{
			name:             "Redirection is not script",
			response:         "Para criar:\n" + "```execute:shell\necho 'hello' > file.txt\n```",
			expectedBlocks:   1,
			expectedCommand:  "echo 'hello' > file.txt",
			expectedLanguage: "shell",
			expectedIsScript: false, // heurística atual não classifica redirecionamento como script
		},
		{
			name:           "No Command Block",
			response:       "Apenas uma resposta de texto.",
			expectedBlocks: 0,
		},
		{
			name:           "Multiple Command Blocks",
			response:       "Primeiro:\n```execute:shell\nls\n```\nDepois:\n```execute:shell\nls | wc -l\n```",
			expectedBlocks: 2,
		},
		{
			name:           "Malformed Block",
			response:       "```execute:shell\nls -la",
			expectedBlocks: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			blocks := agentMode.extractCommandBlocks(tc.response)
			assert.Len(t, blocks, tc.expectedBlocks)
			if tc.expectedBlocks > 0 && tc.name != "Multiple Command Blocks" {
				assert.Equal(t, tc.expectedLanguage, blocks[0].Language)
				assert.Equal(t, tc.expectedCommand, blocks[0].Commands[0])
				assert.Equal(t, tc.expectedIsScript, blocks[0].ContextInfo.IsScript)
			}
		})
	}
}

// --- Testes de Interação ---

func TestHandleCommandBlocks_ExecuteSingleAndDangerousFlow(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	chatCLI := &ChatCLI{logger: logger, animation: NewAnimationManager()}
	agentMode := NewAgentMode(chatCLI, logger)

	blocks := []CommandBlock{
		{Description: "List files", Commands: []string{"ls -la"}, Language: "shell"},
		{Description: "Dangerous", Commands: []string{"sudo rm -rf /"}, Language: "shell"},
	}

	// Helper: executa o loop com um stdin simulado e captura stdout
	runWithInput := func(t *testing.T, input string, setExec func()) (string, []string) {
		t.Helper()

		// Guarda e restaura stdio
		oldStdin := os.Stdin
		oldStdout := os.Stdout
		defer func() {
			os.Stdin = oldStdin
			os.Stdout = oldStdout
		}()

		// stdin simulado via pipe
		rIn, wIn, err := os.Pipe()
		require.NoError(t, err)
		os.Stdin = rIn

		go func() {
			defer wIn.Close()
			_, _ = io.WriteString(wIn, input)
		}()

		// capturar stdout
		rOut, wOut, err := os.Pipe()
		require.NoError(t, err)
		os.Stdout = wOut

		// configurar executor
		var executedCommands []string
		if setExec != nil {
			setExec()
		} else {
			// default: mock seguro que apenas coleta os comandos
			var mu sync.Mutex
			agentMode.executeCommandsFunc = func(ctx context.Context, block CommandBlock) (string, string) {
				mu.Lock()
				executedCommands = append(executedCommands, block.Commands...)
				mu.Unlock()
				return "output", ""
			}
		}

		agentMode.handleCommandBlocks(context.Background(), blocks)

		_ = wOut.Close()
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, rOut)

		return buf.String(), executedCommands
	}

	t.Run("Execute Single Command (1)", func(t *testing.T) {
		out, executed := runWithInput(t, "1\nq\n", nil)
		_ = out
		assert.Equal(t, []string{"ls -la"}, executed)
	})

	t.Run("Dangerous Command - Abort", func(t *testing.T) {
		// Use o executor real para acionar a checagem de comando perigoso
		setExec := func() {
			agentMode.executeCommandsFunc = agentMode.executeCommandsWithOutput
		}
		out, executed := runWithInput(t, "2\nnao\nq\n", setExec)

		// Deve avisar e abortar a execução perigosa
		assert.Contains(t, out, "potencialmente perigoso")
		assert.Contains(t, out, "ABORTADA")
		assert.Empty(t, executed)
	})
}

// --- Teste do modo One-Shot ---

func TestAgentMode_RunOnce(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	mockLLM := new(MockLLMClient)
	chatCLI := &ChatCLI{
		Client:    mockLLM,
		logger:    logger,
		animation: NewAnimationManager(),
		history:   []models.Message{},
	}
	agentMode := NewAgentMode(chatCLI, logger)

	aiResponseSafe := "```execute:shell\ntouch test.txt\n```"
	aiResponseDangerous := "```execute:shell\nrm -rf /tmp\n```"

	// Permitir GetModelName múltiplas vezes (render/animation)
	mockLLM.On("GetModelName").Return("MockGPT").Maybe()

	// Helper p/ capturar stdout de RunOnce
	runOnceCapture := func(t *testing.T, query string, autoExec bool, prepare func()) string {
		t.Helper()
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		if prepare != nil {
			prepare()
		}

		err := agentMode.RunOnce(context.Background(), query, autoExec)
		_ = w.Close()
		os.Stdout = oldStdout

		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)

		require.NoError(t, err)
		return buf.String()
	}

	t.Run("Dry Run (default)", func(t *testing.T) {
		mockLLM.On("SendPrompt", mock.Anything, mock.Anything, mock.Anything).Return(aiResponseSafe, nil).Once()
		out := runOnceCapture(t, "create a file", false, nil)
		assert.Contains(t, out, "Comando Sugerido")
		mockLLM.AssertExpectations(t)
	})

	t.Run("Auto Execute Safe Command", func(t *testing.T) {
		mockLLM.On("SendPrompt", mock.Anything, mock.Anything, mock.Anything).Return(aiResponseSafe, nil).Once()
		out := runOnceCapture(t, "create a file", true, nil)
		assert.Contains(t, out, "Execução Automática")
		assert.Contains(t, out, "touch test.txt")
		assert.Contains(t, out, "Executado com sucesso")
		mockLLM.AssertExpectations(t)
	})

	t.Run("Auto Execute Blocks Dangerous Command", func(t *testing.T) {
		mockLLM.On("SendPrompt", mock.Anything, mock.Anything, mock.Anything).Return(aiResponseDangerous, nil).Once()

		// Esta execução retorna erro — capturamos diretamente
		err := agentMode.RunOnce(context.Background(), "delete tmp folder", true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "potencialmente perigoso")
		mockLLM.AssertExpectations(t)
	})
}
