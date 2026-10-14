/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
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

	"github.com/diillson/chatcli/pkg/pulse"
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
	logFile := GetEnvOrDefault("LOG_FILE", GetEnvOrDefault("CHATCLI_LOG_FILE", config.DefaultLogFile))
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
	rot := logRotationFromEnv(maxSizeMB)
	maxSizeMB = rot.maxSizeMB
	lumberjackLogger := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    rot.maxSizeMB, // em MB
		MaxBackups: rot.maxBackups,
		MaxAge:     rot.maxAgeDays, // em dias
		Compress:   rot.compress,
	}
	fileSyncer := zapcore.AddSync(lumberjackLogger)

	// 4. Configurar o core do logger com base no ambiente (CHATCLI_ENV)
	// Accepts: "dev" (console colorido + arquivo) ou "prod" (apenas arquivo JSON)
	// Backward-compatible: also checks legacy "ENV" if CHATCLI_ENV is not set.
	env := strings.ToLower(os.Getenv("CHATCLI_ENV"))
	if env == "" {
		env = strings.ToLower(GetEnvOrDefault("ENV", "prod"))
	}
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
	conn := pulse.BeginConn(req) // nil while the live dashboard is off

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
			conn.Fail(err)
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
		conn.Fail(err)
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

	// Resposta em streaming (SSE): ler o corpo inteiro aqui seguraria o
	// stream até o servidor fechá-lo — o chamador receberia tudo de uma vez,
	// com o corpo todo em memória. O corpo segue intocado para o chamador.
	if isEventStream(resp.Header.Get("Content-Type")) {
		t.Logger.Debug("Corpo da Resposta em streaming: não capturado")
		resp.Body = conn.Stream(resp.StatusCode, resp.Body)
		return resp, nil
	}

	if resp.Body == nil {
		conn.Finish(resp.StatusCode, 0)
		return resp, nil
	}
	// O corpo segue em streaming para o chamador: lê-lo inteiro aqui
	// colocaria em memória o que o servidor quisesse mandar, antes de
	// qualquer limite do chamador valer. Para o log, basta o começo dele,
	// registrado quando o chamador termina de ler.
	contentType := resp.Header.Get("Content-Type")
	resp.Body = &loggedBody{
		ReadCloser: conn.Stream(resp.StatusCode, resp.Body),
		limit:      t.MaxBodySize,
		done: func(head []byte, total int64) {
			body := t.sanitizeBody(contentType, head)
			if total > int64(len(head)) {
				body = []byte(fmt.Sprintf("[Corpo muito grande para ser logado, tamanho: %d bytes]", total))
			}
			t.Logger.Debug("Corpo da Resposta", zap.ByteString("Body", body))
		},
	}
	return resp, nil
}

// loggedBody repassa o corpo de uma resposta guardando só os primeiros
// limit bytes, e chama done uma vez, no fim da leitura ou no Close.
type loggedBody struct {
	io.ReadCloser
	limit  int
	head   []byte
	total  int64
	logged bool
	done   func(head []byte, total int64)
}

func (b *loggedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.total += int64(n)
	if room := b.limit - len(b.head); room > 0 && n > 0 {
		b.head = append(b.head, p[:min(n, room)]...)
	}
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *loggedBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *loggedBody) finish() {
	if !b.logged {
		b.logged = true
		b.done(b.head, b.total)
	}
}

