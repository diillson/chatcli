/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// withTempMCPHome points the config path at a temp HOME so tests never touch
// the real ~/.chatcli/mcp_servers.json.
func withTempMCPHome(t *testing.T) string {
	t.Helper()
	i18n.Init()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".chatcli", "mcp_servers.json")
}

func TestMCPConfig_AddListGetRemove(t *testing.T) {
	path := withTempMCPHome(t)

	// stdio add with env and command line after --.
	err := RunMCPConfig([]string{"add", "fs", "--env", "TOKEN=abc", "--",
		"npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var file mcpConfigFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	if len(file.Servers) != 1 || file.Servers[0].Name != "fs" {
		t.Fatalf("unexpected servers: %+v", file.Servers)
	}
	s := file.Servers[0]
	if s.Command != "npx" || len(s.Args) != 3 || s.Env["TOKEN"] != "abc" || !s.Enabled {
		t.Fatalf("stdio fields wrong: %+v", s)
	}

	// sse add with header.
	if err := RunMCPConfig([]string{"add", "--transport", "sse", "linear",
		"https://mcp.linear.app/sse", "--header", "Authorization: Bearer x"}); err != nil {
		t.Fatalf("sse add: %v", err)
	}

	// Re-adding the same name replaces, not duplicates.
	if err := RunMCPConfig([]string{"add", "fs", "--", "npx", "-y", "other-server"}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &file)
	if len(file.Servers) != 2 {
		t.Fatalf("re-add must replace, got %d servers", len(file.Servers))
	}

	// list and get run without error; remove deletes.
	if err := RunMCPConfig([]string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := RunMCPConfig([]string{"get", "linear"}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := RunMCPConfig([]string{"remove", "fs"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &file)
	if len(file.Servers) != 1 || file.Servers[0].Name != "linear" {
		t.Fatalf("remove failed: %+v", file.Servers)
	}
	if err := RunMCPConfig([]string{"remove", "ghost"}); err == nil {
		t.Fatal("removing a missing server must error")
	}
}

func TestMCPConfig_AddValidation(t *testing.T) {
	withTempMCPHome(t)
	cases := [][]string{
		{"add"},      // no name
		{"add", "x"}, // stdio without command
		{"add", "x", "--env", "notkv", "--", "cmd"}, // bad env
		{"add", "--transport", "sse", "x"},          // sse without url
		{"add", "--transport", "weird", "x", "--", "cmd"},
		{"nonsense"},
	}
	for _, c := range cases {
		if err := RunMCPConfig(c); err == nil {
			t.Errorf("expected error for %v", c)
		}
	}
}

func TestMCPConfig_PreservesUnknownKeysAndDisabled(t *testing.T) {
	path := withTempMCPHome(t)
	seed := `{"mcpServers":[{"name":"keep","transport":"stdio","command":"x","enabled":true,"customExtension":{"a":1}}]}`
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunMCPConfig([]string{"add", "new", "--disabled", "--", "cmd"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "customExtension") {
		t.Fatalf("unknown keys must survive a rewrite: %s", data)
	}
	var file mcpConfigFile
	_ = json.Unmarshal(data, &file)
	for _, s := range file.Servers {
		if s.Name == "new" && s.Enabled {
			t.Fatal("--disabled must persist enabled=false")
		}
	}
}
