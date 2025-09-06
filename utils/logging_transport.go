/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// InitializeLogger configura e inicializa um logger robusto com base nas variáveis de ambiente.
// Em ambiente de desenvolvimento ('dev'), os logs são enviados para o console de forma colorida e legível,
// e também para um arquivo em formato JSON.
// Em ambiente de produção ('prod'), os logs são enviados apenas para o arquivo em formato JSON.
// Suporta rotação de logs com lumberjack.
func InitializeLogger() (*zap.Logger, error) {
	// 1. Definir o nível de log via variável de ambiente (padrão: Info)
	logLevelEnv := strings.ToLower(os.Getenv("LOG_LEVEL"))
	var level zapcore.Level
	if err := level.Set(logLevelEnv); err != nil {
		level = zap.InfoLevel // Fallback para InfoLevel se a string for inválida
	}

	// 2. Ler configurações de arquivo e rotação do ambiente
	logFile := GetEnvOrDefault("LOG_FILE", config.DefaultLogFile)
	if expanded, err := ExpandPath(logFile); err == nil {
		logFile = expanded
	} else {
		// Logar um aviso se a expansão do caminho falhar, mas continuar com o caminho original.
		fmt.Printf("Aviso: não foi possível expandir o caminho do log '%s': %v\n", logFile, err)
	}

	maxSizeMB := config.DefaultMaxLogSize
	if envValue := os.Getenv("LOG_MAX_SIZE"); envValue != "" {
		// Usa a função ParseSize centralizada que retorna bytes
		sizeBytes, err := ParseSize(envValue)
		if err == nil && sizeBytes > 0 {
			// lumberjack.MaxSize é em megabytes (MB)
			maxSizeMB = int(sizeBytes / (1024 * 1024))
		}
	}

	// 3. Configurar lumberjack para rotação de logs
	lumberjackLogger := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    maxSizeMB, // em MB
		MaxBackups: 3,
		MaxAge:     28, // em dias
		Compress:   true,
	}
	fileSyncer := zapcore.AddSync(lumberjackLogger)

	// 4. Configurar o core do logger com base no ambiente (ENV)
	env := strings.ToLower(GetEnvOrDefault("ENV", "dev"))
	var core zapcore.Core

	if env == "prod" {
		// --- Configuração para Produção: JSON para o arquivo ---
		encoderConfig := zap.NewProductionEncoderConfig()
		encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		jsonEncoder := zapcore.NewJSONEncoder(encoderConfig)

		core = zapcore.NewCore(jsonEncoder, fileSyncer, level)
	} else {
		// --- Configuração para Desenvolvimento: Console colorido + JSON no arquivo ---

		// Encoder para o console (legível e colorido)
		consoleEncoderConfig := zap.NewDevelopmentEncoderConfig()
		consoleEncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder // Habilita cores!
		consoleEncoder := zapcore.NewConsoleEncoder(consoleEncoderConfig)
		consoleSyncer := zapcore.AddSync(os.Stdout)
		consoleCore := zapcore.NewCore(consoleEncoder, consoleSyncer, level)

		// Encoder para o arquivo (JSON estruturado)
		fileEncoderConfig := zap.NewProductionEncoderConfig()
		fileEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		fileEncoder := zapcore.NewJSONEncoder(fileEncoderConfig)
		fileCore := zapcore.NewCore(fileEncoder, fileSyncer, level)

		// Usar NewTee para enviar logs para ambos os destinos (console e arquivo)
		core = zapcore.NewTee(consoleCore, fileCore)
	}

	// 5. Construir o logger final
	// AddCallerSkip(1) é importante para que o logger reporte o local correto da chamada,
	// e não a linha dentro desta função de inicialização.
	logger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))

	logger.Info("Logger inicializado com sucesso",
		zap.String("ambiente", env),
		zap.String("nivel", level.String()),
		zap.String("arquivo", logFile),
		zap.Int("tamanhoMaxMB", maxSizeMB),
	)

	return logger, nil
}

// LoggingTransport é um http.RoundTripper que adiciona logs às requisições e respostas
type LoggingTransport struct {
	Logger      *zap.Logger
	Transport   http.RoundTripper
	MaxBodySize int
}

