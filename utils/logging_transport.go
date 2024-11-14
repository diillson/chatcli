package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	defaultMaxBodySize = 3070 // 3KB
	defaultMaxLogSize  = 50   // 10MB
)

// InitializeLogger configura e inicializa um logger com base nas variáveis de ambiente.
func InitializeLogger() (*zap.Logger, error) {
	// Definir o nível de log via variável de ambiente, default para Info
	logLevelEnv := strings.ToLower(os.Getenv("LOG_LEVEL"))
	var level zapcore.Level
	switch logLevelEnv {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
	case "dpanic":
		level = zap.DPanicLevel
	case "panic":
		level = zap.PanicLevel
	case "fatal":
		level = zap.FatalLevel
	default:
		level = zap.InfoLevel
	}

	// Configuração do encoder
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder // Formato de tempo legível
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	// Determinar o ambiente (development ou production)
	env := strings.ToLower(os.Getenv("ENV"))
	var encoder zapcore.Encoder
	if env == "prod" {
		encoder = zapcore.NewJSONEncoder(encoderConfig) // JSON para Produção
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig) // Console para desenvolvimento
	}

	// Nome do arquivo de log configurável via variável de ambiente
	logFile := os.Getenv("LOG_FILE")
	if logFile == "" {
		logFile = "app.log" // Valor padrão
	}

	// Tamanho máximo do arquivo de log configurável via variável de ambiente
	maxLogSize := getMaxLogSizeFromEnv()

	// Configuração do logger com rotação de logs
	lumberjackLogger := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    maxLogSize, // Tamanho máximo do arquivo de log em MB
		MaxBackups: 3,          // Número máximo de backups
		MaxAge:     28,         // Dias
		Compress:   true,       // Compressão
	}

	// Configuração do WriteSyncer para Dev console e arquivo de log, para Prod apenas arquivo.
	var writeSyncer zapcore.WriteSyncer
	if env == "prod" {
		// Produção: Apenas no arquivo de log
		writeSyncer = zapcore.AddSync(lumberjackLogger)
	} else {
		// Desenvolvimento: Console e arquivo de log
		writeSyncer = zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout), zapcore.AddSync(lumberjackLogger))
	}

	// Configuração do core com nível de log definido
	core := zapcore.NewCore(encoder, writeSyncer, level)

	// Construir o logger
	logger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))

	return logger, nil
}

// getMaxLogSizeFromEnv lê a variável de ambiente LOG_MAX_SIZE e retorna o valor em MB.
// Agora aceita valores como "50MB", "100KB", "1GB", etc.
func getMaxLogSizeFromEnv() int {
	envValue := os.Getenv("LOG_MAX_SIZE")
	if envValue != "" {
		size, err := parseSize(envValue)
		if err == nil && size > 0 {
			// Convertemos o valor para MB, pois o lumberjack espera o tamanho em MB
			return int(size / (1024 * 1024))
		}
	}
	return defaultMaxLogSize
}

// parseSize converte uma string de tamanho legível (como "50MB", "100KB", "1GB") para bytes.
func parseSize(sizeStr string) (int64, error) {
	sizeStr = strings.TrimSpace(sizeStr)
	unit := "B" // Padrão para bytes
	var multiplier int64 = 1

	// Verificar se a string termina com uma unidade de medida
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

	// Remover a unidade da string para obter apenas o número
	sizeStr = strings.TrimSuffix(sizeStr, unit)
	sizeStr = strings.TrimSpace(sizeStr)

	// Converter o número para int64
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tamanho inválido: %s", sizeStr)
	}

	return size * multiplier, nil
}

// LoggingTransport é um http.RoundTripper que adiciona logs às requisições e respostas
type LoggingTransport struct {
	Logger      *zap.Logger
	Transport   http.RoundTripper
	MaxBodySize int
}

// RoundTrip implementa a interface http.RoundTripper
func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Log da requisição
	t.Logger.Info("Enviando Requisição",
		zap.String("Método", req.Method),
		zap.String("URL", req.URL.String()),
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
			zap.String("URL", req.URL.String()),
			zap.Error(err),
			zap.Duration("Duração", duration),
		)
		return resp, err
	}

	// Log da resposta
	t.Logger.Info("Recebendo Resposta",
		zap.String("Método", req.Method),
		zap.String("URL", req.URL.String()),
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

// headersToString converte os cabeçalhos para uma string legível
func headersToString(headers http.Header) string {
	var buf strings.Builder
	for key, values := range headers {
		lowerKey := strings.ToLower(key)
		if lowerKey == "authorization" || lowerKey == "api-key" || lowerKey == "x-api-key" {
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
			// Exemplo: Mascara campos sensíveis
			if _, exists := data["api_key"]; exists {
				data["api_key"] = "[REDACTED]"
			}
			if _, exists := data["password"]; exists {
				data["password"] = "[REDACTED]"
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
