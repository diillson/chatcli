/*
 * ChatCLI - AWS Bedrock embeddings provider.
 *
 * Reuses the credential chain, region resolution, IMDS gating and
 * corporate-CA support that the Bedrock chat client already has via
 * the shared bedrock.LoadBedrockRuntime helper. The embedding API
 * lives on the same `bedrock-runtime` endpoint as InvokeModel — there
 * is NO Converse equivalent for embeddings, so each provider family
 * keeps its own body schema:
 *
 *   - Titan v1 / v2 (amazon.titan-embed-text-*): single text per call,
 *     dimension knob on v2 (256 / 512 / 1024).
 *   - Cohere v3 (cohere.embed-*): batch-native, 1024-dim fixed.
 *
 * For Titan, batches are parallelized with a small worker pool to
 * stay within Bedrock's per-account InvokeModel concurrency budget
 * without serializing IO.
 */
package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"go.uber.org/zap"

	"github.com/diillson/chatcli/llm/bedrock"
)

const (
	bedrockDefaultModel = "amazon.titan-embed-text-v2:0"
	// titanV2DefaultDim is the recommended dim for Titan v2 — 1024 is
	// the highest fidelity tier and matches what Anthropic recommends
	// for retrieval. Users who want cheaper storage can drop to 512 or
	// 256 via CHATCLI_EMBED_DIMENSIONS.
	titanV2DefaultDim = 1024
	titanV1Dim        = 1536
	cohereV3Dim       = 1024
	// bedrockTitanBatchConcurrency caps how many parallel InvokeModel
	// calls the provider issues for Titan when the caller hands in a
	// batch. 8 is a defensive default that lands well under typical
	// Bedrock account quotas while still hiding most of the per-call
	// latency behind concurrency.
	bedrockTitanBatchConcurrency = 8
)

// embedFamily identifies the body schema for a Bedrock embedding model.
type embedFamily string

const (
	embedFamilyTitan  embedFamily = "titan"
	embedFamilyCohere embedFamily = "cohere"
)

// Bedrock is the AWS Bedrock embeddings provider.
//
// The runtime client is built lazily on the first Embed call so that
// missing AWS credentials don't break /config dispatch — the provider
// surfaces the error only when the caller actually wants vectors.
type Bedrock struct {
	model   string
	region  string
	profile string
	dim     int
	family  embedFamily
	logger  *zap.Logger

	once    sync.Once
	runtime *bedrockruntime.Client
	initErr error
}

// NewBedrock constructs the provider. region/profile follow the same
// precedence as the chat client (caller resolves env vars). When dim
// is 0, the family default is used (Titan v2: 1024, Titan v1: 1536,
// Cohere v3: 1024). A nil logger is replaced with zap.NewNop().
func NewBedrock(model, region, profile string, dim int, logger *zap.Logger) (*Bedrock, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(model) == "" {
		model = bedrockDefaultModel
	}
	family := resolveEmbedFamily(model)
	if dim <= 0 {
		dim = defaultDim(model, family)
	}
	if family == embedFamilyTitan && !isValidTitanDim(model, dim) {
		return nil, fmt.Errorf("bedrock embeddings: invalid dimension %d for %s (Titan v2 supports 256/512/1024; v1 fixed at 1536)", dim, model)
	}
	return &Bedrock{
		model:   model,
		region:  region,
		profile: profile,
		dim:     dim,
		family:  family,
		logger:  logger,
	}, nil
}

// Name identifies the provider in /config quality output.
func (b *Bedrock) Name() string { return "bedrock:" + b.model }

// Dimension returns the vector dimensionality.
func (b *Bedrock) Dimension() int { return b.dim }

// Embed converts the batch to vectors. Titan models loop with bounded
// parallelism (one InvokeModel per text); Cohere ships the whole batch
// in a single call.
func (b *Bedrock) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if err := b.ensureRuntime(ctx); err != nil {
		return nil, err
	}
	switch b.family {
	case embedFamilyCohere:
		return b.embedCohere(ctx, texts)
	default:
		return b.embedTitan(ctx, texts)
	}
}

