/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/diillson/chatcli/llm/providerhost"
	"github.com/diillson/chatcli/pkg/providerproto"
	"go.uber.org/zap"
)

type serverMemStore map[string]providerproto.Credential

func (s serverMemStore) Load(p string) (providerproto.Credential, bool) { c, ok := s[p]; return c, ok }
func (s serverMemStore) Save(p string, c providerproto.Credential) error {
	s[p] = c
	return nil
}
func (s serverMemStore) Delete(p string) error { delete(s, p); return nil }

// A plugin has no catalog default model, so the server takes the first one
// the plugin lists — and only for a plugin that can serve requests.
func TestApplyProviderPluginDefaultModel(t *testing.T) {
	dir := t.TempDir()
	name := "echo-provider"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name),
		"github.com/diillson/chatcli/pkg/providersdk/examples/echo").CombinedOutput(); err != nil {
		t.Fatalf("build echo: %v\n%s", err, out)
	}
	store := serverMemStore{}
	ctx := context.Background()
	set := providerhost.Load(ctx, providerhost.LoadOptions{Dir: dir, Store: store, CacheDir: t.TempDir(), Logger: zap.NewNop()})
	t.Cleanup(func() { set.Close(ctx) })

	opts := &ServerOptions{Provider: "ECHO"}
	applyProviderPluginDefaultModel(ctx, set, opts)
	if opts.Model != "" {
		t.Fatalf("a plugin without its credential must not pick a model, got %q", opts.Model)
	}
	store["ECHO"] = providerproto.Credential{"token": "demo"}
	applyProviderPluginDefaultModel(ctx, set, opts)
	if opts.Model != "echo-1" {
		t.Fatalf("Model = %q, want the plugin's first model", opts.Model)
	}
	kept := &ServerOptions{Provider: "ECHO", Model: "pinned"}
	applyProviderPluginDefaultModel(ctx, set, kept)
	builtin := &ServerOptions{Provider: "OPENAI"}
	applyProviderPluginDefaultModel(ctx, set, builtin)
	if kept.Model != "pinned" || builtin.Model != "" {
		t.Fatalf("explicit model or built-in provider changed: %q %q", kept.Model, builtin.Model)
	}
}
