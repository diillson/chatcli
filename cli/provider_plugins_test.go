/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/providerhost"
	"github.com/diillson/chatcli/pkg/providerproto"
	"go.uber.org/zap"
)

// registrarManager is minimalManager plus runtime provider registration.
type registrarManager struct {
	minimalManager
	mu         sync.Mutex
	registered map[string]func(string) (client.LLMClient, error)
}

func (m *registrarManager) RegisterProvider(name string, f func(string) (client.LLMClient, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered[name] = f
}

func (m *registrarManager) UnregisterProvider(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.registered, name)
}

func (m *registrarManager) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.registered[name]
	return ok
}

type memCredStore struct {
	mu    sync.Mutex
	creds map[string]providerproto.Credential
}

func (s *memCredStore) Load(p string) (providerproto.Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[p]
	return c, ok
}
func (s *memCredStore) Save(p string, c providerproto.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[p] = c
	return nil
}
func (s *memCredStore) Delete(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, p)
	return nil
}

func echoPluginSet(t *testing.T) (*providerhost.Set, *memCredStore) {
	t.Helper()
	dir := t.TempDir()
	name := "echo-provider"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name),
		"github.com/diillson/chatcli/pkg/providersdk/examples/echo").CombinedOutput()
	if err != nil {
		t.Fatalf("build echo: %v\n%s", err, out)
	}
	store := &memCredStore{creds: map[string]providerproto.Credential{}}
	set := providerhost.Load(context.Background(), providerhost.LoadOptions{Dir: dir, Store: store, CacheDir: t.TempDir()})
	t.Cleanup(func() { set.Close(context.Background()) })
	if len(set.List()) != 1 {
		t.Fatalf("echo not loaded: %v", set.Errors())
	}
	return set, store
}

func TestRegisterProviderPluginsFollowsLoginState(t *testing.T) {
	set, store := echoPluginSet(t)
	mgr := &registrarManager{registered: map[string]func(string) (client.LLMClient, error){}}
	c := &ChatCLI{logger: zap.NewNop(), manager: mgr, providerPlugins: set}

	c.registerProviderPlugins(context.Background())
	if mgr.has("ECHO") {
		t.Fatal("a plugin that requires login was registered before logging in")
	}
	_ = store.Save("ECHO", providerproto.Credential{"token": "demo"})
	c.registerProviderPlugins(context.Background())
	if !mgr.has("ECHO") {
		t.Fatal("logged-in plugin was not registered")
	}

	status := c.providerPluginStatus(context.Background())
	if !strings.Contains(status, "ECHO") {
		t.Fatalf("status = %q", status)
	}

	if !c.logoutProviderPlugin(context.Background(), "echo") || mgr.has("ECHO") {
		t.Fatal("logout must unregister the provider")
	}
	if c.logoutProviderPlugin(context.Background(), "not-a-plugin") {
		t.Fatal("unknown names must fall through")
	}
}

// A manager without runtime registration (servers, fakes) is left alone.
func TestRegisterProviderPluginsWithoutRegistrar(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), manager: &minimalManager{}}
	c.registerProviderPlugins(context.Background())
	c.closeProviderPlugins(context.Background())
	if c.loginProviderPlugin(context.Background(), "x") {
		t.Fatal("no plugins loaded: login must fall through")
	}
}

func TestSafeBrowserURL(t *testing.T) {
	cases := map[string]bool{
		"https://login.example.com/authorize": true,
		"http://127.0.0.1:8123/callback":      true,
		"http://localhost:9000/":              true,
		"http://login.example.com/":           false,
		"file:///etc/passwd":                  false,
		"javascript:alert(1)":                 false,
		"custom-scheme://x":                   false,
		"not a url":                           false,
	}
	for in, want := range cases {
		if got := safeBrowserURL(in); got != want {
			t.Errorf("safeBrowserURL(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTerminalLoginPromptReadsFields(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	go func() {
		_, _ = w.WriteString("acme-client\n2\n\n")
		_ = w.Close()
	}()

	got, err := TerminalLoginUI().Prompt(context.Background(), providerproto.AuthStep{
		Kind: providerproto.StepPrompt,
		Fields: []providerproto.PromptField{
			{ID: "client_id", Label: map[string]string{"en": "Client ID"}},
			{ID: "env", Label: map[string]string{"en": "Environment"}, Choices: []string{"dev", "prod"}},
			{ID: "region", Label: map[string]string{"en": "Region"}, Default: "sa-east-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["client_id"] != "acme-client" || got["env"] != "prod" || got["region"] != "sa-east-1" {
		t.Fatalf("answers = %v", got)
	}
}

// The server loads plugins without a terminal and shares the set with the
// ChatCLI it embeds (the colocated gateway), which must neither start the
// plugins again nor stop them on shutdown.
func TestProviderPluginsHeadlessAndShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")
	dir := filepath.Join(home, ".chatcli", "providers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := "echo-provider"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name),
		"github.com/diillson/chatcli/pkg/providersdk/examples/echo").CombinedOutput()
	if err != nil {
		t.Fatalf("build echo: %v\n%s", err, out)
	}

	ctx := context.Background()
	mgr := &registrarManager{registered: map[string]func(string) (client.LLMClient, error){}}
	set := LoadProviderPlugins(ctx, mgr, zap.NewNop())
	t.Cleanup(func() { set.Close(ctx) })
	p, ok := set.Get("ECHO")
	if !ok {
		t.Fatalf("echo not loaded headless: %v", set.Errors())
	}
	if mgr.has("ECHO") {
		t.Fatal("a plugin that requires login was registered without a credential")
	}

	embedded := &ChatCLI{logger: zap.NewNop(), manager: mgr}
	embedded.loadProviderPlugins(WithProviderPlugins(ctx, set))
	if embedded.providerPlugins != set || !embedded.providerPluginsShared {
		t.Fatal("the embedded ChatCLI must use the shared set")
	}
	embedded.closeProviderPlugins(ctx)
	if _, err := p.Models(ctx); err != nil {
		t.Fatalf("closing the embedded ChatCLI stopped the shared plugin: %v", err)
	}
}
