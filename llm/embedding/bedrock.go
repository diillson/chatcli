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
 *   - Cohere v3 (cohere.embed-*-v3): batch-native, 1024-dim fixed.
 *   - Cohere v4 (cohere.embed-v4:0, optionally us./eu./global. profile):
 *     batch-native, embeddings come back keyed by type ({"float": [...]}),
 *     1536-dim default.
 *   - Nova Multimodal Embeddings (amazon.nova-2-multimodal-embeddings-*):
 *     single text per call (SINGLE_EMBEDDING task), dimension knob
 *     (256 / 384 / 1024 / 3072).
 *
 * For the single-text families, batches are parallelized with a small
 * worker pool to stay within Bedrock's per-account InvokeModel
 * concurrency budget without serializing IO.
 */
package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

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
	cohereV4Dim       = 1536
	novaMMEDefaultDim = 3072
	// bedrockSingleBatchConcurrency caps how many parallel InvokeModel
	// calls the provider issues for the single-text-per-call families
	// (Titan, Nova MME) when the caller hands in a batch. 8 is a
	// defensive default that lands well under typical Bedrock account
	// quotas while still hiding most of the per-call latency behind
	// concurrency.
	bedrockSingleBatchConcurrency = 8
)

// embedFamily identifies the body schema for a Bedrock embedding model.
type embedFamily string

const (
	embedFamilyTitan  embedFamily = "titan"
	embedFamilyCohere embedFamily = "cohere"
	embedFamilyNova   embedFamily = "nova"
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

	once     sync.Once
	runtime  *bedrockruntime.Client
	endpoint string
	initErr  error

	// Credential circuit breaker. An expired SSO session fails at
	// credential resolution on EVERY call — slowly (IMDS probing) and
	// noisily. After a credential-class failure the breaker fails fast
	// until credRetryAt; the half-open retry window picks up a mid-session
	// `aws sso login` automatically.
	credMu      sync.Mutex
	credRetryAt time.Time
}

// NewBedrock constructs the provider. region/profile follow the same
// precedence as the chat client (caller resolves env vars). When dim
// is 0, the family default is used (Titan v2: 1024, Titan v1: 1536,
// Cohere v3: 1024, Cohere v4: 1536, Nova MME: 3072). A nil logger is
// replaced with zap.NewNop().
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
	if family == embedFamilyNova && !isValidNovaDim(dim) {
		return nil, fmt.Errorf("bedrock embeddings: invalid dimension %d for %s (Nova Multimodal Embeddings supports 256/384/1024/3072)", dim, model)
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

// Embed converts the batch to vectors. Titan and Nova MME models loop
// with bounded parallelism (one InvokeModel per text); Cohere ships the
// whole batch in a single call.
func (b *Bedrock) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	b.credMu.Lock()
	if time.Now().Before(b.credRetryAt) {
		wait := time.Until(b.credRetryAt).Round(time.Second)
		b.credMu.Unlock()
		return nil, fmt.Errorf("%w (aws credential check suspended, retrying in %s)", ErrCredentialsUnavailable, wait)
	}
	b.credMu.Unlock()
	if err := b.ensureRuntime(ctx); err != nil {
		return nil, b.classifyCredError(err)
	}
	b.logger.Debug("bedrock embeddings: request",
		zap.String("model", b.model),
		zap.String("family", string(b.family)),
		zap.String("region", b.region),
		zap.String("endpoint", b.endpoint),
		zap.Int("batch_size", len(texts)),
	)
	var out [][]float32
	var err error
	switch b.family {
	case embedFamilyCohere:
		out, err = b.embedCohere(ctx, texts)
	case embedFamilyNova:
		out, err = b.embedPerText(ctx, texts, "nova", b.invokeNova)
	default:
		out, err = b.embedPerText(ctx, texts, "titan", b.invokeTitan)
	}
	if err != nil {
		return nil, b.classifyCredError(err)
	}
	return out, nil
}

