package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// RefreshOAuth refreshes an OAuth credential in-place and returns it.
// Supported: anthropic, openai-codex.
func RefreshOAuth(ctx context.Context, cred *AuthProfileCredential, logger *zap.Logger) (*AuthProfileCredential, error) {
	if cred == nil {
		return nil, fmt.Errorf("nil cred")
	}
	if cred.CredType != CredentialOAuth {
		return nil, fmt.Errorf("cred is not oauth")
	}
	refresh := strings.TrimSpace(cred.Refresh)
	if refresh == "" {
		return nil, fmt.Errorf("missing refresh token")
	}

	var tokenURL string
	var clientID string
	switch cred.Provider {
	case ProviderAnthropic:
		tokenURL = AnthropicTokenURL
		clientID = cred.ClientID
		if clientID == "" {
			clientID = AnthropicOAuthClientID
		}
	case ProviderOpenAICodex:
		tokenURL = OpenAITokenURL
		clientID = cred.ClientID
		if clientID == "" {
			clientID = OpenAICodexClientID
		}
	default:
		return nil, fmt.Errorf("unsupported oauth provider: %s", cred.Provider)
	}

	payload := map[string]interface{}{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": refresh,
	}
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
		return nil, fmt.Errorf("oauth refresh failed (%s): %s", resp.Status, string(raw))
	}

	var tr OAuthTokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("bad oauth refresh response: %w", err)
	}
	if tr.AccessToken != "" {
		cred.Access = tr.AccessToken
	}
	if tr.RefreshToken != "" {
		cred.Refresh = tr.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		cred.Expires = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UnixMilli()
	}
	return cred, nil
}
