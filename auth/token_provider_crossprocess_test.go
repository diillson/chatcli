/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func expiredProvider(profileID string, refreshes *int32, delay time.Duration) *oauthTokenProvider {
	return &oauthTokenProvider{
		cred: AuthProfileCredential{
			Provider: ProviderAnthropic,
			Access:   "stale-access",
			Refresh:  "stale-refresh",
			Expires:  time.Now().Add(-time.Minute).UnixMilli(),
		},
		profileID: profileID,
		logger:    zap.NewNop(),
		refreshFn: func(_ context.Context, c *AuthProfileCredential, _ *zap.Logger) (*AuthProfileCredential, error) {
			n := atomic.AddInt32(refreshes, 1)
			time.Sleep(delay)
			out := *c
			out.Access = fmt.Sprintf("renewed-%d", n)
			out.Refresh = fmt.Sprintf("rotated-%d", n)
			out.Expires = time.Now().Add(time.Hour).UnixMilli()
			return &out, nil
		},
	}
}

func seedProfile(t *testing.T, id string) {
	t.Helper()
	if err := UpsertProfile(id, &AuthProfileCredential{
		Provider: ProviderAnthropic, Access: "stale-access", Refresh: "stale-refresh",
		Expires: time.Now().Add(-time.Minute).UnixMilli(),
	}, nil); err != nil {
		t.Fatal(err)
	}
}

// Two holders of the same expired profile (two chatcli processes) renew
// once: the second adopts what the first stored instead of spending the
// refresh token the server already rotated.
func TestOAuthRefresh_SerializedAcrossHoldersAndAdopted(t *testing.T) {
	useAuthDir(t)
	seedProfile(t, "anthropic:default")

	var refreshes int32
	a := expiredProvider("anthropic:default", &refreshes, 100*time.Millisecond)
	b := expiredProvider("anthropic:default", &refreshes, 100*time.Millisecond)

	var wg sync.WaitGroup
	tokens := make([]string, 2)
	for i, p := range []*oauthTokenProvider{a, b} {
		wg.Add(1)
		go func(i int, p *oauthTokenProvider) {
			defer wg.Done()
			tok, err := p.Token(context.Background())
			if err != nil {
				t.Errorf("Token: %v", err)
			}
			tokens[i] = tok
		}(i, p)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&refreshes); got != 1 {
		t.Fatalf("refresh round-trips = %d, want 1", got)
	}
	if tokens[0] != tokens[1] || tokens[0] != "renewed-1" {
		t.Fatalf("both holders must end with the one renewed token, got %q and %q", tokens[0], tokens[1])
	}
	for _, p := range []*oauthTokenProvider{a, b} {
		if p.cred.Refresh != "rotated-1" {
			t.Errorf("refresh token = %q, want the rotated one", p.cred.Refresh)
		}
	}
}

// The renewal is written over the current file, not over this process's
// cached copy, which another process may have made stale.
func TestOAuthRefresh_PersistsOverTheCurrentFile(t *testing.T) {
	useAuthDir(t)
	seedProfile(t, "anthropic:default")
	if err := UpsertProfile("openai:default", &AuthProfileCredential{Provider: "openai", Key: "fake-v1"}, nil); err != nil {
		t.Fatal(err)
	}
	stale := LoadStore(nil)
	staleCopy := *stale
	staleCopy.Profiles = map[string]*AuthProfileCredential{}
	for k, v := range stale.Profiles {
		c := *v
		staleCopy.Profiles[k] = &c
	}

	// Another process updates a different profile on disk.
	InvalidateCache()
	if err := UpsertProfile("openai:default", &AuthProfileCredential{Provider: "openai", Key: "fake-v2"}, nil); err != nil {
		t.Fatal(err)
	}
	// This process still holds the old copy in its cache.
	storeMu.Lock()
	cachedStore = &staleCopy
	storeMu.Unlock()

	var refreshes int32
	p := expiredProvider("anthropic:default", &refreshes, 0)
	if _, err := p.Token(context.Background()); err != nil {
		t.Fatal(err)
	}

	InvalidateCache()
	got := LoadStore(nil)
	if k := got.Profiles["openai:default"].Key; k != "fake-v2" {
		t.Errorf("the other process's update was overwritten: key = %q", k)
	}
	if a := got.Profiles["anthropic:default"].Access; a != "renewed-1" {
		t.Errorf("renewal not persisted: access = %q", a)
	}
}