// bedrockCredBreakerWindow is how long the breaker fails fast after a
// credential-class failure before allowing a live retry (half-open).
// Long enough to stop per-call IMDS probing storms, short enough that a
// mid-session `aws sso login` is picked up without restarting ChatCLI.
const bedrockCredBreakerWindow = 2 * time.Minute

// classifyCredError trips the breaker and wraps err in
// ErrCredentialsUnavailable when it is a credential-resolution failure
// (expired SSO, no IMDS role, missing chain); other errors pass through.
func (b *Bedrock) classifyCredError(err error) error {
	if err == nil || !isAWSCredentialError(err) {
		return err
	}
	b.credMu.Lock()
	tripped := time.Now().Before(b.credRetryAt)
	b.credRetryAt = time.Now().Add(bedrockCredBreakerWindow)
	b.credMu.Unlock()
	if !tripped {
		b.logger.Warn("bedrock embeddings: AWS credentials unavailable (expired SSO session?) — suspending embedding calls",
			zap.Duration("retry_in", bedrockCredBreakerWindow),
			zap.String("hint", "run 'aws sso login"+profileHint(b.profile)+"' to restore vector features; keyword retrieval keeps working"),
			zap.Error(err),
		)
	}
	return fmt.Errorf("%w: %w", ErrCredentialsUnavailable, err)
}

func profileHint(profile string) string {
	if profile == "" {
		return ""
	}
	return " --profile " + profile
}

// isAWSCredentialError reports whether err is an AWS credential-chain
// failure. The marker list lives in llm/bedrock (IsCredentialError) so chat,
// listing and embeddings classify the same error identically — two copies
// drifted apart is how an expired session tripped the breaker on one surface
// and surfaced raw on another.
func isAWSCredentialError(err error) bool {
	return bedrock.IsCredentialError(err)
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
		b.endpoint = bedrock.RuntimeEndpointURL(resolvedRegion)
		b.logger.Info("bedrock embeddings: configured",
			zap.String("region", b.region),
			zap.String("endpoint", b.endpoint),
			zap.String("model", b.model),
			zap.String("family", string(b.family)),
			zap.Int("dim", b.dim),
		)
	})
	return b.initErr
}

// ── Single-text families (Titan, Nova MME) ──────────────────────────

// embedPerText fans the batch out over invoke with bounded parallelism
// for the families whose API takes one text per InvokeModel call.
func (b *Bedrock) embedPerText(ctx context.Context, texts []string, label string, invoke func(context.Context, string) ([]float32, error)) ([][]float32, error) {
	out := make([][]float32, len(texts))
	errs := make([]error, len(texts))

	concurrency := bedrockSingleBatchConcurrency
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
			vec, err := invoke(ctx, in)
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
			return nil, fmt.Errorf("bedrock %s: index %d: %w", label, i, err)
		}
	}
	return out, nil
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

// ── Nova Multimodal Embeddings family ───────────────────────────────

// novaRequest is the SINGLE_EMBEDDING task shape of Nova Multimodal
// Embeddings (amazon.nova-2-multimodal-embeddings). Only the text
// modality is used here; segmentation and audio/video need the async
// API and are out of scope for retrieval embeddings.
type novaRequest struct {
	TaskType              string              `json:"taskType"`
	SingleEmbeddingParams novaEmbeddingParams `json:"singleEmbeddingParams"`
}

type novaEmbeddingParams struct {
	EmbeddingPurpose   string       `json:"embeddingPurpose"`
	EmbeddingDimension int          `json:"embeddingDimension"`
	Text               novaTextSpec `json:"text"`
}

type novaTextSpec struct {
	TruncationMode string `json:"truncationMode"`
	Value          string `json:"value"`
}

type novaResponse struct {
	Embeddings []struct {
		EmbeddingType string    `json:"embeddingType"`
		Embedding     []float32 `json:"embedding"`
	} `json:"embeddings"`
}

