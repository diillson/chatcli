package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// TokenExchangeError is returned by the token-endpoint round-trip when the
// server answers with a non-2xx status. It preserves the HTTP status and the
// OAuth error code from the JSON body so callers can distinguish a terminal
// refresh failure (expired/revoked refresh token — only a new interactive
// login can recover) from a transient one (network blip, 5xx, throttling).
type TokenExchangeError struct {
	StatusCode int
	Status     string
	Code       string // OAuth "error" field, e.g. "invalid_grant" or vendor codes like "TOKEN_EXPIRED"
	Body       string // sanitized response body
}

func (e *TokenExchangeError) Error() string {
	return fmt.Sprintf("token exchange failed (%s): %s", e.Status, e.Body)
}

// Terminal reports whether retrying the same exchange can ever succeed.
// 401/403 mean the credential itself was rejected; the listed 400-codes are
// the RFC 6749 §5.2 permanent failures. "TOKEN_EXPIRED" is the vendor code
// AWS sign-in returns for a dead refresh token.
func (e *TokenExchangeError) Terminal() bool {
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		return true
	}
	switch e.Code {
	case "invalid_grant", "invalid_client", "unauthorized_client", "unsupported_grant_type", "invalid_scope":
		return true
	}
	return strings.EqualFold(e.Code, "TOKEN_EXPIRED")
}

// IsTerminalTokenError reports whether err wraps a token-exchange failure
// that retrying cannot fix, i.e. the stored refresh token is dead and the
// user must log in again.
func IsTerminalTokenError(err error) bool {
	var te *TokenExchangeError
	return errors.As(err, &te) && te.Terminal()
}

// exchangeOAuthToken sends a token exchange request with JSON body.
// Used by providers that accept application/json (e.g. OpenAI).
func exchangeOAuthToken(ctx context.Context, logger *zap.Logger, tokenURL string, payload map[string]any) (*OAuthTokenResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	hc := utils.NewHTTPClient(logger, 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	return doTokenExchange(hc, req)
}

// exchangeAnthropicToken sends a token exchange to Anthropic using a plain HTTP client
// to avoid Cloudflare interference from custom transports/TLS fingerprinting.
func exchangeAnthropicToken(ctx context.Context, tokenURL string, payload map[string]any) (*OAuthTokenResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	hc := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ClaudeCodeUserAgent)
	req.Header.Set("Accept", "application/json")

	return doTokenExchange(hc, req)
}

func doTokenExchange(hc *http.Client, req *http.Request) (*OAuthTokenResponse, error) {
	// Set User-Agent to avoid Cloudflare blocking Go's default "Go-http-client/2.0"
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "chatcli/1.0")
	}
	resp, err := hc.Do(req) //#nosec G704 -- OAuth token exchange to provider-defined URLs (Anthropic/OpenAI/GitHub), not user-controlled
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Sanitize response body to prevent leaking tokens in error messages
		sanitized := utils.SanitizeSensitiveText(string(raw))
		var oauthErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &oauthErr)
		return nil, &TokenExchangeError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Code:       strings.TrimSpace(oauthErr.Error),
			Body:       sanitized,
		}
	}
	var tr OAuthTokenResponse

	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("bad token response: %w", err)
	}
	return &tr, nil
}

// fetchAnthropicEmail calls the OAuth profile endpoint to retrieve the user's
// email. Best-effort: returns "" on any failure (logged at debug level).
func fetchAnthropicEmail(ctx context.Context, accessToken string, logger *zap.Logger) string {
	return fetchAnthropicEmailFrom(ctx, AnthropicProfileURL, accessToken, logger)
}

// fetchAnthropicEmailFrom is fetchAnthropicEmail against an explicit
// profile endpoint (tests point it at a local server).
func fetchAnthropicEmailFrom(ctx context.Context, profileURL, accessToken string, logger *zap.Logger) string {
	if accessToken == "" {
		return ""
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		logger.Debug("anthropic profile fetch: request build failed", zap.Error(err))
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", ClaudeCodeUserAgent)

	resp, err := hc.Do(req) //#nosec G704 -- public Anthropic profile endpoint
	if err != nil {
		logger.Debug("anthropic profile fetch: http error", zap.Error(err))
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Debug("anthropic profile fetch: non-2xx", zap.String("status", resp.Status))
		return ""
	}
	var payload struct {
		Account struct {
			Email string `json:"email"`
		} `json:"account"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		logger.Debug("anthropic profile fetch: decode failed", zap.Error(err))
		return ""
	}
	return payload.Account.Email
}

func calcExpiresAtMilli(expiresIn int64) int64 {
	now := time.Now()
	if expiresIn <= 0 {
		return now.Add(1 * time.Hour).UnixMilli()
	}
	m := now.Add(time.Duration(expiresIn) * time.Second).UnixMilli()
	// safety margin (5min) like Clawdbot
	m -= 5 * 60 * 1000
	if m < now.UnixMilli() {
		return now.Add(60 * time.Minute).UnixMilli()
	}
	return m
}
