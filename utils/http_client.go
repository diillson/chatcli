// utils/http_client.go
package utils

import (
	"go.uber.org/zap"
	"net/http"
	"time"
)

// NewHTTPClient cria um cliente HTTP com LoggingTransport e timeout configurado
func NewHTTPClient(logger *zap.Logger, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &LoggingTransport{
			Logger:    logger,
			Transport: http.DefaultTransport,
		},
		Timeout: timeout,
	}
}
