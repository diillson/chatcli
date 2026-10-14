/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/utils"
)

type memStore struct {
	mu    sync.Mutex
	creds map[string]providerproto.Credential
}

func newMemStore() *memStore { return &memStore{creds: map[string]providerproto.Credential{}} }

func (s *memStore) Load(p string) (providerproto.Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[p]
	return c, ok
}
func (s *memStore) Save(p string, c providerproto.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[p] = c
	return nil
}
func (s *memStore) Delete(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, p)
	return nil
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// installFixture copies the test binary into dir under a fixture name.
func installFixture(t *testing.T, dir, name string) {
	t.Helper()
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, exeName(name)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

func buildEcho(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, exeName("echo-provider")),
		"github.com/diillson/chatcli/pkg/providersdk/examples/echo").CombinedOutput()
	if err != nil {
		t.Fatalf("build echo: %v\n%s", err, out)
	}
}

func loadSet(t *testing.T, dir string, store CredentialStore) *Set {
	t.Helper()
	set := Load(context.Background(), LoadOptions{Dir: dir, Store: store, CacheDir: t.TempDir(), HostVersion: "test"})
	t.Cleanup(func() { set.Close(context.Background()) })
	return set
}

func TestFullModePluginEndToEnd(t *testing.T) {
	dir := t.TempDir()
	buildEcho(t, dir)
	store := newMemStore()
	set := loadSet(t, dir, store)
	if errs := set.Errors(); len(errs) > 0 {
		t.Fatalf("load errors: %v", errs)
	}
	p, ok := set.Get("echo")
	if !ok {
		t.Fatal("ECHO not loaded")
	}
	if d, ok := catalog.LookupProvider("ECHO"); !ok || d.Builtin() || d.DisplayName != "Echo (example)" {
		t.Fatalf("catalog descriptor = %+v, %v", d, ok)
	}
	if p.DisplayName("pt-BR") != "Echo (exemplo)" || p.DisplayName("fr") != "Echo (example)" {
		t.Fatalf("display names: %q %q", p.DisplayName("pt-BR"), p.DisplayName("fr"))
	}
	if p.Available() {
		t.Fatal("a plugin that requires login must not be available before it")
	}

	ctx := context.Background()
	if _, err := p.Login(ctx, nil, nil); !errors.Is(err, ErrLoginNeedsPerson) {
		t.Fatalf("unattended login without inputs = %v, want ErrLoginNeedsPerson", err)
	}
	if _, err := p.Login(ctx, nil, map[string]string{"token": "demo"}); err != nil {
		t.Fatal(err)
	}
	if !p.Available() {
		t.Fatal("logged-in plugin should be available")
	}
	st, err := p.Status(ctx)
	if err != nil || st.State != providerproto.AuthLoggedIn {
		t.Fatalf("status = %+v, %v", st, err)
	}

	c, err := p.NewClient("echo-1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.SendPrompt(ctx, "hello there", nil, 0)
	if err != nil || out != "echo: hello there" {
		t.Fatalf("SendPrompt = %q, %v", out, err)
	}
	ua := c.(client.UsageAwareClient)
	if u := ua.LastUsage(); u == nil || u.CompletionTokens <= 0 {
		t.Fatalf("usage = %+v", ua.LastUsage())
	}

	sc := c.(client.StreamingClient)
	if !sc.SupportsStreaming() {
		t.Fatal("echo declares streaming")
	}
	ch, err := sc.SendPromptStream(ctx, "a b c", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var final client.StreamChunk
	for chunk := range ch {
		if chunk.Done {
			final = chunk
			continue
		}
		text.WriteString(chunk.Text)
	}
	if final.Error != nil || text.String() != "echo: a b c" || final.StopReason != "stop" {
		t.Fatalf("stream text=%q final=%+v", text.String(), final)
	}

	lister := c.(client.ModelLister)
	models, err := lister.ListModels(ctx)
	if err != nil || len(models) != 2 || models[0].Source != client.ModelSourceAPI {
		t.Fatalf("models = %+v, %v", models, err)
	}
	if catalog.GetContextWindow("ECHO", "echo-1") != 32000 {
		t.Fatalf("catalog window = %d, want 32000 from models/list", catalog.GetContextWindow("ECHO", "echo-1"))
	}
	if n, err := c.(client.TokenCounter).CountTokens(ctx, "one two three", nil); err != nil || n != 3 {
		t.Fatalf("CountTokens = %d, %v", n, err)
	}

	bad, _ := p.NewClient("no-such-model")
	_, err = bad.SendPrompt(ctx, "x", nil, 0)
	var apiErr *utils.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown model err = %v, want APIError 404", err)
	}

	if err := p.Logout(ctx); err != nil || p.Available() {
		t.Fatalf("logout err=%v available=%v", err, p.Available())
	}
}

func TestDialectModeInjectsPluginHeaders(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		calls++
		first := calls == 1
		mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if first {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer backend.Close()

	dir := t.TempDir()
	installFixture(t, dir, "ph-dialect")
	if err := os.WriteFile(filepath.Join(dir, "backend-url"), []byte(backend.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PH_TEST_SECRET", "declared-value")
	t.Setenv("PH_TEST_LEAK", "must-not-reach-the-plugin")

	store := newMemStore()
	set := loadSet(t, dir, store)
	if errs := set.Errors(); len(errs) > 0 {
		t.Fatalf("load errors: %v", errs)
	}
	p, ok := set.Get("PH_GATEWAY")
	if !ok {
		t.Fatal("PH_GATEWAY not loaded")
	}
	ctx := context.Background()
	if _, err := p.Login(ctx, nil, map[string]string{"token": "tok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Models(ctx); err != nil {
		t.Fatal(err)
	}
	if got := catalog.GetContextWindow("PH_GATEWAY", "gw-sonnet"); got != catalog.GetContextWindow("CLAUDEAI", "claude-sonnet-4-6") {
		t.Fatalf("gateway model window %d did not inherit its upstream", got)
	}

	c, err := p.NewClient("gw-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.SendPrompt(ctx, "ping", nil, 64)
	if err != nil || out != "pong" {
		t.Fatalf("SendPrompt = %q, %v", out, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("backend saw %d requests, want 2 (401 then a refreshed retry)", len(seen))
	}
	first, retry := seen[0], seen[1]
	if first.Get("Authorization") == retry.Get("Authorization") || !strings.HasPrefix(retry.Get("Authorization"), "Bearer tok-") {
		t.Fatalf("token not refreshed after 401: %q then %q", first.Get("Authorization"), retry.Get("Authorization"))
	}
	if retry.Get("X-Route") != "anthropic" {
		t.Fatalf("per-model header missing: %v", retry)
	}
	if retry.Get("X-Env-Seen") != "declared-value" {
		t.Fatalf("declared authEnv did not reach the plugin: %q", retry.Get("X-Env-Seen"))
	}
	if retry.Get("X-Env-Leak") != "" {
		t.Fatalf("an undeclared variable leaked into the plugin: %q", retry.Get("X-Env-Leak"))
	}
	if u := c.(client.UsageAwareClient).LastUsage(); u == nil || u.CacheReadInputTokens != 4 {
		t.Fatalf("dialect usage = %+v, want cached tokens from the built-in parser", u)
	}
	if lm, err := c.(client.ModelLister).ListModels(ctx); err != nil || len(lm) != 1 || lm[0].ID != "gw-sonnet" {
		t.Fatalf("dialect ListModels = %+v, %v (must come from the plugin)", lm, err)
	}
}

func TestCrashedPluginRestartsOnNextCall(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "ph-crash")
	set := loadSet(t, dir, newMemStore())
	p, ok := set.Get("PH_CRASH")
	if !ok {
		t.Fatalf("PH_CRASH not loaded: %v", set.Errors())
	}
	c, _ := p.NewClient("m")
	_, err := c.SendPrompt(context.Background(), "x", nil, 0)
	var apiErr *utils.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable || !strings.Contains(err.Error(), "backend exploded") {
		t.Fatalf("crash err = %v, want 503 naming the stderr line", err)
	}
	time.Sleep(minRestartDelay + 100*time.Millisecond)
	if _, err := p.Host().Info(context.Background()); err != nil {
		t.Fatalf("plugin did not restart: %v", err)
	}
}

func TestImpostorIsRejected(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "ph-impostor")
	set := loadSet(t, dir, newMemStore())
	if len(set.List()) != 0 {
		t.Fatal("an impostor plugin was loaded")
	}
	errs := set.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "built-in") {
		t.Fatalf("errors = %v", errs)
	}
	if d, ok := catalog.LookupProvider("OPENAI"); !ok || !d.Builtin() {
		t.Fatal("the built-in descriptor was touched")
	}
}

func TestBrokenBinaryOpensBreaker(t *testing.T) {
	h := NewHost(HostOptions{Path: filepath.Join(t.TempDir(), "missing")})
	for i := 0; i < breakerThreshold; i++ {
		h.mu.Lock()
		h.nextStart = time.Time{}
		h.mu.Unlock()
		if _, err := h.Info(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	_, err := h.Info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "circuit open") {
		t.Fatalf("after %d failures err = %v, want circuit open", breakerThreshold, err)
	}
}

func TestPluginEnvFilters(t *testing.T) {
	env := []string{"PATH=/bin", "HOME=/h", "HTTPS_PROXY=http://proxy:8080", "OPENAI_API_KEY=sk-x", "ACME_SECRET=s", "OTHER=o"}
	got := strings.Join(pluginEnv(env, []string{"ACME_SECRET"}), " ")
	for _, want := range []string{"PATH=/bin", "HOME=/h", "HTTPS_PROXY=http://proxy:8080", "ACME_SECRET=s"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	for _, leak := range []string{"OPENAI_API_KEY", "OTHER"} {
		if strings.Contains(got, leak) {
			t.Errorf("%s leaked: %s", leak, got)
		}
	}
}

func TestMapError(t *testing.T) {
	cases := map[int]int{
		providerproto.ErrCodeAuthRequired:  http.StatusUnauthorized,
		providerproto.ErrCodeRateLimited:   http.StatusTooManyRequests,
		providerproto.ErrCodeModelNotFound: http.StatusNotFound,
		providerproto.ErrCodeUnavailable:   http.StatusServiceUnavailable,
		providerproto.ErrCodeBadRequest:    http.StatusBadRequest,
		providerproto.ErrCodeInternal:      http.StatusBadGateway,
	}
	for code, status := range cases {
		var apiErr *utils.APIError
		if err := mapError("P", providerproto.NewError(code, "m")); !errors.As(err, &apiErr) || apiErr.StatusCode != status {
			t.Errorf("code %d -> %v, want %d", code, err, status)
		}
	}
	if err := mapError("P", providerproto.NewError(providerproto.ErrCodeContextTooLong, "too big")); !client.IsContextOverflowError(err) {
		t.Errorf("context too long must be recognized as overflow: %v", err)
	}
	if err := mapError("P", providerproto.NewError(providerproto.ErrCodeCancelled, "c")); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled -> %v", err)
	}
}

func TestConvertRoundTrip(t *testing.T) {
	hist := []models.Message{
		{Role: "system", SystemParts: []models.ContentBlock{{Type: "text", Text: "rules", CacheControl: &models.CacheControl{Type: "ephemeral"}}}},
		{Role: "user", Content: "look", Images: []models.ImageContent{{MediaType: "image/png", Data: []byte{1, 2}}}},
		{Role: "assistant", Thinking: []models.ThinkingBlock{{Type: "thinking", Thinking: "hmm", Signature: "sig"}},
			ToolCalls: []models.ToolCall{{ID: "c1", Name: "read", Arguments: map[string]interface{}{"file": "a"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "data", IsError: true, ErrorCode: "ENOENT"},
	}
	msgs := toProtoMessages("next", hist)
	if len(msgs) != 5 || msgs[4].Content[0].Text != "next" {
		t.Fatalf("prompt not appended: %+v", msgs)
	}
	if !msgs[0].Content[0].CacheBreakpoint || msgs[1].Content[1].Data != "AQI=" {
		t.Fatalf("system/image parts: %+v %+v", msgs[0], msgs[1])
	}
	if msgs[2].Content[0].Type != providerproto.PartThinking || msgs[2].ToolCalls[0].Arguments != `{"file":"a"}` {
		t.Fatalf("assistant turn: %+v", msgs[2])
	}
	if msgs[3].ToolCallID != "c1" || !strings.HasPrefix(msgs[3].Content[0].Text, "[ERROR:ENOENT]") {
		t.Fatalf("tool turn: %+v", msgs[3])
	}

	cost := 0.5
	resp := fromProtoResult(providerproto.ChatResult{
		Message: providerproto.Message{Role: "assistant",
			Content:   []providerproto.Part{{Type: "thinking", Text: "t"}, {Type: "text", Text: "done"}},
			ToolCalls: []providerproto.ToolCall{{ID: "c2", Name: "x", Arguments: `{"a":1}`}}},
		StopReason: providerproto.StopToolUse,
		Usage:      &providerproto.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 90, CostUSD: &cost},
	})
	if resp.Content != "done" || resp.StopReason != "tool_calls" || len(resp.Thinking) != 1 || resp.ToolCalls[0].Arguments["a"] != float64(1) {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Usage.InputTokensTotal != 100 || resp.Usage.CostUSD != 0.5 {
		t.Fatalf("usage not normalized as additive: %+v", resp.Usage)
	}
}

func TestValidateInfo(t *testing.T) {
	ok := providerproto.InitializeResult{
		ProtocolVersion: providerproto.ProtocolVersion,
		Provider:        providerproto.ProviderInfo{Name: "ACME", DisplayName: map[string]string{"en": "Acme"}},
		Mode:            providerproto.ModeDialect,
		Dialect:         &providerproto.Dialect{API: providerproto.DialectOpenAIChat, BaseURL: "https://x.example/v1"},
	}
	if err := validateInfo(ok); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Dialect = &providerproto.Dialect{API: providerproto.DialectAPI("smoke-signals"), BaseURL: "https://x.example"}
	if err := validateInfo(bad); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported dialect = %v", err)
	}
	bad = ok
	bad.Dialect = &providerproto.Dialect{API: providerproto.DialectOpenAIChat, BaseURL: "file:///etc"}
	if err := validateInfo(bad); err == nil {
		t.Fatal("non-http base URL accepted")
	}
}

func TestDiscoverSkipsNonPlugins(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{exeName("good"): 0o755, "good.sig": 0o644, ".hidden": 0o755, "notes.txt": 0o644} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(dir)
	if err != nil || len(got) != 1 || filepath.Base(got[0]) != exeName("good") {
		t.Fatalf("Discover = %v, %v", got, err)
	}
	if got, err := Discover(filepath.Join(dir, "absent")); err != nil || got != nil {
		t.Fatalf("missing dir = %v, %v", got, err)
	}
}

// A model refresh racing Close must not outlive it: Close waits for the
// refresh, and a refresh after Close neither calls the plugin nor writes
// the cache.
func TestModelsAfterCloseDoesNotWriteCache(t *testing.T) {
	dir := t.TempDir()
	installFixture(t, dir, "ph-dialect")
	if err := os.WriteFile(filepath.Join(dir, "backend-url"), []byte("http://127.0.0.1:1"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	set := Load(context.Background(), LoadOptions{Dir: dir, Store: newMemStore(), CacheDir: cache, HostVersion: "test"})
	p, ok := set.Get("PH_GATEWAY")
	if !ok {
		t.Fatalf("plugin not loaded: %v", set.Errors())
	}
	set.Close(context.Background())
	if _, err := p.Models(context.Background()); !errors.Is(err, providerproto.ErrClosed) {
		t.Fatalf("Models after Close = %v, want ErrClosed", err)
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 0 {
		t.Fatalf("cache written after Close: %v", entries)
	}
}
