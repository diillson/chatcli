/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"go.uber.org/zap"
)

// SSRFValidator prevents Server-Side Request Forgery by blocking requests to
// private, loopback, and cloud metadata IP ranges.
type SSRFValidator struct {
	allowHTTP bool
	logger    *zap.Logger
	// blockedCIDRs are the private/internal networks that must never be reached.
	blockedCIDRs []*net.IPNet
}

// NewSSRFValidator creates a validator configured from environment.
// Set CHATCLI_ALLOW_HTTP_PROVIDERS=true to permit non-TLS provider URLs.
func NewSSRFValidator(logger *zap.Logger) *SSRFValidator {
	v := &SSRFValidator{
		allowHTTP: strings.EqualFold(os.Getenv("CHATCLI_ALLOW_HTTP_PROVIDERS"), "true"),
		logger:    logger.Named("ssrf"),
	}
	v.initBlockedCIDRs()
	return v
}

func (v *SSRFValidator) initBlockedCIDRs() {
	cidrs := []string{
		// IPv4 private ranges
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		// Link-local (cloud metadata)
		"169.254.0.0/16",
		// Shared address space
		"100.64.0.0/10",
		// IPv6
		"::1/128",       // loopback
		"fc00::/7",      // unique local
		"fe80::/10",     // link-local
		"fd00::/8",      // private
		"ff00::/8",      // multicast
		"::ffff:0:0/96", // IPv4-mapped
	}

	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			v.blockedCIDRs = append(v.blockedCIDRs, network)
		}
	}
}

// ValidateProviderURL checks whether a provider base URL is safe to connect to.
// Returns an error if the URL targets internal/private infrastructure.
func (v *SSRFValidator) ValidateProviderURL(rawURL string) error {
	if rawURL == "" {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid provider URL: %w", err)
	}

	// Scheme validation
	switch parsed.Scheme {
	case "https":
		// always allowed
	case "http":
		if !v.allowHTTP {
			v.logger.Warn("SSRF: blocked non-HTTPS provider URL",
				zap.String("url", sanitizeURLForLog(rawURL)),
			)
			return fmt.Errorf("non-HTTPS provider URLs are blocked; set CHATCLI_ALLOW_HTTP_PROVIDERS=true to allow")
		}
	default:
		return fmt.Errorf("unsupported URL scheme %q, only https (and optionally http) are allowed", parsed.Scheme)
	}

	// Resolve hostname to IP(s) and check each
	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("provider URL has no hostname")
	}

	// Check for direct IP usage
	if ip := net.ParseIP(hostname); ip != nil {
		if v.isBlockedIP(ip) {
			v.logger.Warn("SSRF: blocked direct IP provider URL",
				zap.String("ip", ip.String()),
			)
			return fmt.Errorf("provider URL resolves to blocked IP address %s", ip.String())
		}
		return nil
	}

	// Block well-known cloud metadata hostnames
	lowerHost := strings.ToLower(hostname)
	blockedHosts := []string{
		"metadata.google.internal",
		"metadata.goog",
		"instance-data",
	}
	for _, bh := range blockedHosts {
		if lowerHost == bh || strings.HasSuffix(lowerHost, "."+bh) {
			v.logger.Warn("SSRF: blocked cloud metadata hostname",
				zap.String("hostname", hostname),
			)
			return fmt.Errorf("provider URL targets cloud metadata service %q", hostname)
		}
	}

	// DNS resolution check
	ips, err := net.LookupIP(hostname)
	if err != nil {
		// DNS failure is not necessarily SSRF, but log it
		v.logger.Debug("SSRF: DNS lookup failed", zap.String("hostname", hostname), zap.Error(err))
		return nil // allow — the actual HTTP client will fail later
	}

	for _, ip := range ips {
		if v.isBlockedIP(ip) {
			v.logger.Warn("SSRF: hostname resolves to blocked IP",
				zap.String("hostname", hostname),
				zap.String("ip", ip.String()),
			)
			return fmt.Errorf("provider URL hostname %q resolves to blocked IP %s", hostname, ip.String())
		}
	}

	return nil
}

// ValidateProviderConfig checks provider_config map for SSRF-prone fields.
func (v *SSRFValidator) ValidateProviderConfig(config map[string]string) error {
	// Fields that contain URLs which could be used for SSRF
	urlFields := []string{"base_url", "api_base", "endpoint", "url", "host", "server_url", "realm_url"}

	for _, field := range urlFields {
		if val, ok := config[field]; ok && val != "" {
			if err := v.ValidateProviderURL(val); err != nil {
				return fmt.Errorf("provider_config[%s]: %w", field, err)
			}
		}
	}
	return nil
}

func (v *SSRFValidator) isBlockedIP(ip net.IP) bool {
	for _, cidr := range v.blockedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// sanitizeURLForLog removes credentials from a URL before logging.
func sanitizeURLForLog(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "[invalid-url]"
	}
	parsed.User = nil
	// Remove query params that might contain secrets
	parsed.RawQuery = ""
	return parsed.String()
}
