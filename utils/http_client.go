/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// maxRedirects is the maximum number of HTTP redirects to follow.
const maxRedirects = 10

// NewHTTPClient cria um cliente HTTP com LoggingTransport e timeout configurado.
// Cada chamada cria um http.Transport dedicado para evitar compartilhamento
// de pool de conexões e cache TLS entre providers diferentes.
func NewHTTPClient(logger *zap.Logger, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return NewHTTPClientWithTransport(logger, timeout, transport)
}

// NewHTTPClientWithTransport cria um cliente HTTP com um transport customizado
// envolvido pelo LoggingTransport. Use para injetar transports com TLS customizado.
func NewHTTPClientWithTransport(logger *zap.Logger, timeout time.Duration, inner http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: &LoggingTransport{
			Logger:      logger,
			Transport:   inner,
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
