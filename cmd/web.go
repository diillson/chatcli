/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.org/x/term"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/webui"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/llm/transcription"
	"github.com/diillson/chatcli/llm/tts"
	"github.com/diillson/chatcli/version"
)

// webPage, when set through SetWebPage, replaces the bundled application
// page; nil serves the page embedded in the webui package.
var webPage []byte

// SetWebPage installs a custom application page (a build that wants to
// ship its own front end). The bundled page is used when none is set.
func SetWebPage(page []byte) { webPage = page }

// webBrowserLauncher opens the URL; tests replace it.
var webBrowserLauncher = cli.LaunchBrowser

// RunWeb serves the local web UI: `chatcli web [--addr host:port]
// [--session name] [--no-browser]`. The engine is the same shared backend
// MCP and ACP use, so a session bound here continues in the terminal.
func RunWeb(args []string, mgr manager.LLMManager, logger *zap.Logger) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	addr := fs.String("addr", "", "listen address (default 127.0.0.1 on a free port; loopback only)")
	session := fs.String("session", "", "saved session to bind the browser to (continues the terminal conversation)")
	noBrowser := fs.Bool("no-browser", false, "print the address instead of opening the browser")
	if err := fs.Parse(args); err != nil {
		return err
	}

	backend, cleanup := newRPCBackend("web", mgr, logger)
	defer cleanup()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if name := strings.TrimSpace(*session); name != "" {
		if _, err := backend.ManageSession(ctx, "attach", "web", name); err != nil {
			return fmt.Errorf("%s", i18n.T("web.cli.failed", err))
		}
		fmt.Println(i18n.T("web.cli.bound", name))
	}

	srv, err := webui.Start(webui.Options{
		Backend:   backend,
		Page:      webPage,
		Addr:      *addr,
		Lang:      webLang(),
		ThemeName: cli.ActiveThemeName(),
		ThemeVars: cli.ActiveThemeCSSVars(),
		Version:   version.GetCurrentVersion().Version,
		Voice:     tts.NewFromEnv(logger),
		STT:       transcription.NewFromEnv(logger),
		Images:    imagegen.NewFromEnvContext(ctx, logger),
		Logger:    logger,
	})
	if err != nil {
		return fmt.Errorf("%s", i18n.T("web.cli.failed", err))
	}
	fmt.Println(i18n.T("web.cli.serving", srv.URL()))
	fmt.Println(i18n.T("web.cli.serving_hint"))
	if !*noBrowser && term.IsTerminal(int(os.Stdout.Fd())) {
		_ = webBrowserLauncher(srv.URL())
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	fmt.Println(i18n.T("web.cli.stopped"))
	return nil
}

// webLang maps the process language to the page's: pt-BR or en.
func webLang() string {
	tag := i18n.ActiveTag().String()
	if strings.HasPrefix(strings.ToLower(tag), "pt") {
		return "pt-BR"
	}
	return "en"
}
