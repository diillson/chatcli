package utils

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockCommandExecutor é o nosso mock para a interface CommandExecutor.
type MockCommandExecutor struct {
	mock.Mock
}

func (m *MockCommandExecutor) Run(name string, arg ...string) error {
	args := m.Called(name, arg)
	return args.Error(0)
}

func (m *MockCommandExecutor) Output(name string, arg ...string) ([]byte, error) {
	args := m.Called(name, arg)
	// Lida com o caso de a saída ser nil
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func TestGetGitInfo_InRepo(t *testing.T) {
	mockExec := new(MockCommandExecutor)

	// Configura o mock para simular sucesso em todos os comandos
	mockExec.On("Run", "git", []string{"rev-parse", "--is-inside-work-tree"}).Return(nil)
	mockExec.On("Output", "git", []string{"remote", "-v"}).Return([]byte("origin git@github.com:user/repo.git (fetch)"), nil)
	mockExec.On("Output", "git", []string{"branch", "--show-current"}).Return([]byte("main"), nil)
	mockExec.On("Output", "git", []string{"status", "-s", "-b"}).Return([]byte("## main...origin/main\nM  README.md"), nil)
	// Configure os outros comandos para retornar saídas vazias para simplificar
	mockExec.On("Output", "git", mock.Anything).Return([]byte(""), nil)

	info, err := GetGitInfo(mockExec)

	assert.NoError(t, err)
	assert.Contains(t, info, "Branch Atual: main")
	assert.Contains(t, info, "Status Resumido:\n## main...origin/main\nM  README.md")

	mockExec.AssertExpectations(t)
}

func TestGetGitInfo_NotInRepo(t *testing.T) {
	mockExec := new(MockCommandExecutor)

	// Configura o mock para falhar APENAS na verificação inicial
	expectedErr := errors.New("exit status 1")
	mockExec.On("Run", "git", []string{"rev-parse", "--is-inside-work-tree"}).Return(expectedErr)

	_, err := GetGitInfo(mockExec)

	assert.Error(t, err)
	assert.Equal(t, "não é um repositório Git", err.Error())

	// Garante que nenhum outro comando foi chamado
	mockExec.AssertExpectations(t)
	mockExec.AssertNumberOfCalls(t, "Run", 1)
	mockExec.AssertNotCalled(t, "Output")
}
