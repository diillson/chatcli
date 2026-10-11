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
	"io"
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
	return utils.AtomicWriteFile(path, data, 0o600)
}

// hasStoredMCPOAuth reports whether we already hold OAuth metadata AND a
// credential for the server at serverURL (i.e. a prior login succeeded). A
// credential bound to another resource does not count: it is never sent.
func hasStoredMCPOAuth(server, serverURL string, logger *zap.Logger) bool {
	meta, err := loadOAuthMeta(server)
	if err != nil || meta == nil || meta.ClientID == "" {
		return false
	}
	resource, err := canonicalResource(serverURL)
	if err != nil || !sameResource(meta.Resource, resource) {
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

// discover resolves the authorization-server metadata protecting the MCP
// server whose canonical URI is resource (see canonicalResource).
//
// The protected-resource metadata comes from the resource_metadata URL of
// the 401 challenge, which must be on the server's own origin, or else from
// the RFC 9728 well-known URIs of that origin: the path-inserted one first,
// then the root. Metadata that names a different resource is refused, as
// RFC 9728 section 3.3 requires. Whatever the metadata says, the token is
// requested for resource and nothing else, so a server can never pick the
// audience of the token the user consents to.
func (d *oauthDiscoverer) discover(ctx context.Context, resource string, ch oauthChallenge) (*authServerMetadata, error) {
	origin, err := originOf(resource)
	if err != nil {
		return nil, err
	}
	candidates, err := prmCandidates(resource, origin, ch)
	if err != nil {
		return nil, err
	}

	var prm *protectedResourceMetadata
	for _, prmURL := range candidates {
		p, ferr := fetchJSON[protectedResourceMetadata](ctx, d.hc, prmURL)
		if ferr != nil {
			d.logger.Debug("MCP OAuth: protected-resource metadata unavailable",
				zap.String("prm_url", prmURL), zap.Error(ferr))
			continue
		}
		if !describesResource(p.Resource, resource) {
			return nil, fmt.Errorf("%s", i18n.T("mcp.oauth.prm_resource_mismatch", p.Resource, resource))
		}
		prm = p
		break
	}
	if prm == nil {
		// Some servers skip RFC 9728 and expose the AS directly on their
		// origin. Fall back to treating the server origin as the issuer.
		asMeta, aerr := d.fetchAuthServerMetadata(ctx, origin)
		if aerr != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("mcp.oauth.discovery_failed"), aerr)
		}
		return asMeta, nil
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("%s", i18n.T("mcp.oauth.no_auth_servers"))
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
		return asMeta, nil
	}
	return nil, fmt.Errorf("%s: %w", i18n.T("mcp.oauth.discovery_failed"), lastErr)
}

// prmCandidates lists where to look for the protected-resource metadata of
// resource, in order. A resource_metadata URL from the challenge is the only
// candidate, and only when it is on the server's own origin: anything else
// would let the server send the client to fetch, and trust, a document
// published by someone else.
func prmCandidates(resource, origin string, ch oauthChallenge) ([]string, error) {
	if advertised := strings.TrimSpace(ch.ResourceMetadata); advertised != "" {
		if !sameOrigin(advertised, resource) {
			return nil, fmt.Errorf("%s", i18n.T("mcp.oauth.prm_cross_origin", advertised, origin))
		}
		return []string{advertised}, nil
	}
	const wellKnown = "/.well-known/oauth-protected-resource"
	var out []string
	if u, err := url.Parse(resource); err == nil {
		if path := strings.Trim(u.EscapedPath(), "/"); path != "" {
			out = append(out, origin+wellKnown+"/"+path) // RFC 9728 section 3.1
		}
	}
	return append(out, origin+wellKnown), nil
}

// canonicalResource returns the canonical URI of an MCP server, the value
// sent as the RFC 8707 resource indicator: the configured URL with its
// scheme and host lowercased and any fragment dropped.
func canonicalResource(serverURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.bad_server_url", serverURL))
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	return u.String(), nil
}

// sameResource reports whether stored names exactly the resource whose
// canonical URI is resource. A token recorded for any other resource was
// minted for another audience and must not be sent to this server.
func sameResource(stored, resource string) bool {
	if strings.TrimSpace(stored) == "" {
		return false
	}
	c, err := canonicalResource(stored)
	return err == nil && c == resource
}

// describesResource reports whether protected-resource metadata claiming
// claimed may describe resource: no claim at all, or a claim on the same
// origin whose path is resource's path or one of its parent paths (a server
// publishing one document for its whole origin). The claim is only checked,
// never used: the token is always requested for resource itself.
func describesResource(claimed, resource string) bool {
	if strings.TrimSpace(claimed) == "" {
		return true
	}
	c, err := url.Parse(strings.TrimSpace(claimed))
	if err != nil || c.Fragment != "" || !sameOrigin(claimed, resource) {
		return false
	}
	r, err := url.Parse(resource)
	if err != nil {
		return false
	}
	if c.RawQuery != "" && c.RawQuery != r.RawQuery {
		return false
	}
	cp := strings.TrimRight(c.EscapedPath(), "/")
	rp := strings.TrimRight(r.EscapedPath(), "/")
	return cp == "" || cp == rp || strings.HasPrefix(rp, cp+"/")
}

