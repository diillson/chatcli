package utils

import (
	"bytes"
	"fmt"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/version"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/term"
	"io"
	"os"
	"runtime"
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

// CheckEnvVariables verifica as variáveis de ambiente necessárias e informa o usuário
func CheckEnvVariables(logger *zap.Logger) {
	// Verificar SLUG_NAME e TENANT_NAME
	slugName, slugIsDefault := CheckAndNotifyEnv("SLUG_NAME", config.DefaultSlugName, logger)
	tenantName, tenantIsDefault := CheckAndNotifyEnv("TENANT_NAME", config.DefaultTenantName, logger)
	if slugIsDefault || tenantIsDefault {
		fmt.Println("ATENÇÃO: Variáveis de ambiente não definidas, usando valores padrão:")
		if slugIsDefault {
			fmt.Printf("- SLUG_NAME não definido, usando valor padrão: %s\n", slugName)
		}
		if tenantIsDefault {
			fmt.Printf("- TENANT_NAME não definido, usando valor padrão: %s\n", tenantName)
		}
	}

	// Verificar variáveis de ambiente específicas dos provedores
	CheckProviderEnvVariables(logger)
}

// CheckProviderEnvVariables verifica e notifica sobre as variáveis de ambiente específicas dos provedores
func CheckProviderEnvVariables(logger *zap.Logger) {
	// Verificar STACKSPOT
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		fmt.Println("ATENÇÃO: Variáveis de ambiente necessárias para o provedor STACKSPOT não foram definidas:")
		if clientID == "" {
			fmt.Println("- CLIENT_ID (necessário para o provedor STACKSPOT)")
		}
		if clientSecret == "" {
			fmt.Println("- CLIENT_SECRET (necessário para o provedor STACKSPOT)")
		}
		fmt.Println("O provedor STACKSPOT não estará disponível.")
	}

	// Verificar OPENAI
	openAIKey := os.Getenv("OPENAI_API_KEY")
	openAIModel := os.Getenv("OPENAI_MODEL")
	if openAIKey == "" {
		fmt.Println("ATENÇÃO: OPENAI_API_KEY não definida, o provedor OPENAI não estará disponível.")
	}
	if openAIModel == "" {
		fmt.Printf("ATENÇÃO: OPENAI_MODEL não definido, usando valor padrão: %s\n", config.DefaultOpenAIModel)
	}

	// Verificar CLAUDEAI
	claudeAIKey := os.Getenv("CLAUDEAI_API_KEY")
	claudeAIModel := os.Getenv("CLAUDEAI_MODEL")
	if claudeAIKey == "" {
		fmt.Println("ATENÇÃO: CLAUDEAI_API_KEY não definida, o provedor CLAUDEAI não estará disponível.")
	}
	if claudeAIModel == "" {
		fmt.Printf("ATENÇÃO: CLAUDEAI_MODEL não definido, usando valor padrão: %s\n", config.DefaultClaudeAIModel)
	}
}

func LogStartupInfo(logger *zap.Logger) {
	logger.Info("ChatCLI iniciado",
		zap.String("version", version.Version),
		zap.String("commit", version.CommitHash),
		zap.String("buildDate", version.BuildDate),
		zap.String("goVersion", runtime.Version()),
		zap.String("os", runtime.GOOS),
		zap.String("arch", runtime.GOARCH),
	)
}
