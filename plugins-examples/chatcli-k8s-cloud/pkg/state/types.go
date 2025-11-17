package state

import "time"

// Backend interface para abstrair diferentes backends
type Backend interface {
	// Initialize garante que o backend existe e está configurado
	Initialize() error

	// Save salva o estado de um cluster
	Save(clusterName string, state interface{}) error

	// Load carrega o estado de um cluster
	Load(clusterName string, state interface{}) error

	// Delete remove o estado de um cluster
	Delete(clusterName string) error

	// List lista todos os clusters com estado
	List() ([]string, error)

	// Exists verifica se um cluster tem estado salvo
	Exists(clusterName string) (bool, error)

	// Lock adquire lock para operações concorrentes
	Lock(clusterName string) error

	// Unlock libera lock
	Unlock(clusterName string) error

	// GetInfo retorna informações do backend
	GetInfo() BackendInfo
}

// BackendInfo informações sobre o backend
type BackendInfo struct {
	Type              string            `json:"type"`     // s3, azblob
	Location          string            `json:"location"` // bucket/container name
	Region            string            `json:"region"`
	Encrypted         bool              `json:"encrypted"`
	VersioningEnabled bool              `json:"versioningEnabled"`
	LockingEnabled    bool              `json:"lockingEnabled"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

// BackendConfig configuração para criar um backend
type BackendConfig struct {
	// Identificação
	Type   string `json:"type"` // s3, azblob
	Region string `json:"region"`

	// S3 específico
	BucketName    string `json:"bucketName,omitempty"`
	LockTableName string `json:"lockTableName,omitempty"`

	// Segurança
	Encryption EncryptionConfig `json:"encryption"`

	// Lifecycle
	VersioningDays int `json:"versioningDays"` // 0 = manter para sempre
	ArchiveDays    int `json:"archiveDays"`    // mover para Glacier

	// Compliance
	EnableLogging bool   `json:"enableLogging"`
	LogBucket     string `json:"logBucket,omitempty"`
	PublicAccess  bool   `json:"publicAccess"` // default: false

	// Tags
	Tags map[string]string `json:"tags,omitempty"`
}

// EncryptionConfig configuração de criptografia
type EncryptionConfig struct {
	Type     string `json:"type"` // AES256, aws:kms
	KMSKeyID string `json:"kmsKeyId,omitempty"`
}

// LockInfo informações sobre um lock
type LockInfo struct {
	ID        string    `json:"id"`
	Cluster   string    `json:"cluster"`
	Operation string    `json:"operation"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"createdAt"`
}
