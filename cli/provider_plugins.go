/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/llm/providerhost"
	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// providerModelsRefreshTimeout bounds the background model refresh that
// runs after a plugin registers.
const providerModelsRefreshTimeout = 30 * time.Second

// ProviderPluginLoadOptions builds the loader options shared by the REPL
// boot and the `chatcli provider` subcommand: plugins come from
// ~/.chatcli/providers and run only under the same signature policy as tool
// plugins (trusted keys, unsigned opt-in, quarantine).
func ProviderPluginLoadOptions(logger *zap.Logger, interactive bool) providerhost.LoadOptions {
	dir := providerhost.DefaultDir()
	verifier := plugins.NewPluginVerifier()
	quarantine := plugins.NewQuarantine(dir)
	cacheDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		cacheDir = filepath.Join(home, ".chatcli", "cache", "provider-plugins")
	}
	return providerhost.LoadOptions{
		Dir: dir,
		Resolve: func(path string) (string, error) {
			d, err := verifier.ResolveExec(quarantine, plugins.VerifiedCacheDir(dir), path)
			if err != nil {
				return "", err
			}
			if d.Unsigned {
				logger.Warn("running unsigned provider plugin (dev mode)", zap.String("path", path))
			}
			return d.ExecPath, nil
		},
		Store:       providerhost.AuthStore{Logger: logger},
		Logger:      logger,
		CacheDir:    cacheDir,
		HostVersion: version.Version,
		Interactive: interactive,
	}
}

// sharedProviderPluginsKey carries a plugin set loaded by the process that
// embeds a ChatCLI (the server), so the embedded instance uses it instead
// of starting every plugin a second time.
type sharedProviderPluginsKey struct{}

// WithProviderPlugins returns a context whose ChatCLI instances use set
// instead of loading their own; the caller keeps ownership and closes it.
func WithProviderPlugins(ctx context.Context, set *providerhost.Set) context.Context {
	return context.WithValue(ctx, sharedProviderPluginsKey{}, set)
}

// LoadProviderPlugins starts the installed provider plugins without a
// terminal and registers the available ones with mgr. It is how processes
// other than the REPL (the server) get plugins: credentials come from the
// credential store or from the plugin's declared environment variables,
// never from a prompt. The caller closes the set.
func LoadProviderPlugins(ctx context.Context, mgr manager.LLMManager, logger *zap.Logger) *providerhost.Set {
	set := providerhost.Load(ctx, ProviderPluginLoadOptions(logger, false))
	registerProviderPluginSet(ctx, mgr, set, logger)
	return set
}

// loadProviderPlugins starts the installed provider plugins and registers
// the available ones with the manager. It runs before the initial provider
// is chosen, so LLM_PROVIDER can name a plugin.
func (cli *ChatCLI) loadProviderPlugins(ctx context.Context) {
	if set, ok := ctx.Value(sharedProviderPluginsKey{}).(*providerhost.Set); ok && set != nil {
		cli.providerPlugins, cli.providerPluginsShared = set, true
	} else {
		interactive := term.IsTerminal(int(os.Stdin.Fd())) // #nosec G115 -- a file descriptor always fits in int
		cli.providerPlugins = providerhost.Load(ctx, ProviderPluginLoadOptions(cli.logger, interactive))
	}
	cli.registerProviderPlugins(ctx)
}

// registerProviderPlugins (re)registers every available plugin with the
// current manager. Call it again after the manager is rebuilt (reload,
// project .env) — a new manager starts with built-ins only.
func (cli *ChatCLI) registerProviderPlugins(ctx context.Context) {
	registerProviderPluginSet(ctx, cli.manager, cli.providerPlugins, cli.logger)
}

