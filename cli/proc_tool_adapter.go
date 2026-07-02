/*
 * ChatCLI - Adapter binding the @proc tool to the session process supervisor.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.ProcAdapter over cli/agent/proc: a lazily created,
 * session-scoped Supervisor whose command vetting is the SAME
 * agent.CommandValidator the one-shot exec path uses — @proc is never a side
 * door around policy. Every process still running dies with the session.
 */
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/proc"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/utils"
)

// procToolAdapter is the concrete plugins.ProcAdapter.
type procToolAdapter struct {
	cli *ChatCLI
}

// supervisor returns the session supervisor, creating it on first use.
func (a *procToolAdapter) supervisor() *proc.Supervisor {
	a.cli.procSupOnce.Do(func() {
		validator := agent.NewCommandValidator(a.cli.logger)
		a.cli.procSup = proc.NewSupervisor(validator.ValidateCommand, a.cli.logger)
	})
	return a.cli.procSup
}

// Start implements plugins.ProcAdapter.
func (a *procToolAdapter) Start(command, dir string) (string, error) {
	if dir != "" {
		if expanded, err := utils.ExpandPath(dir); err == nil {
			dir = expanded
		}
	}
	info, err := a.supervisor().Start(command, dir)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("started %s (pid %d): %s\nPoll readiness with @proc logs {\"id\":%q} before testing against it.",
		info.ID, info.PID, info.Command, info.ID), nil
}

// Status implements plugins.ProcAdapter.
func (a *procToolAdapter) Status(id string) (string, error) {
	info, err := a.supervisor().Status(id)
	if err != nil {
		return "", err
	}
	return formatProcInfo(info), nil
}

// Logs implements plugins.ProcAdapter.
func (a *procToolAdapter) Logs(id string, tail int) (string, error) {
	logs, info, err := a.supervisor().Logs(id, tail)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(logs) == "" {
		return fmt.Sprintf("%s — no output yet.", formatProcInfo(info)), nil
	}
	return fmt.Sprintf("%s\n---\n%s", formatProcInfo(info), logs), nil
}

// Stop implements plugins.ProcAdapter.
func (a *procToolAdapter) Stop(id string) (string, error) {
	info, err := a.supervisor().Stop(id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("stopped %s (exit code %d)", info.ID, info.ExitCode), nil
}

// Remove implements plugins.ProcAdapter.
func (a *procToolAdapter) Remove(id string) (string, error) {
	if err := a.supervisor().Remove(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed %s", id), nil
}

// List implements plugins.ProcAdapter.
func (a *procToolAdapter) List() (string, error) {
	infos := a.supervisor().List()
	if len(infos) == 0 {
		return "No background processes tracked. Start one with @proc start.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d process(es):\n", len(infos))
	for _, info := range infos {
		b.WriteString("- " + formatProcInfo(info) + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// formatProcInfo renders one process snapshot as a single line.
func formatProcInfo(info proc.Info) string {
	cmd := info.Command
	if len(cmd) > 80 {
		cmd = cmd[:77] + "..."
	}
	if info.State == proc.StateRunning {
		return fmt.Sprintf("%s running (pid %d, up %s): %s",
			info.ID, info.PID, time.Since(info.Started).Round(time.Second), cmd)
	}
	return fmt.Sprintf("%s exited (code %d, ran %s): %s",
		info.ID, info.ExitCode, info.Ended.Sub(info.Started).Round(time.Second), cmd)
}

// compile-time assertion that the adapter satisfies the plugin interface.
var _ plugins.ProcAdapter = (*procToolAdapter)(nil)

// shutdownProcSupervisor stops every background process the @proc tool
// started. Called from the session cleanup path.
func (cli *ChatCLI) shutdownProcSupervisor() {
	if cli.procSup != nil {
		cli.procSup.CloseAll()
	}
}
