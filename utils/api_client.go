/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// APIClient é um cliente HTTP especializado para APIs REST
type APIClient struct {
	baseURL     string
	client      *http.Client
	headers     map[string]string
	logger      *zap.Logger
	maxRetries  int
	initialWait time.Duration
}

// NewAPIClient cria um novo cliente API com cabeçalhos padrão
func NewAPIClient(logger *zap.Logger, baseURL string, headers map[string]string) *APIClient {
	return &APIClient{
		baseURL:     baseURL,
		client:      NewHTTPClient(logger, 60*time.Second),
		headers:     headers,
		logger:      logger,
		maxRetries:  3,
		initialWait: 1 * time.Second,
	}
}

// Get faz uma requisição GET
func (c *APIClient) Get(ctx context.Context, path string) ([]byte, error) {
	return c.Request(ctx, http.MethodGet, path, nil)
}

// Post faz uma requisição POST
func (c *APIClient) Post(ctx context.Context, path string, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("erro ao serializar payload: %w", err)
	}

	return c.Request(ctx, http.MethodPost, path, data)
}

// Request faz uma requisição HTTP com retry exponencial
func (c *APIClient) Request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar requisição: %w", err)
	}

	// Adicionar cabeçalhos
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}

	// Implementar retry com backoff exponencial
	var resp *http.Response
	var respBody []byte
	wait := c.initialWait

	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		// Criar um novo body reader para cada tentativa
		if attempt > 1 && body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}

		resp, err = c.client.Do(req)
		if err != nil {
			if IsTemporaryError(err) && attempt < c.maxRetries {
				c.logger.Warn("Erro temporário, retentando",
					zap.Int("attempt", attempt),
					zap.Duration("wait", wait),
					zap.Error(err))
				time.Sleep(wait)
				wait *= 2 // Backoff exponencial
				continue
			}
			return nil, err
		}

		// Ler o corpo da resposta
		respBody, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("erro ao ler resposta: %w", err)
		}

		// Verificar status de erro
		if resp.StatusCode >= 400 {
			// Retry para alguns tipos de erro
			if (resp.StatusCode == 429 || (resp.StatusCode >= 500 && resp.StatusCode < 600)) &&
				attempt < c.maxRetries {
				c.logger.Warn("Erro de servidor ou rate limit, retentando",
					zap.Int("status", resp.StatusCode),
					zap.Int("attempt", attempt),
					zap.Duration("wait", wait))
				time.Sleep(wait)
				wait *= 2 // Backoff exponencial
				continue
			}

			return nil, fmt.Errorf("erro na API (HTTP %d): %s", resp.StatusCode, string(respBody))
		}

		// Sucesso!
		return respBody, nil
	}

	return nil, fmt.Errorf("máximo de tentativas excedido")
}
