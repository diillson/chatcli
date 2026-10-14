package providerparity

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The CRD validates provider names with a pattern instead of a closed enum
// (provider plugins have names of their own). Every built-in provider must
// still match it, or an Instance naming one would be rejected.
func TestCRDProviderPatternAdmitsEveryProvider(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	providers, err := LoadProviders(filepath.Join(root, "llm", "catalog", "catalog.go"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "operator", "config", "crd", "bases", "platform.chatcli.io_instances.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*pattern: (\^\[A-Z\]\S*)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no provider name pattern in the Instance CRD")
	}
	pattern := regexp.MustCompile(string(m[1]))
	for _, p := range providers {
		if !pattern.MatchString(p.Value) {
			t.Errorf("provider %s does not match the CRD pattern %s", p.Value, m[1])
		}
	}
	if len(providers) == 0 {
		t.Fatal("no providers loaded")
	}
}
