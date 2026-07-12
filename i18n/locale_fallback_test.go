package i18n

import (
	"runtime"
	"testing"

	"golang.org/x/text/language"
)

// TestSystemLocaleFallbackShape: outside Windows the system-locale hook is
// inert (Unix shells export LANG); on Windows it must return either empty
// or a parseable IETF tag — the contract initI18n depends on.
func TestSystemLocaleFallbackShape(t *testing.T) {
	got := systemLocale()
	if runtime.GOOS != "windows" {
		if got != "" {
			t.Fatalf("systemLocale must be inert off-Windows, got %q", got)
		}
		return
	}
	if got == "" {
		return // API unavailable — English fallback is acceptable
	}
	if _, err := language.Parse(got); err != nil {
		t.Fatalf("systemLocale returned unparseable tag %q: %v", got, err)
	}
}

// TestDetectLangStringChain locks the detection priority and normalization:
// CHATCLI_LANG wins, LC_ALL/LANG follow, "pt_BR.UTF-8" normalizes to pt-BR,
// and with nothing set the system-locale hook decides (inert off-Windows).
func TestDetectLangStringChain(t *testing.T) {
	t.Setenv("CHATCLI_LANG", "pt_BR.UTF-8")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	if got := detectLangString(); got != "pt-BR" {
		t.Fatalf("CHATCLI_LANG must win and normalize, got %q", got)
	}

	t.Setenv("CHATCLI_LANG", "")
	if got := detectLangString(); got != "en-US" {
		t.Fatalf("LC_ALL must be second, got %q", got)
	}

	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "pt_BR")
	if got := detectLangString(); got != "pt-BR" {
		t.Fatalf("LANG must be third, got %q", got)
	}

	t.Setenv("LANG", "")
	got := detectLangString()
	if runtime.GOOS != "windows" && got != "" {
		t.Fatalf("with nothing set off-Windows, detection must be empty, got %q", got)
	}
}
