package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

var (
	storeMu     sync.Mutex
	cachedStore *AuthProfileStore
	// storeLoadErr records why the store on disk could not be opened. While
	// it is set the store is read-only: the in-memory store is empty, and
	// saving it would overwrite every credential the file still holds.
	storeLoadErr error
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

	data, err := os.ReadFile(storePath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err != nil {
		if os.IsNotExist(err) {
			if logger != nil {
				logger.Debug(i18n.T("auth.store.not_found"), zap.String("path", storePath))
			}
			store := NewAuthProfileStore()
			cachedStore = store
			return store
		}
		return lockStoreUnlocked(storePath, i18n.T("auth.store.read_failed"), err, logger)
	}

	// H3: Decrypt data (transparently handles unencrypted legacy stores)
	plaintext, err := decryptData(data)
	if err != nil {
		return lockStoreUnlocked(storePath, i18n.T("auth.store.decrypt_failed"), err, logger)
	}

	var store AuthProfileStore
	if err := json.Unmarshal(plaintext, &store); err != nil {
		return lockStoreUnlocked(storePath, i18n.T("auth.store.parse_failed"), err, logger)
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

// lockStoreUnlocked records that the store at path could not be opened and
// returns an empty in-memory store. Reads see no profiles; every write is
// refused until the cause is fixed and the process restarts (or the cache
// is invalidated), so the file is never replaced by the empty store.
func lockStoreUnlocked(path, what string, cause error, logger *zap.Logger) *AuthProfileStore {
	storeLoadErr = fmt.Errorf("%s: %w", what, cause)
	if logger != nil {
		logger.Error(what, zap.String("path", path), zap.Error(cause))
		logger.Error(i18n.T("auth.store.locked", path, getKeyPath(), keychainEnvVar))
	}
	s := NewAuthProfileStore()
	cachedStore = s
	return s
}

// SaveStore salva o store no disco.
func SaveStore(store *AuthProfileStore, logger *zap.Logger) error {
	storeMu.Lock()
	defer storeMu.Unlock()

	return saveStoreUnlocked(store, logger)
}

func saveStoreUnlocked(store *AuthProfileStore, logger *zap.Logger) error {
	storePath := DefaultStorePath()

	// A save that was not preceded by a load still has to know whether the
	// file on disk opens: only then is replacing it safe.
	if cachedStore == nil {
		loadStoreUnlocked(logger)
	}
	if storeLoadErr != nil {
		return fmt.Errorf("%s: %w", i18n.T("auth.store.locked", storePath, getKeyPath(), keychainEnvVar), storeLoadErr)
	}

	if err := EnsureStoreDir(storePath); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("auth.store.create_dir_failed"), err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("auth.store.marshal_failed"), err)
	}

	// H3: Encrypt data before writing to disk
	encrypted, err := encryptData(data)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("auth.store.encrypt_failed"), err)
	}

	if err := backupStoreFile(storePath); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("auth.store.write_failed"), err)
	}
	// fsync'd temp file and rename: a crash mid-write never leaves a
	// truncated credential store behind.
	if err := utils.AtomicWriteFile(storePath, encrypted, 0o600); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("auth.store.write_failed"), err)
	}

	cachedStore = store

	if logger != nil {
		logger.Debug(i18n.T("auth.store.saved"), zap.String("path", storePath), zap.Int("profiles", len(store.Profiles)))
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
	storeLoadErr = nil
}

// storeBackupSuffix names the copy of the previous store kept next to it.
const storeBackupSuffix = ".bak"

// backupStoreFile copies the current store file to path+".bak" (0600)
// before it is replaced, so a write can always be undone by hand. A missing
// store needs no backup.
func backupStoreFile(path string) error {
	data, err := os.ReadFile(filepath.Clean(path)) //#nosec G304 -- the credential store path (CHATCLI_AUTH_DIR or ~/.chatcli)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(path+storeBackupSuffix, data, 0o600)
}