// RoundTrip implementa a interface http.RoundTripper
func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	safeURL := t.sanitizeURL(req.URL.String())

	// Log da requisição com URL sanitizada
	t.Logger.Info("Enviando Requisição",
		zap.String("Método", req.Method),
		zap.String("URL", safeURL),
		zap.String("Cabeçalhos", headersToString(req.Header)),
	)

	var reqBodyBytes []byte
	if req.Body != nil {
		var err error
		reqBodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			t.Logger.Error("Erro ao ler o corpo da requisição", zap.Error(err))
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewBuffer(reqBodyBytes)) // Resetar o Body
		// Remover dados sensíveis do corpo
		sanitizedBody := t.sanitizeBody(req.Header.Get("Content-Type"), reqBodyBytes)
		t.Logger.Debug("Corpo da Requisição", zap.ByteString("Body", sanitizedBody))
	}

	start := time.Now()
	resp, err := t.Transport.RoundTrip(req)
	duration := time.Since(start)

	if err != nil {
		t.Logger.Error("Erro na Requisição",
			zap.String("Método", req.Method),
			zap.String("URL", safeURL),
			zap.Error(err),
			zap.Duration("Duração", duration),
		)
		return resp, err
	}

	// Log da resposta
	t.Logger.Info("Recebendo Resposta",
		zap.String("Método", req.Method),
		zap.String("URL", safeURL),
		zap.Int("Status", resp.StatusCode),
		zap.Duration("Duração", duration),
		zap.String("Cabeçalhos", headersToString(resp.Header)),
	)

	var respBodyBytes []byte
	if resp.Body != nil {
		var err error
		respBodyBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			t.Logger.Error("Erro ao ler o corpo da resposta", zap.Error(err))
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewBuffer(respBodyBytes)) // Resetar o Body
		// Remover dados sensíveis do corpo
		sanitizedBody := t.sanitizeBody(resp.Header.Get("Content-Type"), respBodyBytes)
		t.Logger.Debug("Corpo da Resposta", zap.ByteString("Body", sanitizedBody))
	}

	return resp, nil
}

// método para sanitizar URLs do logging transport
func (t *LoggingTransport) sanitizeURL(urlStr string) string {
	// Parse a URL
	u, err := url.Parse(urlStr)
	if err != nil {
		return "[URL_INVÁLIDA]"
	}

	// Obter query parameters
	query := u.Query()

	// Lista de parâmetros sensíveis para sanitizar
	sensitiveParams := []string{
		"key",
		"api_key",
		"apikey",
		"api-key",
		"token",
		"access_token",
		"refresh_token",
		"client_secret",
		"password",
		"secret",
	}

	// Sanitizar parâmetros sensíveis
	for _, param := range sensitiveParams {
		if query.Has(param) {
			query.Set(param, "[REDACTED]")
		}
	}

	// Reconstruir a URL com parâmetros sanitizados
	u.RawQuery = query.Encode()

	// Também verificar se a API key está no path (alguns serviços fazem isso)
	path := u.Path
	// Padrão de API key do Google (AIza...)
	googleKeyPattern := regexp.MustCompile(`AIza[A-Za-z0-9_-]{35}`)
	path = googleKeyPattern.ReplaceAllString(path, "[REDACTED_API_KEY]")

	// Padrão genérico de API key
	genericKeyPattern := regexp.MustCompile(`/[A-Za-z0-9]{32,}/`)
	if genericKeyPattern.MatchString(path) {
		path = genericKeyPattern.ReplaceAllString(path, "/[REDACTED]/")
	}

	u.Path = path

	return u.String()
}

// headersToString converte os cabeçalhos para uma string legível
func headersToString(headers http.Header) string {
	var buf strings.Builder
	for key, values := range headers {
		lowerKey := strings.ToLower(key)
		// Adicionar mais casos de headers sensíveis
		if lowerKey == "authorization" ||
			lowerKey == "api-key" ||
			lowerKey == "x-api-key" ||
			lowerKey == "x-goog-api-key" || // Google API key header
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "password") {
			buf.WriteString(fmt.Sprintf("%s: [REDACTED]; ", key))
			continue
		}
		for _, value := range values {
			buf.WriteString(fmt.Sprintf("%s: %s; ", key, value))
		}
	}
	return buf.String()
}

// sanitizeBody remove ou mascara dados sensíveis do corpo da requisição/resposta
func (t *LoggingTransport) sanitizeBody(contentType string, body []byte) []byte {
	if len(body) > t.MaxBodySize {
		return []byte(fmt.Sprintf("[Corpo muito grande para ser logado, tamanho: %d bytes]", len(body)))
	}

	if strings.Contains(contentType, "application/json") {
		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err == nil {
			// Mascara campos sensíveis conhecidos (shallow)
			for _, k := range []string{
				"api_key", "password", "token", "access_token", "refresh_token", "client_secret", "authorization",
			} {
				if _, exists := data[k]; exists {
					data[k] = "[REDACTED]"
				}
			}
			sanitized, _ := json.Marshal(data)
			return sanitized
		}
	}

	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		values, err := url.ParseQuery(string(body))
		if err == nil {
			if _, exists := values["password"]; exists {
				values.Set("password", "[REDACTED]")
			}
			if _, exists := values["api_key"]; exists {
				values.Set("api_key", "[REDACTED]")
			}
			return []byte(values.Encode())
		}
	}

	// Retorna o corpo original se não puder sanitizar
	return body
}
