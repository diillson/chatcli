/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * `security` subcommand — a read-only security-posture snapshot of an API
 * endpoint. Everything here is observation, not exploitation: a GET for the
 * response headers/cookies and a single CORS preflight OPTIONS. It reports what
 * a defender would want to confirm — TLS, the standard security headers,
 * whether the CORS policy is dangerously permissive, cookie hardening flags,
 * the auth challenge and the OIDC discovery document.
 */
package plugins

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// securityHeader is one row of the posture table.
type securityHeader struct {
	name  string
	label string
	// advice is shown when the header is absent.
	advice string
}

var trackedSecurityHeaders = []securityHeader{
	{"Strict-Transport-Security", "HSTS", "no HSTS — downgrade/MITM protection missing"},
	{"Content-Security-Policy", "CSP", "no CSP"},
	{"X-Frame-Options", "X-Frame-Options", "clickjacking protection missing"},
	{"X-Content-Type-Options", "X-Content-Type-Options", "MIME-sniffing protection missing (expected: nosniff)"},
	{"Referrer-Policy", "Referrer-Policy", "no Referrer-Policy"},
	{"Permissions-Policy", "Permissions-Policy", "no Permissions-Policy"},
	{"Cross-Origin-Opener-Policy", "COOP", ""},
	{"Cross-Origin-Resource-Policy", "CORP", ""},
}

func apiExplorerSecurity(ctx context.Context, target string) (string, error) {
	safe, err := validateWebTarget(target)
	if err != nil {
		return "", fmt.Errorf("refusing %q: %w", target, err)
	}
	status, header, _, err := apiExplorerGet(ctx, safe)
	if err != nil {
		return "", fmt.Errorf("could not reach %s: %w", safe, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Security posture: %s\n\n", safe)
	fmt.Fprintf(&b, "Responded HTTP %d.\n\n", status)

	// Transport.
	b.WriteString("## Transport\n\n")
	if strings.HasPrefix(safe, "https://") {
		b.WriteString("- ✅ HTTPS\n")
	} else {
		b.WriteString("- ⚠ plain HTTP — traffic is unencrypted\n")
	}
	if hsts := header.Get("Strict-Transport-Security"); hsts != "" {
		fmt.Fprintf(&b, "- HSTS: %s\n", hsts)
	}
	b.WriteString("\n")

	// Security headers table.
	b.WriteString("## Security headers\n\n")
	for _, sh := range trackedSecurityHeaders {
		if v := strings.TrimSpace(header.Get(sh.name)); v != "" {
			fmt.Fprintf(&b, "- ✅ %s: %s\n", sh.label, firstLine(v))
		} else if sh.advice != "" {
			fmt.Fprintf(&b, "- ⚠ %s missing — %s\n", sh.label, sh.advice)
		} else {
			fmt.Fprintf(&b, "- ○ %s: not set\n", sh.label)
		}
	}
	b.WriteString("\n")

	// CORS preflight — live probe with a foreign origin.
	b.WriteString("## CORS\n\n")
	b.WriteString(analyzeCORS(ctx, safe))
	b.WriteString("\n")

	// Auth.
	b.WriteString("## Authentication\n\n")
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		fmt.Fprintf(&b, "- Endpoint is gated (HTTP %d)\n", status)
	}
	if ch := detectAuthChallenge(header); ch != "" {
		fmt.Fprintf(&b, "- WWW-Authenticate: %s\n", ch)
	} else {
		b.WriteString("- No auth challenge on this response\n")
	}
	if rl := rateLimitSignal(header); rl != "" {
		fmt.Fprintf(&b, "- Rate limiting: %s\n", rl)
	}
	b.WriteString("\n")

	// Cookies.
	if cookies := analyzeCookies(header); cookies != "" {
		b.WriteString("## Cookies\n\n")
		b.WriteString(cookies)
		b.WriteString("\n")
	}

	// Framework fingerprint.
	if tech := fingerprintTech(header); len(tech) > 0 {
		b.WriteString("## Server\n\n")
		for _, t := range tech {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		b.WriteString("\n")
	}

	// OIDC discovery.
	if oidc := fetchOIDC(ctx, originOf(safe)); oidc != nil {
		b.WriteString(renderOIDC(oidc))
	}

	return b.String(), nil
}

// analyzeCORS issues a preflight OPTIONS with a foreign Origin and reports the
// server's CORS decision, flagging the dangerous combinations.
func analyzeCORS(ctx context.Context, target string) string {
	const probeOrigin = "https://api-explorer-probe.example.com"
	_, header, err := apiExplorerRequest(ctx, http.MethodOptions, target, map[string]string{
		"Origin":                        probeOrigin,
		"Access-Control-Request-Method": "GET",
	})
	if err != nil {
		return "- (preflight OPTIONS failed: " + err.Error() + ")\n"
	}
	acao := strings.TrimSpace(header.Get("Access-Control-Allow-Origin"))
	acam := strings.TrimSpace(header.Get("Access-Control-Allow-Methods"))
	acah := strings.TrimSpace(header.Get("Access-Control-Allow-Headers"))
	acac := strings.EqualFold(header.Get("Access-Control-Allow-Credentials"), "true")

	var b strings.Builder
	if acao == "" {
		return "- No CORS headers on the preflight (same-origin only, or CORS disabled)\n"
	}
	fmt.Fprintf(&b, "- Access-Control-Allow-Origin: %s\n", acao)
	if acam != "" {
		fmt.Fprintf(&b, "- Allow-Methods: %s\n", acam)
	}
	if acah != "" {
		fmt.Fprintf(&b, "- Allow-Headers: %s\n", acah)
	}
	if acac {
		b.WriteString("- Allow-Credentials: true\n")
	}
	switch {
	case acao == probeOrigin && acac:
		b.WriteString("- ⚠ **reflects an arbitrary Origin with credentials** — any site can make authenticated cross-origin requests\n")
	case acao == "*" && acac:
		b.WriteString("- ⚠ wildcard origin with credentials (browsers block this combo, but it signals a misconfiguration)\n")
	case acao == probeOrigin:
		b.WriteString("- ⚠ reflects an arbitrary Origin (permissive; risky if any endpoint relies on Origin)\n")
	case acao == "*":
		b.WriteString("- Note: wildcard origin (fine for public, unauthenticated data)\n")
	}
	return b.String()
}

// analyzeCookies reports the hardening flags on each Set-Cookie.
func analyzeCookies(header http.Header) string {
	raw := header.Values("Set-Cookie")
	if len(raw) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range raw {
		name := strings.SplitN(c, "=", 2)[0]
		low := strings.ToLower(c)
		var flags []string
		if strings.Contains(low, "; secure") || strings.HasSuffix(low, "secure") {
			flags = append(flags, "Secure")
		} else {
			flags = append(flags, "⚠ no Secure")
		}
		if strings.Contains(low, "httponly") {
			flags = append(flags, "HttpOnly")
		} else {
			flags = append(flags, "⚠ no HttpOnly")
		}
		if i := strings.Index(low, "samesite="); i >= 0 {
			flags = append(flags, "SameSite="+strings.SplitN(low[i+len("samesite="):], ";", 2)[0])
		} else {
			flags = append(flags, "⚠ no SameSite")
		}
		fmt.Fprintf(&b, "- `%s`: %s\n", strings.TrimSpace(name), strings.Join(flags, ", "))
	}
	return b.String()
}
