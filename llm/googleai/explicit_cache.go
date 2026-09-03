/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Gemini explicit context caching (cachedContents).
 *
 * Gemini caches prompt prefixes implicitly for free; the explicit resource
 * adds a guaranteed discount on every read at the price of storage per
 * token-hour. It only pays off for a large, stable system prompt reused
 * across many turns — which is exactly the shape of a coder session with
 * attached corpora and skills. So the policy is conservative by design:
 *
 *   - opt-in (CHATCLI_PROMPT_CACHE_EXPLICIT=true);
 *   - the system prompt must be large enough to be worth a resource
 *     (explicitCacheMinEstimatedTokens) and any floor the API teaches us;
 *   - the same system prompt must be seen on two consecutive requests
 *     before a resource is created (a one-shot never pays for storage);
 *   - the resource carries the ENTIRE system instruction and the request
 *     then omits system_instruction, so the wire is valid whether or not
 *     the API accepts both at once;
 *   - a resource the API rejects at generation time is dropped and the
 *     request is retried once without it — never a failed turn;
 *   - a creation failure backs the prompt off for a while (no retry storm);
 *   - the lifetime is CHATCLI_PROMPT_CACHE_TTL and is extended when less
 *     than half remains; the resource is deleted when replaced or when the
 *     CLI shuts down (client.ReleaseCacheResources).
 */
package googleai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// explicitCacheMinEstimatedTokens is the smallest system prompt worth a
	// resource: the largest floor Google documents for prefix caching
	// (4,096 tokens on the 3.x family), estimated at ~4 chars/token.
	explicitCacheMinEstimatedTokens = 4096
	explicitCacheCharsPerToken      = 4
	// explicitCacheStabilityTurns is how many consecutive requests must
	// carry the same system prompt before a resource is created.
	explicitCacheStabilityTurns = 2
	// explicitCacheFailureBackoff is how long a prompt the API refused to
	// cache stays uncached before another attempt.
	explicitCacheFailureBackoff = 10 * time.Minute
	// explicitCacheOpTimeout bounds every management call.
	explicitCacheOpTimeout = 15 * time.Second
)

// explicitCache is one client's cached-content state.
type explicitCache struct {
	mu sync.Mutex

	name    string // cachedContents/<id>, "" when none
	hash    string
	tokens  int
	ttl     time.Duration
	expires time.Time

	lastHash    string
	seen        int
	unsupported map[string]time.Time
	learnedMin  int // token floor learned from a "too small" rejection
	releaseKey  string
}

// systemTextOf concatenates the system messages the way
// buildContentsAndSystem sends them, so the cache hash and the wire agree.
func systemTextOf(parts []map[string]string) string {
	if len(parts) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(p["text"])
	}
	return sb.String()
}

func (c *GeminiClient) explicitState() *explicitCache {
	c.explicitOnce.Do(func() {
		c.explicit = &explicitCache{unsupported: map[string]time.Time{}}
	})
	return c.explicit
}

// explicitCacheFor returns the cachedContents name to reference for this
// system text, or "" when the request must carry the system instruction
// itself. Never returns an error: every failure degrades to "".
func (c *GeminiClient) explicitCacheFor(ctx context.Context, systemParts []map[string]string) string {
	if !client.ExplicitCacheEnabled() {
		return ""
	}
	text := systemTextOf(systemParts)
	if text == "" {
		return ""
	}
	est := len(text) / explicitCacheCharsPerToken
	st := c.explicitState()
	st.mu.Lock()
	defer st.mu.Unlock()

	if est < explicitCacheMinEstimatedTokens || (st.learnedMin > 0 && est < st.learnedMin) {
		return ""
	}
	sum := sha256.Sum256([]byte(c.model + "\n" + text))
	hash := hex.EncodeToString(sum[:8])

	if st.name != "" && st.hash == hash {
		if time.Until(st.expires) < st.ttl/2 {
			if !c.refreshExplicitLocked(ctx, st) {
				return ""
			}
		}
		return st.name
	}
	if until, ok := st.unsupported[hash]; ok {
		if time.Now().Before(until) {
			return ""
		}
		delete(st.unsupported, hash)
	}
	if st.lastHash != hash {
		st.lastHash = hash
		st.seen = 1
		return ""
	}
	st.seen++
	if st.seen < explicitCacheStabilityTurns {
		return ""
	}
	return c.createExplicitLocked(ctx, st, hash, text)
}

