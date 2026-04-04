/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package token

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

type Manager interface {
	GetAccessToken(ctx context.Context) (string, error)
	RefreshToken(ctx context.Context) (string, error)
	SetRealm(realm string) // NOVO
}

// TokenManager gerencia a obtenção e renovação de tokens de acesso
type TokenManager struct {
	clientID         string
	clientSecret     string
	realm            string
	accessToken      string
	expiresAt        time.Time
	mu               sync.RWMutex
	logger           *zap.Logger
	client           *http.Client
	tokenURLOverride string
}

// NewTokenManager cria uma nova instância de TokenManager
func NewTokenManager(clientID, clientSecret, realm string, logger *zap.Logger) Manager {
	httpClient := utils.NewHTTPClient(logger, 30*time.Second)
	return &TokenManager{
		clientID:     clientID,
		clientSecret: clientSecret,
		realm:        realm,
		logger:       logger,
		client:       httpClient,
	}
}

// SetRealm atualiza o valor do realm e força uma nova solicitação de token.
func (tm *TokenManager) SetRealm(realm string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.realm != realm {
		tm.realm = realm
		tm.accessToken = "" // Limpa o token para forçar a solicitação de um novo
		tm.logger.Info(i18n.T("llm.token.realm_updated"), zap.String("new_realm", realm))
	}
}

// GetAccessToken retorna o token de acesso válido, renovando-o se necessário
func (tm *TokenManager) GetAccessToken(ctx context.Context) (string, error) {
	tm.mu.RLock()
	token := tm.accessToken
	expiration := tm.expiresAt
	tm.mu.RUnlock()

	if time.Until(expiration) > 60*time.Second && token != "" {
		tm.logger.Debug(i18n.T("llm.token.valid_found"), zap.Time("expires_at", expiration))
		return token, nil
	}

	tm.logger.Info(i18n.T("llm.token.expired_or_missing"))
	return tm.refreshToken(ctx)
}

// RefreshToken força a renovação do token de acesso
func (tm *TokenManager) RefreshToken(ctx context.Context) (string, error) {
	return tm.refreshToken(ctx)
}

// refreshToken renova o token de acesso com retry e backoff exponencial
func (tm *TokenManager) refreshToken(ctx context.Context) (string, error) {
	maxAttempts := 3
	backoff := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		token, err := tm.requestToken(ctx)
		if err != nil {
			if utils.IsTemporaryError(err) {
				tm.logger.Warn(i18n.T("llm.token.temp_error"),
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("backoff", backoff),
				)
				if attempt < maxAttempts {
					tm.logger.Warn(i18n.T("llm.token.backoff_retry"),
						zap.Int("attempt", attempt),
						zap.Duration("backoff", backoff),
					)
					time.Sleep(backoff)
					backoff *= 2 // Backoff exponencial
					continue
				}
			}
			return "", fmt.Errorf("%s: %w", i18n.T("llm.token.renew_error"), err)
		}
		return token, nil
	}

	return "", fmt.Errorf("%s", i18n.T("llm.token.renew_failed_attempts", maxAttempts))
}

// requestToken faz a requisição para obter um novo token de acesso
func (tm *TokenManager) requestToken(ctx context.Context) (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.logger.Info(i18n.T("llm.token.renewing"))

	tokenURL := fmt.Sprintf("https://idm.stackspot.com/%s/oidc/oauth/token", tm.realm)
	if tm.tokenURLOverride != "" {
		tokenURL = tm.tokenURLOverride
	}
	data := strings.NewReader(fmt.Sprintf(
		"grant_type=client_credentials&client_id=%s&client_secret=%s",
		tm.clientID, tm.clientSecret))

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, data)
	if err != nil {
		tm.logger.Error(i18n.T("llm.token.create_request_error"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.token.create_request_error"), err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := tm.client.Do(req)
	if err != nil {
		tm.logger.Error(i18n.T("llm.token.request_error"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.token.request_error"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		// Sanitize response body to prevent leaking tokens in error messages
		sanitized := "(unable to read response body)"
		if readErr == nil {
			sanitized = utils.SanitizeSensitiveText(string(bodyBytes))
		}
		errMsg := i18n.T("llm.token.get_failed", resp.StatusCode, sanitized)
		tm.logger.Error(i18n.T("llm.token.request_failed"), zap.String("response", errMsg))
		return "", errors.New(errMsg)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		tm.logger.Error(i18n.T("llm.token.decode_error"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.token.decode_error"), err)
	}

	accessToken, ok := result["access_token"].(string)
	if !ok {
		// Don't log the full result as it may contain partial credentials
		tm.logger.Error(i18n.T("llm.token.access_token_missing"))
		return "", errors.New(i18n.T("llm.token.access_token_get_failed"))
	}

	expiresIn, ok := result["expires_in"].(float64)
	if !ok {
		tm.logger.Error(i18n.T("llm.token.expires_in_missing"))
		return "", errors.New(i18n.T("llm.token.expires_in_get_failed"))
	}

	tm.accessToken = accessToken
	tm.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	tm.logger.Info(i18n.T("llm.token.renewed_success"), zap.Time("expires_at", tm.expiresAt))

	return tm.accessToken, nil
}
