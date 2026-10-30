/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/pricing"
	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/stateroot"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Plugin is one loaded provider plugin.
type Plugin struct {
	host     *Host
	info     providerproto.InitializeResult
	store    CredentialStore
	logger   *zap.Logger
	cacheDir string

	mu       sync.Mutex
	headers  map[string]string
	headerAt time.Time
	headerTo time.Time
	models   []providerproto.Model

	// closed stops new model refreshes; listing tracks the ones in flight
	// so Close returns only after none can write the cache or the catalog.
	closed  bool
	listing sync.WaitGroup
}

// tokenSkew refreshes auth/token headers this long before they expire.
const tokenSkew = 60 * time.Second

// Name is the provider id users select.
func (p *Plugin) Name() string { return p.info.Provider.Name }

// Info is the plugin's handshake result.
func (p *Plugin) Info() providerproto.InitializeResult { return p.info }

// Host exposes the supervised process (diagnostics, tests).
func (p *Plugin) Host() *Host { return p.host }

// DisplayName localizes the plugin's name for lang (a BCP 47 tag), falling
// back to its primary subtag and then English.
func (p *Plugin) DisplayName(lang string) string {
	names := p.info.Provider.DisplayName
	if v := names[lang]; v != "" {
		return v
	}
	if base, _, ok := strings.Cut(lang, "-"); ok && names[base] != "" {
		return names[base]
	}
	return names["en"]
}

// RequiresLogin reports whether the plugin authenticates its users.
func (p *Plugin) RequiresLogin() bool { return p.info.Capabilities.Auth.Login }

// Credential returns the stored credential.
func (p *Plugin) Credential() (providerproto.Credential, bool) {
	return p.store.Load(p.Name())
}

// Available reports whether the provider can serve now: it needs no login,
// or a credential is stored. Unavailable plugins stay out of /switch, the
// same way a built-in provider without credentials does.
func (p *Plugin) Available() bool {
	if !p.RequiresLogin() {
		return true
	}
	_, ok := p.Credential()
	return ok
}

// Descriptor is the catalog entry the plugin contributes.
func (p *Plugin) Descriptor() catalog.ProviderDescriptor {
	name := p.Name()
	d := catalog.ProviderDescriptor{
		Name:         name,
		DisplayName:  p.DisplayName("en"),
		ModelEnv:     name + "_MODEL",
		MaxOutputEnv: name + "_MAX_TOKENS",
		AuthEnv:      append([]string(nil), p.info.AuthEnv...),
		PreferredAPI: p.preferredAPI(""),
	}
	if cached := p.CachedModels(); len(cached) > 0 {
		d.DefaultModel = cached[0].ID
	}
	return d
}

// preferredAPI is the wire a model of this plugin is spoken on: in dialect
// mode the model's own dialect, else the plugin's; chat completions for a
// plugin that serves chat itself. Usage accounting reads it — the Gemini
// wire reports thinking outside the output count, every other inside.
func (p *Plugin) preferredAPI(modelDialect providerproto.DialectAPI) catalog.PreferredAPI {
	if p.info.Mode != providerproto.ModeDialect || p.info.Dialect == nil {
		return catalog.APIChatCompletions
	}
	api := p.info.Dialect.API
	if modelDialect != "" {
		api = modelDialect
	}
	switch api {
	case providerproto.DialectOpenAIResponses:
		return catalog.APIResponses
	case providerproto.DialectAnthropicMessages:
		return catalog.APIAnthropicMessages
	case providerproto.DialectGemini:
		return catalog.APIGemini
	}
	return catalog.APIChatCompletions
}

