/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/ui/theme"
)

type webThemeCall struct {
	method, path, token, theme string
}

// fakeWebChild stands where the `chatcli web` child listens and records
// what the terminal sends it.
func fakeWebChild(t *testing.T, status int) (*httptest.Server, chan webThemeCall) {
	t.Helper()
	calls := make(chan webThemeCall, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req WebThemeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls <- webThemeCall{r.Method, r.URL.Path, r.Header.Get(webGuardHeader), req.Theme}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func keepTheme(t *testing.T) {
	t.Helper()
	prev := theme.Active().Name
	t.Setenv(config.ThemeEnv, prev)
	t.Cleanup(func() { _ = theme.SetActive(prev) })
}

func waitWebThemeCall(t *testing.T, calls chan webThemeCall) webThemeCall {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("the child never heard about the theme")
		return webThemeCall{}
	}
}

// Changing the theme in the terminal posts it to the running web UI child,
// on the child's API, with the token from the address the child published.
func TestConfigUITheme_TellsTheRunningWebUI(t *testing.T) {
	keepTheme(t)
	child, calls := fakeWebChild(t, http.StatusOK)
	c, _, _ := newWebCLI(t)
	// The spawned "child" publishes the fake child's address, as the real
	// one publishes its own.
	webSpawner = func(_ context.Context, _, urlFile string) (webProcess, error) {
		return &fakeWebProcess{}, os.WriteFile(urlFile, []byte(child.URL+"/?t=deadbeef\n"), 0o600)
	}
	ctx, cancelCommand := context.WithCancel(context.Background())
	_ = captureStdout(t, func() { c.handleWebCommand(ctx, "/web url") })
	cancelCommand() // the command that started the web UI is long over when the theme changes

	_ = captureStdout(t, func() { c.configUITheme([]string{"onyx"}) })
	got := waitWebThemeCall(t, calls)
	want := webThemeCall{http.MethodPost, "/api/" + WebThemeResource, "deadbeef", "onyx"}
	if got != want {
		t.Fatalf("child heard %+v, want %+v", got, want)
	}

	// Any other place that sets the theme makes the same one call.
	if err := theme.SetActive("onyx-light"); err != nil {
		t.Fatal(err)
	}
	c.notifyWebTheme()
	if got := waitWebThemeCall(t, calls); got.theme != "onyx-light" {
		t.Fatalf("child heard %q, want onyx-light", got.theme)
	}

	// An invalid name changes nothing, so there is nothing to tell.
	_ = captureStdout(t, func() { c.configUITheme([]string{"no-such-theme"}) })
	select {
	case got := <-calls:
		t.Fatalf("a rejected theme must not be posted, child heard %+v", got)
	case <-time.After(150 * time.Millisecond):
	}

	// Stopping the web UI ends the delivery; later changes go nowhere.
	_ = captureStdout(t, func() { c.handleWebCommand(context.Background(), "/web off") })
	c.notifyWebTheme()
	select {
	case got := <-calls:
		t.Fatalf("a stopped web UI must not be posted to, child heard %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// Only the latest theme matters: with the loop busy, a pending older name
// is replaced instead of blocking the prompt.
func TestNotifyWebTheme_KeepsOnlyTheLatest(t *testing.T) {
	keepTheme(t)
	c := &ChatCLI{logger: zap.NewNop()}
	c.web.theme = make(chan string, 1)
	for _, name := range []string{"nord", "dracula", "onyx"} {
		if err := theme.SetActive(name); err != nil {
			t.Fatal(err)
		}
		c.notifyWebTheme()
	}
	if got := <-c.web.theme; got != "onyx" {
		t.Fatalf("queued %q, want the latest", got)
	}

	prev := webThemePoster
	t.Cleanup(func() { webThemePoster = prev })
	var posted []string
	webThemePoster = func(_ context.Context, _, name string) error {
		posted = append(posted, name)
		return errors.New("child is gone")
	}
	queue := make(chan string, 2)
	queue <- "onyx"
	queue <- "onyx-light"
	close(queue)
	webThemeLoop(context.Background(), "http://127.0.0.1:1/?t=x", queue, zap.NewNop())
	if len(posted) != 2 || posted[1] != "onyx-light" {
		t.Fatalf("the loop must outlive a failed post, posted %v", posted)
	}
}

// Without a web UI the notification does nothing at all, and a nil
// receiver is tolerated like the other web helpers.
func TestNotifyWebTheme_WithoutAWebUIDoesNothing(t *testing.T) {
	prev := webThemePoster
	t.Cleanup(func() { webThemePoster = prev })
	webThemePoster = func(context.Context, string, string) error {
		t.Error("nothing to deliver to")
		return nil
	}
	var none *ChatCLI
	none.notifyWebTheme()
	c := &ChatCLI{logger: zap.NewNop()}
	c.notifyWebTheme()
}

func TestPostWebTheme_Refusals(t *testing.T) {
	ctx := context.Background()
	for _, addr := range []string{"http://127.0.0.1:60000/", "/?t=abc", "http://[::1"} {
		if err := PostWebTheme(ctx, addr, "onyx"); err == nil {
			t.Errorf("%q: an address without host or token must be refused", addr)
		}
	}
	child, calls := fakeWebChild(t, http.StatusForbidden)
	if err := PostWebTheme(ctx, child.URL+"/?t=wrong", "onyx"); err == nil {
		t.Error("a refusal by the child must be reported")
	}
	<-calls
	child.Close()
	if err := PostWebTheme(ctx, child.URL+"/?t=abc", "onyx"); err == nil {
		t.Error("a dead child must be reported")
	}
}

// /reload re-reads the theme from the environment file, which can change it
// behind the web UI's back: the reload path tells the child like every other
// place that sets the theme. This drives the real reloadConfiguration.
func TestReloadConfiguration_TellsTheRunningWebUI(t *testing.T) {
	keepTheme(t)
	config.ResetBootEnvForTest()
	config.ResetProjectDotenvForTest()
	t.Cleanup(func() {
		config.ResetBootEnvForTest()
		config.ResetProjectDotenvForTest()
	})
	// The reload clears and re-reads these: hand every one back afterwards.
	for _, v := range reloadableEnvVars {
		if cur, ok := os.LookupEnv(v); ok {
			t.Setenv(v, cur)
		} else {
			t.Setenv(v, "")
			_ = os.Unsetenv(v)
		}
	}
	dotenv := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(dotenv, []byte(config.ThemeEnv+"=onyx-light\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_DOTENV", dotenv)
	if err := theme.SetActive("dark"); err != nil {
		t.Fatal(err)
	}

	c := minimalCLI(t)
	config.InitGlobal(c.logger)
	c.web.theme = make(chan string, 1) // a web UI is running; nothing drains the queue here
	_ = captureStdout(t, func() { c.reloadConfiguration(context.Background()) })

	if got := theme.Active().Name; got != "onyx-light" {
		t.Fatalf("the reload must apply the file's theme, got %q", got)
	}
	select {
	case got := <-c.web.theme:
		if got != "onyx-light" {
			t.Fatalf("the child was queued %q, want onyx-light", got)
		}
	default:
		t.Fatal("the reload changed the theme and told the web UI nothing")
	}
}
