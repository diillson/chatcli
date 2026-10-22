/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearRegistryEnv scrubs every credential variable the resolver reads so a
// developer's own exports cannot leak into the assertions.
func clearRegistryEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		registryTokenEnv, registryUserEnv, registryPassEnv,
		registryLegacyTokenEnv, registryLegacyUserEnv, registryLegacyPassEnv,
		"CHATCLI_REGISTRY_GHCR_IO_TOKEN", "CHATCLI_REGISTRY_GHCR_IO_USERNAME", "CHATCLI_REGISTRY_GHCR_IO_PASSWORD",
		"CHATCLI_REGISTRY_DOCKER_IO_TOKEN", "CHATCLI_REGISTRY_HUB_DOCKER_COM_TOKEN",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // no docker config
}

func TestRegistryHostEnvKey(t *testing.T) {
	cases := map[string]string{"ghcr.io": "GHCR_IO", "localhost:5000": "LOCALHOST_5000", "my-reg.example.com": "MY_REG_EXAMPLE_COM"}
	for host, want := range cases {
		if got := registryHostEnvKey(host); got != want {
			t.Errorf("%s: got %q want %q", host, got, want)
		}
	}
	if keys := registryHostEnvKeys("hub.docker.com"); len(keys) != 2 || keys[1] != "DOCKER_IO" {
		t.Fatalf("docker hub must also answer to DOCKER_IO: %v", keys)
	}
}

func TestResolveRegistryCredentialsOrder(t *testing.T) {
	clearRegistryEnv(t)
	cases := []struct {
		name   string
		env    map[string]string
		inline registryTagsArgs
		token  string
		user   string
		source string
	}{
		{"inline wins", map[string]string{"CHATCLI_REGISTRY_GHCR_IO_TOKEN": "envtok"}, registryTagsArgs{Token: "zzsecret"}, "zzsecret", "", "inline"},
		{"per-host token", map[string]string{"CHATCLI_REGISTRY_GHCR_IO_TOKEN": "hosttok", registryTokenEnv: "generic"}, registryTagsArgs{}, "hosttok", "", "env (CHATCLI_REGISTRY_GHCR_IO_TOKEN)"},
		{"per-host pair", map[string]string{"CHATCLI_REGISTRY_GHCR_IO_USERNAME": "bob", "CHATCLI_REGISTRY_GHCR_IO_PASSWORD": "pw"}, registryTagsArgs{}, "", "bob", "env (CHATCLI_REGISTRY_GHCR_IO_USERNAME + CHATCLI_REGISTRY_GHCR_IO_PASSWORD)"},
		{"generic token", map[string]string{registryTokenEnv: "generic"}, registryTagsArgs{}, "generic", "", "env (CHATCLI_REGISTRY_TOKEN)"},
		{"generic pair", map[string]string{registryUserEnv: "u", registryPassEnv: "p"}, registryTagsArgs{}, "", "u", "env (CHATCLI_REGISTRY_USERNAME + CHATCLI_REGISTRY_PASSWORD)"},
		{"legacy token", map[string]string{registryLegacyTokenEnv: "legacy"}, registryTagsArgs{}, "legacy", "", "env (REGISTRY_TOKEN)"},
		{"none", nil, registryTagsArgs{}, "", "", registryAuthNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearRegistryEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cfg := c.inline
			resolveRegistryCredentials("https://ghcr.io", &cfg)
			if cfg.Token != c.token || cfg.Username != c.user || !strings.HasPrefix(cfg.AuthSource, c.source) {
				t.Fatalf("token=%q user=%q source=%q", cfg.Token, cfg.Username, cfg.AuthSource)
			}
			for _, secret := range []string{"envtok", "hosttok", "pw", "generic", "legacy", "zzsecret"} {
				if strings.Contains(cfg.AuthSource, secret) {
					t.Fatalf("auth source must never carry a value: %q", cfg.AuthSource)
				}
			}
		})
	}
}

