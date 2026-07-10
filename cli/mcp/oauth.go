/*
 * ChatCLI - MCP OAuth (authorization for remote MCP servers)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements the MCP Authorization flow so ChatCLI can connect to remote
 * (Streamable HTTP / SSE) MCP servers that gate access behind OAuth 2.1 —
 * the AWS MCP server being the motivating case. The flow follows the MCP
 * authorization spec, which composes several RFCs:
 *
 *   1. The server answers an unauthenticated request with 401 and a
 *      WWW-Authenticate header carrying a `resource_metadata` pointer
 *      (RFC 9728, OAuth Protected Resource Metadata).
 *   2. We fetch that Protected Resource Metadata to learn which
 *      authorization server(s) protect the resource.
 *   3. We fetch the Authorization Server Metadata (RFC 8414) to learn the
 *      authorization / token / registration endpoints.
 *   4. If we have no client_id for this server yet we register one via
 *      Dynamic Client Registration (RFC 7591) with a loopback redirect.
 *   5. We run the authorization-code + PKCE flow through a localhost
 *      callback, echoing the `resource` indicator (RFC 8707) so the token
 *      is bound to this MCP server.
 *   6. Tokens are persisted in the encrypted auth store as profile
 *      "mcp:<server>" and refreshed transparently thereafter.
 *
 * All the OAuth primitives (PKCE, browser launch, token exchange, the
 * refreshable TokenProvider with single-flight + proactive renewal) are
 * reused from the auth package so this file only adds the MCP-specific
 * discovery + dynamic client registration on top.
 */
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// mcpOAuthClientName is the client_name advertised during Dynamic Client
// Registration. Purely cosmetic — shown on the authorization server's consent
// screen and in its registered-clients list.
const mcpOAuthClientName = "ChatCLI"

// mcpOAuthCallbackTimeout bounds how long we wait for the user to complete the
// browser authorization before giving up.
const mcpOAuthCallbackTimeout = 5 * time.Minute

// OAuthRequiredError signals that an MCP HTTP/SSE call could not be completed
// because the server demands OAuth authorization the client has not (yet)
// obtained, or holds an expired/invalid token that could not be refreshed.
//
// It is intentionally actionable: the agent surfaces Error() verbatim so the
// model can react by invoking the @mcp-login tool (or telling the user to run
// /mcp login <server>) instead of treating it as an opaque failure.
type OAuthRequiredError struct {
	Server    string
	Endpoint  string
	Challenge oauthChallenge
}

func (e *OAuthRequiredError) Error() string {
	return i18n.T("mcp.oauth.required_error", e.Server, e.Server)
}

// IsOAuthRequired reports whether err (or anything it wraps) is an
// *OAuthRequiredError, and returns it when so.
func IsOAuthRequired(err error) (*OAuthRequiredError, bool) {
	var oe *OAuthRequiredError
	if errors.As(err, &oe) {
		return oe, true
	}
	return nil, false
}

// oauthChallenge holds the fields we care about from a Bearer
// WWW-Authenticate challenge (RFC 6750 §3 / RFC 9728).
type oauthChallenge struct {
	ResourceMetadata string // resource_metadata="<url>" (RFC 9728)
	Realm            string
	Scope            string
	Present          bool // true when the header carried a Bearer challenge at all
}

// parseWWWAuthenticate extracts the Bearer challenge parameters from a
// WWW-Authenticate header value. It is lenient: it only understands the Bearer
// scheme (the only one MCP OAuth uses) and ignores anything else.
func parseWWWAuthenticate(header string) oauthChallenge {
	var ch oauthChallenge
	header = strings.TrimSpace(header)
	if header == "" {
		return ch
	}
	// Header shape: `Bearer realm="x", resource_metadata="https://...", scope="a b"`.
	// Case-insensitive scheme match; parameters follow after the scheme token.
	rest := header
	if idx := strings.IndexByte(header, ' '); idx > 0 {
		if strings.EqualFold(header[:idx], "Bearer") {
			ch.Present = true
			rest = header[idx+1:]
		}
	} else if strings.EqualFold(header, "Bearer") {
		ch.Present = true
		return ch
	}
	if !ch.Present {
		return ch
	}
	for _, part := range splitAuthParams(rest) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch k {
		case "resource_metadata":
			ch.ResourceMetadata = v
		case "realm":
			ch.Realm = v
		case "scope":
			ch.Scope = v
		}
	}
	return ch
}

