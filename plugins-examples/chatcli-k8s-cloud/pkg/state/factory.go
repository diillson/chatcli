package state

import (
	"fmt"
	"net/url"
	"strings"
)

// NewBackend cria um backend baseado na URL
// Suporta:
//   - s3://bucket-name/path
//   - s3://bucket-name (usa path padrão)
func NewBackend(backendURL, region string) (Backend, error) {
	if backendURL == "" {
		return nil, fmt.Errorf("backend URL é obrigatória")
	}

	// Parse URL
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("URL de backend inválida: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "s3":
		bucketName := u.Host
		if bucketName == "" {
			return nil, fmt.Errorf("nome do bucket S3 não especificado")
		}

		// Lock table pode ser customizado via query param
		// Exemplo: s3://bucket?lock-table=my-locks
		lockTable := u.Query().Get("lock-table")

		backend := NewS3Backend(bucketName, region, lockTable)
		return backend, nil

	case "azblob":
		// TODO: Implementar Azure Blob backend
		return nil, fmt.Errorf("backend Azure Blob ainda não implementado")

	default:
		return nil, fmt.Errorf("tipo de backend desconhecido: %s (suportados: s3, azblob)", u.Scheme)
	}
}