// headersFor returns the auth/token headers, cached until shortly before
// they expire; force refetches (after a 401/403 from the backend).
func (p *Plugin) headersFor(ctx context.Context, force bool) (map[string]string, error) {
	if !p.info.Capabilities.Auth.Token {
		return nil, nil
	}
	p.mu.Lock()
	if !force && p.headers != nil && (p.headerTo.IsZero() || time.Now().Add(tokenSkew).Before(p.headerTo)) {
		h := p.headers
		p.mu.Unlock()
		return h, nil
	}
	p.mu.Unlock()

	cred, _ := p.Credential()
	var res providerproto.AuthTokenResult
	if err := p.host.Call(ctx, providerproto.MethodAuthToken, providerproto.CredentialParams{Credential: cred}, &res); err != nil {
		return nil, mapError(p.Name(), err)
	}
	if len(res.Credential) > 0 {
		if err := p.store.Save(p.Name(), res.Credential); err != nil {
			p.logger.Warn("could not store rotated provider plugin credential", zap.String("provider", p.Name()), zap.Error(err))
		}
	}
	var expires time.Time
	if res.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, res.ExpiresAt); err == nil {
			expires = t
		}
	}
	p.mu.Lock()
	p.headers, p.headerAt, p.headerTo = res.Headers, time.Now(), expires
	p.mu.Unlock()
	return res.Headers, nil
}

// invalidateHeaders drops cached token headers.
func (p *Plugin) invalidateHeaders() {
	p.mu.Lock()
	p.headers = nil
	p.mu.Unlock()
}

// Models lists the plugin's models (models/list), registers them in the
// catalog and pricing, and caches the list on disk so /model still offers
// it when the backend is unreachable. On failure it returns the cache.
func (p *Plugin) Models(ctx context.Context) ([]providerproto.Model, error) {
	if !p.info.Capabilities.Models {
		return nil, nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return p.CachedModels(), fmt.Errorf("%s: %w", p.Name(), providerproto.ErrClosed)
	}
	p.listing.Add(1)
	p.mu.Unlock()
	defer p.listing.Done()
	cred, _ := p.Credential()
	var res providerproto.ModelsListResult
	if err := p.host.Call(ctx, providerproto.MethodModelsList, providerproto.CredentialParams{Credential: cred}, &res); err != nil {
		if cached := p.CachedModels(); len(cached) > 0 {
			return cached, mapError(p.Name(), err)
		}
		return nil, mapError(p.Name(), err)
	}
	p.registerModels(res.Models)
	p.mu.Lock()
	p.models = res.Models
	p.mu.Unlock()
	p.writeModelCache(res.Models)
	return res.Models, nil
}

// Model returns the listed (or cached) entry for id.
func (p *Plugin) Model(id string) (providerproto.Model, bool) {
	p.mu.Lock()
	list := p.models
	p.mu.Unlock()
	if list == nil {
		list = p.CachedModels()
	}
	for _, m := range list {
		if strings.EqualFold(m.ID, id) {
			return m, true
		}
	}
	return providerproto.Model{}, false
}

// registerModels contributes each model to the catalog (sizing, display
// name, capabilities) and, when the plugin or its upstream knows it, to
// pricing — so a gateway model keeps its real limits and price.
func (p *Plugin) registerModels(models []providerproto.Model) {
	name := p.Name()
	for _, m := range models {
		meta := catalog.ModelMeta{
			ID:              m.ID,
			DisplayName:     m.DisplayName,
			Provider:        name,
			ContextWindow:   m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens,
			Capabilities:    append([]string(nil), m.Capabilities...),
			PreferredAPI:    p.preferredAPI(m.Dialect),
		}
		if up := m.Upstream; up != nil {
			if um, ok := catalog.Resolve(up.Provider, up.Model); ok {
				if meta.ContextWindow == 0 {
					meta.ContextWindow = um.ContextWindow
				}
				if meta.MaxOutputTokens == 0 {
					meta.MaxOutputTokens = um.MaxOutputTokens
				}
				if meta.DisplayName == "" {
					meta.DisplayName = um.DisplayName
				}
				if len(meta.Capabilities) == 0 {
					meta.Capabilities = append([]string(nil), um.Capabilities...)
				}
				if meta.CompactRatio == 0 {
					meta.CompactRatio = um.CompactRatio
				}
			}
		}
		if meta.DisplayName == "" {
			meta.DisplayName = m.ID
		}
		// Neither the plugin nor its upstream said what the model takes as
		// input: vision is learned from the model.
		meta.CapabilitiesUnknown = len(meta.Capabilities) == 0
		catalog.Register(meta)

		switch {
		case m.Pricing != nil:
			pricing.Register(name, m.ID, pricing.Rate{InputPerMTok: m.Pricing.Input, OutputPerMTok: m.Pricing.Output})
		case m.Upstream != nil && !strings.EqualFold(m.Upstream.Model, m.ID):
			if in, out, known := pricing.ListPrice(m.Upstream.Provider, m.Upstream.Model); known {
				pricing.Register(name, m.ID, pricing.Rate{InputPerMTok: in, OutputPerMTok: out})
			}
		}
	}
}

