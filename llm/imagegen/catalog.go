/*
 * ChatCLI - Image model catalog.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A small static catalog of image-capable models per backend, plus an optional
 * live GET against OpenAI's /v1/models so users can discover what's actually
 * available on their account — mirroring how the conversational side exposes a
 * catalog + dynamic listing.
 */
package imagegen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ModelInfo describes one image-capable model.
type ModelInfo struct {
	Name     string // model id
	Provider string // openai | google | xai | zai | minimax | bedrock | sdwebui
	API      string // images | responses | gemini | native | stability
	Note     string

	// Generate: the model turns a text prompt into an image.
	Generate bool
	// Edit: the model edits an input image through @image edit.
	Edit bool
	// Status is "active", "preview" or "deprecated".
	Status string
	// Retires is the provider's shutdown date (YYYY-MM-DD). From that day
	// on the model is left out of KnownModels.
	Retires string
	// Regions lists the AWS regions a Bedrock model is offered in,
	// comma-separated (a string keeps ModelInfo comparable); the first one
	// is used when the configured region does not offer it. RegionList
	// splits it.
	Regions string
	// Profile marks a Bedrock model invoked only through the US
	// cross-region inference profile ("us." + id).
	Profile bool
	// Default marks the provider's default model.
	Default bool
}

// catalogNow is the clock KnownModels filters by; tests replace it.
var catalogNow = time.Now