// registerProviderPluginSet registers set's available plugins with mgr and
// unregisters the rest; a manager without runtime registration is left
// alone.
func registerProviderPluginSet(ctx context.Context, mgr manager.LLMManager, set *providerhost.Set, logger *zap.Logger) {
	reg, ok := mgr.(manager.ProviderRegistrar)
	if !ok || set == nil {
		return
	}
	for _, p := range set.List() {
		if !p.Available() {
			reg.UnregisterProvider(p.Name())
			continue
		}
		reg.RegisterProvider(p.Name(), p.NewClient)
		go refreshProviderPluginModels(ctx, p, logger)
	}
}

// refreshProviderPluginModels fills the catalog with the plugin's live
// model list (window, output cap, price) in the background. It is detached
// from ctx's cancellation: the command that triggered it usually returns
// first.
func refreshProviderPluginModels(ctx context.Context, p *providerhost.Plugin, logger *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerModelsRefreshTimeout)
	defer cancel()
	if _, err := p.Models(ctx); err != nil {
		logger.Debug("provider plugin model refresh failed", zap.String("provider", p.Name()), zap.Error(err))
	}
}

// backgroundCtx is the session context when one exists — code paths that
// run outside a command (project .env overlay) derive from it.
func (cli *ChatCLI) backgroundCtx() context.Context {
	if cli.sessionCtx != nil {
		return cli.sessionCtx
	}
	return context.Background()
}

// closeProviderPlugins stops every plugin process this instance started; a
// shared set belongs to the process that loaded it.
func (cli *ChatCLI) closeProviderPlugins(ctx context.Context) {
	if cli.providerPlugins != nil && !cli.providerPluginsShared {
		cli.providerPlugins.Close(ctx)
	}
}

// loginProviderPlugin handles `/auth login <plugin>`; false means name is
// not a provider plugin.
func (cli *ChatCLI) loginProviderPlugin(ctx context.Context, name string) bool {
	p, ok := cli.providerPlugins.Get(name)
	if !ok {
		return false
	}
	if !p.RequiresLogin() {
		fmt.Println(kit.Notice(kit.LevelInfo, i18n.T("provider.plugin.login.not_needed", p.DisplayName(activeLang()))))
		return true
	}
	account, err := p.Login(ctx, terminalLoginUI{}, nil)
	if err != nil {
		fmt.Println(kit.Notice(kit.LevelError, i18n.T("auth.login.failed", err)))
		return true
	}
	if account == "" {
		account = p.Name()
	}
	fmt.Println(kit.Notice(kit.LevelSuccess, i18n.T("auth.login.success", p.DisplayName(activeLang()), account)))
	cli.registerProviderPlugins(ctx)
	model := DefaultModelForProvider(p.Name())
	if model == "" {
		mctx, cancel := context.WithTimeout(ctx, providerModelsRefreshTimeout)
		if models, err := p.Models(mctx); err == nil && len(models) > 0 {
			model = models[0].ID
		}
		cancel()
	}
	if cli.commandHandler != nil {
		cli.commandHandler.autoSwitchProvider(ctx, p.Name(), model)
	}
	return true
}

// logoutProviderPlugin handles `/auth logout <plugin>`.
func (cli *ChatCLI) logoutProviderPlugin(ctx context.Context, name string) bool {
	p, ok := cli.providerPlugins.Get(name)
	if !ok {
		return false
	}
	if err := p.Logout(ctx); err != nil {
		fmt.Println(i18n.T("auth.logout.failed", err))
		return true
	}
	cli.registerProviderPlugins(ctx)
	fmt.Println(i18n.T("auth.logout.success"))
	return true
}

