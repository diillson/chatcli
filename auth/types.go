package auth

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CredentialType representa o tipo de credencial armazenada.
type CredentialType string

const (
	CredentialOAuth  CredentialType = "oauth"
	CredentialAPIKey CredentialType = "api_key"
	CredentialToken  CredentialType = "token"
)

// ProviderID identifica o provedor de autenticação.
type ProviderID string

const (
	ProviderAnthropic   ProviderID = "anthropic"
	ProviderOpenAI      ProviderID = "openai"
	ProviderOpenAICodex ProviderID = "openai-codex"
)

// AuthMode indica como a autenticação foi resolvida.
type AuthMode string

const (
	AuthModeOAuth  AuthMode = "oauth"
	AuthModeAPIKey AuthMode = "api-key"
	AuthModeToken  AuthMode = "token"
	AuthModeEnv    AuthMode = "env"
)

// ProfileID constants para CLIs externos.
const (
	ClaudeCliProfileID = "anthropic:claude-cli"
	CodexCliProfileID  = "openai-codex:codex-cli"
)

// AuthProfileCredential é um wrapper polimórfico para qualquer tipo de credencial.
type AuthProfileCredential struct {
	CredType CredentialType `json:"type"`
	Provider ProviderID     `json:"provider"`
	Email    string         `json:"email,omitempty"`

	// Campos OAuth
	Access    string `json:"access,omitempty"`
	Refresh   string `json:"refresh,omitempty"`
	Expires   int64  `json:"expires,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	ClientID  string `json:"client_id,omitempty"`

	// Campo API Key
	Key string `json:"key,omitempty"`

	// Campo Token
	Token string `json:"token,omitempty"`
}

// IsExpired verifica se a credencial está expirada.
func (c *AuthProfileCredential) IsExpired() bool {
	if c.Expires <= 0 {
		return false
	}
	return time.Now().UnixMilli() >= c.Expires
}

// IsExpiringSoon verifica se expira nos próximos N minutos.
func (c *AuthProfileCredential) IsExpiringSoon(withinMinutes int) bool {
	if c.Expires <= 0 {
		return false
	}
	threshold := time.Now().Add(time.Duration(withinMinutes) * time.Minute).UnixMilli()
	return c.Expires < threshold
}

// GetAccessToken retorna o token de acesso dependendo do tipo.
func (c *AuthProfileCredential) GetAccessToken() string {
	switch c.CredType {
	case CredentialOAuth:
		return c.Access
	case CredentialToken:
		return c.Token
	case CredentialAPIKey:
		return c.Key
	default:
		return ""
	}
}

// AuthProfileStore é o armazém principal de credenciais.
type AuthProfileStore struct {
	Version  int                               `json:"version"`
	Profiles map[string]*AuthProfileCredential `json:"profiles"`
	Order    map[string][]string               `json:"order,omitempty"`
	LastGood map[string]string                 `json:"last_good,omitempty"`
}

// NewAuthProfileStore cria um store vazio.
func NewAuthProfileStore() *AuthProfileStore {
	return &AuthProfileStore{
		Version:  1,
		Profiles: make(map[string]*AuthProfileCredential),
		Order:    make(map[string][]string),
		LastGood: make(map[string]string),
	}
}

// ResolvedAuth representa o resultado da resolução de autenticação.
type ResolvedAuth struct {
	APIKey    string
	ProfileID string
	Source    string
	Mode      AuthMode
	Provider  ProviderID
	Email     string
}

// OAuthTokenResponse representa a resposta de um endpoint de token OAuth.
type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// FormatExpiry retorna uma string legível do tempo restante.
func FormatExpiry(expiresMs int64) string {
	if expiresMs <= 0 {
		return "no expiry"
	}
	remaining := time.Until(time.UnixMilli(expiresMs))
	if remaining <= 0 {
		return "expired"
	}
	if remaining > 2*time.Hour {
		return fmt.Sprintf("expires in %dh", int(remaining.Hours()))
	}
	if remaining > time.Hour {
		return "expires in 1h"
	}
	return fmt.Sprintf("expires in %dm", int(remaining.Minutes()))
}

// StripAuthPrefix removes the "oauth:", "token:", or "apikey:" prefix from a
// resolved API key, returning the raw credential suitable for HTTP headers.
func StripAuthPrefix(key string) string {
	for _, prefix := range []string{"oauth:", "token:", "apikey:"} {
		if strings.HasPrefix(key, prefix) {
			return strings.TrimPrefix(key, prefix)
		}
	}
	return key
}

// Stringer para debug.
func (c *AuthProfileCredential) String() string {
	data, _ := json.MarshalIndent(c, "", "  ")
	return string(data)
}
