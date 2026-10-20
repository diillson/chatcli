package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/stateroot"
	"github.com/diillson/chatcli/utils"
	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// Manager descobre, carrega e gerencia o ciclo de vida dos plugins.
type Manager struct {
	plugins    map[string]Plugin
	builtins   map[string]Plugin
	shadowed   map[string]struct{} // built-in names hidden by MCP overrides
	pluginsDir string
	logger     *zap.Logger
	mu         sync.RWMutex
	reloadMu   sync.Mutex // serializes Reload; never held with mu across a plugin exec
	watcher    *fsnotify.Watcher
	closeOnce  sync.Once
	quarantine *Quarantine
}

func NewManager(logger *zap.Logger) (*Manager, error) {
	home, err := utils.GetHomeDir()
	if err != nil {
		return nil, fmt.Errorf("não foi possível encontrar o diretório home para plugins: %w", err)
	}
	pluginsDir := filepath.Join(stateroot.Under(home), "plugins")

	// Garante que o diretório de plugins exista
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		return nil, fmt.Errorf("não foi possível criar o diretório de plugins: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("não foi possível criar o observador de arquivos: %w", err)
	}

	m := &Manager{
		plugins:    make(map[string]Plugin),
		builtins:   make(map[string]Plugin),
		shadowed:   make(map[string]struct{}),
		pluginsDir: pluginsDir,
		logger:     logger,
		watcher:    watcher,
		quarantine: NewQuarantine(pluginsDir),
	}
	m.Reload()
	go m.watchForChanges()

	return m, nil
}

// Close encerra o watcher de arquivos de forma segura.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		if m.watcher != nil {
			_ = m.watcher.Close()
		}
	})
}

// watchForChanges escuta por eventos no diretório de plugins e aciona o Reload.
func (m *Manager) watchForChanges() {
	// Adiciona o diretório de plugins ao watcher.
	err := m.watcher.Add(m.pluginsDir)
	if err != nil {
		m.logger.Error("Erro ao iniciar a observação do diretório de plugins", zap.Error(err))
		return
	}

	// Debounce: Evita recarregamentos múltiplos em rápida sucessão.
	var reloadTimer *time.Timer

	for {
		select {
		case event, ok := <-m.watcher.Events:
			if !ok {
				return // Canal fechado
			}
			// Reage a qualquer criação, remoção ou escrita de arquivo.
			if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create || event.Op&fsnotify.Remove == fsnotify.Remove || event.Op&fsnotify.Rename == fsnotify.Rename {
				m.logger.Debug("Alteração detectada no diretório de plugins", zap.String("event", event.String()))

				// Reseta o timer de debounce a cada novo evento.
				if reloadTimer != nil {
					reloadTimer.Stop()
				}
				reloadTimer = time.AfterFunc(500*time.Millisecond, func() {
					m.logger.Info("Diretório de plugins modificado, recarregando automaticamente...")
					m.Reload()
				})
			}
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return // Canal fechado
			}
			m.logger.Error("Erro no observador de arquivos de plugins", zap.Error(err))
		}
	}
}

// PluginsDir retorna o diretório onde os plugins estão instalados.
func (m *Manager) PluginsDir() string {
	return m.pluginsDir
}

// Reload limpa e recarrega todos os plugins do diretório de plugins.
//
// Verificação, cópia privada e as chamadas --metadata/--schema rodam SEM o
// lock do manager: um plugin lento (ou travado até o timeout do probe) não
// bloqueia GetPlugin/GetPlugins de ninguém. Só a troca final do mapa é feita
// sob o lock; reloadMu serializa reloads concorrentes (watcher + /reload).
func (m *Manager) Reload() {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	if err := os.MkdirAll(m.pluginsDir, 0o700); err != nil {
		m.logger.Error("Não foi possível criar o diretório de plugins", zap.String("path", m.pluginsDir), zap.Error(err))
		return
	}

	entries, err := os.ReadDir(m.pluginsDir)
	if err != nil {
		m.logger.Error("Erro ao ler o diretório de plugins", zap.Error(err))
		return
	}

	// Security (C3/H9): Verify plugin signatures before loading
	verifier := NewPluginVerifier()
	if verifier.AllowsUnsigned() {
		m.logger.Warn("SECURITY: unsigned plugins are allowed (CHATCLI_ALLOW_UNSIGNED_PLUGINS=true) — disable in production")
	}

	// The quarantine window is read fresh, so /reload picks up a change to
	// CHATCLI_PLUGIN_QUARANTINE the same way every other reloadable setting
	// is picked up.
	quarantine := NewQuarantine(m.pluginsDir)
	m.mu.Lock()
	m.quarantine = quarantine
	m.mu.Unlock()
	if raw, bad := ConfiguredButUnparseable(); bad {
		m.logger.Warn("CHATCLI_PLUGIN_QUARANTINE is not a duration; quarantine stays off",
			zap.String("value", raw))
	}

	present := make(map[string]struct{}, len(entries))
	var loaded []*ExecutablePlugin

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sig") || strings.HasPrefix(entry.Name(), ".") {
			continue // skip signature files and hidden files
		}
		present[entry.Name()] = struct{}{}
		if plugin := m.loadEntry(verifier, quarantine, entry.Name()); plugin != nil {
			loaded = append(loaded, plugin)
		}
	}

	inUse := make(map[string]struct{})
	m.mu.Lock()
	m.plugins = make(map[string]Plugin)
	for _, plugin := range loaded {
		// A plugin can never take a built-in's name: the built-in is what
		// the agent, the policy and the docs mean by that name.
		if builtin, clash := m.builtinNameClash(plugin.Name()); clash {
			m.logger.Warn("Plugin name collides with a built-in tool, skipping",
				zap.String("plugin", plugin.Path()), zap.String("name", plugin.Name()), zap.String("builtin", builtin))
			continue
		}
		m.plugins[plugin.Name()] = plugin
		if plugin.execPath != "" {
			inUse[plugin.execPath] = struct{}{}
		}
	}
	for name, bp := range m.builtins {
		m.plugins[name] = bp
	}
	count := len(m.plugins)
	m.mu.Unlock()

	pruneVerifiedCache(VerifiedCacheDir(m.pluginsDir), inUse)

	// A plugin that is gone must not leave a record that would admit a
	// future binary of the same name without its own waiting period.
	if quarantine.Enabled() {
		for _, e := range quarantine.List() {
			if _, still := present[e.Name]; !still {
				quarantine.Forget(e.Name)
			}
		}
	}

	m.logger.Info("Plugins recarregados.", zap.Int("count", count))
}

