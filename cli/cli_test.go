package cli

import (
	"context"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/token"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/peterh/liner"
	"go.uber.org/zap"
)

// MockLLMClient é um mock para LLMClient
type MockLLMClient struct {
	response string
	err      error
}

func (m *MockLLMClient) GetModelName() string {
	return "ModeloMock"
}

func (m *MockLLMClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	return m.response, m.err
}

// MockLLMManager é um mock para LLMManager
type MockLLMManager struct{}

func (m *MockLLMManager) GetClient(provider string, model string) (client.LLMClient, error) {
	return &MockLLMClient{response: "Resposta Mock"}, nil
}

func (m *MockLLMManager) GetAvailableProviders() []string {
	return []string{"MockProvider"}
}

func (m *MockLLMManager) GetTokenManager() (*token.TokenManager, bool) {
	return nil, false
}

// MockLiner é um mock que implementa a interface Liner
type MockLiner struct {
	inputs    []string
	index     int
	history   []string
	completer liner.Completer
}

func (m *MockLiner) Prompt(prompt string) (string, error) {
	if m.index >= len(m.inputs) {
		return "", io.EOF
	}
	input := m.inputs[m.index]
	m.index++
	return input, nil
}

func (m *MockLiner) Close() error {
	return nil
}

func (m *MockLiner) SetCtrlCAborts(aborts bool) {}

func (m *MockLiner) AppendHistory(item string) {
	m.history = append(m.history, item)
}

func (m *MockLiner) SetCompleter(completer liner.Completer) {
	m.completer = completer
}

func TestNewChatCLI(t *testing.T) {
	logger, _ := zap.NewProduction()
	manager := &MockLLMManager{}
	cli, err := NewChatCLI(manager, logger)
	if err != nil {
		t.Fatalf("Erro ao criar ChatCLI: %v", err)
	}
	if cli == nil {
		t.Fatal("ChatCLI é nil")
	}
}

func TestChatCLI_Start(t *testing.T) {
	// Configurar o ambiente
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, err := NewChatCLI(manager, logger)
	if err != nil {
		t.Fatalf("Erro ao criar ChatCLI: %v", err)
	}

	// Mock do Liner
	oldLine := cli.line
	defer func() { cli.line = oldLine }()
	cli.line = &MockLiner{inputs: []string{"/exit"}}

	// Iniciar o ChatCLI
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cli.Start(ctx)
}

func TestChatCLI_processSpecialCommands(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, _ := NewChatCLI(manager, logger)

	testCases := []struct {
		input         string
		expectedInput string
		expectContext bool
		description   string
	}{
		{"Teste @history", "Teste", true, "Comando @history no meio"},
		{"@history Teste", "Teste", true, "Comando @history no início"},
		{"Teste @history extra", "Teste extra", true, "Comando @history com texto extra"},
		{"Teste @History", "Teste", true, "Comando @History com maiúscula"},
		{"Teste sem comando", "Teste sem comando", false, "Sem comando especial"},
	}

	for _, tc := range testCases {
		userInput, additionalContext := cli.processSpecialCommands(tc.input)
		if (additionalContext != "") != tc.expectContext {
			t.Errorf("Falha no teste (%s): Esperado contexto adicional: %v, obtido: %v", tc.description, tc.expectContext, additionalContext != "")
		}
		if strings.Contains(strings.ToLower(userInput), "@history") {
			t.Errorf("Falha no teste (%s): Esperado que @history seja removido do input do usuário", tc.description)
		}
		if userInput != tc.expectedInput {
			t.Errorf("Falha no teste (%s): Esperado input '%s', obtido '%s'", tc.description, tc.expectedInput, userInput)
		}
	}
}

func TestChatCLI_renderMarkdown(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, _ := NewChatCLI(manager, logger)

	markdown := "# Título\nTexto **negrito**."
	rendered := cli.renderMarkdown(markdown)
	if rendered == markdown {
		t.Error("Esperado que o markdown seja renderizado")
	}
}

func TestChatCLI_typewriterEffect(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, _ := NewChatCLI(manager, logger)

	// Este teste simplesmente verifica se o método executa sem erro
	cli.typewriterEffect("Teste", 0)
}

func TestChatCLI_switchProvider(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, _ := NewChatCLI(manager, logger)

	// Mock do Liner para simular entrada do usuário
	oldLine := cli.line
	defer func() { cli.line = oldLine }()
	cli.line = &MockLiner{inputs: []string{"1"}}

	cli.switchProvider()
	if cli.provider != "MockProvider" {
		t.Errorf("Esperado provider 'MockProvider', obtido '%s'", cli.provider)
	}
}

func TestChatCLI_executeDirectCommand(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, _ := NewChatCLI(manager, logger)

	cli.executeDirectCommand("echo 'Hello'")
	if len(cli.history) == 0 {
		t.Error("Esperado que o histórico seja atualizado")
	}
}

func TestChatCLI_sendOutputToAI(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, _ := NewChatCLI(manager, logger)

	cli.sendOutputToAI("Saída", "Contexto")
	if len(cli.history) == 2 {
		t.Log("Histórico atualizado corretamente")
	} else {
		t.Error("Histórico não atualizado corretamente")
	}
}

func TestChatCLI_completer(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := &MockLLMManager{}
	cli, _ := NewChatCLI(manager, logger)

	completions := cli.completer("/e")
	if len(completions) == 0 {
		t.Error("Esperado sugestões para '/e'")
	}
}
