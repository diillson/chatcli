package main

import (
	"testing"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func TestMainFunction(t *testing.T) {
	// Como a função main chama os outros componentes, podemos testar se não há erros na inicialização
	// Para um teste mais aprofundado, os componentes individuais devem ser testados separadamente
	logger, _ := zap.NewDevelopment()

	utils.CheckEnvVariables(logger, "slug", "tenant")
}
