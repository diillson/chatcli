/*
 * ChatCLI - Voyage AI embeddings provider (Anthropic-recommended).
 *
 * Voyage offers strong quality/$ for retrieval. Default model is
 * `voyage-3` (1024 dim) which is the recommended general-purpose model
 * as of 2026; override via CHATCLI_EMBED_MODEL.
 */
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	voyageDefaultModel = "voyage-3"
	voyageDefaultDim   = 1024
	voyageEndpoint     = "https://api.voyageai.com/v1/embeddings"
)

// Voyage is the Voyage AI provider.
type Voyage struct {
	apiKey   string
	model    string
	endpoint string
	dim      int
	client   *http.Client
}

// NewVoyage constructs a Voyage provider. Returns an error when
// apiKey is empty.
func NewVoyage(apiKey, model string) (*Voyage, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("voyage: API key is required (set VOYAGE_API_KEY)")
	}
	if strings.TrimSpace(model) == "" {
		model = voyageDefaultModel
	}
	return &Voyage{
		apiKey:   apiKey,
		model:    model,
		endpoint: voyageEndpoint,
		dim:      voyageDefaultDim,
		client:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name identifies the provider in /config quality output.
func (v *Voyage) Name() string { return "voyage:" + v.model }

// Dimension returns the vector dimensionality.
func (v *Voyage) Dimension() int { return v.dim }

type voyageRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type voyageResponseDatum struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type voyageResponse struct {
	Data  []voyageResponseDatum `json:"data"`
	Model string                `json:"model"`
}

// Embed sends the batch to the Voyage embeddings endpoint and returns
// the vectors in the same order as the input.
func (v *Voyage) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(voyageRequest{Input: texts, Model: v.model})
	if err != nil {
		return nil, fmt.Errorf("voyage marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voyage HTTP %d: %s", resp.StatusCode, truncateBytes(body, 200))
	}
	var parsed voyageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("voyage decode: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("voyage returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("voyage out-of-range index %d", d.Index)
		}
		out[d.Index] = d.Embedding
		if v.dim == voyageDefaultDim && len(d.Embedding) > 0 {
			v.dim = len(d.Embedding)
		}
	}
	return out, nil
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
