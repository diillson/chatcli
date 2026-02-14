/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// maxRedirects is the maximum number of HTTP redirects to follow.
const maxRedirects = 10

// NewHTTPClient cria um cliente HTTP com LoggingTransport e timeout configurado
func NewHTTPClient(logger *zap.Logger, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &LoggingTransport{
			Logger:      logger,
			Transport:   http.DefaultTransport,
			MaxBodySize: 2048,
		},
		Timeout: timeout,
		// Validate redirects: limit count and strip auth headers on cross-origin redirects
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			// Strip sensitive headers when redirected to a different host
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
				req.Header.Del("Api-Key")
				req.Header.Del("X-Api-Key")
				req.Header.Del("X-Goog-Api-Key")
			}
			return nil
		},
	}
}