// isEventStream reporta se o Content-Type é text/event-stream, ignorando
// parâmetros (charset) e caixa.
func isEventStream(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

// método para sanitizar URLs do logging transport
func (t *LoggingTransport) sanitizeURL(urlStr string) string {
	// Parse a URL
	u, err := url.Parse(urlStr)
	if err != nil {
		return "[URL_INVÁLIDA]"
	}

	// Credentials embedded as user:password@ never reach the log.
	if u.User != nil {
		u.User = url.User("[REDACTED]")
	}

	// Obter query parameters
	query := u.Query()

	// Sanitizar parâmetros sensíveis (same names, any case or separator
	// style, as the body fields: key, apiKey, code, code_verifier, ...)
	for param := range query {
		if isSensitiveKey(param) {
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
			strings.Contains(lowerKey, "apikey") ||
			strings.Contains(lowerKey, "api-key") ||
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "password") {
			fmt.Fprintf(&buf, "%s: [REDACTED]; ", key)
			continue
		}
		for _, value := range values {
			fmt.Fprintf(&buf, "%s: %s; ", key, value)
		}
	}
	return buf.String()
}

// sensitiveKeyNames lists the JSON, form and query keys whose values are
// redacted. Matching ignores case and the _ - . separators (see
// normalizeSensitiveKey), so api_key, apiKey, API-KEY and secretAccessKey
// are all covered by one entry each.
// Security (M14): Extended with comprehensive patterns.
var sensitiveKeyNames = []string{
	"api_key", "password", "passwd", "token",
	"access_token", "refresh_token", "id_token", "session_token",
	"client_secret", "client_key", "authorization", "secret", "key",
	"private_key", "signing_key", "encryption_key", "session_secret",
	"jwt_secret", "bearer", "credentials", "auth",
	"x_api_key", "x_auth_token", "cookie", "set_cookie",
	"secret_access_key", "aws_secret_access_key", "aws_session_token",
	// OAuth: the authorization code, the PKCE verifier, the device-flow
	// code and the dynamic client registration token all grant access.
	"code", "code_verifier", "device_code", "registration_access_token",
}

// sensitiveKeys is the set of normalized key names whose values should be
// redacted.
var sensitiveKeys = func() map[string]bool {
	m := make(map[string]bool, len(sensitiveKeyNames))
	for _, k := range sensitiveKeyNames {
		m[normalizeSensitiveKey(k)] = true
	}
	return m
}()

// normalizeSensitiveKey folds case and drops the separators between words,
// so snake_case, kebab-case and camelCase spellings compare equal.
func normalizeSensitiveKey(k string) string {
	k = strings.ToLower(k)
	return strings.NewReplacer("_", "", "-", "", ".", "").Replace(k)
}

// isSensitiveKey reports whether a field named k carries a secret.
func isSensitiveKey(k string) bool {
	return sensitiveKeys[normalizeSensitiveKey(k)]
}

// sanitizeJSON recursively sanitizes sensitive fields in any JSON value.
// Everything under a sensitive key is redacted, including the members of an
// object or array held there ("credentials": {"accessKeyId": ...}).
func sanitizeJSON(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			if isSensitiveKey(k) {
				val[k] = redactJSONValue(child)
			} else {
				val[k] = sanitizeJSON(child)
			}
		}
		return val
	case []interface{}:
		for i, item := range val {
			val[i] = sanitizeJSON(item)
		}
		return val
	default:
		return v
	}
}

// redactJSONValue replaces every non-empty string in v with [REDACTED],
// keeping the shape of objects and arrays so the log still shows what kind
// of value was there.
func redactJSONValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		if val == "" {
			return val
		}
		return "[REDACTED]"
	case map[string]interface{}:
		for k, child := range val {
			val[k] = redactJSONValue(child)
		}
		return val
	case []interface{}:
		for i, item := range val {
			val[i] = redactJSONValue(item)
		}
		return val
	default:
		return v
	}
}

// RedactURLForLog returns rawURL fit for a log line: user information is
// dropped and every query value is replaced, since servers configured by URL
// (MCP endpoints, webhooks) often carry their API key there. Scheme, host,
// path and the query parameter names are kept for diagnosis. A string that
// does not parse is replaced entirely.
func RedactURLForLog(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "[REDACTED_URL]"
	}
	if u.User != nil {
		u.User = url.User("[REDACTED]")
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			q.Set(k, "[REDACTED]")
		}
		u.RawQuery = q.Encode()
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// sanitizeBody remove ou mascara dados sensíveis do corpo da requisição/resposta
func (t *LoggingTransport) sanitizeBody(contentType string, body []byte) []byte {
	if len(body) > t.MaxBodySize {
		return []byte(fmt.Sprintf("[Corpo muito grande para ser logado, tamanho: %d bytes]", len(body)))
	}

	if strings.Contains(contentType, "application/json") {
		var data interface{}
		if err := json.Unmarshal(body, &data); err == nil {
			// M4: Recursive sanitization of nested JSON structures
			sanitized := sanitizeJSON(data)
			out, _ := json.Marshal(sanitized)
			return out
		}
	}

	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		values, err := url.ParseQuery(string(body))
		if err == nil {
			for param := range values {
				if isSensitiveKey(param) {
					values.Set(param, "[REDACTED]")
				}
			}
			return []byte(values.Encode())
		}
	}

	// Retorna o corpo original se não puder sanitizar
	return body
}
