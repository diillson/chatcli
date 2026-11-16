/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package version

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTrip func(*http.Request) *http.Response

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func answer(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func noTokens(t *testing.T) {
	t.Helper()
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
}

// The environment's token goes to the GitHub API over HTTPS and nowhere
// else: a mirror or a test server set in CHATCLI_LATEST_VERSION_URL never
// sees it.
func TestReleaseToken_OnlyForTheGitHubAPI(t *testing.T) {
	noTokens(t)
	assert.Empty(t, releaseToken(LatestVersionURL), "no token in the environment")

	t.Setenv("GITHUB_TOKEN", "from-github-token")
	assert.Equal(t, "from-github-token", releaseToken(LatestVersionURL))
	t.Setenv("GH_TOKEN", " from-gh-token ")
	assert.Equal(t, "from-gh-token", releaseToken(LatestVersionURL), "GH_TOKEN wins, as it does for gh")

	for _, other := range []string{
		"http://api.github.com/repos/diillson/chatcli/releases/latest",
		"https://api.github.com.evil.example/repos/x/y/releases/latest",
		"https://mirror.example/releases/latest",
		"https://evil.example/?h=api.github.com",
		"http://127.0.0.1:8080/latest",
		"://bad",
	} {
		assert.Empty(t, releaseToken(other), other)
	}
}

func TestRateLimited(t *testing.T) {
	spent := http.Header{"X-Ratelimit-Remaining": {"0"}}
	later := http.Header{"Retry-After": {"30"}}
	left := http.Header{"X-Ratelimit-Remaining": {"12"}}
	assert.True(t, rateLimited(http.StatusForbidden, spent))
	assert.True(t, rateLimited(http.StatusTooManyRequests, later))
	assert.False(t, rateLimited(http.StatusForbidden, left), "a 403 with allowance left is not the limit")
	assert.False(t, rateLimited(http.StatusForbidden, http.Header{}), "a proxy's 403 carries no rate limit headers")
	assert.False(t, rateLimited(http.StatusNotFound, spent))
}

func TestRateLimitReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	assert.Equal(t, now.Add(30*time.Second), rateLimitReset(http.Header{"Retry-After": {"30"}}, now))
	at := now.Add(17 * time.Minute)
	assert.Equal(t, at, rateLimitReset(http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(at.Unix(), 10)}}, now))
	assert.True(t, rateLimitReset(http.Header{"X-Ratelimit-Reset": {"soon"}}, now).IsZero())
	assert.True(t, rateLimitReset(http.Header{}, now).IsZero())
}

func releaseRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, LatestVersionURL, nil)
	require.NoError(t, err)
	return req
}

// With a token the request is authenticated; without one it is sent as it
// always was.
func TestDoReleaseRequest_SendsTheToken(t *testing.T) {
	noTokens(t)
	var seen []string
	client := &http.Client{Transport: roundTrip(func(r *http.Request) *http.Response {
		seen = append(seen, r.Header.Get("Authorization"))
		return answer(http.StatusOK, `{}`)
	})}

	resp, authed, err := doReleaseRequest(client, releaseRequest(t))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.False(t, authed)

	t.Setenv("GH_TOKEN", "tok")
	req := releaseRequest(t)
	resp, authed, err = doReleaseRequest(client, req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.True(t, authed)
	assert.Equal(t, []string{"", "Bearer tok"}, seen)
	assert.Empty(t, req.Header.Get("Authorization"), "the caller's request is left without the token")
}

// A token GitHub rejects must not break a check that needs none: the
// request is made again without it.
func TestDoReleaseRequest_RejectedTokenFallsBack(t *testing.T) {
	noTokens(t)
	t.Setenv("GITHUB_TOKEN", "expired")
	var seen []string
	client := &http.Client{Transport: roundTrip(func(r *http.Request) *http.Response {
		auth := r.Header.Get("Authorization")
		seen = append(seen, auth)
		if auth != "" {
			return answer(http.StatusUnauthorized, `{"message":"Bad credentials"}`)
		}
		return answer(http.StatusOK, `{"tag_name":"v1.2.3"}`)
	})}
	resp, authed, err := doReleaseRequest(client, releaseRequest(t))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.False(t, authed, "the answer came from the anonymous request")
	assert.Equal(t, []string{"Bearer expired", ""}, seen)
}

// The real fetch against a server that answers like a spent allowance: the
// error says it is the rate limit and when it lifts, and a plain 403 stays
// a plain 403.
func TestFetchLatestRelease_RateLimitIsTyped(t *testing.T) {
	noTokens(t)
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	limited := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if limited {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	_, err := FetchLatestReleaseImpl(context.Background())
	var status *ReleaseStatusError
	require.True(t, errors.As(err, &status))
	assert.True(t, status.RateLimited)
	assert.False(t, status.Authenticated)
	assert.True(t, status.ResetAt.Equal(reset), "reset %v, want %v", status.ResetAt, reset)
	assert.Contains(t, err.Error(), "status 403")
	assert.Contains(t, err.Error(), reset.Local().Format("15:04"))

	limited = false
	_, err = FetchLatestReleaseImpl(context.Background())
	require.True(t, errors.As(err, &status))
	assert.False(t, status.RateLimited)
	assert.Equal(t, "erro ao verificar versão: status 403", err.Error(), "the message callers already match on is unchanged")
}

func TestReleaseStatusError_RateLimitWithoutReset(t *testing.T) {
	e := &ReleaseStatusError{StatusCode: 429, RateLimited: true}
	assert.Equal(t, "erro ao verificar versão: status 429 (limite de requisições da API do GitHub)", e.Error())
}
