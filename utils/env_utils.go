package utils

import (
	"fmt"
	"go.uber.org/zap"
	"os"
	"strings"
)

// GetEnv retorna o valor de uma variável de ambiente ou um valor padrão se não estiver definida
// além disso, retorna um booleano indicando se o valor padrão foi usado
func GetEnv(key, defaultValue string, logger *zap.Logger) (string, bool) {
	value := os.Getenv(key)
	if value == "" {
		logger.Info(fmt.Sprintf("%s não definido, assumindo default: %s", key, defaultValue))
		return defaultValue, true // true indica que o valor padrão foi usado
	}
	return value, false // false indica que o valor foi obtido da variável de ambiente
}

// GetEnvVariables retorna todas as variáveis de ambiente como uma string formatada.
func GetEnvVariables() string {
	envVars := os.Environ()
	return strings.Join(envVars, "\n")
}
