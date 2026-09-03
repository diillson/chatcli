/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Local tokenizer for the GPT family.
 *
 * OpenAI publishes no counting endpoint, so every surface that serves GPT
 * models — OpenAI Chat Completions and Responses, GitHub Models, Copilot,
 * OpenRouter openai/* and the Bedrock OpenAI family — counts locally with
 * the model's own BPE encoding (o200k_base for GPT-4o/4.1/4.5/5.x and the
 * o-series, cl100k_base for GPT-4/3.5). The vocabulary is fetched once
 * from OpenAI's public blob, cached under ~/.chatcli/tokenizers and read
 * from there for the life of the install: no key, no per-call network,
 * and nothing embedded in the binary.
 *
 * Loading is asynchronous: the first count on a cold cache starts the
 * fetch in the background and returns ErrTokenizerLoading, so a turn is
 * never held by a download; counts become exact from the next request.
 */
package tokenizer

import (
	"context"
	"crypto/sha1" // #nosec G505 -- cache file name for a public vocabulary URL, not a security digest
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	tiktoken "github.com/pkoukk/tiktoken-go"
)

const (
	// EncodingO200k serves GPT-4o, 4.1, 4.5, the 5.x line and the o-series.
	EncodingO200k = "o200k_base"
	// EncodingCL100k serves GPT-4 and GPT-3.5.
	EncodingCL100k = "cl100k_base"

	// Chat-format overhead per OpenAI's counting guide: every message
	// costs 3 tokens of framing, a name costs 1, and the reply is primed
	// with 3 more.
	tokensPerMessage = 3
	tokensPerName    = 1
	tokensPriming    = 3

	fetchTimeout = 60 * time.Second
)

// ErrTokenizerLoading reports that the vocabulary is being fetched: the
// caller should fall back for this call and try again later.
var ErrTokenizerLoading = errors.New("tokenizer: vocabulary loading in the background")

// ErrUnsupportedModel reports a model outside the GPT family (no local
// encoding is known for it).
var ErrUnsupportedModel = errors.New("tokenizer: no local encoding for this model")

// IsGPTModel reports whether a model id (provider prefixes ignored)
// belongs to the GPT / o-series family this package can count.
func IsGPTModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "chatgpt")
}

// ChatMessage is the minimal chat shape the counter needs.
type ChatMessage struct {
	Role    string
	Name    string
	Content string
}

// EncodingForModel picks the BPE encoding for a model id (case-insensitive;
// provider prefixes such as "openai/" are ignored).
func EncodingForModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "gpt-3.5"), strings.HasPrefix(m, "gpt-4-"), m == "gpt-4", strings.HasPrefix(m, "gpt-4-32k"):
		return EncodingCL100k
	default:
		return EncodingO200k
	}
}

// cacheLoader is the BPE loader tiktoken calls with the vocabulary URL:
// it serves the on-disk copy under the ChatCLI state dir and fetches it
// once when absent (never concurrently for the same file).
type cacheLoader struct {
	dir    string
	client *http.Client
	mu     sync.Mutex
}

func (l *cacheLoader) cachePath(blob string) string {
	return filepath.Join(l.dir, fmt.Sprintf("%x.tiktoken", sha1.Sum([]byte(blob)))) // #nosec G401 -- file name only
}

// LoadTiktokenBpe implements tiktoken.BpeLoader.
func (l *cacheLoader) LoadTiktokenBpe(blob string) (map[string]int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := l.cached(blob)
	if err != nil {
		data, err = l.fetch(blob)
		if err != nil {
			return nil, err
		}
	}
	return parseBpe(data)
}

func (l *cacheLoader) cached(blob string) ([]byte, error) {
	return os.ReadFile(l.cachePath(blob)) // #nosec G304 -- path under the state dir, name derived from a constant URL
}

func (l *cacheLoader) fetch(blob string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blob, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tokenizer: vocabulary fetch failed: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return nil, err
	}
	if err := utils.AtomicWriteFile(l.cachePath(blob), data, 0o600); err != nil {
		return nil, err
	}
	return data, nil
}

// parseBpe decodes the tiktoken "<base64 token> <rank>" lines.
func parseBpe(data []byte) (map[string]int, error) {
	ranks := make(map[string]int, 200_000)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf("tokenizer: malformed vocabulary line")
		}
		tok, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, fmt.Errorf("tokenizer: malformed vocabulary token: %w", err)
		}
		var rank int
		if _, err := fmt.Sscanf(parts[1], "%d", &rank); err != nil {
			return nil, err
		}
		ranks[string(tok)] = rank
	}
	return ranks, nil
}

