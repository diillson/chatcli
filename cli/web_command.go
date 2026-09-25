/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/term"

	"github.com/diillson/chatcli/i18n"
)

// /web opens the local web UI next to the terminal. The UI runs as a child
// `chatcli web` process bound to the terminal's session, so both surfaces
// read and write the same saved session: what is said in the browser shows
// up here and the other way round, through the same write-through the
// MCP and ACP surfaces use. The child dies with the terminal.

// webProcess is the running child, abstracted so tests need no process.
type webProcess interface {
	PID() int
	Kill() error
}

type webState struct {
	mu    sync.Mutex
	proc  webProcess
	url   string
	bound string
}

type osWebProcess struct{ cmd *exec.Cmd }

func (p *osWebProcess) PID() int { return p.cmd.Process.Pid }
func (p *osWebProcess) Kill() error {
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_ = p.cmd.Wait()
	return nil
}

// webSpawner starts `chatcli web` bound to session, writing its URL to
// urlFile. Tests replace it.
var webSpawner = func(ctx context.Context, session, urlFile string) (webProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"web", "--no-browser", "--url-file", urlFile}
	if session != "" {
		args = append(args, "--session", session)
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, args...) // #nosec G204 -- our own binary with fixed arguments
	cmd.Stdin = nil
	if logf, err := os.OpenFile(webLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osWebProcess{cmd: cmd}, nil
}

var webBrowserLauncher = openBrowserURL

// webStartTimeout bounds how long the terminal waits for the child to
// publish its address.
var webStartTimeout = 20 * time.Second

func webLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "chatcli-web.log")
	}
	return filepath.Join(home, ".chatcli", "web.log")
}

func (cli *ChatCLI) handleWebCommand(ctx context.Context, userInput string) {
	args := strings.Fields(strings.TrimSpace(userInput))
	sub := "open"
	if len(args) >= 2 {
		sub = strings.ToLower(args[1])
	}
	switch sub {
	case "open", "on", "start", "ui":
		cli.webOpen(ctx, true)
	case "url", "link":
		cli.webOpen(ctx, false)
	case "status", "st":
		cli.webStatus()
	case "off", "stop", "close":
		cli.webStop(ctx)
	default:
		fmt.Println(colorize("  "+i18n.T("web.usage.title"), ColorCyan))
		fmt.Println(colorize("  "+i18n.T("web.usage.body"), ColorGray))
	}
}

// webBindSession returns the saved session the browser should share with
// this terminal. A terminal that is not bound to a saved session gets bound
// to a fresh one first, so the conversation has a name both can follow.
func (cli *ChatCLI) webBindSession() string {
	if name := cli.boundSessionName(); name != "" {
		cli.persistBoundSession()
		return name
	}
	if cli.sessionManager == nil {
		return ""
	}
	name := "web-" + time.Now().Format("20060102-150405")
	if err := cli.sessionManager.SaveSessionV2(name, cli.buildSessionData()); err != nil {
		cli.logger.Warn("web: binding the terminal session failed", zap.Error(err))
		return ""
	}
	cli.currentSessionName = name
	cli.markBoundSessionSynced(name)
	fmt.Println(colorize("  "+i18n.T("web.cmd.bound", name), ColorGray))
	return name
}

func (cli *ChatCLI) webEnsureRunning(ctx context.Context) (string, error) {
	cli.web.mu.Lock()
	defer cli.web.mu.Unlock()
	if cli.web.proc != nil {
		return cli.web.url, nil
	}
	bound := cli.webBindSession()
	dir, err := os.MkdirTemp("", "chatcli-web-")
	if err != nil {
		return "", err
	}
	urlFile := filepath.Join(dir, "url")
	proc, err := webSpawner(ctx, bound, urlFile)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	url, err := waitForURL(ctx, urlFile, webStartTimeout)
	_ = os.RemoveAll(dir)
	if err != nil {
		_ = proc.Kill()
		return "", err
	}
	cli.web.proc, cli.web.url, cli.web.bound = proc, url, bound
	return url, nil
}

func waitForURL(ctx context.Context, path string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" { // #nosec G304 -- private temp file this process created
			return strings.TrimSpace(string(data)), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("%s", i18n.T("web.cmd.start_timeout", webLogPath()))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (cli *ChatCLI) webOpen(ctx context.Context, launch bool) {
	url, err := cli.webEnsureRunning(ctx)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("web.cmd.start_failed", err), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("web.cmd.serving", url), ColorCyan))
	fmt.Println(colorize("  "+i18n.T("web.cmd.serving_hint"), ColorGray))
	if launch && term.IsTerminal(int(os.Stdout.Fd())) {
		_ = webBrowserLauncher(url)
	}
}

func (cli *ChatCLI) webStatus() {
	cli.web.mu.Lock()
	proc, url, bound := cli.web.proc, cli.web.url, cli.web.bound
	cli.web.mu.Unlock()
	if proc == nil {
		fmt.Println(colorize("  "+i18n.T("web.cmd.not_running"), ColorGray))
		return
	}
	fmt.Println(colorize("  "+i18n.T("web.cmd.serving", url), ColorCyan))
	fmt.Println("  " + i18n.T("web.cmd.status", strconv.Itoa(proc.PID()), bound, webLogPath()))
}

func (cli *ChatCLI) webStop(ctx context.Context) {
	if !cli.shutdownWeb(ctx) {
		fmt.Println(colorize("  "+i18n.T("web.cmd.not_running"), ColorGray))
		return
	}
	fmt.Println(colorize("  "+i18n.T("web.cmd.stopped"), ColorCyan))
}

// shutdownWeb stops the child; the terminal's exit takes the web UI with
// it, since the address carries a token only this process handed out.
func (cli *ChatCLI) shutdownWeb(_ context.Context) bool {
	if cli == nil {
		return false
	}
	cli.web.mu.Lock()
	proc := cli.web.proc
	cli.web.proc, cli.web.url, cli.web.bound = nil, "", ""
	cli.web.mu.Unlock()
	if proc == nil {
		return false
	}
	_ = proc.Kill()
	return true
}
