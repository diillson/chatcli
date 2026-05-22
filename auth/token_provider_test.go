package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestStaticTokenProvider_Token(t *testing.T) {
	p := NewStaticTokenProvider("raw-key", AuthModeAPIKey, ProviderOpenAI)
	got, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "raw-key" {
		t.Errorf("token = %q, want raw-key", got)
	}
	if p.Mode() != AuthModeAPIKey {
		t.Errorf("mode = %q, want api-key", p.Mode())
	}
}

func TestStaticTokenProvider_EmptyTokenErrors(t *testing.T) {
	p := NewStaticTokenProvider("", AuthModeAPIKey, ProviderOpenAI)
	if _, err := p.Token(context.Background()); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestNewStaticTokenProviderFromResolved_ParsesPrefixes(t *testing.T) {
	cases := []struct {
		in       string
		wantTok  string
		wantMode AuthMode
	}{
		{"oauth:abc", "abc", AuthModeOAuth},
		{"token:xyz", "xyz", AuthModeToken},
		{"apikey:k1", "k1", AuthModeAPIKey},
		{"plain", "plain", AuthModeAPIKey},
	}
	for _, c := range cases {
		p := NewStaticTokenProviderFromResolved(c.in, ProviderOpenAI)
		got, err := p.Token(context.Background())
		if err != nil {
			t.Fatalf("%q: Token: %v", c.in, err)
		}
		if got != c.wantTok {
			t.Errorf("%q: token = %q, want %q", c.in, got, c.wantTok)
		}
		if p.Mode() != c.wantMode {
			t.Errorf("%q: mode = %q, want %q", c.in, p.Mode(), c.wantMode)
		}
	}
}

func TestDoWithRefresh_NonOAuthSkipsRetry(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	p := NewStaticTokenProvider("k", AuthModeAPIKey, ProviderOpenAI)
	httpClient := server.Client()
	resp, err := DoWithRefresh(context.Background(), p, func(_ string) (*http.Response, error) {
		return httpClient.Get(server.URL)
	})
	if err != nil {
		t.Fatalf("DoWithRefresh: %v", err)
	}
	_ = resp.Body.Close()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (no retry for non-oauth)", got)
	}
}