var (
	setupOnce sync.Once
	loader    *cacheLoader

	encMu    sync.Mutex
	encs     = map[string]*tiktoken.Tiktoken{}
	loading  = map[string]bool{}
	lastErrs = map[string]error{}
)

// stateDir is where vocabularies live: ~/.chatcli/tokenizers, or the
// system temp dir when the home is unknown.
func stateDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".chatcli", "tokenizers")
	}
	return filepath.Join(os.TempDir(), "chatcli-tokenizers")
}

func setup() {
	setupOnce.Do(func() {
		loader = &cacheLoader{dir: stateDir(), client: &http.Client{Timeout: fetchTimeout}}
		tiktoken.SetBpeLoader(loader)
	})
}

// Ready reports whether the encoding is loaded and usable right now.
func Ready(encoding string) bool {
	encMu.Lock()
	defer encMu.Unlock()
	return encs[encoding] != nil
}

// encodingFor returns the loaded encoding, or starts loading it and
// reports ErrTokenizerLoading. A loaded vocabulary on disk resolves on
// the first call synchronously (a local read); only a cold cache defers.
func encodingFor(encoding string) (*tiktoken.Tiktoken, error) {
	setup()
	encMu.Lock()
	if e := encs[encoding]; e != nil {
		encMu.Unlock()
		return e, nil
	}
	if loading[encoding] {
		encMu.Unlock()
		return nil, ErrTokenizerLoading
	}
	loading[encoding] = true
	encMu.Unlock()

	load := func() (*tiktoken.Tiktoken, error) {
		e, err := tiktoken.GetEncoding(encoding)
		encMu.Lock()
		defer encMu.Unlock()
		loading[encoding] = false
		if err != nil {
			lastErrs[encoding] = err
			return nil, err
		}
		encs[encoding] = e
		return e, nil
	}
	if _, err := loader.cached(blobFor(encoding)); err == nil {
		return load() // on disk: synchronous, milliseconds
	}
	go func() { _, _ = load() }()
	return nil, ErrTokenizerLoading
}

// blobFor mirrors the URLs tiktoken asks the loader for.
func blobFor(encoding string) string {
	switch encoding {
	case EncodingCL100k:
		return "https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken"
	default:
		return "https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken"
	}
}

// CountText counts the tokens of one text under the model's encoding.
func CountText(model, text string) (int, error) {
	enc, err := encodingFor(EncodingForModel(model))
	if err != nil {
		return 0, err
	}
	return len(enc.EncodeOrdinary(text)), nil
}

// CountChat counts a chat request the way OpenAI bills it: message
// framing plus role, name and content tokens, plus the reply priming.
func CountChat(model string, messages []ChatMessage) (int, error) {
	enc, err := encodingFor(EncodingForModel(model))
	if err != nil {
		return 0, err
	}
	total := 0
	for _, m := range messages {
		total += tokensPerMessage
		total += len(enc.EncodeOrdinary(m.Role))
		total += len(enc.EncodeOrdinary(m.Content))
		if m.Name != "" {
			total += tokensPerName + len(enc.EncodeOrdinary(m.Name))
		}
	}
	return total + tokensPriming, nil
}

// MessagesFromHistory shapes a prompt and history into chat messages the
// way the OpenAI-style adapters do: history verbatim (unknown roles as
// user), tool calls rendered as name plus arguments, and the prompt
// appended as the final user turn unless the history already ends with it.
func MessagesFromHistory(prompt string, history []models.Message) []ChatMessage {
	out := make([]ChatMessage, 0, len(history)+1)
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "system" && role != "user" && role != "assistant" && role != "tool" {
			role = "user"
		}
		content := msg.Content
		for _, tc := range msg.ToolCalls {
			content += "\n" + tc.Name + " " + tc.ArgumentsJSON()
		}
		out = append(out, ChatMessage{Role: role, Content: content})
	}
	if strings.TrimSpace(prompt) != "" && (len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt) {
		out = append(out, ChatMessage{Role: "user", Content: prompt})
	}
	return out
}

// Prefetch warms the encoding for a model in the background (idempotent).
func Prefetch(model string) {
	_, _ = encodingFor(EncodingForModel(model))
}
