/*
 * ChatCLI - Embedding provider factory.
 *
 * Selects a provider based on either the explicit name or the
 * CHATCLI_EMBED_PROVIDER env. Falls back to NewNull when no provider
 * is configured. The factory keeps env reading centralized so the
 * memory package never imports os/env.
 */
package embedding

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// NewByName returns a provider by short name. Supported names: "voyage",
// "openai", "bedrock", "null", "" (== "null"). Unknown names return
// ErrUnknownProvider.
//
// Required env vars per provider:
//   - voyage: VOYAGE_API_KEY (model: CHATCLI_EMBED_MODEL or "voyage-3")
//   - openai: OPENAI_API_KEY (model: CHATCLI_EMBED_MODEL or
//     "text-embedding-3-small"; dim: CHATCLI_EMBED_DIMENSIONS)
//   - bedrock: AWS credential chain (AWS_PROFILE / AWS_ACCESS_KEY_ID /
//     IAM role); model: CHATCLI_EMBED_MODEL or "amazon.titan-embed-text-v2:0";
//     dim: CHATCLI_EMBED_DIMENSIONS (Titan v2: 256/512/1024); region:
//     BEDROCK_REGION or AWS_REGION; profile: AWS_PROFILE.
func NewByName(name string) (Provider, error) {
	switch NormalizeName(name) {
	case "", "null", "off":
		return NewNull(), nil
	case "voyage":
		return NewVoyage(os.Getenv("VOYAGE_API_KEY"), os.Getenv("CHATCLI_EMBED_MODEL"))
	case "openai":
		dim := 0
		if v := os.Getenv("CHATCLI_EMBED_DIMENSIONS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				dim = n
			}
		}
		return NewOpenAI(os.Getenv("OPENAI_API_KEY"), os.Getenv("CHATCLI_EMBED_MODEL"), dim)
	case "bedrock":
		dim := 0
		if v := os.Getenv("CHATCLI_EMBED_DIMENSIONS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				dim = n
			}
		}
		region := firstNonEmpty(os.Getenv("BEDROCK_REGION"), os.Getenv("AWS_REGION"))
		profile := os.Getenv("AWS_PROFILE")
		return NewBedrock(os.Getenv("CHATCLI_EMBED_MODEL"), region, profile, dim, nil)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewFromEnv reads CHATCLI_EMBED_PROVIDER and dispatches via NewByName.
// Empty env yields the null provider so callers always get a valid
// (non-nil) Provider back.
func NewFromEnv() (Provider, error) {
	return NewByName(strings.TrimSpace(os.Getenv("CHATCLI_EMBED_PROVIDER")))
}

// IsNull reports whether p is the null no-op provider. Used by callers
// (HyDE, vector store) to decide whether to take the keyword-only fast
// path without calling Embed and getting an error back.
func IsNull(p Provider) bool {
	if p == nil {
		return true
	}
	_, ok := p.(*Null)
	return ok
}
