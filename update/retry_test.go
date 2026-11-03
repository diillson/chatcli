/*
 * ChatCLI - Tests for the update retry policy
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/version"
)

// TestMain zeroes the pause between attempts and pins the default count:
// the package's tests exercise retries without waiting 5 seconds each.
func TestMain(m *testing.M) {
	_ = os.Setenv("CHATCLI_UPDATE_RETRY_DELAY", "0")
	_ = os.Unsetenv("CHATCLI_UPDATE_RETRIES")
	os.Exit(m.Run())
}

// recorder collects the notices Apply emits, retries included.
type recorder struct {
	mu      sync.Mutex
	retries []RetryNotice
	notices []Notice
}

func (r *recorder) Notify(n Notice) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, n)
}

func (r *recorder) NotifyRetry(n RetryNotice) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retries = append(r.retries, n)
}

func stubTools(t *testing.T) {
	t.Helper()
	origRun, origLook, origSelf := runCommandFn, lookPathFn, selfReplaceFn
	t.Cleanup(func() { runCommandFn, lookPathFn, selfReplaceFn = origRun, origLook, origSelf })
	lookPathFn = func(name string) (string, error) { return "/usr/bin/" + name, nil }
}

// A proxy that drops the first two go install attempts: the third works,
// and the user is told about each retry with the attempt count.
func TestApplyGoInstallRetriesTransientFailure(t *testing.T) {
	stubTools(t)
	calls := 0
	runCommandFn = func(_ context.Context, opts Options, _ string, _ ...string) error {
		calls++
		if calls < 3 {
			_, _ = opts.Stderr.Write([]byte("go: github.com/diillson/chatcli@v1.2.3: Get \"https://proxy.golang.org/...\": proxyconnect tcp: connection reset by peer\n"))
			return errors.New("exit status 1")
		}
		return nil
	}
	rec := &recorder{}
	if err := Apply(context.Background(), Info{Method: MethodGoInstall}, "v1.2.3", Options{Notify: rec}); err != nil {
		t.Fatalf("third attempt should succeed: %v", err)
	}
	if calls != 3 {
		t.Fatalf("go install ran %d times, want 3", calls)
	}
	if len(rec.retries) != 2 {
		t.Fatalf("want 2 retry notices, got %+v", rec.retries)
	}
	for i, n := range rec.retries {
		if n.Step != "go-install" || n.Attempt != i+1 || n.Max != 1+DefaultRetryAttempts || n.Err == nil {
			t.Fatalf("notice %d = %+v", i, n)
		}
	}
}

// After 1+DefaultRetryAttempts failures the last error comes back untouched.
func TestApplyBrewGivesUpAfterTheLastAttempt(t *testing.T) {
	stubTools(t)
	calls := 0
	last := errors.New("exit status 1 (attempt 4)")
	runCommandFn = func(context.Context, Options, string, ...string) error {
		calls++
		if calls == 1+DefaultRetryAttempts {
			return last
		}
		return errors.New("exit status 1")
	}
	rec := &recorder{}
	err := Apply(context.Background(), Info{Method: MethodHomebrew}, "v1.2.3", Options{Notify: rec})
	if !errors.Is(err, last) {
		t.Fatalf("want the last attempt's error, got %v", err)
	}
	if calls != 1+DefaultRetryAttempts || len(rec.retries) != DefaultRetryAttempts {
		t.Fatalf("calls=%d retries=%d, want %d and %d", calls, len(rec.retries), 1+DefaultRetryAttempts, DefaultRetryAttempts)
	}
}

// Failures a retry cannot fix stop at once.
func TestApplyDoesNotRetryPermanentFailures(t *testing.T) {
	t.Run("missing tool", func(t *testing.T) {
		stubTools(t)
		lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
		rec := &recorder{}
		err := Apply(context.Background(), Info{Method: MethodHomebrew}, "v1.2.3", Options{Notify: rec})
		var missing *MissingToolError
		if !errors.As(err, &missing) || len(rec.retries) != 0 {
			t.Fatalf("err=%v retries=%d", err, len(rec.retries))
		}
	})
	t.Run("go.mod directive refusal falls back without retrying", func(t *testing.T) {
		stubTools(t)
		calls := 0
		runCommandFn = func(_ context.Context, opts Options, _ string, _ ...string) error {
			calls++
			_, _ = opts.Stderr.Write([]byte("The go.mod file for the module providing named packages contains one or more replace directives. It must not contain directives that would cause it to be interpreted differently than if it were the main module.\n"))
			return errors.New("exit status 1")
		}
		fellBack := false
		selfReplaceFn = func(context.Context, string, string, Options) error { fellBack = true; return nil }
		rec := &recorder{}
		if err := Apply(context.Background(), Info{Method: MethodGoInstall, ExecPath: "/x/chatcli"}, "v1.2.3", Options{Notify: rec}); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || len(rec.retries) != 0 || !fellBack {
			t.Fatalf("calls=%d retries=%d fellBack=%v", calls, len(rec.retries), fellBack)
		}
	})
}

// A Notifier without NotifyRetry (the background auto-update passes none)
// still gets the retries, silently.
func TestRetryWithoutRetryNotifierIsSilent(t *testing.T) {
	calls := 0
	var plain []Notice
	err := Retry(context.Background(), NotifierFunc(func(n Notice) { plain = append(plain, n) }), "check", func() error {
		calls++
		if calls < 2 {
			return errors.New("proxy said 502")
		}
		return nil
	})
	if err != nil || calls != 2 || len(plain) != 0 {
		t.Fatalf("err=%v calls=%d plain=%v", err, calls, plain)
	}
	if err := Retry(context.Background(), nil, "check", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Ctrl+C during the pause ends the update at once instead of waiting it out.
func TestRetryStopsWhenTheContextEndsDuringThePause(t *testing.T) {
	t.Setenv("CHATCLI_UPDATE_RETRY_DELAY", "5m")

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- Retry(ctx, nil, "download", func() error {
			calls++
			return errors.New("connection reset")
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry kept waiting after the context ended")
	}
}

// flakyRelease serves a signed release where each file fails its first
// fails[name] requests: a 502 from the proxy, or a body cut halfway.
func flakyRelease(t *testing.T, tag, asset, binary string, fails map[string]int, cut bool) map[string]int {
	t.Helper()
	_, priv := installTestReleaseKey(t)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(binary), asset)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(checksums)))
	files := map[string]string{"checksums.txt": checksums, "checksums.txt.sig": sig, asset: binary}

	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/"+tag+"/")
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		hits[name]++
		n := hits[name]
		mu.Unlock()
		if n <= fails[name] {
			if cut && name == asset {
				// Half the binary, then the connection drops.
				w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = w.Write([]byte(body[:len(body)/2]))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, _ := hj.Hijack()
					_ = conn.Close()
				}
				return
			}
			http.Error(w, "proxy error", http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	orig := downloadBaseURL
	downloadBaseURL = srv.URL
	t.Cleanup(func() {
		downloadBaseURL = orig
		srv.Close()
	})
	return hits
}

// Each release file fails twice behind the proxy; the binary still lands,
// verified, and the user saw every retry.
func TestSelfReplaceRetriesEveryNetworkStep(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	const binary = "new binary v2 with enough bytes to cut in half"
	hits := flakyRelease(t, "v2.0.0", asset, binary, map[string]int{"checksums.txt": 2, "checksums.txt.sig": 1, asset: 2}, false)

	target := filepath.Join(t.TempDir(), "chatcli")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil { // #nosec G306 -- test binary
		t.Fatal(err)
	}
	rec := &recorder{}
	if err := Apply(context.Background(), Info{Method: MethodReleaseBinary, ExecPath: target}, "v2.0.0", Options{Notify: rec}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != binary {
		t.Fatalf("installed %q, want %q", got, binary)
	}
	steps := map[string]int{}
	for _, n := range rec.retries {
		steps[n.Step]++
	}
	if steps["checksums"] != 2 || steps["signature"] != 1 || steps["download"] != 2 {
		t.Fatalf("retry notices per step = %v", steps)
	}
	if hits[asset] != 3 {
		t.Fatalf("binary fetched %d times, want 3", hits[asset])
	}
}

// A binary cut halfway leaves bytes in the staging file; the next attempt
// starts over, so the checksum matches and no partial binary is installed.
func TestSelfReplaceRestartsAPartialDownload(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	const binary = "complete binary contents, long enough to be cut"
	flakyRelease(t, "v2.0.0", asset, binary, map[string]int{asset: 1}, true)

	target := filepath.Join(t.TempDir(), "chatcli")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil { // #nosec G306 -- test binary
		t.Fatal(err)
	}
	if err := selfReplaceWithOptions(context.Background(), target, "v2.0.0", Options{}); err != nil {
		t.Fatalf("selfReplace: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != binary {
		t.Fatalf("installed %q, want %q", got, binary)
	}
}

// A 404 is the release's answer, not the network's: no retry.
func TestSelfReplaceDoesNotRetryMissingChecksums(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	installTestReleaseKey(t)
	hits := serveRelease(t, "v2.0.0", "", nil, map[string]string{asset: "x"})
	rec := &recorder{}
	err = selfReplaceWithOptions(context.Background(), filepath.Join(t.TempDir(), "chatcli"), "v2.0.0", Options{Notify: rec})
	var unavailable *ChecksumsUnavailableError
	if !errors.As(err, &unavailable) || len(rec.retries) != 0 || *hits != 0 {
		t.Fatalf("err=%v retries=%d binary hits=%d", err, len(rec.retries), *hits)
	}
}

func TestRetryableStatus(t *testing.T) {
	for status, want := range map[int]bool{
		403: true, 408: true, 425: true, 429: true, 500: true, 502: true, 503: true, 504: true,
		400: false, 401: false, 404: false, 410: false,
	} {
		if got := retryableStatus(status); got != want {
			t.Errorf("status %d: retryable=%v, want %v", status, got, want)
		}
	}
}

// Retryable tells "gave up after every attempt" from "stopped at once",
// also after Retry has unwrapped the permanent marker.
func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("connection reset by peer"), true},
		{&statusError{what: "x", status: 502}, true},
		{&statusError{what: "x", status: 404}, false},
		{statusFailure("x", 403), true},
		{statusFailure("x", 401), false},
		{&version.ReleaseStatusError{StatusCode: 403}, true},
		{&version.ReleaseStatusError{StatusCode: 404}, false},
		{&ChecksumsUnavailableError{Tag: "v1"}, false},
		{&SignatureInvalidError{Tag: "v1"}, false},
		{&MissingToolError{Tool: "brew"}, false},
		{context.Canceled, false},
		{nil, false},
	} {
		if got := Retryable(tc.err); got != tc.want {
			t.Errorf("Retryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	// A 404 asset comes back from Retry without the marker and is still
	// not retryable.
	err := Retry(context.Background(), nil, "download", func() error { return statusFailure("asset", 404) })
	if Retryable(err) {
		t.Fatalf("unwrapped 404 reported retryable: %v", err)
	}
}

// CHATCLI_UPDATE_RETRIES and CHATCLI_UPDATE_RETRY_DELAY steer the policy;
// a value that does not parse or is out of range keeps the default.
func TestRetryPolicyFromEnv(t *testing.T) {
	for _, tc := range []struct {
		retries, delay string
		wantN          int
		wantD          time.Duration
	}{
		{"", "", DefaultRetryAttempts, DefaultRetryDelay},
		{"5", "10s", 5, 10 * time.Second},
		{"0", "", 0, DefaultRetryDelay},
		{"", "7", DefaultRetryAttempts, 7 * time.Second},
		{"", "1m30s", DefaultRetryAttempts, 90 * time.Second},
		{"", "0", DefaultRetryAttempts, 0},
		{"11", "6m", DefaultRetryAttempts, DefaultRetryDelay},
		{"-1", "-5s", DefaultRetryAttempts, DefaultRetryDelay},
		{"three", "soon", DefaultRetryAttempts, DefaultRetryDelay},
		{" 4 ", " 2s ", 4, 2 * time.Second},
	} {
		t.Setenv("CHATCLI_UPDATE_RETRIES", tc.retries)
		t.Setenv("CHATCLI_UPDATE_RETRY_DELAY", tc.delay)
		n, d := RetryPolicy()
		if n != tc.wantN || d != tc.wantD {
			t.Errorf("retries=%q delay=%q: got %d/%s, want %d/%s", tc.retries, tc.delay, n, d, tc.wantN, tc.wantD)
		}
	}
}

// The configured count is what Retry runs: 5 retries means 6 attempts, 0
// means a single attempt; the notices carry the configured delay.
func TestRetryFollowsTheConfiguredPolicy(t *testing.T) {
	for _, tc := range []struct {
		retries   string
		wantCalls int
	}{{"5", 6}, {"0", 1}, {"1", 2}} {
		t.Setenv("CHATCLI_UPDATE_RETRIES", tc.retries)
		t.Setenv("CHATCLI_UPDATE_RETRY_DELAY", "0")
		calls := 0
		rec := &recorder{}
		_ = Retry(context.Background(), rec, "download", func() error { calls++; return errors.New("reset") })
		if calls != tc.wantCalls || len(rec.retries) != tc.wantCalls-1 {
			t.Fatalf("retries=%s: calls=%d notices=%d, want %d and %d", tc.retries, calls, len(rec.retries), tc.wantCalls, tc.wantCalls-1)
		}
		if len(rec.retries) > 0 && rec.retries[0].Max != tc.wantCalls {
			t.Fatalf("notice Max=%d, want %d", rec.retries[0].Max, tc.wantCalls)
		}
	}
}

// An intermittent 403 from a corporate proxy is retried and recovers.
func TestSelfReplaceRetriesAnIntermittent403(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	const binary = "binary behind a proxy that blocks now and then"
	_, priv := installTestReleaseKey(t)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(binary), asset)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(checksums)))
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/v2.0.0/")
		if name == asset {
			mu.Lock()
			hits++
			n := hits
			mu.Unlock()
			if n == 1 {
				http.Error(w, "blocked by policy", http.StatusForbidden)
				return
			}
			_, _ = fmt.Fprint(w, binary)
			return
		}
		switch name {
		case "checksums.txt":
			_, _ = fmt.Fprint(w, checksums)
		case "checksums.txt.sig":
			_, _ = fmt.Fprint(w, sig)
		default:
			http.NotFound(w, r)
		}
	}))
	orig := downloadBaseURL
	downloadBaseURL = srv.URL
	t.Cleanup(func() { downloadBaseURL = orig; srv.Close() })

	target := filepath.Join(t.TempDir(), "chatcli")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil { // #nosec G306 -- test binary
		t.Fatal(err)
	}
	rec := &recorder{}
	if err := selfReplaceWithOptions(context.Background(), target, "v2.0.0", Options{Notify: rec}); err != nil {
		t.Fatalf("selfReplace: %v", err)
	}
	if len(rec.retries) != 1 || !strings.Contains(rec.retries[0].Err.Error(), "403") {
		t.Fatalf("the user must see the real 403 in the retry notice: %+v", rec.retries)
	}
}
