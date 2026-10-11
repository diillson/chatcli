/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package netguard fetches URLs that a remote party chose, without letting
// that party reach anything but the public internet.
//
// A URL handed to the process by someone else (an attachment link in an
// inbound webhook, for example) is a server-side request forgery vector:
// pointed at 169.254.169.254 it reads cloud credentials, pointed at
// 127.0.0.1 or a private range it probes services that trust the host.
// The guard is enforced in three places, because checking the URL alone is
// bypassable:
//
//  1. CheckURL, before the request: https only, a host is required, known
//     metadata hostnames are refused and a literal IP must be public.
//  2. The dialer's Control hook, after DNS resolution, on every connection:
//     the concrete IP being dialed must be public. This closes DNS rebinding
//     and covers every redirect hop, since each hop dials anew.
//  3. CheckRedirect, on every hop: the target is re-checked with CheckURL
//     and the chain is capped.
//
// The client never uses a proxy: through a proxy the dial targets the proxy,
// and the dial-time check could no longer see the real destination.
//
// The package is deliberately strict and has no knobs. Callers that need to
// reach private services on purpose (the operator's own web tools) keep
// their own, configurable policy.
package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// maxRedirects caps a redirect chain.
const maxRedirects = 5

// ErrBlocked is wrapped by every refusal, so a caller can tell a guarded
// target from a network failure with errors.Is.
var ErrBlocked = errors.New("netguard: target not allowed")

// metadataHostnames are cloud metadata names refused however they resolve.
var metadataHostnames = []string{
	"metadata.google.internal",
	"metadata.goog",
	"metadata.azure.com",
	"instance-data",
	"instance-data.ec2.internal",
}

// extraBlocked lists the non-public ranges the net.IP predicates in CheckIP
// do not cover. Loopback, RFC 1918, unique-local (which holds the AWS IPv6
// metadata address fd00:ec2::254), link-local (169.254.169.254) and
// multicast are covered by those predicates.
var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT, home of several metadata services
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, includes broadcast
	netip.MustParsePrefix("::ffff:0:0:0/96"), // IPv4-translated
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64, can embed any IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4, can embed any IPv4
	netip.MustParsePrefix("fec0::/10"),       // deprecated site-local
}

// CheckIP returns nil only for a public unicast address.
func CheckIP(ip net.IP) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return ErrBlocked
	}
	// An IPv4-mapped IPv6 address is judged as the IPv4 address it carries.
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return ErrBlocked
	}
	for _, p := range extraBlocked {
		if p.Contains(addr) {
			return ErrBlocked
		}
	}
	return nil
}

// CheckURL validates a URL before it is requested: https, a host, not a
// metadata hostname, and a public address when the host is a literal IP.
// Hostnames are checked again at dial time, after resolution.
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, ErrBlocked
	}
	if !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return nil, ErrBlocked
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || isMetadataHostname(host) {
		return nil, ErrBlocked
	}
	if ip := net.ParseIP(host); ip != nil {
		if err := CheckIP(ip); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func isMetadataHostname(host string) bool {
	for _, m := range metadataHostnames {
		if host == m || strings.HasSuffix(host, "."+m) {
			return true
		}
	}
	return false
}

// dialControl runs after resolution with the concrete address being dialed.
func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	// A post-resolution dial always carries a literal IP; anything else is
	// refused rather than trusted.
	return CheckIP(net.ParseIP(host))
}

// checkRedirect re-validates every hop and caps the chain. Credentials are
// never carried to another host.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return ErrBlocked
	}
	if _, err := CheckURL(req.URL.String()); err != nil {
		return err
	}
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("Cookie")
	}
	return nil
}

// NewClient returns an HTTP client that only reaches public addresses over
// https. It clones the process default transport, so the TLS trust the
// process configured still applies, and replaces its proxy and dialer.
func NewClient(timeout time.Duration) *http.Client {
	var tr *http.Transport
	if def, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = def.Clone()
	} else {
		tr = &http.Transport{TLSHandshakeTimeout: 10 * time.Second}
	}
	tr.Proxy = nil
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: dialControl}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}
	tr.DialTLSContext = nil
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: checkRedirect}
}