func (p *Plugin) modelCachePath() string {
	if p.cacheDir == "" {
		return ""
	}
	return filepath.Join(p.cacheDir, strings.ToLower(p.Name())+"-models.json")
}

func (p *Plugin) writeModelCache(models []providerproto.Model) {
	path := p.modelCachePath()
	if path == "" {
		return
	}
	data, err := json.Marshal(models)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if err := utils.AtomicWriteFile(path, data, 0o600); err != nil {
		p.logger.Debug("could not cache provider plugin models", zap.String("provider", p.Name()), zap.Error(err))
	}
}

// CachedModels returns the last model list written to disk.
func (p *Plugin) CachedModels() []providerproto.Model {
	path := p.modelCachePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed name inside chatcli's private cache dir
	if err != nil {
		return nil
	}
	var models []providerproto.Model
	if json.Unmarshal(data, &models) != nil {
		return nil
	}
	return models
}

// Status runs auth/status against the stored credential.
func (p *Plugin) Status(ctx context.Context) (providerproto.AuthStatusResult, error) {
	if !p.info.Capabilities.Auth.Token {
		return providerproto.AuthStatusResult{State: providerproto.AuthLoggedIn}, nil
	}
	cred, _ := p.Credential()
	var res providerproto.AuthStatusResult
	err := p.host.Call(ctx, providerproto.MethodAuthStatus, providerproto.CredentialParams{Credential: cred}, &res)
	return res, mapError(p.Name(), err)
}

// LoginUI renders login steps. Implementations never see a credential;
// they show URLs and codes and collect the fields the plugin asks for.
type LoginUI interface {
	// OpenURL shows (and may open) a URL the user must visit, then returns
	// once the user is ready to continue.
	OpenURL(ctx context.Context, step providerproto.AuthStep) error
	// DeviceCode shows a code and its verification URL; polling is done by
	// the caller.
	DeviceCode(ctx context.Context, step providerproto.AuthStep) error
	// Prompt asks for the fields, honoring Secret and Choices.
	Prompt(ctx context.Context, step providerproto.AuthStep) (map[string]string, error)
}

// ErrLoginNeedsPerson reports a login step that requires a person when no
// LoginUI was provided (unattended login with incomplete inputs).
var ErrLoginNeedsPerson = errors.New("login requires interaction")

// maxLoginSteps bounds the login state machine.
const maxLoginSteps = 16

