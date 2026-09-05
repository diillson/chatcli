package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	watcher    *fsnotify.Watcher
	closeOnce  sync.Once
	quarantine *Quarantine
}

func NewManager(logger *zap.Logger) (*Manager, error) {
	home, err := utils.GetHomeDir()
	if err != nil {
		return nil, fmt.Errorf("não foi possível encontrar o diretório home para plugins: %w", err)
	}
	pluginsDir := filepath.Join(home, ".chatcli", "plugins")

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
func (m *Manager) Reload() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.plugins = make(map[string]Plugin)

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
	m.quarantine = NewQuarantine(m.pluginsDir)
	if raw, bad := ConfiguredButUnparseable(); bad {
		m.logger.Warn("CHATCLI_PLUGIN_QUARANTINE is not a duration; quarantine stays off",
			zap.String("value", raw))
	}

	present := make(map[string]struct{}, len(entries))

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sig") || strings.HasPrefix(entry.Name(), ".") {
			continue // skip signature files and hidden files
		}
		pluginPath := filepath.Join(m.pluginsDir, entry.Name())
		present[entry.Name()] = struct{}{}

		// Inspect the signature, then apply policy. The two are separate
		// because "unsigned" and "signature does not verify" are different
		// facts that call for different handling.
		switch status, err := verifier.Inspect(pluginPath); status {
		case StatusVerified:
			// Signed by a key this machine trusts: load it.

		case StatusUnsigned, StatusUnverifiable:
			// Unverifiable joins unsigned here on purpose: with no trusted
			// key on this machine, a signature is not evidence of anything,
			// so it neither helps nor should it start refusing a plugin that
			// loaded before.
			if !verifier.AllowsUnsigned() {
				m.logger.Warn("Plugin signature cannot be trusted and unsigned plugins are not allowed, skipping",
					zap.String("plugin", entry.Name()), zap.Error(err))
				continue
			}
			// Unsigned, and unsigned is tolerated. Quarantine — when
			// configured — is the remaining gate: a binary that just
			// appeared waits before it can run with this process's
			// permissions.
			if admitted, remaining := m.quarantine.Admit(pluginPath); !admitted {
				m.logger.Warn("Plugin held in quarantine",
					zap.String("plugin", entry.Name()),
					zap.Duration("remaining", remaining.Round(time.Second)))
				continue
			}
			m.logger.Warn("Loading unsigned plugin (dev mode)", zap.String("plugin", entry.Name()))

		default:
			m.logger.Warn("Plugin signature verification failed, skipping",
				zap.String("plugin", entry.Name()), zap.Error(err))
			continue
		}

		plugin, err := NewPluginFromPath(pluginPath)
		if err != nil {
			m.logger.Warn("Arquivo inválido no diretório de plugins, pulando.", zap.String("path", pluginPath), zap.Error(err))
			continue
		}
		m.plugins[plugin.Name()] = plugin
	}

	// Re-inject builtins that were not overridden by external plugins
	for name, bp := range m.builtins {
		if _, exists := m.plugins[name]; !exists {
			m.plugins[name] = bp
		}
	}

	// A plugin that is gone must not leave a record that would admit a
	// future binary of the same name without its own waiting period.
	if m.quarantine.Enabled() {
		for _, e := range m.quarantine.List() {
			if _, still := present[e.Name]; !still {
				m.quarantine.Forget(e.Name)
			}
		}
	}

	m.logger.Info("Plugins recarregados.", zap.Int("count", len(m.plugins)))
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
// map so that Reload() can re-inject it when no external override exists.
func (m *Manager) RegisterBuiltinPlugin(plugin Plugin) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builtins[plugin.Name()] = plugin
	// Only inject if no external plugin already loaded with the same name
	if _, exists := m.plugins[plugin.Name()]; !exists {
		m.plugins[plugin.Name()] = plugin
	}
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
