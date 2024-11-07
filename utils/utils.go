package utils

import (
	"bytes"
	"fmt"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/term"
	"io"
	"os"
	"strings"
)

// GetEnvOrDefault retorna o valor da variável de ambiente ou um valor padrão se não estiver definida
func GetEnvOrDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// CheckAndNotifyEnv verifica se uma variável de ambiente está definida, registra se o padrão foi usado e retorna o valor
func CheckAndNotifyEnv(key, defaultValue string, logger *zap.Logger) (string, bool) {
	value := os.Getenv(key)
	if value == "" {
		logger.Info(fmt.Sprintf("%s não definido, usando valor padrão: %s", key, defaultValue))
		return defaultValue, true
	}
	return value, false
}

// GetEnvVariables retorna todas as variáveis de ambiente como uma string formatada.
func GetEnvVariables() string {
	envVars := os.Environ()
	return strings.Join(envVars, "\n")
}

// GenerateUUID gera um UUID (Universally Unique Identifier)
func GenerateUUID() string {
	return uuid.New().String()
}

// NewJSONReader cria um io.Reader a partir de um []byte para enviar em requisições HTTP
func NewJSONReader(data []byte) io.Reader {
	return bytes.NewReader(data)
}

// GetTerminalSize retorna a largura e a altura do terminal.
func GetTerminalSize() (width int, height int, err error) {
	return term.GetSize(int(os.Stdout.Fd()))
}
