package llm

import (
	"testing"
)

func TestLLMClientInterface(t *testing.T) {
	var _ LLMClient = &MockLLMClient{}
}

func TestLLMError(t *testing.T) {
	err := &LLMError{Code: 500, Message: "Internal Error"}
	if err.Error() != "LLMError: 500 - Internal Error" {
		t.Errorf("Mensagem de erro inesperada: %s", err.Error())
	}
}
