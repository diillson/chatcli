package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// GitHub Copilot OAuth Device Flow (RFC 8628)
	defaultCopilotOAuthClientID = "Ov23lifEydOk2Non90tJ"
	CopilotDeviceCodeURL        = "https://github.com/login/device/code"
	CopilotTokenURL             = "https://github.com/login/oauth/access_token"
	CopilotScope                = "read:user"

	// Polling defaults per GitHub docs
	copilotDefaultInterval = 5 * time.Second
	copilotDeviceTimeout   = 15 * time.Minute
)

// CopilotOAuthClientID returns the GitHub Copilot OAuth client ID, allowing override via env var.
func CopilotOAuthClientID() string {
	if id := os.Getenv("CHATCLI_COPILOT_CLIENT_ID"); id != "" {
		return id
	}
	return defaultCopilotOAuthClientID
}

// DeviceCodeResponse is the response from GitHub's device code endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceTokenResponse is the response from GitHub's token polling endpoint.
type DeviceTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// LoginGitHubCopilotOAuth authenticates via the GitHub OAuth Device Flow.
// This is used for GitHub Copilot subscriptions (Individual, Business, Enterprise).
// The resulting token is stored as CredentialToken since GitHub device flow tokens
// do not expire and have no refresh token.
func LoginGitHubCopilotOAuth(ctx context.Context, logger *zap.Logger) (profileID string, err error) {
	// Step 1: Request device code
	deviceResp, err := requestDeviceCode(ctx, logger)
	if err != nil {
		return "", fmt.Errorf("failed to request device code: %w", err)
	}

	// Step 2: Display instructions to user
	fmt.Println()
	fmt.Println("AUTH [GitHub Copilot] Device authentication required.")
	fmt.Println()
	fmt.Printf("  1. Open: %s\n", deviceResp.VerificationURI)
	fmt.Printf("  2. Enter code: %s\n", deviceResp.UserCode)
	fmt.Println()

	// Try to open browser automatically
	if openErr := openBrowser(deviceResp.VerificationURI); openErr != nil {
		fmt.Println("Could not open browser automatically. Open the URL above manually.")
	} else {
		fmt.Println("Browser opened. Enter the code above and authorize the application.")
	}
	fmt.Println()
	fmt.Println("Waiting for authorization (press Ctrl+C to cancel)...")

	// Step 3: Poll for token
	interval := copilotDefaultInterval
	if deviceResp.Interval > 0 {
		interval = time.Duration(deviceResp.Interval) * time.Second
	}
	expiresAt := time.Now().Add(copilotDeviceTimeout)
	if deviceResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second)
	}

	token, err := pollForToken(ctx, logger, deviceResp.DeviceCode, interval, expiresAt)
	if err != nil {
		return "", err
	}

	// Step 4: Store credential
	profileID = "github-copilot:default"
	cred := &AuthProfileCredential{
		CredType: CredentialToken,
		Provider: ProviderGitHubCopilot,
		Token:    token,
		// GitHub OAuth tokens from device flow do not expire
		Expires: 0,
	}
	if err := UpsertProfile(profileID, cred, logger); err != nil {
		return "", fmt.Errorf("failed to save credentials: %w", err)
	}

	fmt.Println("GitHub Copilot authentication successful!")
	return profileID, nil
}

// requestDeviceCode initiates the device flow by requesting a device code from GitHub.
func requestDeviceCode(ctx context.Context, logger *zap.Logger) (*DeviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", CopilotOAuthClientID())
	form.Set("scope", CopilotScope)

	hc := utils.NewHTTPClient(logger, 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CopilotDeviceCodeURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		sanitized := utils.SanitizeSensitiveText(string(raw))
		return nil, fmt.Errorf("device code request failed (%s): %s", resp.Status, sanitized)
	}

	var dcResp DeviceCodeResponse
	if err := json.Unmarshal(raw, &dcResp); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	if dcResp.DeviceCode == "" || dcResp.UserCode == "" {
		return nil, fmt.Errorf("invalid device code response: missing device_code or user_code")
	}

	return &dcResp, nil
}

// pollForToken polls GitHub's token endpoint until the user authorizes or the code expires.
func pollForToken(ctx context.Context, logger *zap.Logger, deviceCode string, interval time.Duration, expiresAt time.Time) (string, error) {
	hc := utils.NewHTTPClient(logger, 30*time.Second)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		if time.Now().After(expiresAt) {
			return "", fmt.Errorf("device code expired; please try again")
		}

		time.Sleep(interval)

		payload := map[string]string{
			"client_id":   CopilotOAuthClientID(),
			"device_code": deviceCode,
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, CopilotTokenURL, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := hc.Do(req)
		if err != nil {
			logger.Debug("Token poll request failed, retrying", zap.Error(err))
			continue
		}

		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			continue
		}

		var tr DeviceTokenResponse
		if err := json.Unmarshal(raw, &tr); err != nil {
			continue
		}

		switch tr.Error {
		case "":
			// Success
			if tr.AccessToken != "" {
				return tr.AccessToken, nil
			}
			return "", fmt.Errorf("empty access token in response")

		case "authorization_pending":
			// User hasn't authorized yet, continue polling
			continue

		case "slow_down":
			// GitHub wants us to slow down — add 5 seconds per spec
			interval += 5 * time.Second
			logger.Debug("Slowing down poll interval", zap.Duration("new_interval", interval))
			continue

		case "expired_token":
			return "", fmt.Errorf("device code expired; please try again")

		case "access_denied":
			return "", fmt.Errorf("authorization denied by user")

		default:
			return "", fmt.Errorf("OAuth error: %s — %s", tr.Error, tr.ErrorDesc)
		}
	}
}
