/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// recallToolName is the bare name of the @recall builtin (without the '@'
// prefix). Its output must never be compressed — see CompressHinted.
const recallToolName = "recall"

// isRecallTool reports whether toolName refers to the @recall builtin. Tool
// names reach the layer either bare ("recall") or '@'-prefixed ("@recall")
// since GetPlugin accepts both forms, so we normalize before comparing.
func isRecallTool(toolName string) bool {
	name := strings.TrimPrefix(strings.TrimSpace(toolName), "@")
	return strings.EqualFold(name, recallToolName)
}

// Layer is the high-level facade the rest of ChatCLI talks to. It bundles a
// ContentRouter over every built-in compressor, a CCR store, the active mode/
// threshold, and a metrics accumulator. One Layer is created per session and
// shared (it is safe for concurrent use).
//
// The zero Layer is not usable; build one with NewLayer or NewLayerFromEnv.
type Layer struct {
	router    *ContentRouter
	store     Store
	mode      atomic.Int32 // holds a Mode; mutable at runtime via SetMode (/config compression)
	threshold int
	metrics   *Metrics
}

// Config controls Layer construction. Fields left zero take documented
// defaults (see NewLayerFromEnv).
type Config struct {
	Mode      Mode
	Threshold int
	Store     Store
}

// Default tuning constants. Threshold is intentionally generous: small tool
// outputs are returned byte-identical, so compression only ever engages on the
// large payloads where it pays off.
const (
	DefaultThreshold = 4000 // bytes; below this -> verbatim passthrough
	DefaultCCRMaxMB  = 256  // CCR store size cap
	DefaultCCRTTL    = 7 * 24 * time.Hour
	ccrDirName       = "ccr"
)

// allCompressors returns a fresh router over the full built-in set. Order is
// irrelevant — the router selects by detection confidence.
func newDefaultRouter() *ContentRouter {
	return NewContentRouter(
		NewSearchCompressor(),
		NewLogCompressor(),
		NewDiffCompressor(),
		NewJSONCrusher(),
		// Never auto-fires (Detect returns 0 unless Hint.MIME=="code"); kept in
		// the router so explicit code compression flows through the same path.
		NewCodeCompressor(),
		// Auto-fires only on web/reference tool output (not local file reads)
		// or an explicit prose/markdown hint.
		NewProseCompressor(),
	)
}

// NewLayer builds a Layer from an explicit Config. A nil Store with
// ModeLossyWithCCR is allowed: lossy compressors degrade to lossless, never
// dropping information.
func NewLayer(cfg Config) *Layer {
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	l := &Layer{
		router:    newDefaultRouter(),
		store:     cfg.Store,
		threshold: threshold,
		metrics:   NewMetrics(),
	}
	l.mode.Store(int32(cfg.Mode))
	return l
}

// NewLayerFromEnv builds a Layer from environment configuration, creating the
// on-disk CCR store under stateDir/ccr. stateDir is typically ~/.chatcli; when
// empty it is resolved from the user home (falling back to the temp dir).
//
// Recognized variables:
//
//	CHATCLI_COMPRESSION            off | lossless | lossy-with-ccr (default lossy-with-ccr)
//	CHATCLI_COMPRESSION_THRESHOLD  bytes below which output is untouched (default 4000)
//	CHATCLI_COMPRESSION_CCR_DIR    override the CCR store directory
//	CHATCLI_COMPRESSION_CCR_MAX_MB CCR size cap in MiB (default 256; 0 = unbounded)
//	CHATCLI_COMPRESSION_CCR_TTL    CCR entry TTL as a Go duration (default 168h; 0 = no TTL)
//
// The CCR knobs nest under the CHATCLI_COMPRESSION_ prefix to match the
// subsystem-prefix convention used across ChatCLI (CHATCLI_AGENT_*,
// CHATCLI_QUALITY_*, CHATCLI_MICROCOMPACT_*, ...).
func NewLayerFromEnv(stateDir string) *Layer {
	mode, _ := ParseMode(os.Getenv("CHATCLI_COMPRESSION"))

	threshold := DefaultThreshold
	if v := os.Getenv("CHATCLI_COMPRESSION_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			threshold = n
		}
	}

	// The CCR store is always built (even in off/lossless mode) so the user
	// can switch to lossy mode at runtime via `/config compression lossy`
	// without restarting. An empty store directory is cheap and harmless.
	cfg := Config{Mode: mode, Threshold: threshold, Store: newCCRStoreFromEnv(stateDir)}
	return NewLayer(cfg)
}

