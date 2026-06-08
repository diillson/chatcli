/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
)

// SSRF guard for the web tools (@webfetch / @websearch / @osv).
//
// These tools fetch URLs that originate from the LLM, from tool arguments, or
// from operator config. A prompt-injected model can try to point them at
// internal infrastructure — most dangerously the cloud metadata endpoint
// (169.254.169.254 / metadata.google.internal), which hands out short-lived
// cloud credentials.
//
// The defense is enforced in DEPTH, at three layers, because upfront URL
// validation alone is bypassable:
//
//  1. validateWebTarget — fast, upfront checks on the requested URL: scheme
//     must be http/https, cloud-metadata hostnames are refused, and a literal
//     IP in the URL is range-checked. Gives an early, clear error.
//  2. ssrfDialControl — a net.Dialer.Control hook that runs AFTER DNS
//     resolution, on EVERY connection attempt, and re-checks the concrete IP
//     being dialed. This is the airtight layer: it closes DNS-rebinding /
//     TOCTOU (a hostname that resolved "safe" during validation but flips to a
//     metadata IP at dial time) and it covers every redirect hop, since each
//     hop dials anew.
//  3. validateRedirect — re-runs the upfront checks on every redirect target
//     and strips credentials on cross-host hops, so a 302 to an internal URL
//     cannot launder past the initial validation.
//
// Policy:
//   - Scheme must be http or https.
//   - Cloud-metadata / link-local targets are ALWAYS blocked. There is no
//     legitimate reason for these tools to reach them, and that is the SSRF
//     vector worth closing unconditionally.
//   - Private / loopback ranges are ALLOWED BY DEFAULT, because fetching an
//     internal service (e.g. http://svc/metrics) is a documented use case.
//     Set CHATCLI_WEBFETCH_BLOCK_PRIVATE=true to deny them in hardened
//     deployments.
//   - When the request travels through a corporate proxy, the dial targets the
//     PROXY (the egress control point), so ssrfDialControl validates the proxy
//     address; the proxy itself enforces egress to the final target. Link-local
//     is still refused even for a proxy address.

// maxWebRedirects caps redirect chains for the web tools.
const maxWebRedirects = 10

// metadataHostnames are well-known cloud metadata DNS names blocked regardless
// of how they resolve.
var metadataHostnames = []string{
	"metadata.google.internal",
	"metadata.goog",
	"instance-data",
}

// validateWebTarget validates rawURL against the SSRF policy and returns a
// canonical, re-encoded URL string safe to hand to the HTTP client. Hostname
// targets are only checked for scheme/metadata here; their resolved IP is
// enforced at dial time by ssrfDialControl (rebinding-proof).
func validateWebTarget(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	switch parsed.Scheme {
	case "http", "https":
		// allowed
	default:
		return "", fmt.Errorf("unsupported URL scheme %q (only http/https are allowed)", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("URL has no host")
	}

	if isMetadataHostname(host) {
		return "", fmt.Errorf("blocked cloud metadata hostname %q", host)
	}

	// A literal IP is checked immediately (no DNS needed). Hostnames are
	// enforced at dial time.
	if ip := net.ParseIP(host); ip != nil {
		if err := checkWebIP(ip, webBlockPrivate(), host); err != nil {
			return "", err
		}
	}

	return parsed.String(), nil
}

// ssrfDialControl is installed as net.Dialer.Control on the web transport. It
// runs after DNS resolution with the concrete address about to be dialed,
// making it the authoritative, rebinding-proof SSRF enforcement point.
func ssrfDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Post-resolution dials always carry a literal IP; if we somehow can't
		// parse it, fail closed for the always-blocked classes is impossible,
		// so allow and let the upper layers handle it.
		return nil
	}
	return checkWebIP(ip, webBlockPrivate(), host)
}

// validateRedirect is installed as http.Client.CheckRedirect. It re-validates
// every redirect target and strips sensitive headers on cross-host hops.
func validateRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxWebRedirects {
		return fmt.Errorf("stopped after %d redirects", maxWebRedirects)
	}
	if _, err := validateWebTarget(req.URL.String()); err != nil {
		return fmt.Errorf("refusing redirect to %q: %w", req.URL.Redacted(), err)
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("Api-Key")
		req.Header.Del("X-Api-Key")
	}
	return nil
}

// checkWebIP enforces the always-blocked (metadata/link-local) set and the
// opt-in private/loopback block.
func checkWebIP(ip net.IP, blockPrivate bool, host string) error {
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("blocked metadata/link-local address %s (host %q)", ip, host)
	}
	if blockPrivate && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
		return fmt.Errorf("blocked private address %s (host %q); unset CHATCLI_WEBFETCH_BLOCK_PRIVATE to allow", ip, host)
	}
	return nil
}

// isMetadataHostname reports whether host is (a subdomain of) a known cloud
// metadata hostname.
func isMetadataHostname(host string) bool {
	lower := strings.ToLower(host)
	for _, bh := range metadataHostnames {
		if lower == bh || strings.HasSuffix(lower, "."+bh) {
			return true
		}
	}
	return false
}

// webBlockPrivate reports whether private/loopback ranges should be denied.
func webBlockPrivate() bool {
	return boolEnvTrue("CHATCLI_WEBFETCH_BLOCK_PRIVATE")
}

// boolEnvTrue reports whether env var name is set to a truthy value.
func boolEnvTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
