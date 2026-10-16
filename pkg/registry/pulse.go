/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package registry

import (
	"net/http"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// newRegistryHTTPClient is the client every skill registry talks through.
// Its requests show on the live dashboard as connections to the registry's
// host — never the path, which carries search terms and skill slugs.
func newRegistryHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: pulse.MeterTransport(nil)}
}
