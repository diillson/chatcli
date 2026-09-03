/*
 * ChatCLI - Ollama embeddings provider (keyless, local).
 *
 * Talks to a local Ollama server (OLLAMA_HOST, default
 * http://localhost:11434) through POST /api/embed. No key, no metering:
 * the keyless-first floor for semantic retrieval on a laptop. The model
 * (CHATCLI_EMBED_MODEL, default nomic-embed-text) decides the dimension,
 * learned from the first embedding: the vector store adopts the first
 * vector's length and validates every later one against it.
 */
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	ollamaDefaultModel = "nomic-embed-text"
	ollamaDefaultHost  = "http://localhost:11434"
)

// Ollama embeds through a local Ollama server.
type Ollama struct {
	host   string
	model  string
	mu     sync.RWMutex
	dim    int
	client *http.Client
}

// NewOllama builds the provider. The dimension is learned from the first
// embedding when not given (the vector index adopts the first vector's
// length), so construction needs no network round trip and no context.
func NewOllama(host, model string, dim int) (*Ollama, error) {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		host = ollamaDefaultHost
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	if strings.TrimSpace(model) == "" {
		model = ollamaDefaultModel
	}
	return &Ollama{host: host, model: model, dim: dim, client: &http.Client{Timeout: 120 * time.Second}}, nil
}

// Name implements Provider.
func (o *Ollama) Name() string { return "ollama:" + o.model }

// Dimension implements Provider: 0 until the first embedding taught it.
func (o *Ollama) Dimension() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.dim
}

type ollamaRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

// Embed implements Provider.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(ollamaRequest{Model: o.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("ollama embeddings marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.host+"/api/embed", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embeddings request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embeddings HTTP %d: %s", resp.StatusCode, truncateBytes(raw, 200))
	}
	var parsed ollamaResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ollama embeddings decode: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("ollama embeddings: %s", parsed.Error)
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama returned %d vectors for %d inputs", len(parsed.Embeddings), len(texts))
	}
	o.mu.Lock()
	if o.dim == 0 && len(parsed.Embeddings) > 0 {
		o.dim = len(parsed.Embeddings[0])
	}
	dim := o.dim
	o.mu.Unlock()
	for i, v := range parsed.Embeddings {
		if len(v) != dim {
			return nil, fmt.Errorf("ollama vector %d has %d dims, expected %d", i, len(v), dim)
		}
	}
	return parsed.Embeddings, nil
}

// ollamaHostFromEnv honors OLLAMA_HOST (the Ollama convention).
func ollamaHostFromEnv() string {
	return os.Getenv("OLLAMA_HOST")
}
