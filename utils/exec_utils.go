package utils

import "os/exec"

// CommandExecutor define uma interface para executar comandos.
// Isso nos permite mockar a execução de comandos nos testes.
type CommandExecutor interface {
	Run(name string, arg ...string) error
	Output(name string, arg ...string) ([]byte, error)
}

// OSCommandExecutor é a implementação real que usa os/exec.
type OSCommandExecutor struct{}

// Run executa um comando e espera ele terminar.
func (e *OSCommandExecutor) Run(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	return cmd.Run()
}

// Output executa um comando e retorna sua saída padrão.
func (e *OSCommandExecutor) Output(name string, arg ...string) ([]byte, error) {
	cmd := exec.Command(name, arg...)
	return cmd.Output()
}

// NewOSCommandExecutor cria um novo executor de comandos do sistema operacional.
func NewOSCommandExecutor() CommandExecutor {
	return &OSCommandExecutor{}
}
