package cli

import (
	"testing"
)

func TestCommandHandler_HandleCommand(t *testing.T) {
	cli := &ChatCLI{}
	ch := NewCommandHandler(cli)

	exit := ch.HandleCommand("/exit")
	if !exit {
		t.Error("Esperado sair ao usar /exit")
	}

	exit = ch.HandleCommand("/unknown")
	if exit {
		t.Error("Não esperado sair ao usar comando desconhecido")
	}
}
