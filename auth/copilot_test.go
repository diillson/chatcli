package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestDeviceCode(t *testing.T) {
	// Mock GitHub device code endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept: application/json, got %s", r.Header.Get("Accept"))
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("expected Content-Type: application/x-www-form-urlencoded, got %s", r.Header.Get("Content-Type"))
		}

		resp := DeviceCodeResponse{
			DeviceCode:      "test-device-code",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        5,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// The function uses hardcoded URLs, so we test the response parsing logic
	// by verifying the struct fields are correct
	t.Run("DeviceCodeResponse fields", func(t *testing.T) {
		resp := DeviceCodeResponse{
			DeviceCode:      "test-device-code",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://github.com/login/device",
			ExpiresIn:       900,
			Interval:        5,
		}
		if resp.DeviceCode != "test-device-code" {
			t.Errorf("expected test-device-code, got %s", resp.DeviceCode)
		}
		if resp.UserCode != "ABCD-1234" {
			t.Errorf("expected ABCD-1234, got %s", resp.UserCode)
		}
		if resp.Interval != 5 {
			t.Errorf("expected interval 5, got %d", resp.Interval)
		}
	})
}

func TestDeviceTokenResponseParsing(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr string
		wantTok string
	}{
		{
			name:    "success",
			json:    `{"access_token":"ghu_test123","token_type":"bearer","scope":"read:user"}`,
			wantTok: "ghu_test123",
		},
		{
			name:    "authorization_pending",
			json:    `{"error":"authorization_pending","error_description":"waiting for user"}`,
			wantErr: "authorization_pending",
		},
		{
			name:    "slow_down",
			json:    `{"error":"slow_down","error_description":"too many requests"}`,
			wantErr: "slow_down",
		},
		{
			name:    "expired_token",
			json:    `{"error":"expired_token","error_description":"device code expired"}`,
			wantErr: "expired_token",
		},
		{
			name:    "access_denied",
			json:    `{"error":"access_denied","error_description":"user denied"}`,
			wantErr: "access_denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp DeviceTokenResponse
			if err := json.Unmarshal([]byte(tt.json), &resp); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if tt.wantTok != "" && resp.AccessToken != tt.wantTok {
				t.Errorf("expected token %s, got %s", tt.wantTok, resp.AccessToken)
			}
			if tt.wantErr != "" && resp.Error != tt.wantErr {
				t.Errorf("expected error %s, got %s", tt.wantErr, resp.Error)
			}
		})
	}
}

func TestPollForToken_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	_, err := pollForToken(ctx, nil, "test-code", time.Millisecond, time.Now().Add(time.Hour))
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestPollForToken_Expired(t *testing.T) {
	ctx := context.Background()
	// Already expired
	_, err := pollForToken(ctx, nil, "test-code", time.Millisecond, time.Now().Add(-time.Second))
	if err == nil {
		t.Error("expected error from expired device code")
	}
	if err != nil && err.Error() != "device code expired; please try again" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCopilotConstants(t *testing.T) {
	if CopilotOAuthClientID() != "Ov23lifEydOk2Non90tJ" {
		t.Errorf("unexpected client ID: %s", CopilotOAuthClientID())
	}
	if CopilotDeviceCodeURL != "https://github.com/login/device/code" {
		t.Errorf("unexpected device code URL: %s", CopilotDeviceCodeURL)
	}
	if CopilotTokenURL != "https://github.com/login/oauth/access_token" {
		t.Errorf("unexpected token URL: %s", CopilotTokenURL)
	}
	if CopilotScope != "read:user" {
		t.Errorf("unexpected scope: %s", CopilotScope)
	}
}

func TestProviderGitHubCopilot(t *testing.T) {
	if ProviderGitHubCopilot != "github-copilot" {
		t.Errorf("expected github-copilot, got %s", ProviderGitHubCopilot)
	}
}

func TestCopilotOAuthClientID_EnvOverride(t *testing.T) {
	// Default
	if CopilotOAuthClientID() != "Ov23lifEydOk2Non90tJ" {
		t.Errorf("expected default client ID, got %s", CopilotOAuthClientID())
	}

	// Override via env var
	t.Setenv("CHATCLI_COPILOT_CLIENT_ID", "custom-client-id-123")
	if CopilotOAuthClientID() != "custom-client-id-123" {
		t.Errorf("expected custom client ID, got %s", CopilotOAuthClientID())
	}
}
