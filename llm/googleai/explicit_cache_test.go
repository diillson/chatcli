/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package googleai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// fakeGemini records the management and generation calls a client makes.
type fakeGemini struct {
	mu          sync.Mutex
	creates     int
	patches     int
	deletes     int
	generates   []map[string]interface{}
	createFail  int    // HTTP status to fail creation with (0 = succeed)
	createMsg   string // error message body on failure
	rejectCache bool   // generateContent rejects any cachedContent
	tokens      int
}

func (f *fakeGemini) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cachedContents"):
			f.creates++
			if f.createFail != 0 {
				w.WriteHeader(f.createFail)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]interface{}{"message": f.createMsg}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":          "cachedContents/abc123",
				"expireTime":    time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano),
				"usageMetadata": map[string]int{"totalTokenCount": f.tokens},
			})
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/cachedContents/"):
			f.patches++
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "cachedContents/abc123"})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/cachedContents/"):
			f.deletes++
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":generateContent"):
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.generates = append(f.generates, body)
			if f.rejectCache {
				if _, ok := body["cachedContent"]; ok {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":400,"message":"CachedContent not found","status":"INVALID_ARGUMENT"}}`))
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"candidates":    []map[string]interface{}{{"content": map[string]interface{}{"parts": []map[string]string{{"text": "ok"}}}, "finishReason": "STOP"}},
				"usageMetadata": map[string]int{"promptTokenCount": 10, "candidatesTokenCount": 1, "totalTokenCount": 11},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newExplicitTestClient(t *testing.T, f *fakeGemini) *GeminiClient {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c := NewGeminiClient(auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("googleai")), "gemini-2.5-flash", zap.NewNop(), 1, time.Millisecond)
	c.baseURL = srv.URL
	return c
}

func bigSystemHistory(marker string) []models.Message {
	return []models.Message{{Role: "system", Content: marker + strings.Repeat("stable system prompt text ", 1200)}}
}

func TestExplicitCache_DisabledSendsInline(t *testing.T) {
	t.Setenv(client.PromptCacheExplicitEnv, "false")
	f := &fakeGemini{tokens: 5000}
	c := newExplicitTestClient(t, f)
	for i := 0; i < 3; i++ {
		if _, err := c.SendPrompt(context.Background(), "hi", bigSystemHistory(""), 100); err != nil {
			t.Fatal(err)
		}
	}
	if f.creates != 0 {
		t.Fatalf("explicit cache must be opt-in; got %d creates", f.creates)
	}
	for _, g := range f.generates {
		if _, ok := g["system_instruction"]; !ok {
			t.Fatal("system instruction must travel inline when caching is off")
		}
	}
}

func TestExplicitCache_StabilityThenReference(t *testing.T) {
	t.Setenv(client.PromptCacheExplicitEnv, "true")
	t.Setenv(client.PromptCacheTTLEnv, "5m")
	var events []client.CacheResourceEvent
	client.RegisterCacheResourceObserver(func(ev client.CacheResourceEvent) { events = append(events, ev) })
	t.Cleanup(func() { client.RegisterCacheResourceObserver(nil) })

	f := &fakeGemini{tokens: 5000}
	c := newExplicitTestClient(t, f)
	ctx := context.Background()
	hist := bigSystemHistory("")

	// Turn 1: first sighting — no resource yet, instruction inline.
	if _, err := c.SendPrompt(ctx, "one", hist, 100); err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 {
		t.Fatalf("a one-off prompt must not buy storage; creates=%d", f.creates)
	}
	// Turn 2: same prompt again — resource created and referenced.
	if _, err := c.SendPrompt(ctx, "two", hist, 100); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatalf("second sighting must create the resource; creates=%d", f.creates)
	}
	g := f.generates[1]
	if g["cachedContent"] != "cachedContents/abc123" {
		t.Fatalf("request must reference the resource, got %v", g["cachedContent"])
	}
	if _, ok := g["system_instruction"]; ok {
		t.Fatal("request must omit system_instruction when the resource carries it")
	}
	// Turn 3: reused, no new create.
	if _, err := c.SendPrompt(ctx, "three", hist, 100); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || f.generates[2]["cachedContent"] != "cachedContents/abc123" {
		t.Fatalf("resource must be reused; creates=%d", f.creates)
	}
	if len(events) != 1 || events[0].Action != client.CacheResourceCreated || events[0].Tokens != 5000 || events[0].TTL != 5*time.Minute {
		t.Fatalf("created event expected, got %+v", events)
	}
	// Shutdown releases it.
	if n := client.ReleaseCacheResources(ctx); n != 1 {
		t.Fatalf("one releaser expected, got %d", n)
	}
	if f.deletes != 1 {
		t.Fatalf("release must delete the resource; deletes=%d", f.deletes)
	}
	if events[len(events)-1].Action != client.CacheResourceReleased {
		t.Fatalf("released event expected, got %+v", events[len(events)-1])
	}
}

