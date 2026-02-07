package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"
)

var (
	storeMu     sync.Mutex
	cachedStore *AuthProfileStore
)

// DefaultStorePath retorna o caminho padrão do arquivo de perfis de autenticação.
func DefaultStorePath() string {
	if dir := os.Getenv("CHATCLI_AUTH_DIR"); dir != "" {
		return filepath.Join(dir, "auth-profiles.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".chatcli", "auth-profiles.json")
}

// EnsureStoreDir ensures the directory for the store file exists.
func EnsureStoreDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0o700)
}

// LoadStore carrega o store do disco.
func LoadStore(logger *zap.Logger) *AuthProfileStore {
	storeMu.Lock()
	defer storeMu.Unlock()

	return loadStoreUnlocked(logger)
}

func loadStoreUnlocked(logger *zap.Logger) *AuthProfileStore {
	if cachedStore != nil {
		return cachedStore
	}

	storePath := DefaultStorePath()

	data, err := os.ReadFile(storePath)
	if err != nil {
		if os.IsNotExist(err) {
			if logger != nil {
				logger.Debug("Auth store not found, creating new", zap.String("path", storePath))
			}
			store := NewAuthProfileStore()
			cachedStore = store
			return store
		}
		if logger != nil {
			logger.Error("Failed to read auth store", zap.Error(err))
		}
		store := NewAuthProfileStore()
		cachedStore = store
		return store
	}

	var store AuthProfileStore
	if err := json.Unmarshal(data, &store); err != nil {
		if logger != nil {
			logger.Error("Failed to parse auth store", zap.Error(err))
		}
		s := NewAuthProfileStore()
		cachedStore = s
		return s
	}

	if store.Profiles == nil {
		store.Profiles = make(map[string]*AuthProfileCredential)
	}
	if store.Order == nil {
		store.Order = make(map[string][]string)
	}
	if store.LastGood == nil {
		store.LastGood = make(map[string]string)
	}

	cachedStore = &store
	return &store
}

// SaveStore salva o store no disco.
func SaveStore(store *AuthProfileStore, logger *zap.Logger) error {
	storeMu.Lock()
	defer storeMu.Unlock()

	return saveStoreUnlocked(store, logger)
}

func saveStoreUnlocked(store *AuthProfileStore, logger *zap.Logger) error {
	storePath := DefaultStorePath()

	if err := EnsureStoreDir(storePath); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal auth store: %w", err)
	}

	if err := os.WriteFile(storePath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write auth store: %w", err)
	}

	cachedStore = store

	if logger != nil {
		logger.Debug("Auth store saved", zap.String("path", storePath), zap.Int("profiles", len(store.Profiles)))
	}

	return nil
}

// UpsertProfile adiciona ou atualiza um perfil no store.
func UpsertProfile(profileID string, cred *AuthProfileCredential, logger *zap.Logger) error {
	storeMu.Lock()
	defer storeMu.Unlock()

	store := loadStoreUnlocked(logger)
	store.Profiles[profileID] = cred
	return saveStoreUnlocked(store, logger)
}

// GetProfile retorna um perfil pelo ID.
func GetProfile(profileID string, logger *zap.Logger) *AuthProfileCredential {
	store := LoadStore(logger)
	return store.Profiles[profileID]
}

// ListProfilesForProvider retorna todos os profile IDs para um provedor.
func ListProfilesForProvider(provider ProviderID, logger *zap.Logger) []string {
	store := LoadStore(logger)
	var ids []string
	for id, cred := range store.Profiles {
		if cred.Provider == provider {
			ids = append(ids, id)
		}
	}
	return ids
}

// DeleteProfile remove um perfil do store.
func DeleteProfile(profileID string, logger *zap.Logger) error {
	storeMu.Lock()
	defer storeMu.Unlock()

	store := loadStoreUnlocked(logger)
	delete(store.Profiles, profileID)
	return saveStoreUnlocked(store, logger)
}

// InvalidateCache limpa o cache em memória.
func InvalidateCache() {
	storeMu.Lock()
	defer storeMu.Unlock()
	cachedStore = nil
}