// splitAuthParams splits a comma-separated auth-param list while respecting
// double-quoted values (a quoted scope like "a, b" must not be split).
func splitAuthParams(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}

// protectedResourceMetadata is the RFC 9728 document served at
// /.well-known/oauth-protected-resource.
type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServerMetadata is the RFC 8414 / OpenID Connect discovery document.
type authServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	GrantTypesSupported           []string `json:"grant_types_supported"`
}

// oauthServerMeta is the per-server OAuth state we persist so subsequent
// sessions can refresh without re-discovering or re-registering. Tokens live
// separately in the encrypted auth store; this file holds only non-secret
// endpoints plus the (public) registered client_id.
type oauthServerMeta struct {
	Server                string `json:"server"`
	Resource              string `json:"resource"`
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint,omitempty"`
	ClientID              string `json:"client_id"`
	Scope                 string `json:"scope,omitempty"`
	RedirectURI           string `json:"redirect_uri"`
}

// mcpOAuthProfileID is the auth-store profile id for a server's tokens.
func mcpOAuthProfileID(server string) string { return "mcp:" + server }

// oauthMetaDir is the directory holding per-server OAuth metadata files.
func oauthMetaDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.no_home"))
	}
	return filepath.Join(home, ".chatcli", "mcp_oauth"), nil
}

func oauthMetaPath(server string) (string, error) {
	dir, err := oauthMetaDir()
	if err != nil {
		return "", err
	}
	// Sanitize the server name into a safe filename.
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, server)
	return filepath.Join(dir, safe+".json"), nil
}

