/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"crypto/tls"
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
	// Corporate TLS trust overrides (CHATCLI_CA_BUNDLE /
	// CHATCLI_TLS_INSECURE_SKIP_VERIFY). Cloned so each provider keeps its
	// dedicated TLS session cache.
	if tlsCfg := GlobalTLSConfig(); tlsCfg != nil {
		transport.TLSClientConfig = tlsCfg.Clone()
	}
	return NewHTTPClientWithTransport(logger, timeout, transport)
}

// NewHTTPClientH1 is like NewHTTPClient but pins the connection to HTTP/1.1.
//
// Go's bundled HTTP/2 client intermittently fails a POST-with-body with
// "unexpected EOF" against some Cloudflare-fronted hosts (observed on
// api.openai.com and api.x.ai image endpoints): the edge closes an idle h2
// connection and the next request races the GOAWAY, and net/http does NOT
// retry requests that carry a body. HTTP/1.1 sidesteps the whole race and is
// rock-solid for request/response calls. Use it for non-streaming endpoints
// that send/receive large payloads (image generation), where h2 multiplexing
// buys nothing. Corporate TLS trust (CHATCLI_CA_BUNDLE /
// CHATCLI_TLS_INSECURE_SKIP_VERIFY) is preserved; only ALPN is constrained.
func NewHTTPClientH1(logger *zap.Logger, timeout time.Duration) *http.Client {
	tlsCfg := &tls.Config{NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
	if g := GlobalTLSConfig(); g != nil {
		tlsCfg = g.Clone()
		tlsCfg.NextProtos = []string{"http/1.1"}
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		TLSClientConfig:       tlsCfg,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Non-nil empty map is the canonical way to disable HTTP/2 upgrade.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
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
