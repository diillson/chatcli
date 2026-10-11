/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rest

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// dashboardCSP builds the Content-Security-Policy for the embedded
// dashboard. Its inline scripts are allowed by the SHA-256 of their exact
// text, computed from the embedded file itself, so a script injected
// through API data never runs and inline event handlers are refused.
// Styles stay inline-capable: the markup sets style attributes throughout.
// Every fetch goes to the same origin.
func dashboardCSP(static fs.FS) (string, error) {
	raw, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return "", err
	}
	hashes, err := inlineScriptHashes(raw)
	if err != nil {
		return "", err
	}
	if len(hashes) == 0 {
		return "", errors.New("dashboard has no inline script to allow")
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src " + strings.Join(hashes, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
		"object-src 'none'",
	}, "; "), nil
}

// inlineScriptHashes returns the CSP source of every executable inline
// script ('sha256-<base64>'). Data blocks such as type="application/json"
// are not scripts to the browser and need no hash.
func inlineScriptHashes(doc []byte) ([]string, error) {
	z := html.NewTokenizer(bytes.NewReader(doc))
	var hashes []string
	inScript := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("parsing dashboard: %w", err)
			}
			return hashes, nil
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			inScript = string(name) == "script" && executableScript(z, hasAttr)
		case html.TextToken:
			if inScript {
				sum := sha256.Sum256(z.Raw())
				hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
			}
		case html.EndTagToken:
			inScript = false
		}
	}
}

// executableScript reports whether the script tag being read runs as
// JavaScript: no type, or a JavaScript type, and no src.
func executableScript(z *html.Tokenizer, hasAttr bool) bool {
	for hasAttr {
		var key, val []byte
		key, val, hasAttr = z.TagAttr()
		switch string(key) {
		case "src":
			return false
		case "type":
			t := strings.ToLower(strings.TrimSpace(string(val)))
			if t != "" && t != "text/javascript" && t != "module" {
				return false
			}
		}
	}
	return true
}

// withDashboardHeaders sets the dashboard's security headers on every
// non-API response.
func withDashboardHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
