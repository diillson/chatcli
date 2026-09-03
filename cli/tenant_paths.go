/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Per-tenant paths for the stores that resolve their directory
 * themselves: parked runs and task-graph runs follow the active state
 * root (the tenant root under the gateway), so principals never see or
 * resume each other's work. The scheduler daemon, the conversation hub
 * database and the tokenizer vocabulary cache stay process-wide by
 * design: the first two are the cross-process bus, the third holds
 * public data.
 */
package cli

import (
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/taskgraph"
)

// tenantRootActive reports whether the state root is a tenant root.
func (cli *ChatCLI) tenantRootActive() bool {
	if cli == nil || cli.stateRoot == "" {
		return false
	}
	sep := string(filepath.Separator)
	return strings.Contains(cli.stateRoot, sep+"tenants"+sep)
}

// taskGraphBaseDir is where task-graph runs live for the active root.
func (cli *ChatCLI) taskGraphBaseDir() (string, error) {
	if cli.tenantRootActive() {
		return filepath.Join(cli.stateRoot, "taskgraph"), nil
	}
	return taskgraph.DefaultBaseDir()
}

// applyTenantPaths points the self-resolving stores at the active root
// (called on every tenant swap; the global set restores the defaults).
func (cli *ChatCLI) applyTenantPaths() {
	if cli.tenantRootActive() {
		park.SetBaseDir(filepath.Join(cli.stateRoot, "parked"))
		return
	}
	park.SetBaseDir("")
}
