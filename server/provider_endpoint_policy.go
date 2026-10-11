/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Client-supplied provider_config and the endpoints the server dials.
//
// The server dials from its own network position, so a URL chosen by a
// remote client is a request forgery primitive: it reaches loopback
// services, the cluster network and the cloud metadata endpoint, and
// non-2xx bodies come back to the caller inside the error. The server
// therefore decides every endpoint itself. provider_config is read
// against a closed list of keys:
//
//   - client_id, client_key: caller credentials for a fixed vendor host.
//   - realm, agent_id: single path segments under that fixed host.
//   - base_url: refused unless its origin is listed by the operator in
//     CHATCLI_SERVER_PROVIDER_ENDPOINTS, and even then every connection
//     is checked at dial time, after DNS resolution, against loopback,
//     private, link-local, multicast and metadata ranges. A private
//     model server is configured on the server itself (OLLAMA_BASE_URL),
//     never chosen by a client.
//
// Any other key is refused rather than ignored.

// providerEndpointsEnv lists the origins a client may name in base_url.
const providerEndpointsEnv = "CHATCLI_SERVER_PROVIDER_ENDPOINTS"

// providerEndpointTimeout bounds one request to a client-chosen endpoint,
// the same budget the Ollama client gets from the manager.
const providerEndpointTimeout = 300 * time.Second

type providerConfigKeyKind int

const (
	providerKeyCredential providerConfigKeyKind = iota
	providerKeyPathSegment
	providerKeyEndpoint
)

// providerConfigKeys is every provider_config key the server accepts.
var providerConfigKeys = map[string]providerConfigKeyKind{
	"client_id":  providerKeyCredential,
	"client_key": providerKeyCredential,
	"realm":      providerKeyPathSegment,
	"agent_id":   providerKeyPathSegment,
	"base_url":   providerKeyEndpoint,
}

var providerPathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// errProviderDialBlocked marks a connection refused by the dial guard.
var errProviderDialBlocked = errors.New("provider endpoint address blocked")

// vettedProviderConfig is provider_config after the policy ran.
type vettedProviderConfig struct {
	// config holds the accepted keys, minus the endpoint.
	config map[string]string
	// endpoint is the allowlisted base_url, nil when the caller sent none.
	endpoint *url.URL
}

// vetProviderConfig applies the policy above to a caller's provider_config.
func (h *Handler) vetProviderConfig(provider string, cfg map[string]string) (vettedProviderConfig, error) {
	out := vettedProviderConfig{}
	if len(cfg) == 0 {
		return out, nil
	}
	out.config = make(map[string]string, len(cfg))
	for key, val := range cfg {
		kind, known := providerConfigKeys[key]
		if !known {
			return vettedProviderConfig{}, errors.New(i18n.T("server.provider_endpoint.key_refused", key))
		}
		switch kind {
		case providerKeyPathSegment:
			if val != "" && !providerPathSegment.MatchString(val) {
				return vettedProviderConfig{}, errors.New(i18n.T("server.provider_endpoint.segment_invalid", key))
			}
			out.config[key] = val
		case providerKeyEndpoint:
			if val == "" {
				continue
			}
			endpoint, err := h.allowedProviderEndpoint(provider, val)
			if err != nil {
				return vettedProviderConfig{}, err
			}
			out.endpoint = endpoint
		default:
			out.config[key] = val
		}
	}
	return out, nil
}

// allowedProviderEndpoint accepts raw only when it names an origin the
// operator listed and the provider is one that takes a base URL.
func (h *Handler) allowedProviderEndpoint(provider, raw string) (*url.URL, error) {
	if !strings.EqualFold(strings.TrimSpace(provider), "OLLAMA") {
		return nil, errors.New(i18n.T("server.provider_endpoint.base_url_provider", provider))
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New(i18n.T("server.provider_endpoint.invalid"))
	}
	origin := endpointOrigin(u)
	if !originAllowlisted(origin, h.logger) {
		return nil, errors.New(i18n.T("server.provider_endpoint.not_allowlisted", origin, providerEndpointsEnv))
	}
	if h.ssrfValidator != nil {
		if err := h.ssrfValidator.ValidateProviderURL(u.String()); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// endpointOrigin renders scheme://host:port with the default port made
// explicit, so "https://h" and "https://h:443" compare equal.
func endpointOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
		if strings.EqualFold(u.Scheme, "http") {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// originAllowlisted reports whether origin is listed in the operator's
// CHATCLI_SERVER_PROVIDER_ENDPOINTS. Unset means no client endpoint at all.
func originAllowlisted(origin string, logger *zap.Logger) bool {
	for _, entry := range strings.Split(os.Getenv(providerEndpointsEnv), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		u, err := url.Parse(entry)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			logger.Warn(i18n.T("server.provider_endpoint.allowlist_entry_invalid"), zap.String("entry", entry))
			continue
		}
		if endpointOrigin(u) == origin {
			return true
		}
	}
	return false
}

// providerDialControl runs after DNS resolution on every connection the
// guarded client opens, redirects and retries included, so a hostname that
// resolves to a public address at check time and to an internal one at
// dial time is still refused.
func providerDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || providerDialBlockedIP(ip) {
		return fmt.Errorf("%w: %s", errProviderDialBlocked, i18n.T("server.provider_endpoint.dial_blocked", host))
	}
	return nil
}

// providerDialBlockedNets are the ranges the standard library predicates
// do not cover: shared address space, "this network", IETF protocol
// assignments, NAT64 and 6to4, the last two because they embed an IPv4
// address that could be internal.
var providerDialBlockedNets = func() []*net.IPNet {
	var nets []*net.IPNet
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "64:ff9b::/96", "2002::/16"} {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// providerDialBlockedIP covers loopback, private (RFC 1918 and IPv6 ULA,
// which holds the EC2 metadata address fd00:ec2::254), link-local (which
// holds 169.254.169.254), multicast and unspecified addresses.
func providerDialBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range providerDialBlockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// guardedProviderHTTPClient is the HTTP client for a client-chosen
// endpoint. It never uses a proxy (the guard must see the real target),
// never follows a redirect off the allowlisted origin, and checks every
// dialed address with providerDialControl.
func guardedProviderHTTPClient(logger *zap.Logger, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: providerDialControl}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if tlsCfg := utils.GlobalTLSConfig(); tlsCfg != nil {
		transport.TLSClientConfig = tlsCfg.Clone()
	}
	c := utils.NewHTTPClientWithTransport(logger, timeout, transport)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// providerRetryPolicy is the retry budget the manager gives its clients.
func providerRetryPolicy() (int, time.Duration) {
	if config.Global == nil {
		return config.DefaultMaxRetries, config.DefaultInitialBackoff
	}
	return config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries),
		config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)
}
