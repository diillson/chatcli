/*
 * ChatCLI - OpenAI-compatible image generation provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * POST {base}/images/generations with {model, prompt, n, size} returning
 * base64 PNGs or image URLs. Serves OpenAI and the OpenAI-shaped generators
 * (xAI, Z.AI, self-hosted servers like LocalAI). Generation is one shape for
 * all of them; editing is not, so each instance carries its own editShape.
 */
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// defaultImageModel is OpenAI's current fast image model (gpt-image-2.5,
	// 2026-09). The gpt-image-1 family is deprecated (gpt-image-1 shuts down
	// 2026-10-23; 1.5/1-mini 2026-12-01). GPT image models always return
	// b64_json (no response_format needed).
	defaultImageModel = "gpt-image-2.5-flare"
	imageGenTimeout   = 180 * time.Second
	imagesPath        = "/images/generations"
	imagesEditPath    = "/images/edits"
	maxErrBody        = 300
)

// editShape is the /images/edits contract an OpenAI-shaped endpoint speaks.
// The providers share the generation request but not the edit one, so the
// factory sets it per provider rather than assuming OpenAI's.
type editShape int

const (
	// editNone: the endpoint only generates (Z.AI, a self-hosted server
	// declared generate-only). Edit fails with ErrEditUnsupported.
	editNone editShape = iota
	// editMultipart: OpenAI Images API — multipart/form-data with the image
	// file part(s), plus servers that copy it (LocalAI and similar).
	editMultipart
	// editJSONImageURL: xAI — application/json with the input as
	// image.url (a data URI or public URL), several inputs as images[];
	// "size" is not accepted and the result comes back as a URL.
	editJSONImageURL
)

// OpenAICompatible generates images against an OpenAI-shaped endpoint.
type OpenAICompatible struct {
	baseURL  string
	apiKey   string
	model    string
	label    string
	omitSize bool      // some servers (xAI grok-image) reject the "size" field
	omitN    bool      // some servers (Z.AI CogView/GLM-Image) reject the "n" field
	edit     editShape // the edit contract this endpoint speaks; editNone = generate-only
	client   *http.Client
}

// supportsEdit reports whether this endpoint edits images at all.
func (o *OpenAICompatible) supportsEdit() bool { return o.edit != editNone }

// Model returns the model this endpoint runs.
func (o *OpenAICompatible) Model() string { return o.model }

// NewOpenAICompatible builds the provider. baseURL is required; apiKey may be
// empty for keyless self-hosted servers; model falls back to dall-e-3.
func NewOpenAICompatible(baseURL, apiKey, model, label string, logger *zap.Logger) (*OpenAICompatible, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("imagegen: base URL is required")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("imagegen: base URL must be http(s): %q", baseURL)
	}
	if strings.TrimSpace(model) == "" {
		model = defaultImageModel
	}
	if strings.TrimSpace(label) == "" {
		label = "openai-compatible"
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OpenAICompatible{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(apiKey),
		model:   model,
		label:   label,
		client:  utils.NewHTTPClientH1(logger, imageGenTimeout),
	}, nil
}

// Name returns the backend label.
func (o *OpenAICompatible) Name() string { return o.label }

// Generate posts the prompt and decodes the returned base64 images.
func (o *OpenAICompatible) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	n := opts.N
	if n <= 0 {
		n = 1
	}
	size := opts.Size
	if size == "" {
		size = "1024x1024"
	}

	// Note: response_format is intentionally omitted. Newer models (e.g.
	// gpt-image-1) reject it and always return b64_json, while dall-e returns a
	// URL by default — we handle both shapes in the response below. Sending the
	// field would 400 on gpt-image-1.
	payload := map[string]interface{}{
		"model":  o.model,
		"prompt": prompt,
	}
	if !o.omitN {
		payload["n"] = n
	}
	if !o.omitSize {
		payload["size"] = size
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+imagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: %s returned %d: %s", o.label, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	return o.decodeImageResponse(ctx, resp.Body)
}

// Edit sends the input image(s) and prompt to {base}/images/edits in the
// contract this endpoint speaks (see editShape).
func (o *OpenAICompatible) Edit(ctx context.Context, prompt string, inputs []Image, opts EditOptions) ([]Image, error) {
	if o.edit == editNone {
		return nil, fmt.Errorf("%w (%s)", ErrEditUnsupported, o.label)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	if len(inputs) == 0 || len(inputs[0].Data) == 0 {
		return nil, fmt.Errorf("imagegen: edit requires an input image")
	}
	n := opts.N
	if n <= 0 {
		n = 1
	}
	var (
		body        io.Reader
		contentType string
		err         error
	)
	if o.edit == editJSONImageURL {
		body, contentType, err = o.jsonEditBody(prompt, inputs, opts, n)
	} else {
		body, contentType, err = o.multipartEditBody(prompt, inputs, opts, n)
	}
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+imagesEditPath, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		err := fmt.Errorf("imagegen: %s edit returned %d: %s", o.label, resp.StatusCode, strings.TrimSpace(string(snippet)))
		if o.label == "selfhosted" && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed) {
			// Many OpenAI-compatible servers only generate.
			err = fmt.Errorf("%w — this server has no /images/edits; set %s=false to declare it generate-only", err, envSelfHostedEdits)
		}
		return nil, err
	}
	return o.decodeImageResponse(ctx, resp.Body)
}

