/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/version"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/term"
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

// GetEnvVariablesSanitized retorna variáveis de ambiente com valores sensíveis redigidos.
func GetEnvVariablesSanitized() string {
	env := os.Environ()
	var b strings.Builder
	for _, kv := range env {
		// kv = "KEY=VALUE"
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := parts[0]
		v := parts[1]

		if isSensitiveEnvKey(k) {
			b.WriteString(k)
			b.WriteString("=[REDACTED]\n")
			continue
		}
		// também sanitiza valor por regex (fallback)
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(maskSensitiveInText(v))
		b.WriteString("\n")
	}
	return b.String()
}

func isSensitiveEnvKey(key string) bool {
	k := strings.ToUpper(key)
	// lista de padrões comuns
	sensitiveSubstr := []string{
		"KEY", "TOKEN", "SECRET", "PASSWORD", "API_KEY", "ACCESS_TOKEN", "REFRESH_TOKEN", "CLIENT_SECRET", "AUTH",
	}
	for _, s := range sensitiveSubstr {
		if strings.Contains(k, s) {
			return true
		}
	}

	// nomes exatos conhecidos
	exact := map[string]bool{
		"OPENAI_API_KEY":   true,
		"CLAUDEAI_API_KEY": true,
		"GOOGLEAI_API_KEY": true,
		"CLIENT_SECRET":    true,
	}
	return exact[k]
}

// SanitizeSensitiveText remove/mascara tokens em qualquer texto antes de ir para histórico/LLM
func SanitizeSensitiveText(s string) string {
	return maskSensitiveInText(s)
}

// maskSensitiveInText aplica regex para esconder padrões comuns de segredos

func maskSensitiveInText(s string) string {
	// padrões comuns
	patterns := []struct {
		re   *regexp.Regexp
		repl string
	}{
		// OpenAI
		{regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`), "sk-[REDACTED]"},
		// Anthropic
		{regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{20,}`), "sk-ant-[REDACTED]"},
		// Google API key (padrão mais genérico para o teste)
		{regexp.MustCompile(`AIza[0-9A-Za-z\-_.]+`), "[REDACTED_API_KEY]"},
		// Bearer tokens
		{regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\.\-_]+`), "Bearer [REDACTED]"},
		// JSON/OAuth fields (captura a chave para preservar a estrutura do JSON)
		{regexp.MustCompile(`("access_token"\s*:\s*)"[^"]+"`), `${1}"[REDACTED]"`},
		{regexp.MustCompile(`("refresh_token"\s*:\s*)"[^"]+"`), `${1}"[REDACTED]"`},
		{regexp.MustCompile(`("client_secret"\s*:\s*)"[^"]+"`), `${1}"[REDACTED]"`},
		{regexp.MustCompile(`("api_key"\s*:\s*)"[^"]+"`), `${1}"[REDACTED]"`},
		{regexp.MustCompile(`("password"\s*:\s*)"[^"]+"`), `${1}"[REDACTED]"`},
		// KEY=VALUE linhas cruas
		{regexp.MustCompile(`(?im)^(API_KEY|ACCESS_TOKEN|REFRESH_TOKEN|CLIENT_SECRET|SECRET|PASSWORD)\s*=\s*.*$`), "$1=[REDACTED]"},
	}
	out := s
	for _, p := range patterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
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

	// Verificar GOOGLEAI
	googleAIKey := os.Getenv("GOOGLEAI_API_KEY")
	googleAIModel := os.Getenv("GOOGLEAI_MODEL")
	if googleAIKey == "" {
		fmt.Println("ATENÇÃO: GOOGLEAI_API_KEY não definida, o provedor GOOGLEAI não estará disponível.")
	}
	if googleAIModel == "" {
		fmt.Printf("ATENÇÃO: GOOGLEAI_MODEL não definido, usando valor padrão: %s\n", config.DefaultGoogleAIModel)
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