// KnownModels returns the curated catalog of image models still in service:
// entries whose shutdown date has arrived are left out, so a model that
// reaches its retirement leaves the list on that day without a release.
func KnownModels() []ModelInfo {
	today := catalogNow().UTC().Format("2006-01-02")
	all := allModels()
	out := make([]ModelInfo, 0, len(all))
	for _, m := range all {
		if m.Retires != "" && m.Retires <= today {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Bedrock regions, from the model cards and ListFoundationModels.
const (
	// Stable Image Core/Ultra and SD3.5 Large: In-Region in us-west-2 only.
	bedrockStabilityRegions = "us-west-2"
	// Stability Image Services: only through the "us." profile, callable
	// from these source regions.
	bedrockServicesRegions = "us-east-1,us-west-2,us-east-2"
)

// RegionList returns Regions as a list.
func (m ModelInfo) RegionList() []string {
	if m.Regions == "" {
		return nil
	}
	return strings.Split(m.Regions, ",")
}

// allModels is the full catalog, retired entries included so a live listing
// that still reports a shut-down id can be recognized and left out.
// Sources: each provider's model pages and deprecation lists (Oct 2026).
func allModels() []ModelInfo {
	stab := bedrockStabilityRegions
	svc := bedrockServicesRegions
	return []ModelInfo{
		// OpenAI Images API. gpt-image-2.5 (2026-09) is the current
		// generation; the gpt-image-1 family is deprecated; DALL-E 2/3 were
		// shut down on 2026-05-12.
		{Name: "gpt-image-2.5-flare", Provider: "openai", API: "images", Generate: true, Edit: true, Status: "active", Default: true, Note: "Fastest gpt-image-2.5, for everyday images."},
		{Name: "gpt-image-2.5-sunburst", Provider: "openai", API: "images", Generate: true, Edit: true, Status: "active", Note: "Most capable gpt-image-2.5; best editing precision; quality up to max."},
		{Name: "gpt-image-2", Provider: "openai", API: "images", Generate: true, Edit: true, Status: "active", Note: "Previous generation."},
		{Name: "gpt-image-1.5", Provider: "openai", API: "images", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-12-01", Note: "Replaced by gpt-image-2.5."},
		{Name: "gpt-image-1-mini", Provider: "openai", API: "images", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-12-01", Note: "Replaced by gpt-image-2.5-flare."},
		{Name: "chatgpt-image-latest", Provider: "openai", API: "images", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-12-01", Note: "Replaced by gpt-image-2.5."},
		{Name: "gpt-image-1", Provider: "openai", API: "images", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-10-23", Note: "Replaced by gpt-image-2.5."},
		// OpenAI Responses API: a chat model draws through the
		// image_generation tool and edits with action "edit".
		{Name: "gpt-5.6-sol", Provider: "openai", API: "responses", Generate: true, Edit: true, Status: "active", Default: true, Note: "Flagship chat model with the image_generation tool."},
		{Name: "gpt-5.6-terra", Provider: "openai", API: "responses", Generate: true, Edit: true, Status: "active", Note: "Mid-tier chat model with the image_generation tool."},
		{Name: "gpt-5.6-luna", Provider: "openai", API: "responses", Generate: true, Edit: true, Status: "active", Note: "Cheapest chat model with the image_generation tool."},
		{Name: "gpt-5.5", Provider: "openai", API: "responses", Generate: true, Edit: true, Status: "active", Note: "Chat model with the image_generation tool."},
		{Name: "gpt-5.4-mini", Provider: "openai", API: "responses", Generate: true, Edit: true, Status: "active", Note: "Small chat model with the image_generation tool."},
		// Google Gemini image models (generateContent): generate and edit.
		// Imagen was shut down on 2026-08-17.
		{Name: "gemini-nano-banana-2.1", Provider: "google", API: "gemini", Generate: true, Edit: true, Status: "active", Default: true, Note: "Nano Banana 2.1 (2026-10); Google's recommended image model; 1K-4K."},
		{Name: "gemini-3.1-flash-image", Provider: "google", API: "gemini", Generate: true, Edit: true, Status: "active", Note: "Nano Banana 2; adds 512px output."},
		{Name: "gemini-3-pro-image", Provider: "google", API: "gemini", Generate: true, Edit: true, Status: "active", Note: "Nano Banana Pro; studio quality."},
		{Name: "gemini-3.1-flash-lite-image", Provider: "google", API: "gemini", Generate: true, Edit: true, Status: "active", Note: "Nano Banana 2 Lite; fastest; 1K only."},
		{Name: "gemini-2.5-flash-image", Provider: "google", API: "gemini", Generate: true, Edit: true, Status: "deprecated", Retires: "2027-03-15", Note: "Replaced by gemini-3.1-flash-lite-image."},
		{Name: "gemini-3.1-flash-image-preview", Provider: "google", API: "gemini", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-06-25"},
		{Name: "gemini-3-pro-image-preview", Provider: "google", API: "gemini", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-06-25"},
		// xAI Grok Imagine: OpenAI-shaped generation, JSON edits.
		{Name: "grok-imagine-image-2.0", Provider: "xai", API: "native", Generate: true, Edit: true, Status: "active", Default: true, Note: "Grok Imagine 2.0; edits take up to 5 source images."},
		{Name: "grok-imagine-image", Provider: "xai", API: "native", Generate: true, Edit: true, Status: "active", Note: "Cheaper Grok Imagine tier."},
		{Name: "grok-imagine-image-quality", Provider: "xai", API: "native", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-11-02", Note: "Redirects to grok-imagine-image-2.0 after retirement."},
		// Z.AI: text-to-image only, one URL per call.
		{Name: "glm-image", Provider: "zai", API: "images", Generate: true, Status: "active", Default: true, Note: "GLM-Image; bilingual text in images."},
		{Name: "cogview-4-250304", Provider: "zai", API: "images", Generate: true, Status: "active", Note: "CogView-4; no longer in the API reference's model list."},
		// MiniMax: text-to-image.
		{Name: "image-01", Provider: "minimax", API: "native", Generate: true, Status: "active", Default: true, Note: "Image-01; up to 9 images per call."},
		// AWS Bedrock: Stability only (Nova Canvas reached EOL on
		// 2026-09-30; Titan Image is gone). Core/Ultra/SD3.5 run In-Region
		// in us-west-2 only; the Image Services only through "us." profiles.
		{Name: "stability.stable-image-core-v1:1", Provider: "bedrock", API: "stability", Generate: true, Status: "active", Default: true, Regions: stab, Note: "Stable Image Core; fastest and cheapest."},
		{Name: "stability.stable-image-ultra-v1:1", Provider: "bedrock", API: "stability", Generate: true, Edit: true, Status: "active", Regions: stab, Note: "Stable Image Ultra; highest quality."},
		{Name: "stability.sd3-5-large-v1:0", Provider: "bedrock", API: "stability", Generate: true, Edit: true, Status: "active", Regions: stab, Note: "SD3.5 Large; image-to-image with strength."},
		{Name: "stability.stable-image-inpaint-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "Inpaint: repaint the masked area (or the image's transparent area) from the prompt."},
		{Name: "stability.stable-image-erase-object-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "Erase the masked object; no prompt."},
		{Name: "stability.stable-image-remove-background-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "Remove the background; no prompt."},
		{Name: "stability.stable-image-control-sketch-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "Turn a sketch into a finished image guided by the prompt."},
		{Name: "stability.stable-image-control-structure-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "New image that keeps the input's structure."},
		{Name: "stability.stable-image-style-guide-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "New image in the input's style."},
		{Name: "stability.stable-creative-upscale-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "Creative upscale to 4K (input up to 1 megapixel)."},
		{Name: "stability.stable-conservative-upscale-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "Faithful upscale to 4K."},
		{Name: "stability.stable-fast-upscale-v1:0", Provider: "bedrock", API: "stability", Edit: true, Status: "active", Regions: svc, Profile: true, Note: "Fast 4x upscale; no prompt."},
		{Name: "amazon.nova-canvas-v1:0", Provider: "bedrock", API: "native", Generate: true, Edit: true, Status: "deprecated", Retires: "2026-09-30"},
	}
}

// LookupModel returns the catalog entry for id (case-insensitive; a Bedrock
// "us." profile id resolves to its model). retired is true for a model the
// catalog knows has been shut down.
func LookupModel(id string) (m ModelInfo, retired bool, ok bool) {
	want := strings.ToLower(strings.TrimSpace(id))
	want = strings.TrimPrefix(want, "us.")
	today := catalogNow().UTC().Format("2006-01-02")
	for _, c := range allModels() {
		if strings.ToLower(c.Name) == want {
			return c, c.Retires != "" && c.Retires <= today, true
		}
	}
	return ModelInfo{}, false, false
}

// DefaultModelFor returns the catalog default for a provider ("" if none).
// For "openai" the api selects between the Images and Responses defaults.
func DefaultModelFor(provider, api string) string {
	for _, m := range KnownModels() {
		if m.Provider == provider && m.Default && (api == "" || m.API == api) {
			return m.Name
		}
	}
	return ""
}

// FetchOpenAIModels lists models from the account via GET {base}/models and
// returns the ones whose id looks image-capable (gpt-image*, dall-e*, plus the
// gpt-5/4.1 family usable via the Responses API). Keyless callers get nil.
func FetchOpenAIModels(ctx context.Context, baseURL, apiKey string, logger *zap.Logger) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = openAIBaseURL
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	resp, err := utils.NewHTTPClientH1(logger, imageGenTimeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, &apiError{status: resp.StatusCode, body: strings.TrimSpace(string(snippet))}
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Data {
		if isImageCapableID(m.ID) {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func isImageCapableID(id string) bool {
	id = strings.ToLower(id)
	switch {
	case strings.Contains(id, "image"): // gpt-image-1, etc.
		return true
	case strings.HasPrefix(id, "dall-e"):
		return true
	case strings.HasPrefix(id, "gpt-5"), strings.HasPrefix(id, "gpt-4.1"), strings.HasPrefix(id, "gpt-4o"):
		return true // usable via the Responses API image_generation tool
	}
	return false
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return "openai models API status " + itoa(e.status) + ": " + e.body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
