/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * release_request.go
 *
 * The release check against the GitHub API. Without a token GitHub allows
 * 60 requests an hour per IP address, shared by everything behind that
 * address, and answers 403 once they are spent. Two things follow:
 *
 *   - the answer says it is the rate limit and when it lifts, so the check
 *     reports that instead of a bare 403 and nobody retries into the limit;
 *   - a GitHub token already in the environment (GH_TOKEN, GITHUB_TOKEN)
 *     raises the allowance to the account's, so the check to GitHub is
 *     made with it. A token GitHub rejects never breaks the check: the
 *     request is made again without it.
 */
package version

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// releaseToken is the GitHub token in the environment, "" when there is
// none. GH_TOKEN wins, as it does for gh.
func releaseToken() string {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// releaseUserAgent identifies the check to the server that answers it.
const releaseUserAgent = "ChatCLI-Version-Checker"

// rateLimitReset reads when a rate-limited answer says requests are accepted
// again: Retry-After (seconds) for the secondary limits, X-RateLimit-Reset
// (Unix time) for the hourly one. Zero when the answer does not say.
func rateLimitReset(h http.Header, now time.Time) time.Time {
	if secs, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && secs > 0 {
		return now.Add(time.Duration(secs) * time.Second)
	}
	if epoch, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && epoch > 0 {
		return time.Unix(epoch, 0)
	}
	return time.Time{}
}

// rateLimited reports whether a refusal is GitHub's rate limit rather than
// a proxy's 403: the allowance is spent, or the answer asks to come back
// later.
func rateLimited(status int, h http.Header) bool {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false
	}
	return strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0" || h.Get("Retry-After") != ""
}

// statusErrorFrom builds the check's error for an answer other than 200.
func statusErrorFrom(resp *http.Response, authenticated bool) *ReleaseStatusError {
	e := &ReleaseStatusError{StatusCode: resp.StatusCode}
	if rateLimited(resp.StatusCode, resp.Header) {
		e.RateLimited = true
		e.ResetAt = rateLimitReset(resp.Header, time.Now())
		e.Authenticated = authenticated
	}
	return e
}

// authenticatedRelease asks the GitHub API for the latest release with the
// environment's token. It only ever addresses LatestVersionURL: the token
// has one destination, fixed in the binary, and a release check endpoint
// set through CHATCLI_LATEST_VERSION_URL is never offered it.
//
// ok is false when the caller should ask without a token: there is none,
// or GitHub answered 401 for it (expired, revoked, issued by another
// GitHub). The check needs no token, so a bad one must not break it.
func authenticatedRelease(ctx context.Context, client *http.Client) (resp *http.Response, ok bool, err error) {
	token := releaseToken()
	if token == "" {
		return nil, false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LatestVersionURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", releaseUserAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, false, nil
	}
	return resp, true, nil
}
