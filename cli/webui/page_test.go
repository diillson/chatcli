/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// The bundled page is self-contained (no external resources: the CSP would
// block them and the UI must work offline), carries the boot placeholder,
// and every string the page can show has a translation key.
func TestPage_IsBundledSelfContainedAndTranslated(t *testing.T) {
	page := string(Page())
	if page == "" || !strings.Contains(page, bootPlaceholder) {
		t.Fatal("the bundled page must exist and carry the boot placeholder")
	}
	for _, forbidden := range []string{"<link ", "https://", "http://", "@import", "@font-face", "<script src="} {
		if strings.Contains(page, forbidden) {
			t.Errorf("page references an external resource: %q", forbidden)
		}
	}
	// Every key the page's default table declares is a known UI key, and
	// every key the server ships exists in the page's defaults.
	defaults := map[string]bool{}
	block := page[strings.Index(page, "const DEFAULTS = {"):strings.Index(page, "};\n// The server ships every switchable language")]
	for _, m := range regexp.MustCompile(`'([a-zA-Z0-9_.]+)':`).FindAllStringSubmatch(block, -1) {
		defaults[m[1]] = true
	}
	known := map[string]bool{}
	for _, k := range uiKeys {
		known[k] = true
		if !defaults[k] {
			t.Errorf("uiKeys has %q but the page has no default for it", k)
		}
	}
	for k := range defaults {
		if !known[k] {
			t.Errorf("page default %q is not in uiKeys, so it never gets translated", k)
		}
	}
	for _, m := range regexp.MustCompile(`data-i18n(?:-placeholder)?="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		if !known[m[1]] {
			t.Errorf("markup uses unknown i18n key %q", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`\bt\('([a-zA-Z0-9_.]+)'`).FindAllStringSubmatch(page, -1) {
		if !known[m[1]] && !strings.HasPrefix(m[1], "mode.") {
			t.Errorf("script uses unknown i18n key %q", m[1])
		}
	}
	strs := UIStrings()
	if len(strs) != len(uiKeys) {
		t.Fatalf("UIStrings has %d entries for %d keys", len(strs), len(uiKeys))
	}
}

// The server serves the bundled page with the boot JSON injected and the
// process strings on it.
func TestServer_ServesBundledPageWithBoot(t *testing.T) {
	fb := newFakeBackend()
	srv, err := Start(Options{Backend: fb, Version: "9.9", Lang: "pt-BR"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()
	code, body := call(t, srv, http.MethodGet, "/", nil, false)
	if code != http.StatusOK || strings.Contains(string(body), bootPlaceholder) || !strings.Contains(string(body), "id=\"composer\"") {
		t.Fatalf("index = %d, page not rendered", code)
	}
	start := strings.Index(string(body), "const BOOT = ") + len("const BOOT = ")
	end := strings.Index(string(body)[start:], ";\n")
	var b boot
	if err := json.Unmarshal(body[start:start+end], &b); err != nil {
		t.Fatalf("boot JSON: %v", err)
	}
	if b.Version != "9.9" || b.Lang != "pt-BR" || b.Token != srv.Token() || b.Strings["composer.send"] == "" {
		t.Fatalf("boot = %+v", b)
	}
}
