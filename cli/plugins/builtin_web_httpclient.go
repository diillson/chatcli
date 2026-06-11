/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/utils"
	"golang.org/x/net/http/httpproxy"
)

// Corporate-proxy support shared by the @webfetch / @websearch / @osv builtins.
//
// These tools used to issue requests via http.DefaultClient. Its transport
// reads the standard HTTP_PROXY/HTTPS_PROXY/NO_PROXY variables and DOES inject
// Proxy-Authorization — BUT only when the proxy URL's userinfo parses cleanly
// with net/url. Corporate credentials routinely break that: a Windows-domain
// user (`DOMAIN\jdoe`) or a password containing `%`, `#`, or a space is not a
// valid percent-encoded userinfo, so url.Parse returns an error. The real trap
// is what http.ProxyFromEnvironment does with that error — it SWALLOWS it and
// returns a nil proxy, so Go silently bypasses the proxy and connects directly.
// Whatever sits in that path then answers 401/407. curl works in the same
// shell only because it parses the credentials tolerantly.
//
// So the fix is NOT a new set of chatcli-specific variables: it is to honor the
// SAME standard variables the user already sets (HTTP_PROXY/HTTPS_PROXY/
// ALL_PROXY/NO_PROXY, including embedded login:senha) and parse the credentials
// tolerantly, re-encoding them so Go emits Proxy-Authorization on both the
// plain-HTTP request path and the HTTPS CONNECT tunnel.
//
// CHATCLI_PROXY_AUTH is the one additive knob: a ready-to-send
// Proxy-Authorization header value for proxies that need a NON-Basic scheme
// (Negotiate/NTLM/Kerberos/Bearer), which a login:senha URL cannot express.
const envProxyAuth = "CHATCLI_PROXY_AUTH"

var (
	webClientOnce sync.Once
	webClient     *http.Client
)

// webHTTPClient returns the shared, proxy-aware HTTP client used by the
// @webfetch / @websearch / @osv builtins. It mirrors http.DefaultClient's
// "no client-level timeout" contract (every caller already scopes its request
// with a context deadline) while adding the corporate-proxy authentication the
// default transport drops on the floor.
//
// The client is built once, but every proxy decision is re-read from the
// environment per request, so runtime changes — and t.Setenv in tests — take
// effect without a rebuild.
func webHTTPClient() *http.Client {
	webClientOnce.Do(func() {
		webClient = &http.Client{
			Transport: &proxyAuthTransport{base: newWebTransport()},
			// Re-validate every redirect hop so a 302 to an internal URL can't
			// launder past the initial SSRF check.
			CheckRedirect: validateRedirect,
		}
	})
	return webClient
}

// fallbackUserAgent is the non-browser User-Agent used when a gateway rejects
// the browser UA. Corporate TLS-intercepting gateways (Secure Web Gateways)
// routinely answer a browser-looking UA with 401/407 because they expect a real
// browser to complete an interactive SSO/portal flow, while letting plain tools
// through. A curl identity is the most widely accepted across such gateways.
// Overridable via CHATCLI_WEBFETCH_USER_AGENT.
const fallbackUserAgent = "curl/8.7.1"

// webGet issues a GET to url through the shared proxy/SSRF-aware client with the
// given extra headers (User-Agent is managed here, do not pass it). It sends a
// browser User-Agent by default so public CDNs (Cloudflare/Mintlify) don't
// bot-block it with a 403. If the response is a gateway auth challenge
// (401/407) — which TLS-intercepting corporate proxies return for
// browser-looking clients — it retries ONCE with neutralUserAgent, which such
// gateways allow through. An explicit CHATCLI_WEBFETCH_USER_AGENT disables the
// fallback, since the user chose that UA deliberately.
func webGet(ctx context.Context, url string, extraHeaders map[string]string) (*http.Response, error) {
	resp, err := webGetWithUA(ctx, url, browserUA(), extraHeaders)
	if err != nil {
		return nil, err
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusProxyAuthRequired) &&
		strings.TrimSpace(os.Getenv("CHATCLI_WEBFETCH_USER_AGENT")) == "" {
		_ = resp.Body.Close()
		return webGetWithUA(ctx, url, fallbackUserAgent, extraHeaders)
	}
	return resp, nil
}

