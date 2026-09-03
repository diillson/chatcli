/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tokenizer

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

func TestEncodingForModel_FamilyMapping(t *testing.T) {
	cases := map[string]string{
		"gpt-5.6-terra": EncodingO200k, "openai/gpt-5.6-luna": EncodingO200k, "gpt-4o-mini": EncodingO200k,
		"o3-mini": EncodingO200k, "gpt-4.1": EncodingO200k,
		"gpt-4": EncodingCL100k, "gpt-4-0613": EncodingCL100k, "gpt-3.5-turbo": EncodingCL100k, "GPT-4-32K": EncodingCL100k,
	}
	for model, want := range cases {
		if got := EncodingForModel(model); got != want {
			t.Fatalf("%s → %s, want %s", model, got, want)
		}
	}
}

func TestIsGPTModel(t *testing.T) {
	for model, want := range map[string]bool{"gpt-5.6-sol": true, "openai/gpt-4o": true, "o4-mini": true, "chatgpt-4o-latest": true,
		"claude-sonnet-5": false, "anthropic/claude-opus-5": false, "meta/llama-4": false, "": false} {
		if IsGPTModel(model) != want {
			t.Fatalf("%q: want %v", model, want)
		}
	}
}

func TestMessagesFromHistory_ShapesLikeTheAdapters(t *testing.T) {
	hist := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolCall{{Name: "bash", Arguments: map[string]interface{}{"cmd": "ls"}}}},
		{Role: "weird", Content: "x"},
	}
	msgs := MessagesFromHistory("next", hist)
	if len(msgs) != 5 || msgs[1].Role != "user" || msgs[3].Role != "user" || msgs[4].Content != "next" {
		t.Fatalf("messages = %+v", msgs)
	}
	if !strings.Contains(msgs[2].Content, "bash") || !strings.Contains(msgs[2].Content, "ls") {
		t.Fatalf("tool calls must be rendered: %q", msgs[2].Content)
	}
	// Prompt already at the tail (same role and text): not appended twice.
	again := MessagesFromHistory("hi", hist[1:2])
	if len(again) != 1 {
		t.Fatalf("duplicate tail prompt must not be appended: %+v", again)
	}
}

func TestParseBpe(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("hello")) + " 42\n" + base64.StdEncoding.EncodeToString([]byte(" ")) + " 220\n\n"
	ranks, err := parseBpe([]byte(data))
	if err != nil || ranks["hello"] != 42 || ranks[" "] != 220 || len(ranks) != 2 {
		t.Fatalf("ranks=%v err=%v", ranks, err)
	}
	if _, err := parseBpe([]byte("not base64!! 1\n")); err == nil {
		t.Fatal("malformed lines must error")
	}
}

func TestCacheLoader_FetchesOnceThenServesFromDisk(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte("a")) + " 1\n"))
	}))
	defer srv.Close()
	l := &cacheLoader{dir: filepath.Join(t.TempDir(), "tok"), client: srv.Client()}
	ranks, err := l.LoadTiktokenBpe(srv.URL + "/vocab")
	if err != nil || ranks["a"] != 1 {
		t.Fatalf("first load: %v %v", ranks, err)
	}
	if _, err := os.Stat(l.cachePath(srv.URL + "/vocab")); err != nil {
		t.Fatal("vocabulary must be cached on disk")
	}
	if _, err := l.LoadTiktokenBpe(srv.URL + "/vocab"); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("second load must come from disk, got %d fetches", hits)
	}
}

// TestCountChat_RealVocabulary is the end-to-end check against the real
// o200k vocabulary; it needs network (or a warm cache) and runs only when
// CHATCLI_TEST_TOKENIZER_NETWORK=1.
func TestCountChat_RealVocabulary(t *testing.T) {
	if os.Getenv("CHATCLI_TEST_TOKENIZER_NETWORK") != "1" {
		t.Skip("set CHATCLI_TEST_TOKENIZER_NETWORK=1 to fetch the vocabulary")
	}
	Prefetch("gpt-5.6-terra")
	deadline := 60
	var n int
	var err error
	for i := 0; i < deadline; i++ {
		n, err = CountChat("gpt-5.6-terra", []ChatMessage{{Role: "user", Content: "Hello, world! How are you today?"}})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrTokenizerLoading) {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	if err != nil || n < 10 || n > 20 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}

func TestCountText_ColdCacheLoadsInBackgroundOrCounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // cold vocabulary cache
	n, err := CountText("gpt-5.6-terra", "hello world")
	if err != nil && !errors.Is(err, ErrTokenizerLoading) {
		t.Fatalf("cold cache must load in the background, got %v", err)
	}
	if err == nil && n <= 0 {
		t.Fatalf("a warm count must be positive, got %d", n)
	}
	Prefetch("gpt-4o") // idempotent, never blocks
	_ = Ready(EncodingO200k)
}
