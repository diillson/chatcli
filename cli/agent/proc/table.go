/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * table.go — one session's view of the supervisor. The supervisor belongs
 * to the chatcli process, and a server process (ACP, web, gateway) serves
 * many sessions through it: unscoped, every session listed, read and
 * stopped every other session's processes. A Table starts processes tagged
 * with its owner and finds only those; another owner's process answers
 * like an id that was never issued. The Supervisor's own methods remain
 * the process-wide view (shutdown, telemetry).
 */
package proc

import (
	"context"
	"regexp"
	"time"
)

type ownerKey struct{}

// WithOwner returns ctx marked with the session whose process table the
// calls made under it use.
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerKey{}, owner)
}

// OwnerFrom returns the session ctx was marked with, "" when none was.
func OwnerFrom(ctx context.Context) string {
	owner, _ := ctx.Value(ownerKey{}).(string)
	return owner
}

// Table is the supervisor scoped to one owner. Its methods are the
// Supervisor's of the same name over that owner's processes only.
type Table struct {
	s     *Supervisor
	owner string
}

// Table returns the view of the processes owner started. The empty owner
// is the terminal session, which is also what Supervisor.Start tags.
func (s *Supervisor) Table(owner string) *Table { return &Table{s: s, owner: owner} }

func (t *Table) Start(command, dir string) (Info, error) { return t.s.startFor(t.owner, command, dir) }

func (t *Table) StartPTY(command, dir string) (Info, error) {
	return t.s.startPTYFor(t.owner, command, dir)
}

func (t *Table) Stdin(id, text string, pressEnter bool) (Info, error) {
	return t.s.stdinFor(&t.owner, id, text, pressEnter)
}

func (t *Table) Status(id string) (Info, error) { return t.s.statusFor(&t.owner, id) }

func (t *Table) Logs(id string, tailLines int) (string, Info, error) {
	return t.s.logsFor(&t.owner, id, tailLines)
}

func (t *Table) LogsMatching(id string, re *regexp.Regexp, tailLines int) (lines []string, matched, total int, info Info, err error) {
	return t.s.logsMatchingFor(&t.owner, id, re, tailLines)
}

func (t *Table) WaitFor(ctx context.Context, id string, re *regexp.Regexp, timeout time.Duration) (WaitResult, error) {
	return t.s.waitForOwned(ctx, &t.owner, id, re, timeout)
}

func (t *Table) Stop(id string) (Info, error) { return t.s.stopFor(&t.owner, id) }

func (t *Table) StopAsync(id string) (Info, error) { return t.s.stopAsyncFor(&t.owner, id) }

func (t *Table) Remove(id string) error { return t.s.removeFor(&t.owner, id) }

func (t *Table) List() []Info { return t.s.listFor(&t.owner) }