// Login runs the plugin's login state machine and stores the credential.
// inputs pre-answer prompt fields; ui may be nil for unattended logins.
func (p *Plugin) Login(ctx context.Context, ui LoginUI, inputs map[string]string) (string, error) {
	if !p.RequiresLogin() {
		return "", nil
	}
	var step providerproto.AuthStep
	if err := p.host.Call(ctx, providerproto.MethodAuthLogin, providerproto.AuthLoginParams{Inputs: inputs}, &step); err != nil {
		return "", mapError(p.Name(), err)
	}
	for i := 0; i < maxLoginSteps; i++ {
		var answers map[string]string
		switch step.Kind {
		case providerproto.StepDone:
			if len(step.Credential) == 0 {
				return "", fmt.Errorf("%s: login finished without a credential", p.Name())
			}
			if err := p.store.Save(p.Name(), step.Credential); err != nil {
				return "", fmt.Errorf("store credential: %w", err)
			}
			p.invalidateHeaders()
			return step.Account, nil
		case providerproto.StepPrompt:
			answers = map[string]string{}
			var missing []providerproto.PromptField
			for _, f := range step.Fields {
				if v, ok := inputs[f.ID]; ok {
					answers[f.ID] = v
				} else {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				if ui == nil {
					ids := make([]string, len(missing))
					for i, f := range missing {
						ids[i] = f.ID
					}
					return "", fmt.Errorf("%w: missing %s", ErrLoginNeedsPerson, strings.Join(ids, ", "))
				}
				ask := step
				ask.Fields = missing
				got, err := ui.Prompt(ctx, ask)
				if err != nil {
					return "", err
				}
				for k, v := range got {
					answers[k] = v
				}
			}
		case providerproto.StepOpenURL:
			if ui == nil {
				return "", fmt.Errorf("%w: open %s", ErrLoginNeedsPerson, step.URL)
			}
			if err := ui.OpenURL(ctx, step); err != nil {
				return "", err
			}
		case providerproto.StepDeviceCode:
			if ui == nil {
				return "", fmt.Errorf("%w: device code %s at %s", ErrLoginNeedsPerson, step.Code, step.URL)
			}
			if i == 0 || step.Code != "" {
				if err := ui.DeviceCode(ctx, step); err != nil {
					return "", err
				}
			}
			wait := time.Duration(step.PollMs) * time.Millisecond
			if wait <= 0 {
				wait = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
		default:
			return "", fmt.Errorf("%s: unknown login step %q", p.Name(), step.Kind)
		}
		if err := p.host.Call(ctx, providerproto.MethodAuthContinue,
			providerproto.AuthContinueParams{Session: step.Session, Inputs: answers}, &step); err != nil {
			return "", mapError(p.Name(), err)
		}
	}
	return "", fmt.Errorf("%s: login did not finish within %d steps", p.Name(), maxLoginSteps)
}

// Logout revokes the credential upstream (when supported) and deletes it.
func (p *Plugin) Logout(ctx context.Context) error {
	cred, ok := p.Credential()
	if ok && p.info.Capabilities.Auth.Logout {
		if err := p.host.Call(ctx, providerproto.MethodAuthLogout, providerproto.CredentialParams{Credential: cred}, nil); err != nil {
			p.logger.Warn("provider plugin logout failed upstream; deleting the local credential anyway",
				zap.String("provider", p.Name()), zap.Error(err))
		}
	}
	p.invalidateHeaders()
	return p.store.Delete(p.Name())
}

// Close stops the plugin process.
func (p *Plugin) Close(ctx context.Context) {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.host.Close(ctx)
	// The stopped host fails in-flight calls at once; wait for them so a
	// late result never writes the cache or re-registers the catalog
	// entries Set.Close removes.
	p.listing.Wait()
}

// ------------------------------------------------------------------ Set

// LoadOptions configures Load.
type LoadOptions struct {
	// Dir holds the plugin binaries; empty means DefaultDir().
	Dir string
	// Resolve applies the signature policy (see HostOptions.Resolve).
	Resolve func(path string) (string, error)
	Store   CredentialStore
	Logger  *zap.Logger
	// CacheDir keeps per-plugin model lists; empty disables the cache.
	CacheDir    string
	HostVersion string
	Interactive bool
	// StartTimeout bounds each plugin's startup during Load.
	StartTimeout time.Duration
}

// LoadError reports a plugin binary that did not load.
type LoadError struct {
	Path string
	Err  error
}

func (e LoadError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }

// Set is the collection of loaded plugins.
type Set struct {
	mu      sync.RWMutex
	plugins map[string]*Plugin
	errors  []LoadError
}

// DefaultDir is ~/.chatcli/providers, next to the tool plugins directory
// (~/.chatcli/plugins) and following the same fixed-location convention.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(stateroot.Under(home), "providers")
}

// Discover lists candidate plugin binaries in dir: regular files that are
// executable (on Unix), skipping signatures, hidden files and directories.
func Discover(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".sig") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			continue
		}
		if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(name), ".exe") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out, nil
}

