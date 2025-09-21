package utils

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"go.uber.org/zap"
)

// APIError é um erro estruturado para respostas HTTP com status code.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error: status %d - %s", e.StatusCode, e.Message)
}

// Retry executa uma função com retry exponencial para erros temporários.
// - maxAttempts: Número máximo de tentativas (lido de ENV ou default).
// - initialBackoff: Tempo inicial de espera entre tentativas (lido de ENV ou default).
// - fn: Função a executar, que recebe ctx e retorna um resultado genérico T e erro.
func Retry[T any](ctx context.Context, logger *zap.Logger, maxAttempts int, initialBackoff time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var result T
	backoff := initialBackoff

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err := fn(ctx)
		if err == nil {
			logger.Debug("Requisição bem-sucedida na tentativa",
				zap.Int("attempt", attempt))
			return res, nil
		}

		// Apenas retry para erros temporários (ex: timeout, 429, 5xx)
		if IsTemporaryError(err) {
			logger.Warn("Erro temporário detectado, retentando",
				zap.Int("attempt", attempt),
				zap.Int("max_attempts", maxAttempts),
				zap.Error(err),
				zap.Duration("backoff", backoff))
			if attempt < maxAttempts {
				time.Sleep(backoff)
				backoff *= 2 // Backoff exponencial
				continue
			}
		}

		// Erro permanente: loga e retorna
		logger.Error("Erro permanente na requisição, abortando",
			zap.Int("attempt", attempt),
			zap.Error(err))
		return result, err
	}

	// Falha após todas as tentativas
	errMsg := fmt.Errorf("falha após %d tentativas", maxAttempts)
	logger.Error("Máximo de tentativas excedido", zap.Error(errMsg))
	return result, errMsg
}

// IsTemporaryError verifica se o erro é temporário e pode ser retryado.
// Agora usa errors.Unwrap para desembrulhar e As para checar APIError com status 429 ou 5xx, além de timeouts.
func IsTemporaryError(err error) bool {
	// Desembrulha o erro se for wrapped
	for err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return true
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			// Retry para Rate Limit (429) e Server Errors (5xx)
			return apiErr.StatusCode == 429 || (apiErr.StatusCode >= 500 && apiErr.StatusCode < 600)
		}
		err = errors.Unwrap(err) // Desembrulha para checar inner errors
	}
	return false
}