// webGetWithUA performs a single GET with the given User-Agent and headers.
//
// gosec G704 flags the request/Do below as an SSRF taint flow because callers
// may build the URL from operator config read via os.Getenv (e.g. SEARXNG_URL).
// The egress is validated upstream by validateWebTarget and the ssrfDialControl
// dial hook (which block cloud metadata/link-local, DNS rebinding and
// redirects), and G704 has no sanitizer model to recognize that — so the sinks
// are annotated as reviewed false positives, matching the repo convention for
// G304/G204.
func webGetWithUA(ctx context.Context, url, userAgent string, extraHeaders map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //#nosec G704 -- url validated upstream by validateWebTarget + ssrfDialControl (metadata/link-local, DNS rebinding, redirects)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	return webHTTPClient().Do(req) //#nosec G704 -- request URL validated upstream (see above)
}

// newWebTransport builds the proxy-aware transport. The connection settings
// mirror utils.NewHTTPClient so behavior is consistent with the LLM providers.
func newWebTransport() *http.Transport {
	return &http.Transport{
		Proxy: webProxyForRequest,
		// GetProxyConnectHeader injects a raw (non-Basic) Proxy-Authorization
		// on the HTTPS CONNECT request. Basic credentials carried in the proxy
		// userinfo are handled by the transport itself, so we only step in when
		// the URL has none and CHATCLI_PROXY_AUTH is set.
		GetProxyConnectHeader: func(_ context.Context, proxyURL *url.URL, _ string) (http.Header, error) {
			if proxyURL != nil && proxyURL.User != nil {
				return nil, nil
			}
			if auth := proxyAuthRawHeader(); auth != "" {
				return http.Header{"Proxy-Authorization": {auth}}, nil
			}
			return nil, nil
		},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			// Airtight SSRF enforcement: runs after DNS resolution on every
			// connection (initial + each redirect hop), closing DNS-rebinding.
			Control: ssrfDialControl,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Corporate TLS trust overrides (CHATCLI_CA_BUNDLE /
		// CHATCLI_TLS_INSECURE_SKIP_VERIFY) — web tools cross the same
		// TLS-intercepting proxy as the LLM providers.
		TLSClientConfig: utils.GlobalTLSConfig().Clone(),
	}
}

// proxyAuthTransport wraps the base transport to cover the one path Go won't
// handle on its own: a raw (non-Basic) Proxy-Authorization header for a
// plain-HTTP target. For such targets the request is forwarded to the proxy
// verbatim, so the header must be set on the request. HTTPS targets are left
// untouched here — their CONNECT header is supplied via GetProxyConnectHeader.
type proxyAuthTransport struct {
	base *http.Transport
}

func (t *proxyAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "http" && req.Header.Get("Proxy-Authorization") == "" {
		if auth := proxyAuthRawHeader(); auth != "" {
			// Attach only when a proxy actually applies AND it carries no Basic
			// userinfo of its own — so we never double up, and never leak the
			// credential on a direct (NO_PROXY) connection.
			if p, _ := webProxyForRequest(req); p != nil && p.User == nil {
				req = req.Clone(req.Context())
				req.Header.Set("Proxy-Authorization", auth)
			}
		}
	}
	return t.base.RoundTrip(req)
}

// webProxyForRequest resolves the proxy URL for req using the STANDARD
// environment variables (HTTPS_PROXY/HTTP_PROXY/ALL_PROXY, gated by NO_PROXY).
// Unlike http.ProxyFromEnvironment, it parses the proxy credentials tolerantly
// so a domain login or a password with special characters still authenticates
// instead of silently disabling the proxy.
func webProxyForRequest(req *http.Request) (*url.URL, error) {
	return webProxyForURL(req.URL)
}

// webProxyForURL is the URL-level core of webProxyForRequest, also used by the
// SSRF guard to decide whether a target is reached through a proxy (in which
// case the proxy is the egress control point and local IP resolution is both
// ineffective and prone to false positives).
func webProxyForURL(target *url.URL) (*url.URL, error) {
	raw := rawProxyForScheme(target.Scheme)
	if raw == "" {
		return nil, nil
	}

	proxyURL, err := parseProxyURLTolerant(raw)
	if err != nil || proxyURL == nil {
		return proxyURL, err
	}

	// Apply NO_PROXY using the standard matcher. We feed it a credential-
	// stripped copy (which always parses cleanly) purely for the include/
	// exclude decision, then return our credential-bearing URL.
	if !proxyAppliesForHost(target, proxyURL) {
		return nil, nil
	}
	return proxyURL, nil
}

// rawProxyForScheme returns the raw proxy string that applies to the target
// scheme, honoring both upper- and lower-case forms and falling back to
// ALL_PROXY (a curl-ism the standard library ignores, included here so behavior
// matches the curl the user is comparing against).
func rawProxyForScheme(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return firstNonEmptyEnv("HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy")
	}
	return firstNonEmptyEnv("HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
}

// parseProxyURLTolerant parses a proxy URL, recovering credentials that
// net/url rejects when they are not percent-encoded (e.g. `DOMAIN\user`, or a
// password containing `%`, `#`, or a space). It first tries the strict path;
// on failure it splits scheme / userinfo / host by hand and re-wraps the
// credentials with url.UserPassword, which percent-encodes them correctly so
// Go's transport can emit a valid Basic Proxy-Authorization.
func parseProxyURLTolerant(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	// Strict path: a clean, already-encoded URL is used verbatim (its decoded
	// credentials round-trip correctly through Go's transport).
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u, nil
	}

	// Tolerant path. Peel an explicit scheme; default to http like the proxy
	// env conventions do.
	scheme := "http"
	work := raw
	if i := strings.Index(work, "://"); i >= 0 {
		scheme = work[:i]
		work = work[i+len("://"):]
	}

	// Split userinfo from host on the LAST '@' so an '@' inside the password is
	// kept with the credentials, not mistaken for the host delimiter.
	var userinfo, hostpart string
	if at := strings.LastIndex(work, "@"); at >= 0 {
		userinfo, hostpart = work[:at], work[at+1:]
	} else {
		hostpart = work
	}
	hostpart = strings.TrimRight(hostpart, "/")
	if hostpart == "" {
		return nil, fmt.Errorf("proxy URL %q has no host", raw)
	}

	u := &url.URL{Scheme: scheme, Host: hostpart}
	if userinfo != "" {
		// First ':' splits user from password; everything after it (including
		// further ':' or '@') is the password.
		if c := strings.IndexByte(userinfo, ':'); c >= 0 {
			u.User = url.UserPassword(userinfo[:c], userinfo[c+1:])
		} else {
			u.User = url.User(userinfo)
		}
	}
	return u, nil
}

// proxyAppliesForHost reports whether the proxy should be used for reqURL given
// NO_PROXY, reusing the standard library matcher on a credential-stripped copy
// of the proxy (so the matcher never trips over the very credentials we are
// trying to preserve).
func proxyAppliesForHost(reqURL *url.URL, proxyURL *url.URL) bool {
	stripped := *proxyURL
	stripped.User = nil
	cfg := &httpproxy.Config{
		HTTPProxy:  stripped.String(),
		HTTPSProxy: stripped.String(),
		NoProxy:    firstNonEmptyEnv("NO_PROXY", "no_proxy"),
	}
	u, err := cfg.ProxyFunc()(reqURL)
	return err == nil && u != nil
}

// proxyAuthRawHeader returns the explicit Proxy-Authorization header value
// (CHATCLI_PROXY_AUTH), or "" when none is configured. This is the escape hatch
// for non-Basic proxy schemes; Basic credentials flow through the proxy URL
// userinfo instead, so Go handles them uniformly across HTTP and CONNECT.
func proxyAuthRawHeader() string {
	return strings.TrimSpace(os.Getenv(envProxyAuth))
}

// firstNonEmptyEnv returns the trimmed value of the first set, non-blank env
// var among names.
func firstNonEmptyEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}