// newCCRStoreFromEnv resolves the CCR directory and size/TTL caps and opens a
// DiskStore. On any failure it falls back to an in-memory store so the session
// still benefits from compression (just without cross-restart persistence).
func newCCRStoreFromEnv(stateDir string) Store {
	dir := os.Getenv("CHATCLI_COMPRESSION_CCR_DIR")
	if dir == "" {
		if stateDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				stateDir = filepath.Join(home, ".chatcli")
			} else {
				stateDir = filepath.Join(os.TempDir(), "chatcli")
			}
		}
		dir = filepath.Join(stateDir, ccrDirName)
	}

	maxBytes := int64(DefaultCCRMaxMB) * 1024 * 1024
	if v := os.Getenv("CHATCLI_COMPRESSION_CCR_MAX_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			maxBytes = n * 1024 * 1024
		}
	}

	ttl := DefaultCCRTTL
	if v := os.Getenv("CHATCLI_COMPRESSION_CCR_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			ttl = d
		}
	}

	store, err := NewDiskStore(dir, maxBytes, ttl)
	if err != nil {
		return NewMemoryStore()
	}
	return store
}

// Mode reports the Layer's active mode. Safe for concurrent use.
func (l *Layer) Mode() Mode {
	if l == nil {
		return ModeOff
	}
	return Mode(l.mode.Load())
}

// SetMode changes the active mode at runtime (used by /config compression).
// Safe for concurrent use; takes effect on the next CompressToolOutput call.
func (l *Layer) SetMode(m Mode) {
	if l != nil {
		l.mode.Store(int32(m))
	}
}

// Enabled reports whether compression will engage (mode is not off).
func (l *Layer) Enabled() bool { return l != nil && l.Mode() != ModeOff }

// CompressToolOutput reduces one tool's output, attributing the result to the
// originating tool for routing and metrics. It always returns a usable string;
// a nil or disabled Layer returns the input unchanged.
func (l *Layer) CompressToolOutput(toolName, content string) (string, Result) {
	return l.CompressHinted(Hint{ToolName: toolName}, content)
}

// CompressHinted reduces content using an explicit routing hint. This is the
// entry point for callers that know the content type out of band — e.g. the
// @compress tool passing Hint.MIME=="code" to request code skeletonization,
// which never happens on the automatic path. A nil or disabled Layer returns
// the input unchanged.
func (l *Layer) CompressHinted(h Hint, content string) (string, Result) {
	if !l.Enabled() {
		return content, passthrough(content)
	}
	// The @recall tool exists to hand back a previously-offloaded original
	// verbatim. The agent loop funnels every tool's output through this layer,
	// so without this guard recall's output would be re-compressed: the model
	// would receive a truncated view with a fresh <<ccr:KEY>> marker instead of
	// the full original it explicitly asked for — defeating recall entirely.
	if isRecallTool(h.ToolName) {
		return content, passthrough(content)
	}
	// Idempotent: content that already carries a CCR marker was compressed
	// earlier (e.g. by a sub-agent before its output reached the parent). Don't
	// re-compress — it would nest markers and offload a second copy.
	if ExtractKeys(content) != nil {
		return content, passthrough(content)
	}
	res := l.router.Compress(content, h, Options{
		Mode:      l.Mode(),
		Store:     l.store,
		Threshold: l.threshold,
		Metrics:   l.metrics,
	})
	return res.Compressed, res
}

// Recall returns the original content stored under a CCR key, or ok=false when
// the key is unknown/evicted. Used by the @recall tool.
func (l *Layer) Recall(key string) (string, bool) {
	if l == nil || l.store == nil {
		return "", false
	}
	content, ok, err := l.store.Get(key)
	if err != nil || !ok {
		l.metrics.RecordCCRMiss()
		return "", false
	}
	l.metrics.RecordCCRHit()
	return content, true
}

// Stats returns a snapshot of compression metrics plus the CCR store
// footprint, for /compression stats and the cost footer.
func (l *Layer) Stats() (Stats, StoreStats) {
	if l == nil {
		return Stats{}, StoreStats{}
	}
	var ss StoreStats
	if l.store != nil {
		ss = l.store.Stats()
	}
	return l.metrics.Snapshot(), ss
}
