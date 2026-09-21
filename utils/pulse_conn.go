/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for outbound HTTP. Every client built by NewHTTPClient* has
 * the logging transport on top, so measuring there sees every connection to
 * a provider or an auth server. The metering itself lives in pkg/pulse, the
 * stdlib-only leaf, so packages this one imports (and that therefore cannot
 * import it back) can meter their own clients too.
 */
package utils

import (
	"net/http"

	"github.com/diillson/chatcli/pkg/pulse"
)

// MeterTransport wraps a transport so its requests show on the live
// dashboard. It is for the clients built outside NewHTTPClient* (web tools,
// embedding providers), which the logging transport never sees. A nil base
// means http.DefaultTransport. Kept here for its existing callers; it is
// pulse.MeterTransport.
func MeterTransport(base http.RoundTripper) http.RoundTripper {
	return pulse.MeterTransport(base)
}
