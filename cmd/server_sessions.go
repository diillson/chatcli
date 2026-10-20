/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"errors"
	"path/filepath"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/stateroot"
	"github.com/diillson/chatcli/server"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// errInvalidPrincipalKey refuses a key that is not the server's digest.
var errInvalidPrincipalKey = errors.New("invalid principal key")

// serverSessionStores opens the session stores the gRPC server serves.
//
// shared is the store sessions lived in before they had an owner
// (~/.chatcli/sessions); the server only lets RoleAdmin reach it. Every
// other principal gets its own store under
// ~/.chatcli/server/principals/<key>/sessions, where key is a digest the
// server derives from the authenticated identity. Either result is nil
// when it cannot be opened, and the server then answers Unavailable for
// the callers that need it.
func serverSessionStores(logger *zap.Logger) (shared server.SessionStore, perPrincipal server.PrincipalSessionStoreFactory) {
	if sm, err := cli.NewSessionManager(logger); err != nil {
		logger.Warn(i18n.T("cmd.server.session_init_failed"), zap.Error(err))
	} else {
		shared = sm
	}

	home, err := utils.GetHomeDir()
	if err != nil {
		logger.Warn(i18n.T("cmd.server.session_init_failed"), zap.Error(err))
		return shared, nil
	}
	return shared, principalSessionFactory(filepath.Join(stateroot.Under(home), "server", "principals"), logger)
}

// principalSessionFactory opens a principal's store under root.
func principalSessionFactory(root string, logger *zap.Logger) server.PrincipalSessionStoreFactory {
	return func(key string) (server.SessionStore, error) {
		if !isPrincipalKey(key) {
			return nil, errInvalidPrincipalKey
		}
		return cli.NewSessionManagerAt(filepath.Join(root, key, "sessions"), logger)
	}
}

// isPrincipalKey accepts only the hex SHA-256 digest the server derives,
// so the key can never name a path outside root.
func isPrincipalKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
