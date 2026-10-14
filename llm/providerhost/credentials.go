/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/pkg/providerproto"
	"go.uber.org/zap"
)

// CredentialStore keeps each plugin's opaque credential. The default
// implementation is the encrypted auth store; tests swap in memory.
type CredentialStore interface {
	Load(provider string) (providerproto.Credential, bool)
	Save(provider string, cred providerproto.Credential) error
	Delete(provider string) error
}

// ProfileID is the auth-store profile holding a plugin's credential.
func ProfileID(provider string) string {
	return string(auth.ProviderPlugin) + ":" + strings.ToUpper(provider)
}

// AuthStore is the CredentialStore backed by the encrypted auth profile
// store (AES-GCM, key in the keychain or ~/.chatcli/.auth-key). The plugin
// credential is serialized as JSON into the profile's token field.
type AuthStore struct {
	Logger *zap.Logger
}

func (s AuthStore) logger() *zap.Logger {
	if s.Logger == nil {
		return zap.NewNop()
	}
	return s.Logger
}

// Load implements CredentialStore.
func (s AuthStore) Load(provider string) (providerproto.Credential, bool) {
	p := auth.GetProfile(ProfileID(provider), s.logger())
	if p == nil || p.Token == "" {
		return nil, false
	}
	var cred providerproto.Credential
	if err := json.Unmarshal([]byte(p.Token), &cred); err != nil || len(cred) == 0 {
		return nil, false
	}
	return cred, true
}

// Save implements CredentialStore.
func (s AuthStore) Save(provider string, cred providerproto.Credential) error {
	raw, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("encode credential: %w", err)
	}
	return auth.UpsertProfile(ProfileID(provider), &auth.AuthProfileCredential{
		CredType: auth.CredentialToken,
		Provider: auth.ProviderPlugin,
		Token:    string(raw),
	}, s.logger())
}

// Delete implements CredentialStore.
func (s AuthStore) Delete(provider string) error {
	return auth.DeleteProfile(ProfileID(provider), s.logger())
}