// createExplicitLocked creates the resource for text, replacing any live
// one. Caller holds st.mu.
func (c *GeminiClient) createExplicitLocked(ctx context.Context, st *explicitCache, hash, text string) string {
	ttl := client.PromptCacheTTLDuration()
	body := map[string]interface{}{
		"model":             "models/" + c.model,
		"displayName":       "chatcli-" + hash,
		"ttl":               fmt.Sprintf("%ds", int(ttl.Seconds())),
		"systemInstruction": map[string]interface{}{"parts": []map[string]string{{"text": text}}},
	}
	status, resp, err := c.cacheRequest(ctx, http.MethodPost, c.baseURL+"/cachedContents", body)
	if err != nil || status/100 != 2 {
		msg := c.cacheErrorText(status, resp, err)
		st.unsupported[hash] = time.Now().Add(explicitCacheFailureBackoff)
		if status == http.StatusBadRequest && strings.Contains(strings.ToLower(msg), "token") {
			// The API told us the prompt is below its floor: remember the
			// floor so smaller prompts skip the round trip entirely.
			if est := len(text) / explicitCacheCharsPerToken; est+1 > st.learnedMin {
				st.learnedMin = est + 1
			}
		}
		c.logger.Warn("Gemini explicit cache unavailable; sending the system instruction inline",
			zap.String("model", c.model), zap.Int("status", status), zap.String("error", msg))
		client.EmitCacheResource(client.CacheResourceEvent{Provider: "GOOGLEAI", Model: c.model,
			Action: client.CacheResourceFailed, Err: msg})
		return ""
	}
	var created struct {
		Name          string `json:"name"`
		ExpireTime    string `json:"expireTime"`
		UsageMetadata struct {
			TotalTokenCount int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(resp, &created); err != nil || created.Name == "" {
		st.unsupported[hash] = time.Now().Add(explicitCacheFailureBackoff)
		c.logger.Warn("Gemini explicit cache: unreadable create response", zap.Error(err))
		return ""
	}
	// Replace the previous resource (different prompt) so its storage stops.
	if st.name != "" {
		c.deleteExplicit(ctx, st.name)
		client.EmitCacheResource(client.CacheResourceEvent{Provider: "GOOGLEAI", Model: c.model,
			Name: st.name, Action: client.CacheResourceReleased, Tokens: st.tokens})
	}
	st.name = created.Name
	st.hash = hash
	st.tokens = created.UsageMetadata.TotalTokenCount
	st.ttl = ttl
	st.expires = time.Now().Add(ttl)
	if t, err := time.Parse(time.RFC3339Nano, created.ExpireTime); err == nil {
		st.expires = t
	}
	if st.releaseKey == "" {
		st.releaseKey = fmt.Sprintf("googleai:%p", c)
		client.RegisterCacheReleaser(st.releaseKey, c.releaseExplicitCache)
	}
	c.logger.Info("Gemini explicit cache created",
		zap.String("model", c.model), zap.String("name", st.name),
		zap.Int("tokens", st.tokens), zap.Duration("ttl", ttl))
	client.EmitCacheResource(client.CacheResourceEvent{Provider: "GOOGLEAI", Model: c.model,
		Name: st.name, Action: client.CacheResourceCreated, Tokens: st.tokens, TTL: ttl})
	return st.name
}

// refreshExplicitLocked extends the live resource by one TTL. Caller holds
// st.mu. On failure the resource is dropped (it may already be gone).
func (c *GeminiClient) refreshExplicitLocked(ctx context.Context, st *explicitCache) bool {
	ttl := client.PromptCacheTTLDuration()
	url := c.baseURL + "/" + st.name + "?updateMask=ttl"
	body := map[string]interface{}{"ttl": fmt.Sprintf("%ds", int(ttl.Seconds()))}
	status, resp, err := c.cacheRequest(ctx, http.MethodPatch, url, body)
	if err != nil || status/100 != 2 {
		msg := c.cacheErrorText(status, resp, err)
		c.logger.Warn("Gemini explicit cache refresh failed; dropping the resource",
			zap.String("name", st.name), zap.Int("status", status), zap.String("error", msg))
		client.EmitCacheResource(client.CacheResourceEvent{Provider: "GOOGLEAI", Model: c.model,
			Name: st.name, Action: client.CacheResourceFailed, Err: msg})
		st.name, st.hash, st.tokens = "", "", 0
		return false
	}
	st.ttl = ttl
	st.expires = time.Now().Add(ttl)
	client.EmitCacheResource(client.CacheResourceEvent{Provider: "GOOGLEAI", Model: c.model,
		Name: st.name, Action: client.CacheResourceRefreshed, Tokens: st.tokens, TTL: ttl})
	return true
}

// invalidateExplicitCache forgets the live resource after the generation
// API rejected it (expired or deleted elsewhere). The next request sends
// the system instruction inline and the stability rule starts over.
func (c *GeminiClient) invalidateExplicitCache(name string) {
	st := c.explicitState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.name != name {
		return
	}
	st.name, st.hash, st.tokens = "", "", 0
	st.lastHash, st.seen = "", 0
}

// releaseExplicitCache deletes the live resource (shutdown / replacement).
func (c *GeminiClient) releaseExplicitCache(ctx context.Context) {
	st := c.explicitState()
	st.mu.Lock()
	name, tokens := st.name, st.tokens
	st.name, st.hash, st.tokens = "", "", 0
	key := st.releaseKey
	st.releaseKey = ""
	st.mu.Unlock()
	if key != "" {
		client.UnregisterCacheReleaser(key)
	}
	if name == "" {
		return
	}
	c.deleteExplicit(ctx, name)
	client.EmitCacheResource(client.CacheResourceEvent{Provider: "GOOGLEAI", Model: c.model,
		Name: name, Action: client.CacheResourceReleased, Tokens: tokens})
}

func (c *GeminiClient) deleteExplicit(ctx context.Context, name string) {
	status, resp, err := c.cacheRequest(ctx, http.MethodDelete, c.baseURL+"/"+name, nil)
	if err != nil || (status/100 != 2 && status != http.StatusNotFound) {
		c.logger.Debug("Gemini explicit cache delete failed (it expires on its own)",
			zap.String("name", name), zap.Int("status", status), zap.String("error", c.cacheErrorText(status, resp, err)))
	}
}

// isExplicitCacheRejection reports whether a generation error means the
// referenced cachedContent is unusable (expired, deleted, wrong model).
func isExplicitCacheRejection(err error) bool {
	var apiErr *utils.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
		return strings.Contains(strings.ToLower(apiErr.Message), "cachedcontent") ||
			strings.Contains(strings.ToLower(apiErr.Message), "cached content")
	}
	return false
}

// cacheRequest performs one management call with its own timeout.
func (c *GeminiClient) cacheRequest(ctx context.Context, method, url string, body interface{}) (int, []byte, error) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), explicitCacheOpTimeout)
	defer cancel()
	token, err := c.provider.Token(opCtx)
	if err != nil {
		return 0, nil, err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(opCtx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", token)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func (c *GeminiClient) cacheErrorText(status int, resp []byte, err error) string {
	if err != nil {
		return utils.SanitizeSensitiveText(err.Error())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(resp, &body) == nil && body.Error.Message != "" {
		return utils.SanitizeSensitiveText(body.Error.Message)
	}
	return fmt.Sprintf("HTTP %d", status)
}