// Load starts every plugin in the directory concurrently, validates its
// handshake and registers its catalog descriptor. Plugins that fail are
// reported in Set.Errors and never registered.
func Load(ctx context.Context, opts LoadOptions) *Set {
	set := &Set{plugins: map[string]*Plugin{}}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	store := opts.Store
	if store == nil {
		store = AuthStore{Logger: logger}
	}
	dir := opts.Dir
	if dir == "" {
		dir = DefaultDir()
	}
	if dir == "" {
		return set
	}
	paths, err := Discover(dir)
	if err != nil {
		set.errors = append(set.errors, LoadError{Path: dir, Err: err})
		return set
	}
	timeout := opts.StartTimeout
	if timeout <= 0 {
		timeout = startTimeout
	}

	type loaded struct {
		path   string
		plugin *Plugin
		err    error
	}
	results := make([]loaded, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			host := NewHost(HostOptions{
				Path: path, Resolve: opts.Resolve, Logger: logger,
				HostVersion: opts.HostVersion, Interactive: opts.Interactive,
			})
			sctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			info, err := host.Info(sctx)
			if err != nil {
				host.Close(ctx)
				results[i] = loaded{path: path, err: err}
				return
			}
			results[i] = loaded{path: path, plugin: &Plugin{
				host: host, info: info, store: store, logger: logger, cacheDir: opts.CacheDir,
			}}
		}(i, path)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			set.errors = append(set.errors, LoadError{Path: r.path, Err: r.err})
			continue
		}
		name := r.plugin.Name()
		if _, dup := set.plugins[name]; dup {
			r.plugin.Close(ctx)
			set.errors = append(set.errors, LoadError{Path: r.path, Err: fmt.Errorf("provider %s is already provided by another plugin", name)})
			continue
		}
		if err := catalog.RegisterProvider(r.plugin.Descriptor()); err != nil {
			r.plugin.Close(ctx)
			set.errors = append(set.errors, LoadError{Path: r.path, Err: err})
			continue
		}
		// Cached models make the catalog useful before the first live list.
		r.plugin.registerModels(r.plugin.CachedModels())
		set.plugins[name] = r.plugin
	}
	for _, e := range set.errors {
		logger.Warn("provider plugin not loaded", zap.String("path", e.Path), zap.Error(e.Err))
	}
	return set
}

// Get returns the plugin serving provider name (case-insensitive).
func (s *Set) Get(name string) (*Plugin, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.plugins[strings.ToUpper(strings.TrimSpace(name))]
	return p, ok
}

// List returns the loaded plugins sorted by name.
func (s *Set) List() []*Plugin {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Plugin, 0, len(s.plugins))
	for _, p := range s.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Errors returns the plugins that failed to load.
func (s *Set) Errors() []LoadError {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]LoadError(nil), s.errors...)
}

// Close stops every plugin and removes their catalog descriptors.
func (s *Set) Close(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	plugins := s.plugins
	s.plugins = map[string]*Plugin{}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for name, p := range plugins {
		wg.Add(1)
		go func(name string, p *Plugin) {
			defer wg.Done()
			p.Close(ctx)
			catalog.UnregisterProvider(name)
		}(name, p)
	}
	wg.Wait()
}
