package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/diillson/chatcli/utils"
	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// Manager descobre, carrega e gerencia o ciclo de vida dos plugins.
type Manager struct {
	plugins    map[string]Plugin
	pluginsDir string
	logger     *zap.Logger
	mu         sync.RWMutex
	watcher    *fsnotify.Watcher
	closeOnce  sync.Once
}

func NewManager(logger *zap.Logger) (*Manager, error) {
	home, err := utils.GetHomeDir()
	if err != nil {
		return nil, fmt.Errorf("não foi possível encontrar o diretório home para plugins: %w", err)
	}
	pluginsDir := filepath.Join(home, ".chatcli", "plugins")

	// Garante que o diretório de plugins exista
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		return nil, fmt.Errorf("não foi possível criar o diretório de plugins: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("não foi possível criar o observador de arquivos: %w", err)
	}

	m := &Manager{
		plugins:    make(map[string]Plugin),
		pluginsDir: pluginsDir,
		logger:     logger,
		watcher:    watcher,
	}
	m.Reload()
	go m.watchForChanges()

	return m, nil
}

// Close encerra o watcher de arquivos de forma segura.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		if m.watcher != nil {
			m.watcher.Close()
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

	if err := os.MkdirAll(m.pluginsDir, 0755); err != nil {
		m.logger.Error("Não foi possível criar o diretório de plugins", zap.String("path", m.pluginsDir), zap.Error(err))
		return
	}

	entries, err := os.ReadDir(m.pluginsDir)
	if err != nil {
		m.logger.Error("Erro ao ler o diretório de plugins", zap.Error(err))
		return
	}

	for _, entry := range entries {
		pluginPath := filepath.Join(m.pluginsDir, entry.Name())
		plugin, err := NewPluginFromPath(pluginPath)
		if err != nil {
			m.logger.Warn("Arquivo inválido no diretório de plugins, pulando.", zap.String("path", pluginPath), zap.Error(err))
			continue
		}
		m.plugins[plugin.Name()] = plugin
	}
	m.logger.Info("Plugins recarregados.", zap.Int("count", len(m.plugins)))
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
		list = append(list, p)
	}
	// Ordena por nome para consistência
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name() < list[j].Name()
	})
	return list
}