// sameOrigin reports whether two absolute URLs share scheme, host and port,
// with the scheme's default port made explicit.
func sameOrigin(a, b string) bool {
	oa, ok := urlOrigin(a)
	if !ok {
		return false
	}
	ob, ok := urlOrigin(b)
	return ok && oa == ob
}

func urlOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), true
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

// loginClientID resolves the client_id for one interactive login. When the AS
// offers Dynamic Client Registration, it ALWAYS registers a fresh client:
// dynamic registrations can be expired or purged server-side at any time
// (observed on AWS signin — the authorize page then fails with an
// invalid-redirect_uri error AFTER the user logs in, and every retry reuses
// the same dead client, a loop with no recovery path). Registration is cheap
// for public PKCE clients and an interactive login is rare, so fresh always
// wins over stale. Without a registration endpoint the stored client is a
// manual registration: reuse it when it was registered against this exact
// redirect_uri, and fail actionably otherwise.
func loginClientID(ctx context.Context, disc *oauthDiscoverer, asMeta *authServerMetadata, meta *oauthServerMeta, redirectURI, scope string) (string, error) {
	if strings.TrimSpace(asMeta.RegistrationEndpoint) != "" {
		return disc.registerClient(ctx, asMeta.RegistrationEndpoint, redirectURI, scope)
	}
	if meta != nil && meta.ClientID != "" && meta.RedirectURI == redirectURI {
		return meta.ClientID, nil
	}
	return "", fmt.Errorf("%s", i18n.T("mcp.oauth.no_registration_endpoint"))
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
	_ = decodeBoundedJSON(resp.Body, &reg)
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
// and persists the resulting credential + metadata. serverURL is the MCP
// endpoint the user configured; its canonical form is the RFC 8707 resource
// indicator of every token request. ch is the challenge
// parsed from a prior 401, if any (empty is fine — discovery falls back to the
// well-known path).
//
// This opens a browser and blocks until the user completes the flow or the
// timeout elapses, so it must only be called from an interactive surface
// (the /mcp login command or the @mcp-login tool).
func LoginServer(ctx context.Context, serverName, serverURL string, ch oauthChallenge, logger *zap.Logger) error {
	disc := newOAuthDiscoverer(logger)

	// The token is always requested for the server the user configured.
	resource, err := canonicalResource(serverURL)
	if err != nil {
		return err
	}

	// Reuse cached metadata when present so we skip discovery/registration on
	// re-login; fall back to full discovery otherwise. Metadata recorded for
	// another resource (the server URL changed, or an older release took the
	// resource from the server's own claim) is discarded, client included.
	meta, _ := loadOAuthMeta(serverName)
	if meta != nil && !sameResource(meta.Resource, resource) {
		meta = nil
	}

	var asMeta *authServerMetadata
	if meta == nil || meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		am, err := disc.discover(ctx, resource, ch)
		if err != nil {
			return err
		}
		asMeta = am
	} else {
		asMeta = &authServerMetadata{
			Issuer:                meta.Issuer,
			AuthorizationEndpoint: meta.AuthorizationEndpoint,
			TokenEndpoint:         meta.TokenEndpoint,
			RegistrationEndpoint:  meta.RegistrationEndpoint,
		}
	}

	scope := chooseScope(ch, asMeta)

	// Bind the loopback listener first so the redirect_uri is fixed before we
	// (possibly) register a client against it.
	listener, redirectURI, err := bindLoopback()
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("mcp.oauth.listener_failed"), err)
	}
	defer listener.Close() //nolint:errcheck // best-effort; server.Shutdown owns the listener

	clientID, err := loginClientID(ctx, disc, asMeta, meta, redirectURI, scope)
	if err != nil {
		return err
	}

	code, verifier, err := runLoopbackAuth(ctx, listener, loopbackAuthParams{
		authorizationEndpoint: asMeta.AuthorizationEndpoint,
		clientID:              clientID,
		redirectURI:           redirectURI,
		scope:                 scope,
		resource:              resource,
		serverName:            serverName,
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

	// A credential recorded for another resource was minted for another
	// audience: never send it to this server. Without a provider the server
	// answers 401 and the user is asked to log in again.
	resource, err := canonicalResource(serverURL)
	if err != nil {
		return nil, err
	}
	if !sameResource(meta.Resource, resource) {
		logger.Warn("MCP OAuth: stored credential is bound to a different resource; log in again",
			zap.String("server", serverName), zap.String("stored_resource", meta.Resource), zap.String("resource", resource))
		return nil, nil
	}
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
	if err := decodeBoundedJSON(resp.Body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// maxOAuthDocumentBytes caps a discovery or registration response. Real
// documents are a few kilobytes; the servers serving them are not trusted.
const maxOAuthDocumentBytes = 1 << 20

// decodeBoundedJSON decodes one JSON document from r into v, refusing a
// document larger than maxOAuthDocumentBytes.
func decodeBoundedJSON(r io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(r, maxOAuthDocumentBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxOAuthDocumentBytes {
		return fmt.Errorf("%s", i18n.T("mcp.oauth.document_too_large", maxOAuthDocumentBytes))
	}
	return json.Unmarshal(data, v)
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
