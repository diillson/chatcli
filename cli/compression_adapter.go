/*
 * ChatCLI - Adapter binding the @compress / @recall tools to the live
 * compression layer.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.CompressionAdapter over the session's compress.Layer:
 * on-demand content-aware compression (@compress), byte-identical retrieval of
 * offloaded originals (@recall), and a session savings summary. Wired via
 * plugins.SetCompressionAdapter at startup.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/cli/plugins"
)

// compressionPluginAdapter is the concrete plugins.CompressionAdapter.
type compressionPluginAdapter struct {
	cli *ChatCLI
}

// Recall returns the byte-identical original stored under a CCR key.
func (a *compressionPluginAdapter) Recall(key string) (string, bool) {
	if a.cli == nil || a.cli.compressionLayer == nil {
		return "", false
	}
	return a.cli.compressionLayer.Recall(key)
}

// Compress reduces content on demand. The hint (a content type the model
// supplies, e.g. "log") is translated to the routing signal the layer's
// detectors recognize; an empty hint lets content detection choose.
func (a *compressionPluginAdapter) Compress(hint, content string) (string, error) {
	if a.cli == nil || a.cli.compressionLayer == nil {
		return "", fmt.Errorf("compression layer unavailable in this session")
	}
	if !a.cli.compressionLayer.Enabled() {
		return "", fmt.Errorf("compression is disabled (CHATCLI_COMPRESSION=off); enable it with /config compression")
	}
	out, _ := a.cli.compressionLayer.CompressHinted(buildCompressionHint(hint), content)
	return out, nil
}

// Stats renders a human/model-readable session compression summary.
func (a *compressionPluginAdapter) Stats() string {
	if a.cli == nil || a.cli.compressionLayer == nil {
		return "compression layer unavailable in this session"
	}
	stats, store := a.cli.compressionLayer.Stats()
	var b strings.Builder
	fmt.Fprintf(&b, "Compression — mode=%s\n", a.cli.compressionLayer.Mode())
	fmt.Fprintf(&b, "  calls: %d (reduced %d)\n", stats.Calls, stats.Reductions)
	fmt.Fprintf(&b, "  bytes: %d -> %d (%.0f%% of original, %d saved)\n",
		stats.BytesIn, stats.BytesOut, stats.Ratio()*100, stats.SavedBytes())
	fmt.Fprintf(&b, "  CCR: %d stored, %d recalled, %d misses\n",
		stats.CCRPuts, stats.CCRHits, stats.CCRMisses)
	if len(stats.ByStrategy) > 0 {
		b.WriteString("  by strategy:\n")
		for _, s := range stats.ByStrategy {
			fmt.Fprintf(&b, "    %-12s calls=%d  %d->%d bytes\n", s.Strategy, s.Calls, s.BytesIn, s.BytesOut)
		}
	}
	fmt.Fprintf(&b, "  CCR store: %d entries, %d bytes", store.Entries, store.TotalBytes)
	if store.MaxBytes > 0 {
		fmt.Fprintf(&b, " (cap %d)", store.MaxBytes)
	}
	b.WriteByte('\n')
	return b.String()
}

// buildCompressionHint translates a model-supplied content-type hint into the
// routing Hint compress.Layer's detectors recognize. "code" maps to
// Hint.MIME=="code" — the only signal that enables the (otherwise dormant)
// code skeletonizer. An unknown or empty hint yields a zero Hint so content
// detection decides.
func buildCompressionHint(hint string) compress.Hint {
	switch strings.ToLower(strings.TrimSpace(hint)) {
	case "search", "grep", "rg", "ripgrep":
		return compress.Hint{ToolName: "@search"}
	case "log", "logs", "build", "test":
		return compress.Hint{ToolName: "build"}
	case "diff", "git", "patch":
		return compress.Hint{ToolName: "git diff"}
	case "code", "source", "src":
		return compress.Hint{MIME: "code"}
	case "prose", "markdown", "md", "text", "html", "web":
		return compress.Hint{MIME: "prose"}
	default:
		// json, auto, and anything else: let content detection route it.
		return compress.Hint{}
	}
}

// compile-time assertion that the adapter satisfies the plugin interface.
var _ plugins.CompressionAdapter = (*compressionPluginAdapter)(nil)