func (b *Bedrock) ensureRuntime(ctx context.Context) error {
	b.once.Do(func() {
		runtime, resolvedRegion, err := bedrock.LoadBedrockRuntime(ctx, b.region, b.profile, b.logger)
		if err != nil {
			b.initErr = err
			return
		}
		b.runtime = runtime
		b.region = resolvedRegion
	})
	return b.initErr
}

// ── Titan family ────────────────────────────────────────────────────

type titanRequest struct {
	InputText  string `json:"inputText"`
	Dimensions int    `json:"dimensions,omitempty"`
	Normalize  bool   `json:"normalize,omitempty"`
}

type titanResponse struct {
	Embedding           []float32 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

func (b *Bedrock) embedTitan(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	errs := make([]error, len(texts))

	concurrency := bedrockTitanBatchConcurrency
	if concurrency > len(texts) {
		concurrency = len(texts)
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, text := range texts {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, in string) {
			defer wg.Done()
			defer func() { <-sem }()
			vec, err := b.invokeTitan(ctx, in)
			if err != nil {
				errs[idx] = err
				return
			}
			out[idx] = vec
		}(i, text)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("bedrock titan: index %d: %w", i, err)
		}
	}
	return out, nil
}

func (b *Bedrock) invokeTitan(ctx context.Context, text string) ([]float32, error) {
	body := titanRequest{InputText: text, Normalize: true}
	if b.isTitanV2() {
		body.Dimensions = b.dim
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("titan marshal: %w", err)
	}
	out, err := b.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(b.model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        payload,
	})
	if err != nil {
		return nil, err
	}
	var parsed titanResponse
	if err := json.Unmarshal(out.Body, &parsed); err != nil {
		return nil, fmt.Errorf("titan decode: %w", err)
	}
	if len(parsed.Embedding) == 0 {
		return nil, fmt.Errorf("titan: empty embedding in response")
	}
	return parsed.Embedding, nil
}

// ── Cohere family ───────────────────────────────────────────────────

type cohereRequest struct {
	Texts     []string `json:"texts"`
	InputType string   `json:"input_type"`
	Truncate  string   `json:"truncate,omitempty"`
}

type cohereResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (b *Bedrock) embedCohere(ctx context.Context, texts []string) ([][]float32, error) {
	body := cohereRequest{
		Texts:     texts,
		InputType: "search_document",
		Truncate:  "END",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("cohere marshal: %w", err)
	}
	out, err := b.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(b.model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        payload,
	})
	if err != nil {
		return nil, fmt.Errorf("cohere invoke: %w", err)
	}
	var parsed cohereResponse
	if err := json.Unmarshal(out.Body, &parsed); err != nil {
		return nil, fmt.Errorf("cohere decode: %w", err)
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("cohere returned %d vectors for %d inputs", len(parsed.Embeddings), len(texts))
	}
	return parsed.Embeddings, nil
}

// ── Family + dim helpers ────────────────────────────────────────────

func resolveEmbedFamily(model string) embedFamily {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(m, "cohere.embed") || strings.Contains(m, "cohere-embed") {
		return embedFamilyCohere
	}
	return embedFamilyTitan
}

func defaultDim(model string, family embedFamily) int {
	switch family {
	case embedFamilyCohere:
		return cohereV3Dim
	}
	if isTitanV1ID(model) {
		return titanV1Dim
	}
	return titanV2DefaultDim
}

func (b *Bedrock) isTitanV2() bool {
	return b.family == embedFamilyTitan && !isTitanV1ID(b.model)
}

func isTitanV1ID(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "titan-embed-text-v1")
}

func isValidTitanDim(model string, dim int) bool {
	if isTitanV1ID(model) {
		return dim == titanV1Dim
	}
	switch dim {
	case 256, 512, 1024:
		return true
	}
	return false
}