// loadEntry applies the signature policy to one file in the plugins
// directory and, when admitted, builds the plugin. Runs without m.mu.
func (m *Manager) loadEntry(verifier *PluginVerifier, quarantine *Quarantine, fileName string) *ExecutablePlugin {
	pluginPath := filepath.Join(m.pluginsDir, fileName)
	decision, err := verifier.ResolveExec(quarantine, VerifiedCacheDir(m.pluginsDir), pluginPath)
	if err != nil {
		m.logger.Warn("Plugin not loaded", zap.String("plugin", fileName), zap.Error(err))
		return nil
	}
	if decision.Unsigned {
		m.logger.Warn("Loading unsigned plugin (dev mode)", zap.String("plugin", fileName))
	}
	execPath := decision.ExecPath

	plugin, err := newPluginFromExec(pluginPath, execPath)
	if err != nil {
		m.logger.Warn("Arquivo inválido no diretório de plugins, pulando.", zap.String("path", pluginPath), zap.Error(err))
		return nil
	}
	return plugin
}

// toolKey normalizes a tool name for collision checks: the leading "@" is
// optional at lookup (GetPlugin) and case must not make two names distinct.
func toolKey(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "@"))
}

// builtinNameClash reports the built-in that name would shadow. Caller
// holds m.mu.
func (m *Manager) builtinNameClash(name string) (string, bool) {
	key := toolKey(name)
	for builtin := range m.builtins {
		if toolKey(builtin) == key {
			return builtin, true
		}
	}
	return "", false
}

// Quarantine exposes the gate so the /plugin surface can list what is
// waiting and release a reviewed binary.
func (m *Manager) Quarantine() *Quarantine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.quarantine
}

func (m *Manager) GetPlugin(name string) (Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Permite buscar com ou sem o '@'
	p, ok := m.plugins[name]
	if !ok {
		p, ok = m.plugins["@"+name]
	}
	return p, ok
}

func (m *Manager) GetPlugins() []Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		// Skip built-ins that are shadowed by MCP server overrides
		if _, hidden := m.shadowed[p.Name()]; hidden {
			continue
		}
		list = append(list, p)
	}
	// Ordena por nome para consistência
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name() < list[j].Name()
	})
	return list
}

// SetShadowedBuiltins updates the set of built-in plugin names that are hidden
// because MCP servers declared them as overrides. Call this whenever MCP server
// connectivity changes so that built-ins are restored when servers disconnect.
func (m *Manager) SetShadowedBuiltins(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.shadowed = make(map[string]struct{}, len(names))
	for _, n := range names {
		m.shadowed[n] = struct{}{}
	}
}

// RegisterBuiltinPlugin registers a builtin plugin. It is stored in a separate
// map so that Reload() re-injects it on every reload. A built-in always wins
// its name: an executable plugin loaded earlier under the same name (with
// or without "@", in any case) is dropped, since the first Reload runs
// before the built-ins are registered.
func (m *Manager) RegisterBuiltinPlugin(plugin Plugin) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builtins[plugin.Name()] = plugin
	key := toolKey(plugin.Name())
	for name, existing := range m.plugins {
		if _, external := existing.(*ExecutablePlugin); external && toolKey(name) == key {
			m.logger.Warn("Plugin name collides with a built-in tool, unloading it",
				zap.String("plugin", existing.Path()), zap.String("builtin", plugin.Name()))
			delete(m.plugins, name)
		}
	}
	m.plugins[plugin.Name()] = plugin
}

// RegisterRemotePlugin registers a remote plugin in the manager without saving to disk.
// This allows remote plugins to be discoverable via GetPlugin/GetPlugins.
func (m *Manager) RegisterRemotePlugin(plugin Plugin) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins[plugin.Name()] = plugin
}

// ClearRemotePlugins removes all remote plugins (identified by Path() == "[remote]").
func (m *Manager) ClearRemotePlugins() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, p := range m.plugins {
		if p.Path() == "[remote]" {
			delete(m.plugins, name)
		}
	}
}