func TestExplicitCache_SmallPromptNeverCaches(t *testing.T) {
	t.Setenv(client.PromptCacheExplicitEnv, "true")
	f := &fakeGemini{tokens: 100}
	c := newExplicitTestClient(t, f)
	small := []models.Message{{Role: "system", Content: "short"}}
	for i := 0; i < 3; i++ {
		if _, err := c.SendPrompt(context.Background(), "x", small, 100); err != nil {
			t.Fatal(err)
		}
	}
	if f.creates != 0 {
		t.Fatalf("prompts under the floor must not create resources; creates=%d", f.creates)
	}
}

func TestExplicitCache_RejectedAtGenerationFallsBackInline(t *testing.T) {
	t.Setenv(client.PromptCacheExplicitEnv, "true")
	f := &fakeGemini{tokens: 5000, rejectCache: true}
	c := newExplicitTestClient(t, f)
	ctx := context.Background()
	hist := bigSystemHistory("")
	_, _ = c.SendPrompt(ctx, "one", hist, 100)
	resp, err := c.SendPrompt(ctx, "two", hist, 100)
	if err != nil || resp != "ok" {
		t.Fatalf("turn must succeed inline after the rejection, got %q err=%v", resp, err)
	}
	// generates: turn1 inline, turn2 with cache (rejected), turn2 retry inline.
	if len(f.generates) != 3 {
		t.Fatalf("expected 3 generate calls, got %d", len(f.generates))
	}
	if _, ok := f.generates[1]["cachedContent"]; !ok {
		t.Fatal("second call must have referenced the resource")
	}
	if _, ok := f.generates[2]["system_instruction"]; !ok {
		t.Fatal("retry must carry the system instruction inline")
	}
	if c.explicitState().name != "" {
		t.Fatal("rejected resource must be forgotten")
	}
}

func TestExplicitCache_CreateFailureBacksOffAndLearnsFloor(t *testing.T) {
	t.Setenv(client.PromptCacheExplicitEnv, "true")
	f := &fakeGemini{tokens: 5000, createFail: http.StatusBadRequest, createMsg: "Cached content is too small. total_token_count=4800, min_total_token_count=32768"}
	c := newExplicitTestClient(t, f)
	ctx := context.Background()
	hist := bigSystemHistory("")
	for i := 0; i < 4; i++ {
		if _, err := c.SendPrompt(ctx, "x", hist, 100); err != nil {
			t.Fatalf("turn %d must not fail because caching failed: %v", i, err)
		}
	}
	if f.creates != 1 {
		t.Fatalf("a refused prompt must back off, got %d creates", f.creates)
	}
	if c.explicitState().learnedMin == 0 {
		t.Fatal("a too-small rejection must teach the floor")
	}
	for _, g := range f.generates {
		if _, ok := g["system_instruction"]; !ok {
			t.Fatal("every request must have carried the instruction inline")
		}
	}
}