func TestResolveRegistryCredentialsDockerConfig(t *testing.T) {
	clearRegistryEnv(t)
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	auth := base64.StdEncoding.EncodeToString([]byte("robot:s3cret"))
	cfgJSON := `{"auths":{"https://ghcr.io":{"auth":"` + auth + `"},"quay.io":{"identitytoken":"idtok"}},"credHelpers":{"gcr.io":"gcloud"},"credsStore":"osxkeychain"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg registryTagsArgs
	resolveRegistryCredentials("https://ghcr.io", &cfg)
	if cfg.Username != "robot" || cfg.Password != "s3cret" || !strings.HasPrefix(cfg.AuthSource, "docker-config (") || strings.Contains(cfg.AuthSource, "s3cret") {
		t.Fatalf("basic auth from docker config: %+v", cfg)
	}
	cfg = registryTagsArgs{}
	resolveRegistryCredentials("https://quay.io", &cfg)
	if cfg.Token != "idtok" || !strings.HasPrefix(cfg.AuthSource, "docker-config") {
		t.Fatalf("identity token from docker config: %+v", cfg)
	}
	cfg = registryTagsArgs{}
	resolveRegistryCredentials("https://gcr.io", &cfg)
	if cfg.hasInlineCreds() || !strings.Contains(cfg.AuthSource, "credHelper gcloud") || !strings.Contains(cfg.AuthSource, "CHATCLI_REGISTRY_GCR_IO_TOKEN") {
		t.Fatalf("credHelper host must be reported with the env var to export: %+v", cfg)
	}
	cfg = registryTagsArgs{}
	resolveRegistryCredentials("https://other.example.com", &cfg)
	if cfg.hasInlineCreds() || !strings.Contains(cfg.AuthSource, "credsStore osxkeychain") {
		t.Fatalf("credsStore must be reported: %+v", cfg)
	}
}

func TestRegistryTagsRedactsInlineSecrets(t *testing.T) {
	cfg := registryTagsArgs{Username: "bob", Password: "hunter22", Token: "tok-abc"}
	err := redactRegistrySecrets(errors.New("dial https://x?p=hunter22 token tok-abc"), cfg)
	if strings.Contains(err.Error(), "hunter22") || strings.Contains(err.Error(), "tok-abc") || strings.Count(err.Error(), "[redacted]") != 2 {
		t.Fatalf("secrets leaked: %v", err)
	}
	if e := errors.New("plain"); !errors.Is(redactRegistrySecrets(e, registryTagsArgs{}), e) {
		t.Fatal("no inline creds: error must pass through untouched")
	}
	// DescribeCall never echoes a credential, even when the image is missing
	// and the only argv tokens are secrets.
	p := NewBuiltinRegistryTagsPlugin()
	for _, args := range [][]string{
		{"--password", "hunter22"},
		{"--token=tok-abc"},
		{`{"token":"tok-abc"}`},
	} {
		if got := p.DescribeCall(args); strings.Contains(got, "hunter22") || strings.Contains(got, "tok-abc") {
			t.Fatalf("DescribeCall(%v) leaked: %q", args, got)
		}
	}
	if got := p.DescribeCall([]string{"--image", "redis", "--token", "tok-abc"}); !strings.Contains(got, "redis") || strings.Contains(got, "tok-abc") {
		t.Fatalf("DescribeCall must show the image only: %q", got)
	}
}

func TestRegistryTagsExecuteUsesEnvTokenAndReportsSource(t *testing.T) {
	clearRegistryEnv(t)
	var seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"tags":["1.0"]}`))
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	envName := "CHATCLI_REGISTRY_" + registryHostEnvKey(host) + "_TOKEN"
	t.Setenv(envName, "env-bearer")

	p := NewBuiltinRegistryTagsPlugin()
	out, err := p.Execute(context.Background(), []string{`{"image":"team/app","registry":"` + server.URL + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if seenAuth != "Bearer env-bearer" {
		t.Fatalf("env token must reach the registry, got %q", seenAuth)
	}
	if !strings.Contains(out, "auth: env ("+envName+")") || strings.Contains(out, "env-bearer") {
		t.Fatalf("output must name the source, never the value: %q", out)
	}

	// Inline creds still work (compat) but are flagged and never echoed.
	out, err = p.Execute(context.Background(), []string{`{"image":"team/app","registry":"` + server.URL + `","token":"inline-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	if seenAuth != "Bearer inline-secret" || !strings.Contains(out, "auth: inline") || strings.Contains(out, "inline-secret") || !strings.Contains(out, "CHATCLI_REGISTRY_<HOST>_TOKEN") {
		t.Fatalf("inline path: auth=%q out=%q", seenAuth, out)
	}
	if !strings.Contains(p.Usage(), "CHATCLI_REGISTRY_<HOST>_TOKEN") || !strings.Contains(p.Schema(), "DO NOT pass secrets") {
		t.Fatal("Usage/Schema must steer the model away from inline secrets")
	}
}