// providerPluginStatus renders one line per plugin for /auth status.
func (cli *ChatCLI) providerPluginStatus(ctx context.Context) string {
	list := cli.providerPlugins.List()
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(i18n.T("provider.plugin.status.header"))
	b.WriteByte('\n')
	for _, p := range list {
		state := i18n.T("provider.plugin.status.no_login")
		if p.RequiresLogin() {
			sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			st, err := p.Status(sctx)
			cancel()
			switch {
			case err != nil:
				state = i18n.T("provider.plugin.status.error", err)
			case st.State == providerproto.AuthLoggedIn && st.Account != "":
				state = i18n.T("provider.plugin.status.logged_in_as", st.Account)
			case st.State == providerproto.AuthLoggedIn:
				state = i18n.T("provider.plugin.status.logged_in")
			case st.State == providerproto.AuthExpired:
				state = i18n.T("provider.plugin.status.expired")
			default:
				state = i18n.T("provider.plugin.status.logged_out")
			}
		}
		fmt.Fprintf(&b, "  %-20s %s — %s\n", p.Name(), p.DisplayName(activeLang()), state)
	}
	return strings.TrimRight(b.String(), "\n")
}

// activeLang is the BCP 47 tag of the active UI language.
func activeLang() string { return i18n.ActiveTag().String() }

// TerminalLoginUI renders provider-plugin login steps on the terminal
// (shared with the `chatcli provider login` subcommand).
func TerminalLoginUI() providerhost.LoginUI { return terminalLoginUI{} }

// terminalLoginUI renders login steps on the terminal.
type terminalLoginUI struct{}

func localized(m map[string]string) string {
	lang := activeLang()
	if v := m[lang]; v != "" {
		return v
	}
	if base, _, ok := strings.Cut(lang, "-"); ok && m[base] != "" {
		return m[base]
	}
	return m["en"]
}

// OpenURL implements providerhost.LoginUI.
func (terminalLoginUI) OpenURL(_ context.Context, step providerproto.AuthStep) error {
	if msg := localized(step.Message); msg != "" {
		fmt.Println(msg)
	}
	fmt.Println(i18n.T("provider.plugin.login.open_url", step.URL))
	if safeBrowserURL(step.URL) {
		_ = openBrowserURL(step.URL)
	}
	fmt.Print(i18n.T("provider.plugin.login.press_enter"))
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return err
}

// DeviceCode implements providerhost.LoginUI.
func (terminalLoginUI) DeviceCode(_ context.Context, step providerproto.AuthStep) error {
	if msg := localized(step.Message); msg != "" {
		fmt.Println(msg)
	}
	fmt.Println(i18n.T("provider.plugin.login.device_code", step.URL, step.Code))
	if safeBrowserURL(step.URL) {
		_ = openBrowserURL(step.URL)
	}
	return nil
}

// Prompt implements providerhost.LoginUI.
func (terminalLoginUI) Prompt(_ context.Context, step providerproto.AuthStep) (map[string]string, error) {
	if msg := localized(step.Message); msg != "" {
		fmt.Println(msg)
	}
	reader := bufio.NewReader(os.Stdin)
	out := make(map[string]string, len(step.Fields))
	for _, f := range step.Fields {
		label := localized(f.Label)
		if label == "" {
			label = f.ID
		}
		if len(f.Choices) > 0 {
			for i, c := range f.Choices {
				fmt.Printf("  %d) %s\n", i+1, c)
			}
		}
		if f.Default != "" {
			fmt.Printf("%s [%s]: ", label, f.Default)
		} else {
			fmt.Printf("%s: ", label)
		}
		var value string
		if f.Secret && term.IsTerminal(int(os.Stdin.Fd())) { // #nosec G115 -- a file descriptor always fits in int
			raw, err := term.ReadPassword(int(os.Stdin.Fd())) // #nosec G115 -- a file descriptor always fits in int
			fmt.Println()
			if err != nil {
				return nil, err
			}
			value = string(raw)
		} else {
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				return nil, err
			}
			value = line
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = f.Default
		}
		if len(f.Choices) > 0 {
			if n, err := strconv.Atoi(value); err == nil && n >= 1 && n <= len(f.Choices) {
				value = f.Choices[n-1]
			}
		}
		out[f.ID] = value
	}
	return out, nil
}

// safeBrowserURL admits only https URLs and http on loopback, so a plugin
// can never make the browser open file:// or a custom URL scheme.
func safeBrowserURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}
	return false
}