func loadOAuthMeta(server string) (*oauthServerMeta, error) {
	path, err := oauthMetaPath(server)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //#nosec G304 -- path derived from sanitized server name under ~/.chatcli
	if err != nil {
		return nil, err
	}
	var meta oauthServerMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func saveOAuthMeta(meta *oauthServerMeta) error {
	dir, err := oauthMetaDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path, err := oauthMetaPath(meta.Server)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// hasStoredMCPOAuth reports whether we already hold OAuth metadata AND a
// credential for the server (i.e. a prior login succeeded).
func hasStoredMCPOAuth(server string, logger *zap.Logger) bool {
	meta, err := loadOAuthMeta(server)
	if err != nil || meta == nil || meta.ClientID == "" {
		return false
	}
	cred := auth.GetProfile(mcpOAuthProfileID(server), logger)
	return cred != nil && cred.GetAccessToken() != ""
}

// oauthDiscoverer performs the RFC 9728 → RFC 8414 discovery chain. It is a
// thin struct so the HTTP client and logger are threaded once.
type oauthDiscoverer struct {
	hc     *http.Client
	logger *zap.Logger
}

func newOAuthDiscoverer(logger *zap.Logger) *oauthDiscoverer {
	return &oauthDiscoverer{hc: utils.NewHTTPClient(logger, 30*time.Second), logger: logger}
}

// discover resolves the authorization-server metadata for an MCP server,
// starting from an optional resource_metadata URL (from the 401 challenge) and
// falling back to the well-known path on the server's own origin.
func (d *oauthDiscoverer) discover(ctx context.Context, serverURL string, ch oauthChallenge) (*authServerMetadata, *protectedResourceMetadata, error) {
	prmURL := strings.TrimSpace(ch.ResourceMetadata)
	if prmURL == "" {
		origin, err := originOf(serverURL)
		if err != nil {
			return nil, nil, err
		}
		prmURL = origin + "/.well-known/oauth-protected-resource"
	}

	prm, err := fetchJSON[protectedResourceMetadata](ctx, d.hc, prmURL)
	if err != nil {
		// Some servers skip RFC 9728 and expose the AS directly on their
		// origin. Fall back to treating the server origin as the issuer.
		d.logger.Debug("MCP OAuth: protected-resource metadata unavailable, trying origin as issuer",
			zap.String("prm_url", prmURL), zap.Error(err))
		origin, oerr := originOf(serverURL)
		if oerr != nil {
			return nil, nil, err
		}
		asMeta, aerr := d.fetchAuthServerMetadata(ctx, origin)
		if aerr != nil {
			return nil, nil, fmt.Errorf("%s: %w", i18n.T("mcp.oauth.discovery_failed"), aerr)
		}
		return asMeta, &protectedResourceMetadata{Resource: origin}, nil
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, nil, fmt.Errorf("%s", i18n.T("mcp.oauth.no_auth_servers"))
	}

	// Use the first advertised authorization server. Spec allows many; the
	// first is the server's stated preference.
	var lastErr error
	for _, as := range prm.AuthorizationServers {
		asMeta, err := d.fetchAuthServerMetadata(ctx, as)
		if err != nil {
			lastErr = err
			continue
		}
		return asMeta, prm, nil
	}
	return nil, nil, fmt.Errorf("%s: %w", i18n.T("mcp.oauth.discovery_failed"), lastErr)
}

// fetchAuthServerMetadata tries the RFC 8414 well-known endpoints (OAuth AS
// metadata first, then OpenID Connect discovery) for an issuer URL. Per RFC
// 8414 the well-known segment is inserted between the host and any path
// component; we try both the path-preserving and the simple-append forms to
// interoperate with the range of real servers.
func (d *oauthDiscoverer) fetchAuthServerMetadata(ctx context.Context, issuer string) (*authServerMetadata, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	candidates := wellKnownCandidates(issuer)
	var lastErr error
	for _, u := range candidates {
		meta, err := fetchJSON[authServerMetadata](ctx, d.hc, u)
		if err != nil {
			lastErr = err
			continue
		}
		if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
			lastErr = fmt.Errorf("%s", i18n.T("mcp.oauth.incomplete_metadata", u))
			continue
		}
		return meta, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s", i18n.T("mcp.oauth.no_metadata_endpoint"))
	}
	return nil, lastErr
}

// wellKnownCandidates builds the ordered list of discovery URLs to try for an
// issuer. For an issuer with a path (e.g. https://host/tenant) RFC 8414
// mandates host-insertion (https://host/.well-known/.../tenant); we also try
// the naive suffix form and OIDC discovery to be liberal in what we accept.
func wellKnownCandidates(issuer string) []string {
	u, err := url.Parse(issuer)
	if err != nil {
		return []string{issuer + "/.well-known/oauth-authorization-server"}
	}
	path := strings.Trim(u.Path, "/")
	origin := u.Scheme + "://" + u.Host
	var out []string
	if path == "" {
		out = append(out,
			origin+"/.well-known/oauth-authorization-server",
			origin+"/.well-known/openid-configuration",
		)
	} else {
		out = append(out,
			origin+"/.well-known/oauth-authorization-server/"+path, // RFC 8414 host-insertion
			issuer+"/.well-known/oauth-authorization-server",       // naive suffix
			origin+"/.well-known/openid-configuration/"+path,
			issuer+"/.well-known/openid-configuration",
		)
	}
	return out
}

// registerClient performs RFC 7591 Dynamic Client Registration against the
// authorization server, returning the issued client_id. Used only when we have
// no client_id for the server yet.
func (d *oauthDiscoverer) registerClient(ctx context.Context, registrationEndpoint, redirectURI, scope string) (string, error) {
	if strings.TrimSpace(registrationEndpoint) == "" {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.no_registration_endpoint"))
	}
	reqBody := map[string]any{
		"client_name":                mcpOAuthClientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none", // public client (PKCE)
	}
	if scope != "" {
		reqBody["scope"] = scope
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := d.hc.Do(req) //#nosec G704 -- registration endpoint from server-advertised AS metadata
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var reg struct {
		ClientID string `json:"client_id"`
		Error    string `json:"error"`
		ErrDesc  string `json:"error_description"`
	}
	dec := json.NewDecoder(resp.Body)
	_ = dec.Decode(&reg)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || reg.ClientID == "" {
		msg := reg.ErrDesc
		if msg == "" {
			msg = reg.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.registration_failed", msg))
	}
	return reg.ClientID, nil
}

// LoginServer runs the full interactive OAuth authorization for an MCP server
// and persists the resulting credential + metadata. serverURL is the canonical
// MCP endpoint (used as the RFC 8707 resource indicator). ch is the challenge
// parsed from a prior 401, if any (empty is fine — discovery falls back to the
// well-known path).
//
// This opens a browser and blocks until the user completes the flow or the
// timeout elapses, so it must only be called from an interactive surface
// (the /mcp login command or the @mcp-login tool).
func LoginServer(ctx context.Context, serverName, serverURL string, ch oauthChallenge, logger *zap.Logger) error {
	disc := newOAuthDiscoverer(logger)

	// Reuse cached metadata when present so we skip discovery/registration on
	// re-login; fall back to full discovery otherwise.
	meta, _ := loadOAuthMeta(serverName)

	var asMeta *authServerMetadata
	var resource string
	if meta == nil || meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		am, prm, err := disc.discover(ctx, serverURL, ch)
		if err != nil {
			return err
		}
		asMeta = am
		resource = firstNonEmpty(prm.Resource, serverURL)
	} else {
		asMeta = &authServerMetadata{
			Issuer:                meta.Issuer,
			AuthorizationEndpoint: meta.AuthorizationEndpoint,
			TokenEndpoint:         meta.TokenEndpoint,
			RegistrationEndpoint:  meta.RegistrationEndpoint,
		}
		resource = firstNonEmpty(meta.Resource, serverURL)
	}

	scope := chooseScope(ch, asMeta)

	// Bind the loopback listener first so the redirect_uri is fixed before we
	// (possibly) register a client against it.
	listener, redirectURI, err := bindLoopback()
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("mcp.oauth.listener_failed"), err)
	}
	defer listener.Close() //nolint:errcheck // best-effort; server.Shutdown owns the listener

	// Ensure we have a client_id. Reuse the stored one only if it was
	// registered against the same redirect_uri; otherwise register fresh.
	clientID := ""
	if meta != nil && meta.ClientID != "" && meta.RedirectURI == redirectURI {
		clientID = meta.ClientID
	}
	if clientID == "" {
		clientID, err = disc.registerClient(ctx, asMeta.RegistrationEndpoint, redirectURI, scope)
		if err != nil {
			return err
		}
	}

	code, verifier, err := runLoopbackAuth(ctx, listener, loopbackAuthParams{
		authorizationEndpoint: asMeta.AuthorizationEndpoint,
		clientID:              clientID,
		redirectURI:           redirectURI,
		scope:                 scope,
		resource:              resource,
	}, logger)
	if err != nil {
		return err
	}

	tr, err := auth.ExchangeTokenForm(ctx, logger, asMeta.TokenEndpoint, auth.TokenExchangeRequest{
		GrantType:    "authorization_code",
		ClientID:     clientID,
		Code:         code,
		RedirectURI:  redirectURI,
		CodeVerifier: verifier,
		Resource:     resource,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("mcp.oauth.token_exchange_failed"), err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("%s", i18n.T("mcp.oauth.empty_access_token"))
	}

	cred := &auth.AuthProfileCredential{
		CredType: auth.CredentialOAuth,
		Provider: auth.ProviderMCP,
		Access:   tr.AccessToken,
		Refresh:  tr.RefreshToken,
		Expires:  auth.TokenExpiryMilli(tr.ExpiresIn),
		ClientID: clientID,
	}
	if err := auth.UpsertProfile(mcpOAuthProfileID(serverName), cred, logger); err != nil {
		return err
	}

	newMeta := &oauthServerMeta{
		Server:                serverName,
		Resource:              resource,
		Issuer:                asMeta.Issuer,
		AuthorizationEndpoint: asMeta.AuthorizationEndpoint,
		TokenEndpoint:         asMeta.TokenEndpoint,
		RegistrationEndpoint:  asMeta.RegistrationEndpoint,
		ClientID:              clientID,
		Scope:                 scope,
		RedirectURI:           redirectURI,
	}
	if err := saveOAuthMeta(newMeta); err != nil {
		logger.Warn("MCP OAuth: failed to persist server metadata", zap.String("server", serverName), zap.Error(err))
	}

	logger.Info("MCP OAuth authorization complete", zap.String("server", serverName))
	return nil
}

// mcpTokenProvider builds a refreshable TokenProvider for a server from its
// stored credential + metadata. Returns (nil, nil) when no credential exists
// yet, so callers can treat "not logged in" distinctly from a hard error.
func mcpTokenProvider(serverName, serverURL string, logger *zap.Logger) (auth.TokenProvider, error) {
	meta, err := loadOAuthMeta(serverName)
	if err != nil || meta == nil {
		return nil, nil
	}
	cred := auth.GetProfile(mcpOAuthProfileID(serverName), logger)
	if cred == nil || cred.GetAccessToken() == "" {
		return nil, nil
	}

	resource := firstNonEmpty(meta.Resource, serverURL)
	refresh := newMCPRefreshFunc(meta.TokenEndpoint, meta.ClientID, resource, meta.Scope)
	return auth.NewOAuthTokenProviderWithRefresh(cred, mcpOAuthProfileID(serverName), "auth-store", refresh, logger), nil
}

// newMCPRefreshFunc returns a refresh callback bound to a server's discovered
// token endpoint + client_id. Mirrors auth.RefreshOAuth but for the
// runtime-discovered MCP case (which the provider-switch in RefreshOAuth
// cannot handle).
func newMCPRefreshFunc(tokenEndpoint, clientID, resource, scope string) func(ctx context.Context, cred *auth.AuthProfileCredential, logger *zap.Logger) (*auth.AuthProfileCredential, error) {
	return func(ctx context.Context, cred *auth.AuthProfileCredential, logger *zap.Logger) (*auth.AuthProfileCredential, error) {
		if cred == nil {
			return nil, fmt.Errorf("%s", i18n.T("mcp.oauth.refresh_nil_cred"))
		}
		refresh := strings.TrimSpace(cred.Refresh)
		if refresh == "" {
			return nil, fmt.Errorf("%s", i18n.T("mcp.oauth.refresh_missing"))
		}
		tr, err := auth.ExchangeTokenForm(ctx, logger, tokenEndpoint, auth.TokenExchangeRequest{
			GrantType:    "refresh_token",
			ClientID:     clientID,
			RefreshToken: refresh,
			Resource:     resource,
			Scope:        scope,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("mcp.oauth.refresh_failed"), err)
		}
		if tr.AccessToken != "" {
			cred.Access = tr.AccessToken
		}
		if tr.RefreshToken != "" {
			cred.Refresh = tr.RefreshToken
		}
		if tr.ExpiresIn > 0 {
			cred.Expires = auth.TokenExpiryMilli(tr.ExpiresIn)
		}
		return cred, nil
	}
}

// LogoutServer deletes a server's stored OAuth credential and metadata.
func LogoutServer(serverName string, logger *zap.Logger) error {
	_ = auth.DeleteProfile(mcpOAuthProfileID(serverName), logger)
	if path, err := oauthMetaPath(serverName); err == nil {
		_ = os.Remove(path)
	}
	return nil
}

// --- helpers ---

// originOf returns scheme://host for a URL.
func originOf(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.bad_server_url", rawURL))
	}
	return u.Scheme + "://" + u.Host, nil
}

