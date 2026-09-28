/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// fakeRemoteBackend stands in for *remote.Client: an LLM client plus the
// discovery RPCs, answered from memory.
type fakeRemoteBackend struct {
	closed  int
	plugins []remote.RemotePluginInfo
	agents  []remote.RemoteAgentInfo
	skills  []remote.RemoteSkillInfo
}

func (f *fakeRemoteBackend) GetModelName() string { return "remote-model" }
func (f *fakeRemoteBackend) GetProvider() string  { return "REMOTE" }
func (f *fakeRemoteBackend) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	return "", nil
}
func (f *fakeRemoteBackend) Close() error { f.closed++; return nil }
func (f *fakeRemoteBackend) ListRemotePlugins(context.Context) ([]remote.RemotePluginInfo, error) {
	return f.plugins, nil
}
func (f *fakeRemoteBackend) ListRemoteAgents(context.Context) ([]remote.RemoteAgentInfo, error) {
	return f.agents, nil
}
func (f *fakeRemoteBackend) ListRemoteSkills(context.Context) ([]remote.RemoteSkillInfo, error) {
	return f.skills, nil
}

// stubPlugin is what the factory hands the plugin manager for a server
// plugin in these tests.
type stubPlugin struct{ name string }

func (p stubPlugin) Name() string        { return p.name }
func (p stubPlugin) Description() string { return "" }
func (p stubPlugin) Usage() string       { return "" }
func (p stubPlugin) Version() string     { return "" }
func (p stubPlugin) Path() string        { return "[remote]" }
func (p stubPlugin) Schema() string      { return "" }
func (p stubPlugin) Execute(context.Context, []string) (string, error) {
	return "", nil
}
func (p stubPlugin) ExecuteWithStream(context.Context, []string, func(string)) (string, error) {
	return "", nil
}

func newRemoteAttachTestCLI(t *testing.T) *ChatCLI {
	t.Helper()
	pm, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("plugin manager: %v", err)
	}
	t.Cleanup(pm.Close)
	local := &fakeRemoteBackend{}
	return &ChatCLI{
		logger:        zap.NewNop(),
		pluginManager: pm,
		Client:        local,
		Provider:      "LOCAL",
		Model:         "local-model",
	}
}

// Binding a server connection (what `chatcli connect` and /connect share)
// marks the session remote, keeps the local client for /disconnect and
// registers the server's plugins, agents and skills.
func TestBindRemote_SetsRemoteStateAndDiscoversResources(t *testing.T) {
	c := newRemoteAttachTestCLI(t)
	localClient := c.Client
	backend := &fakeRemoteBackend{
		plugins: []remote.RemotePluginInfo{{Name: "@k8s-audit"}},
		agents:  []remote.RemoteAgentInfo{{Name: "sre", Description: "on-call helper"}},
		skills:  []remote.RemoteSkillInfo{{Name: "rollback"}},
	}
	factory := func(info remote.RemotePluginInfo) plugins.Plugin { return stubPlugin{name: info.Name} }

	c.bindRemote(context.Background(), backend, "chatcli.example.com:443", "connect")
	c.discoverRemoteResources(context.Background(), backend, factory)

	if !c.isRemote || c.remoteConn == nil || c.remoteAddress != "chatcli.example.com:443" {
		t.Fatalf("remote state not set: isRemote=%v conn=%v addr=%q", c.isRemote, c.remoteConn, c.remoteAddress)
	}
	if c.Client != backend || c.Provider != "REMOTE" || c.Model != "remote-model" {
		t.Fatalf("session not swapped onto the server: %v %q %q", c.Client, c.Provider, c.Model)
	}
	if c.localClient != localClient || c.localProvider != "LOCAL" || c.localModel != "local-model" {
		t.Fatalf("local client not kept for /disconnect: %v %q %q", c.localClient, c.localProvider, c.localModel)
	}
	if _, ok := c.pluginManager.GetPlugin("@k8s-audit"); !ok {
		t.Fatal("server plugin not registered")
	}
	if len(c.remoteAgents) != 1 || len(c.remoteSkills) != 1 {
		t.Fatalf("remote agents/skills not cached: %d/%d", len(c.remoteAgents), len(c.remoteSkills))
	}

	// /agent list and /agent skills show the cached server entries after
	// the local ones.
	c.personaHandler = NewPersonaHandler(zap.NewNop())
	ch := &CommandHandler{cli: c}
	out := captureSessionStdout(t, func() { ch.handleAgentPersonaSubcommand("/agent list") })
	if !strings.Contains(out, "sre") || !strings.Contains(out, "on-call helper") || !strings.Contains(out, "chatcli.example.com:443") {
		t.Fatalf("remote agents section: %q", out)
	}
	out = captureSessionStdout(t, func() { ch.handleAgentPersonaSubcommand("/agent skills") })
	if !strings.Contains(out, "rollback") {
		t.Fatalf("remote skills section: %q", out)
	}

	// /disconnect restores the local client and closes the connection.
	_ = captureSessionStdout(t, func() { ch.handleDisconnectCommand(context.Background()) })
	if c.isRemote || c.Client != localClient || c.Provider != "LOCAL" || backend.closed != 1 {
		t.Fatalf("disconnect: isRemote=%v client=%v provider=%q closed=%d", c.isRemote, c.Client, c.Provider, backend.closed)
	}
	if _, ok := c.pluginManager.GetPlugin("@k8s-audit"); ok {
		t.Fatal("server plugin still registered after /disconnect")
	}
	if len(c.remoteAgents) != 0 || len(c.remoteSkills) != 0 {
		t.Fatal("remote agents/skills cache not cleared after /disconnect")
	}
	if out := captureSessionStdout(t, c.printRemoteAgents); out != "" {
		t.Fatalf("remote agents printed while local: %q", out)
	}
}
