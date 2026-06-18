package models

import (
	"bytes"
	"image"
	// Register the decoders we rely on for dimension-based token
	// estimation. These are stdlib (no new dependency); webp is handled
	// via a byte-size fallback below so we don't pull golang.org/x/image.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
)

// ImageContent is a single image attached to a Message, enabling
// vision-capable providers to "see" it. Exactly one of Data or URL
// carries the bytes; provider adapters convert to whatever their wire
// format needs (inline base64, a URL reference, or the AWS SDK image
// block). Carrying images on the Message (rather than mangling
// Content string) keeps the text path byte-identical for every
// provider that does not opt in to vision.
type ImageContent struct {
	MediaType string `json:"media_type"`          // canonical MIME: image/png|image/jpeg|image/gif|image/webp
	Data      []byte `json:"data,omitempty"`      // raw bytes (NOT base64); adapters encode on the wire
	URL       string `json:"url,omitempty"`       // remote URL alternative to Data
	FileName  string `json:"file_name,omitempty"` // original filename, for display/persistence
}

// supportedImageMediaTypes is the set of MIME types every vision-capable
// provider in the catalog accepts. Keep this conservative: a type here
// is a promise that at least the primary providers (Anthropic, OpenAI,
// Bedrock, Gemini) can decode it.
var supportedImageMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// SupportedImageMediaTypes returns a copy of the accepted MIME types,
// for callers that surface the list to the user (errors, help text).
func SupportedImageMediaTypes() []string {
	out := make([]string, 0, len(supportedImageMediaTypes))
	for mt := range supportedImageMediaTypes {
		out = append(out, mt)
	}
	return out
}

// NormalizeImageMediaType lowercases and strips any parameters (e.g.
// "image/jpeg; charset=binary" -> "image/jpeg") and reports whether the
// result is a supported image type. Also folds the common "image/jpg"
// alias onto the canonical "image/jpeg".
func NormalizeImageMediaType(mime string) (string, bool) {
	mt := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	if mt == "image/jpg" {
		mt = "image/jpeg"
	}
	return mt, supportedImageMediaTypes[mt]
}

// DetectImageMediaType sniffs the media type from the leading bytes and
// reports whether it is a supported image. Used when an attachment has
// no declared MIME (local file read, raw paste).
func DetectImageMediaType(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	return NormalizeImageMediaType(http.DetectContentType(data))
}

// IsValid reports whether the image carries usable bytes (Data or URL)
// and a supported media type.
func (ic ImageContent) IsValid() bool {
	if _, ok := NormalizeImageMediaType(ic.MediaType); !ok {
		return false
	}
	return len(ic.Data) > 0 || strings.TrimSpace(ic.URL) != ""
}

// EstimateImageTokens approximates the prompt-token cost of an image.
//
// When the bytes are present and decodable we use Anthropic's documented
// heuristic, tokens ≈ (width × height) / 750, after clamping the longest
// edge to 1568px (providers downscale larger images server-side). When we
// cannot decode dimensions (URL-only, or a webp we did not register a
// decoder for), we fall back to a coarse byte-size estimate so token
// accounting never silently reports zero for a real image.
func EstimateImageTokens(ic ImageContent) int {
	const (
		maxEdge       = 1568
		pixelsPerTok  = 750
		bytesPerTok   = 2048 // coarse fallback when dimensions are unknown
		minImageToken = 85   // providers floor very small images near here
	)

	if len(ic.Data) > 0 {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(ic.Data)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			w, h := cfg.Width, cfg.Height
			longest := w
			if h > longest {
				longest = h
			}
			if longest > maxEdge {
				// Scale both dimensions down proportionally.
				ratio := float64(maxEdge) / float64(longest)
				w = int(float64(w) * ratio)
				h = int(float64(h) * ratio)
			}
			tokens := (w * h) / pixelsPerTok
			if tokens < minImageToken {
				return minImageToken
			}
			return tokens
		}
		// Undecodable bytes (e.g. webp) — fall back to size.
		if t := len(ic.Data) / bytesPerTok; t > minImageToken {
			return t
		}
		return minImageToken
	}

	// URL-only: we have no bytes to measure. Charge the conservative floor.
	return minImageToken
}