// chooseScope selects the OAuth scope to request: the challenge's scope wins,
// then the AS-advertised scopes, else empty (server default).
func chooseScope(ch oauthChallenge, asMeta *authServerMetadata) string {
	if strings.TrimSpace(ch.Scope) != "" {
		return ch.Scope
	}
	if asMeta != nil && len(asMeta.ScopesSupported) > 0 {
		return strings.Join(asMeta.ScopesSupported, " ")
	}
	return ""
}

// fetchJSON GETs url and decodes the JSON body into T. Non-2xx is an error.
func fetchJSON[T any](ctx context.Context, hc *http.Client, url string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req) //#nosec G704 -- discovery URLs derived from configured server URL / server-advertised metadata
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", i18n.T("mcp.oauth.http_get_failed", url, resp.StatusCode))
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// mcpOAuthLoopbackPort returns the preferred fixed loopback port (env override
// CHATCLI_MCP_OAUTH_PORT), or 0 for an ephemeral port. A fixed port keeps the
// redirect_uri — and therefore the registered client_id — stable across logins.
func mcpOAuthLoopbackPort() string {
	if p := strings.TrimSpace(os.Getenv("CHATCLI_MCP_OAUTH_PORT")); p != "" {
		return p
	}
	return "8765"
}

// bindLoopback binds a localhost listener and returns it with its redirect URI.
// It tries the preferred fixed port first (stable client registration) and
// falls back to an ephemeral port if that port is busy.
func bindLoopback() (net.Listener, string, error) {
	port := mcpOAuthLoopbackPort()
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, "", err
		}
	}
	actual := ln.Addr().(*net.TCPAddr).Port
	return ln, fmt.Sprintf("http://127.0.0.1:%d/callback", actual), nil
}