// jsonEditBody builds xAI's JSON edit request. One input rides as image, more
// as images[]; each is a data URI so no public hosting is needed. size is
// never sent (xAI rejects it); the response defaults to an image URL.
func (o *OpenAICompatible) jsonEditBody(prompt string, inputs []Image, opts EditOptions, n int) (io.Reader, string, error) {
	refs := make([]map[string]string, 0, len(inputs))
	for _, in := range inputs {
		if len(in.Data) == 0 {
			continue
		}
		refs = append(refs, map[string]string{"url": dataURI(partContentType(in), in.Data), "type": "image_url"})
	}
	payload := map[string]interface{}{
		"model":  o.model,
		"prompt": prompt,
	}
	if len(refs) == 1 {
		payload["image"] = refs[0]
	} else {
		payload["images"] = refs
	}
	if len(opts.Mask) > 0 {
		payload["mask"] = map[string]string{"url": dataURI("image/png", opts.Mask)}
	}
	if n > 1 && !o.omitN {
		payload["n"] = n
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	return bytes.NewReader(raw), "application/json", nil
}

// multipartEditBody builds OpenAI's multipart/form-data edit request. Only
// the first input is the primary image; extra inputs ride as image[] for
// models that accept several references.
func (o *OpenAICompatible) multipartEditBody(prompt string, inputs []Image, opts EditOptions, n int) (io.Reader, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("model", o.model); err != nil {
		return nil, "", err
	}
	if err := mw.WriteField("prompt", prompt); err != nil {
		return nil, "", err
	}
	if !o.omitN {
		_ = mw.WriteField("n", fmt.Sprintf("%d", n))
	}
	if !o.omitSize && opts.Size != "" {
		_ = mw.WriteField("size", opts.Size)
	}
	// Primary image plus optional additional references.
	for i, in := range inputs {
		if len(in.Data) == 0 {
			continue
		}
		field := "image"
		if i > 0 {
			field = "image[]"
		}
		// IMPORTANT: set the part's Content-Type explicitly. The stdlib's
		// CreateFormFile always writes application/octet-stream, which OpenAI's
		// /images/edits rejects ("unsupported mimetype"). Derive it from the
		// image's MIME (sniffed at load time), falling back to the extension.
		if err := writeImagePart(mw, field, imageFormName(in, i), partContentType(in), in.Data); err != nil {
			return nil, "", err
		}
	}
	if len(opts.Mask) > 0 {
		if err := writeImagePart(mw, "mask", "mask.png", "image/png", opts.Mask); err != nil {
			return nil, "", err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}

	return &buf, mw.FormDataContentType(), nil
}

// imageFormName returns a plausible multipart filename with the right
// extension so the server infers the content type.
func imageFormName(in Image, i int) string {
	ext := in.Ext
	if ext == "" {
		ext = "png"
	}
	return fmt.Sprintf("image-%d.%s", i, ext)
}

// partContentType resolves the MIME type for a multipart image part: the
// image's own Mime when present, otherwise inferred from its extension, with a
// png fallback. OpenAI's /images/edits validates this header.
func partContentType(in Image) string {
	if in.Mime != "" {
		return in.Mime
	}
	switch strings.ToLower(in.Ext) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

// writeImagePart writes a multipart file part with an explicit Content-Type
// header (CreateFormFile would hardcode application/octet-stream).
func writeImagePart(mw *multipart.Writer, field, filename, contentType string, data []byte) error {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(field), escapeQuotes(filename)))
	h.Set("Content-Type", contentType)
	fw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}

// dataURI inlines bytes as a base64 data URI.
func dataURI(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// escapeQuotes mirrors the stdlib's multipart quoting for header values.
func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s)
}

// decodeImageResponse parses the shared {data:[{b64_json|url}]} response
// produced by both /images/generations and /images/edits.
func (o *OpenAICompatible) decodeImageResponse(ctx context.Context, body io.Reader) ([]Image, error) {
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("imagegen: %s returned no images", o.label)
	}
	images := make([]Image, 0, len(out.Data))
	var firstErr error // why an entry was dropped, reported when none survive
	for _, d := range out.Data {
		if d.B64 != "" {
			raw, derr := base64.StdEncoding.DecodeString(d.B64)
			if derr == nil && len(raw) > 0 {
				images = append(images, Image{Data: raw, Mime: "image/png", Ext: "png"})
			} else if firstErr == nil {
				firstErr = dropReason("decode b64_json", derr)
			}
			continue
		}
		if d.URL != "" {
			raw, mime, derr := o.fetchURL(ctx, d.URL)
			if derr == nil && len(raw) > 0 {
				ext := "png"
				if strings.Contains(mime, "jpeg") {
					ext = "jpg"
				}
				images = append(images, Image{Data: raw, Mime: mime, Ext: ext})
			} else if firstErr == nil {
				firstErr = dropReason("download result", derr)
			}
		}
	}
	if len(images) == 0 {
		if firstErr != nil {
			return nil, fmt.Errorf("imagegen: %s returned no decodable images: %w", o.label, firstErr)
		}
		return nil, fmt.Errorf("imagegen: %s returned no decodable images", o.label)
	}
	return images, nil
}

// dropReason explains why a result entry yielded no image.
func dropReason(step string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", step, err)
	}
	return fmt.Errorf("%s: empty image", step)
}

// fetchURL downloads an image the API returned by URL (dall-e default shape).
func (o *OpenAICompatible) fetchURL(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}
