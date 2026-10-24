package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/stateroot"
)

// The environment overrides apply on the very first run too, when
// registries.yaml does not exist yet — and are never written into it.
func TestLoadConfig_FirstRunHonorsEnvOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(stateroot.Env, filepath.Join(home, "state"))
	install := filepath.Join(home, "skills-here")
	t.Setenv("CHATCLI_SKILL_INSTALL_DIR", install)
	t.Setenv("CHATCLI_REGISTRY_URLS", "https://skills.example.test/api")
	t.Setenv("CHATCLI_REGISTRY_DISABLE", "")

	for run := 1; run <= 2; run++ {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.InstallDir != install {
			t.Fatalf("run %d: install dir = %q, want %q", run, cfg.InstallDir, install)
		}
		found := false
		for _, r := range cfg.Registries {
			found = found || r.URL == "https://skills.example.test/api"
		}
		if !found {
			t.Fatalf("run %d: CHATCLI_REGISTRY_URLS not applied: %+v", run, cfg.Registries)
		}
	}
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "skills-here") || strings.Contains(string(data), "skills.example.test") {
		t.Fatalf("environment values persisted into registries.yaml:\n%s", data)
	}
}
