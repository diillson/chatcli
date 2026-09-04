package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every CHATCLI_* variable the code reads must be visible in /config.
//
// A variable that only exists in the source is a setting users cannot
// discover, cannot verify, and cannot see the effective value of — the
// class of drift that turned up once per audit. This test is the gate:
// adding a variable without registering it fails here, and a variable
// that genuinely has no place in /config has to say so out loud by
// joining the exemption list below, with a reason.
//
// It reads the repository rather than a generated list on purpose: a
// generated list is one more thing to forget to regenerate.

// configExempt lists variables that are deliberately absent from /config.
var configExempt = map[string]string{
	"CHATCLI_TEST_":            "test fixtures, never a user setting",
	"CHATCLI_PLUGIN_TEST_":     "plugin test fixtures",
	"CHATCLI_NOT_IN_FILE":      "fixture for the env-file discovery tests",
	"CHATCLI_DEFINITELY_UNSET": "fixture asserting an unset variable",
	"CHATCLI_OPERATOR_":        "read by the operator module, which has its own surface",
	// Names built at runtime from a provider or setting: the literal in
	// the source is a prefix, and /config shows the resolved values in
	// the fallback and quality sections instead.
	"CHATCLI_FALLBACK_MODEL_": "prefix; per-provider values render in the fallback section",
	"CHATCLI_QUALITY_":        "prefix; the quality pipeline renders its own settings",
}

func TestEveryEnvVarIsVisibleInConfig(t *testing.T) {
	root := repoRoot(t)
	used := envVarsInSource(t, root, func(path string) bool {
		if strings.HasSuffix(path, "_test.go") {
			return false
		}
		// The operator is a separate module with its own configuration
		// surface; cli/config does not describe it.
		return !strings.Contains(path, string(filepath.Separator)+"operator"+string(filepath.Separator))
	})
	registered := envVarsInSource(t, filepath.Join(root, "cli"), func(path string) bool {
		return strings.HasPrefix(filepath.Base(path), "config")
	})

	var missing []string
	for name := range used {
		if registered[name] || exempt(name) {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d environment variable(s) the code reads are not shown by /config.\n"+
			"Register each one in its cli/config_*.go section, or add it to configExempt with a reason:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func exempt(name string) bool {
	for prefix := range configExempt {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

var envVarPattern = regexp.MustCompile(`"(CHATCLI_[A-Z0-9_]+)"`)

// envVarsInSource collects every CHATCLI_* literal in the Go files under
// dir that keep passes.
func envVarsInSource(t *testing.T, dir string, keep func(string) bool) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || !keep(path) {
			return nil
		}
		data, readErr := os.ReadFile(path) //#nosec G304 -- repository source, test-only walk
		if readErr != nil {
			return nil
		}
		for _, m := range envVarPattern.FindAllStringSubmatch(string(data), -1) {
			found[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return found
}

// repoRoot walks up from the package directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("module root not found")
	return ""
}
