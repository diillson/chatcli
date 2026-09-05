/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"net/http"
	"os"
	"strings"
)

// CORSPolicy is the operator API's cross-origin configuration.
//
// It stays deny-all until an origin is named. What changed is that naming
// one now has an effect: the variable the Helm chart has been setting all
// along was read by nobody, so the dashboard could not call the API from a
// browser no matter how it was configured.
type CORSPolicy struct {
	// AllowedOrigins is the exact set of origins allowed. "*" allows any,
	// and is refused together with AllowCredentials.
	AllowedOrigins []string
	// AllowedMethods defaults to the verbs the API actually serves.
	AllowedMethods []string
	// AllowCredentials permits cookies and Authorization on cross-origin
	// requests.
	AllowCredentials bool
}

var defaultCORSMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}

// CORSPolicyFromEnv reads the policy from the environment.
//
//	CHATCLI_CORS_ALLOWED_ORIGINS    comma-separated list, or "*"
//	CHATCLI_CORS_ORIGIN             a single origin (kept: the chart has
//	                                been setting it since before the list
//	                                existed)
//	CHATCLI_CORS_ALLOWED_METHODS    comma-separated verbs
//	CHATCLI_CORS_ALLOW_CREDENTIALS  "true" to allow credentials
func CORSPolicyFromEnv() CORSPolicy {
	p := CORSPolicy{
		AllowedOrigins:   splitList(os.Getenv("CHATCLI_CORS_ALLOWED_ORIGINS")),
		AllowedMethods:   splitList(os.Getenv("CHATCLI_CORS_ALLOWED_METHODS")),
		AllowCredentials: strings.EqualFold(strings.TrimSpace(os.Getenv("CHATCLI_CORS_ALLOW_CREDENTIALS")), "true"),
	}
	if single := strings.TrimSpace(os.Getenv("CHATCLI_CORS_ORIGIN")); single != "" {
		p.AllowedOrigins = append(p.AllowedOrigins, single)
	}
	if len(p.AllowedMethods) == 0 {
		p.AllowedMethods = defaultCORSMethods
	}
	return p
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// enabled reports whether any origin is allowed.
func (p CORSPolicy) enabled() bool { return len(p.AllowedOrigins) > 0 }

// wildcard reports whether the policy allows any origin.
func (p CORSPolicy) wildcard() bool {
	for _, o := range p.AllowedOrigins {
		if o == "*" {
			return true
		}
	}
	return false
}

// originFor returns the value to echo in Access-Control-Allow-Origin for a
// request, or "" when the request's origin is not allowed.
//
// An allowlist of several origins cannot be expressed in the header, which
// carries one value: the request's own origin is echoed back after being
// matched, and Vary: Origin tells caches the response depends on it.
// Echoing an unmatched origin would turn the allowlist into "any site".
func (p CORSPolicy) originFor(requestOrigin string) string {
	if requestOrigin == "" {
		return ""
	}
	for _, allowed := range p.AllowedOrigins {
		if allowed == requestOrigin {
			return requestOrigin
		}
	}
	if p.wildcard() {
		// With credentials, "*" is not a legal value and browsers reject
		// the response; echoing the origin is the only working form.
		if p.AllowCredentials {
			return requestOrigin
		}
		return "*"
	}
	return ""
}

// apply writes the CORS headers for a request, and reports whether the
// request is a preflight that has been answered.
func (p CORSPolicy) apply(w http.ResponseWriter, r *http.Request, apiKeyHeader string) (handled bool) {
	if !p.enabled() {
		return false
	}

	// The response differs by origin whenever the allowlist is not a bare
	// wildcard, so a shared cache must not serve one origin's response to
	// another.
	w.Header().Add("Vary", "Origin")

	allow := p.originFor(r.Header.Get("Origin"))
	if allow == "" {
		return false
	}

	w.Header().Set("Access-Control-Allow-Origin", allow)
	w.Header().Set("Access-Control-Allow-Methods", strings.Join(p.AllowedMethods, ", "))
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+apiKeyHeader+", Authorization")
	if p.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	} else {
		w.Header().Set("Access-Control-Allow-Credentials", "false")
	}
	w.Header().Set("Access-Control-Max-Age", "3600") // 1 hour, not 24h

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}
