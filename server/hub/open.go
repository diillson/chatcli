/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package hub

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/utils"
)

// DefaultDBPath returns the conversation hub database path: CHATCLI_HUB_DB when
// set, otherwise ~/.chatcli/hub.db. The parent directory is created.
func DefaultDBPath() (string, error) {
	if p := os.Getenv("CHATCLI_HUB_DB"); p != "" {
		return p, nil
	}
	homeDir, err := utils.GetHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(homeDir, ".chatcli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "hub.db"), nil
}

// OpenDefault opens the hub at DefaultDBPath wrapped in a fan-out Manager so
// connected frontends can live-tail. The tail buffer is tunable via
// CHATCLI_HUB_TAIL_BUFFER. Both the gRPC server and the gateway daemon open the
// same database file, so a conversation is shared across every channel; for
// real-time cross-process push, co-locate the gateway in the hub server
// process (one in-memory Manager).
func OpenDefault(ctx context.Context, logger *zap.Logger) (*Manager, error) {
	dbPath, err := DefaultDBPath()
	if err != nil {
		return nil, err
	}
	store, err := OpenSQLiteStore(ctx, dbPath, logger)
	if err != nil {
		return nil, err
	}
	bufSize := 0
	if v := os.Getenv("CHATCLI_HUB_TAIL_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			bufSize = n
		}
	}
	return NewManager(store, logger, bufSize), nil
}
