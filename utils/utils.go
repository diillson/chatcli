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
	"strconv"
	"strings"

	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
)

// GetEnvOrDefault retorna o valor da variável de ambiente ou um valor padrão se não estiver definida
func GetEnvOrDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
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
		"KEY", "TOKEN", "SECRET", "PASSWORD", "API_KEY", "ACCESS_TOKEN", "REFRESH_TOKEN", "CLIENT_SECRET", "CLIENT_KEY", "AUTH",
	}
	for _, s := range sensitiveSubstr {
		if strings.Contains(k, s) {
			return true
		}
	}

	// nomes exatos conhecidos
	exact := map[string]bool{
		"OPENAI_API_KEY":    true,
		"ANTHROPIC_API_KEY": true,
		"GOOGLEAI_API_KEY":  true,
		"CLIENT_SECRET":     true,
		"CLIENT_KEY":        true,
	}
	return exact[k]
}

// SanitizeSensitiveText remove/mascara tokens em qualquer texto antes de ir para histórico/LLM
func SanitizeSensitiveText(s string) string {
	return maskSensitiveInText(s)
}

// maskSensitiveInText aplica regex para esconder padrões comuns de segredos

func maskSensitiveInText(s string) string {
	patterns := []struct {
		re   *regexp.Regexp
		repl string
	}{
		// Chaves de API comuns (OpenAI, Anthropic, Stripe, etc.)
		{regexp.MustCompile(`(?i)(sk|pk)_(test|live)_[a-zA-Z0-9]{20,}`), "[REDACTED_API_KEY]"},
		{regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`), "sk-[REDACTED]"},
		{regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{20,}`), "sk-ant-[REDACTED]"},
		// Google AI
		{regexp.MustCompile(`AIza[0-9A-Za-z\-_]{30,}`), "[REDACTED_GOOGLE_API_KEY]"},
		// Bearer tokens e JWTs (parte do meio)
		{regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\.\-_]+`), "Bearer [REDACTED]"},
		{regexp.MustCompile(`ey[A-Za-z0-9-_=]+\.ey[A-Za-z0-9-_=]+\.[A-Za-z0-9-_.+/=]+`), "[REDACTED_JWT]"},
		// Campos em JSON
		{regexp.MustCompile(`("access_token"|"refresh_token"|"client_secret"|"client_key"|"api_key"|"password"|"token")\s*:\s*"[^"]+"`), `${1}:"[REDACTED]"`},
		// Formato KEY=VALUE
		{regexp.MustCompile(`(?im)^(API_KEY|ACCESS_TOKEN|CLIENT_SECRET|CLIENT_KEY|SECRET|PASSWORD)\s*=\s*.*$`), "$1=[REDACTED]"},
	}
	out := s
	for _, p := range patterns {
		out = p.re.ReplaceAllString(out, p.repl)
	}
	return out
}

// NewJSONReader cria um io.Reader a partir de um []byte para enviar em requisições HTTP
func NewJSONReader(data []byte) io.Reader {
	return bytes.NewReader(data)
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

// ParseSize converte uma string de tamanho legível (como "50MB") para bytes.
func ParseSize(sizeStr string) (int64, error) {
	sizeStr = strings.TrimSpace(strings.ToUpper(sizeStr))
	var multiplier int64 = 1

	// Extrai a unidade (KB, MB, GB)
	unit := ""
	if strings.HasSuffix(sizeStr, "KB") {
		unit = "KB"
		multiplier = 1024
	} else if strings.HasSuffix(sizeStr, "MB") {
		unit = "MB"
		multiplier = 1024 * 1024
	} else if strings.HasSuffix(sizeStr, "GB") {
		unit = "GB"
		multiplier = 1024 * 1024 * 1024
	}

	// Remove a unidade para obter o número
	if unit != "" {
		sizeStr = strings.TrimSuffix(sizeStr, unit)
	}

	// Converte o número
	size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tamanho inválido: %s", sizeStr)
	}

	return size * multiplier, nil
}