func (b *Bedrock) invokeNova(ctx context.Context, text string) ([]float32, error) {
	body := novaRequest{
		TaskType: "SINGLE_EMBEDDING",
		SingleEmbeddingParams: novaEmbeddingParams{
			EmbeddingPurpose:   "GENERIC_INDEX",
			EmbeddingDimension: b.dim,
			Text:               novaTextSpec{TruncationMode: "END", Value: text},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("nova marshal: %w", err)
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
	var parsed novaResponse
	if err := json.Unmarshal(out.Body, &parsed); err != nil {
		return nil, fmt.Errorf("nova decode: %w", err)
	}
	if len(parsed.Embeddings) == 0 || len(parsed.Embeddings[0].Embedding) == 0 {
		return nil, fmt.Errorf("nova: empty embedding in response")
	}
	return parsed.Embeddings[0].Embedding, nil
}

// ── Cohere family ───────────────────────────────────────────────────

type cohereRequest struct {
	Texts     []string `json:"texts"`
	InputType string   `json:"input_type"`
	Truncate  string   `json:"truncate,omitempty"`
	// EmbeddingTypes is required by Embed v4 (whose response keys the
	// vectors by type); v3 works without it and keeps its flat response,
	// so it is only sent for v4.
	EmbeddingTypes []string `json:"embedding_types,omitempty"`
}

// cohereResponse tolerates both Cohere response generations: v3 returns
// "embeddings" as a plain array of vectors, v4 (embedding_types) keys
// them by type ({"embeddings": {"float": [...]}}).
type cohereResponse struct {
	Embeddings json.RawMessage `json:"embeddings"`
}

func (r *cohereResponse) vectors() ([][]float32, error) {
	var flat [][]float32
	if err := json.Unmarshal(r.Embeddings, &flat); err == nil {
		return flat, nil
	}
	var keyed struct {
		Float [][]float32 `json:"float"`
	}
	if err := json.Unmarshal(r.Embeddings, &keyed); err != nil {
		return nil, err
	}
	return keyed.Float, nil
}

func (b *Bedrock) embedCohere(ctx context.Context, texts []string) ([][]float32, error) {
	body := cohereRequest{
		Texts:     texts,
		InputType: "search_document",
		Truncate:  "END",
	}
	if isCohereV4ID(b.model) {
		body.EmbeddingTypes = []string{"float"}
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
	vecs, err := parsed.vectors()
	if err != nil {
		return nil, fmt.Errorf("cohere decode embeddings: %w", err)
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("cohere returned %d vectors for %d inputs", len(vecs), len(texts))
	}
	return vecs, nil
}

// ── Family + dim helpers ────────────────────────────────────────────

func resolveEmbedFamily(model string) embedFamily {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(m, "cohere.embed") || strings.Contains(m, "cohere-embed") {
		return embedFamilyCohere
	}
	// amazon.nova-2-multimodal-embeddings-v1:0, optionally behind an
	// inference-profile prefix (us./eu./global.).
	if strings.Contains(m, "nova") && strings.Contains(m, "embed") {
		return embedFamilyNova
	}
	return embedFamilyTitan
}

func defaultDim(model string, family embedFamily) int {
	switch family {
	case embedFamilyCohere:
		if isCohereV4ID(model) {
			return cohereV4Dim
		}
		return cohereV3Dim
	case embedFamilyNova:
		return novaMMEDefaultDim
	}
	if isTitanV1ID(model) {
		return titanV1Dim
	}
	return titanV2DefaultDim
}

// isCohereV4ID reports whether the model is Cohere Embed v4
// (cohere.embed-v4:0, possibly profile-prefixed) as opposed to the v3
// pair (cohere.embed-english-v3 / cohere.embed-multilingual-v3).
func isCohereV4ID(model string) bool {
	return strings.Contains(strings.ToLower(model), "embed-v4")
}

func isValidNovaDim(dim int) bool {
	switch dim {
	case 256, 384, 1024, 3072:
		return true
	}
	return false
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
