package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

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

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Sanitize response body to prevent leaking tokens in error messages
		sanitized := utils.SanitizeSensitiveText(string(raw))
		return nil, fmt.Errorf("token exchange failed (%s): %s", resp.Status, sanitized)
	}
	var tr OAuthTokenResponse

	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("bad token response: %w", err)
	}
	return &tr, nil
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