func TestDoWithRefresh_OAuthRetriesOn401(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		gotAuth := r.Header.Get("Authorization")
		if n == 1 {
			if gotAuth != "Bearer first" {
				t.Errorf("first call auth = %q, want 'Bearer first'", gotAuth)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if gotAuth != "Bearer second" {
			t.Errorf("retry call auth = %q, want 'Bearer second'", gotAuth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	tokens := []string{"first", "second"}
	var idx int32
	p := &oauthTokenProvider{
		cred: AuthProfileCredential{
			Provider: ProviderAnthropic,
			Access:   tokens[0],
			Refresh:  "refresh-token",
			Expires:  time.Now().Add(1 * time.Hour).UnixMilli(),
		},
		profileID: "test",
		source:    "test",
		logger:    zap.NewNop(),
		refreshFn: func(_ context.Context, c *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
			out := *c
			out.Access = tokens[atomic.AddInt32(&idx, 1)]
			out.Expires = time.Now().Add(1 * time.Hour).UnixMilli()
			return &out, nil
		},
	}

	httpClient := server.Client()
	resp, err := DoWithRefresh(context.Background(), p, func(token string) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return httpClient.Do(req)
	})
	if err != nil {
		t.Fatalf("DoWithRefresh: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server calls = %d, want 2 (one retry on 401)", got)
	}
}

func TestOAuthTokenProvider_RefreshOnExpired(t *testing.T) {
	var refreshCount int32
	p := &oauthTokenProvider{
		cred: AuthProfileCredential{
			Provider: ProviderAnthropic,
			Access:   "stale",
			Refresh:  "rt",
			Expires:  time.Now().Add(-1 * time.Hour).UnixMilli(),
		},
		logger: zap.NewNop(),
		refreshFn: func(_ context.Context, c *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
			atomic.AddInt32(&refreshCount, 1)
			out := *c
			out.Access = "fresh"
			out.Expires = time.Now().Add(1 * time.Hour).UnixMilli()
			return &out, nil
		},
	}

	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "fresh" {
		t.Errorf("token = %q, want fresh", tok)
	}
	if atomic.LoadInt32(&refreshCount) != 1 {
		t.Errorf("refreshes = %d, want 1", refreshCount)
	}

	// Second call should not refresh again
	tok, err = p.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "fresh" {
		t.Errorf("second token = %q, want fresh", tok)
	}
	if got := atomic.LoadInt32(&refreshCount); got != 1 {
		t.Errorf("refreshes after second call = %d, want still 1", got)
	}
}

func TestOAuthTokenProvider_InvalidateForcesRefresh(t *testing.T) {
	var refreshCount int32
	p := &oauthTokenProvider{
		cred: AuthProfileCredential{
			Provider: ProviderAnthropic,
			Access:   "v1",
			Refresh:  "rt",
			Expires:  time.Now().Add(1 * time.Hour).UnixMilli(),
		},
		logger: zap.NewNop(),
		refreshFn: func(_ context.Context, c *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
			n := atomic.AddInt32(&refreshCount, 1)
			out := *c
			out.Access = fmt.Sprintf("v%d", n+1)
			out.Expires = time.Now().Add(1 * time.Hour).UnixMilli()
			return &out, nil
		},
	}

	if tok, _ := p.Token(context.Background()); tok != "v1" {
		t.Errorf("initial token = %q, want v1", tok)
	}
	p.Invalidate()
	tok, _ := p.Token(context.Background())
	if tok != "v2" {
		t.Errorf("after Invalidate token = %q, want v2", tok)
	}
}

func TestOAuthTokenProvider_RefreshCoalesces(t *testing.T) {
	var refreshCount int32
	block := make(chan struct{})
	p := &oauthTokenProvider{
		cred: AuthProfileCredential{
			Provider: ProviderAnthropic,
			Access:   "stale",
			Refresh:  "rt",
			Expires:  time.Now().Add(-1 * time.Hour).UnixMilli(),
		},
		logger: zap.NewNop(),
		refreshFn: func(_ context.Context, c *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
			atomic.AddInt32(&refreshCount, 1)
			<-block
			out := *c
			out.Access = "fresh"
			out.Expires = time.Now().Add(1 * time.Hour).UnixMilli()
			return &out, nil
		},
	}

	const N = 8
	var wg sync.WaitGroup
	tokens := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tok, err := p.Token(context.Background())
			if err != nil {
				t.Errorf("Token: %v", err)
				return
			}
			tokens[idx] = tok
		}(i)
	}
	// Let the goroutines line up on the in-flight channel
	time.Sleep(20 * time.Millisecond)
	close(block)
	wg.Wait()

	if got := atomic.LoadInt32(&refreshCount); got != 1 {
		t.Errorf("refreshes = %d, want 1 (coalesced)", got)
	}
	for i, tok := range tokens {
		if tok != "fresh" {
			t.Errorf("token[%d] = %q, want fresh", i, tok)
		}
	}
}

func TestOAuthTokenProvider_RefreshErrorSurfacesToCallers(t *testing.T) {
	refreshErr := errors.New("refresh boom")
	p := &oauthTokenProvider{
		cred: AuthProfileCredential{
			Provider: ProviderAnthropic,
			Access:   "stale",
			Refresh:  "rt",
			Expires:  time.Now().Add(-1 * time.Hour).UnixMilli(),
		},
		logger: zap.NewNop(),
		refreshFn: func(_ context.Context, _ *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
			return nil, refreshErr
		},
	}
	_, err := p.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Token err = %v, want refresh error", err)
	}
}

func TestOAuthTokenProvider_BackgroundRefreshFires(t *testing.T) {
	// Build a provider with shrunk timing knobs so the test runs in tens of
	// milliseconds even under -race. Set them BEFORE spawning the goroutine.
	cred := &AuthProfileCredential{
		Provider: ProviderAnthropic,
		Access:   "v1",
		Refresh:  "rt",
		Expires:  time.Now().Add(200 * time.Millisecond).UnixMilli(),
	}
	refreshed := make(chan struct{}, 4)
	p := &oauthTokenProvider{
		cred:                *cred,
		logger:              zap.NewNop(),
		refreshLead:         10 * time.Millisecond,
		refreshMinWait:      50 * time.Millisecond,
		refreshErrorBackoff: 50 * time.Millisecond,
		refreshFn: func(_ context.Context, c *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
			out := *c
			out.Access = "renewed"
			// Push expiry forward so the loop sleeps the full min-wait between
			// refreshes (otherwise we hammer refreshFn repeatedly).
			out.Expires = time.Now().Add(10 * time.Minute).UnixMilli()
			select {
			case refreshed <- struct{}{}:
			default:
			}
			return &out, nil
		},
	}
	p.bgCtx, p.bgCancel = context.WithCancel(context.Background())
	p.bgDone = make(chan struct{})
	go p.backgroundLoop()
	defer p.Close()

	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("background refresh did not fire within expected window")
	}
}

func TestOAuthTokenProvider_CloseStopsBackground(t *testing.T) {
	cred := &AuthProfileCredential{
		Provider: ProviderAnthropic,
		Access:   "v1",
		Refresh:  "rt",
		Expires:  time.Now().Add(1 * time.Hour).UnixMilli(),
	}
	p := newOAuthTokenProvider(cred, "", "test", zap.NewNop())
	done := p.bgDone
	p.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background loop did not exit after Close")
	}
	// Close is idempotent.
	p.Close()
}
