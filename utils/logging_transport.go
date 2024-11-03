// utils/logging_transport.go
package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

// LoggingTransport é um http.RoundTripper que adiciona logs às requisições e respostas
type LoggingTransport struct {
	Logger    *zap.Logger
	Transport http.RoundTripper
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
		reqBodyBytes, _ = ioutil.ReadAll(req.Body)
		req.Body = ioutil.NopCloser(bytes.NewBuffer(reqBodyBytes)) // Resetar o Body
		// Opcional: Remover dados sensíveis do corpo
		sanitizedBody := sanitizeBody(req.Header.Get("Content-Type"), reqBodyBytes)
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
		respBodyBytes, _ = ioutil.ReadAll(resp.Body)
		resp.Body = ioutil.NopCloser(bytes.NewBuffer(respBodyBytes)) // Resetar o Body
		// Opcional: Remover dados sensíveis do corpo
		sanitizedBody := sanitizeBody(resp.Header.Get("Content-Type"), respBodyBytes)
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
func sanitizeBody(contentType string, body []byte) []byte {
	const maxBodySize = 1024 // 1KB

	if len(body) > maxBodySize {
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
	// Retorna o corpo original se não puder sanitizar
	return body
}
