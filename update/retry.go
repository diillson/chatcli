/*
 * ChatCLI - Retry with backoff for every network step of the update.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Corporate networks put proxies and TLS inspection between the user and
 * GitHub, and those fail transiently: a reset connection, a 502 from the
 * proxy, a handshake that times out. The update retries each network step
 * (release check, checksums, signature, binary, brew, go install) a fixed
 * number of times with a fixed pause, telling the user each time, and gives
 * up early on failures a retry cannot fix (404, bad signature, checksum
 * mismatch, missing tool, Ctrl+C).
 */
package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/version"
)

const (
	// DefaultRetryAttempts is how many times a failed network step is tried
	// again after the first attempt; CHATCLI_UPDATE_RETRIES overrides it.
	DefaultRetryAttempts = 3
	// DefaultRetryDelay is the pause before each new attempt;
	// CHATCLI_UPDATE_RETRY_DELAY overrides it.
	DefaultRetryDelay = 5 * time.Second
	// MaxRetryAttempts and MaxRetryDelay bound the overrides, so a typo
	// cannot turn /update into a wait of hours.
	MaxRetryAttempts = 10
	MaxRetryDelay    = 5 * time.Minute
)

// RetryPolicy returns the retries in effect: CHATCLI_UPDATE_RETRIES (0 to
// MaxRetryAttempts; 0 turns retrying off) and CHATCLI_UPDATE_RETRY_DELAY
// (a Go duration such as "10s", or whole seconds; 0 to MaxRetryDelay). A
// value that does not parse or is out of range falls back to the default.
func RetryPolicy() (attempts int, delay time.Duration) {
	attempts, delay = DefaultRetryAttempts, DefaultRetryDelay
	if raw := strings.TrimSpace(os.Getenv("CHATCLI_UPDATE_RETRIES")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= MaxRetryAttempts {
			attempts = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("CHATCLI_UPDATE_RETRY_DELAY")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			if secs, serr := strconv.Atoi(raw); serr == nil {
				d, err = time.Duration(secs)*time.Second, nil
			}
		}
		if err == nil && d >= 0 && d <= MaxRetryDelay {
			delay = d
		}
	}
	return attempts, delay
}

// RetryNotice describes one failed attempt that is about to be retried.
type RetryNotice struct {
	// Step names what failed: "check", "checksums", "signature",
	// "download", "brew" or "go-install".
	Step string
	// Attempt is the attempt that failed (1-based); Max is the total number
	// of attempts, the first one included.
	Attempt, Max int
	// Delay is the pause before the next attempt.
	Delay time.Duration
	// Err is why the attempt failed.
	Err error
}

// RetryNotifier is implemented by a Notifier that also wants to hear about
// retries. Options.Notify is type-asserted to it, so Options, Notifier and
// NotifierFunc keep their shape; a Notifier without it retries silently,
// like the background auto-update.
type RetryNotifier interface {
	NotifyRetry(RetryNotice)
}

// permanentError marks a failure a new attempt cannot fix.
type permanentError struct{ err error }

func (p *permanentError) Error() string { return p.err.Error() }
func (p *permanentError) Unwrap() error { return p.err }

// permanent wraps err so retry stops at once and returns err itself.
func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// statusError is an HTTP answer other than 200 on a release asset.
type statusError struct {
	what   string
	status int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("update: fetching %s: status %d", e.what, e.status)
}

// retryableStatus reports whether a new attempt can get a different answer:
// timeouts, rate limits, the 5xx a proxy or GitHub returns while busy, and
// 403, which corporate proxies return intermittently while blocking. A 404,
// 410, 401 or 400 is the release's own answer and does not change.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

// statusFailure turns a non-200 answer into the error retry acts on.
func statusFailure(what string, status int) error {
	err := &statusError{what: what, status: status}
	if retryableStatus(status) {
		return err
	}
	return permanent(err)
}

// isPermanent reports whether err must not be retried: marked permanent,
// one of the typed failures of this package, or a cancelled context.
func isPermanent(err error) bool {
	var p *permanentError
	var missing *MissingToolError
	var platform *UnsupportedPlatformError
	var notWritable *NotWritableError
	var noChecksums *ChecksumsUnavailableError
	var mismatch *ChecksumMismatchError
	var badTag *InvalidTagError
	var noSig *SignatureUnavailableError
	var badSig *SignatureInvalidError
	if code, ok := HTTPStatus(err); ok && !retryableStatus(code) {
		return true
	}
	return errors.As(err, &p) || errors.As(err, &missing) || errors.As(err, &platform) ||
		errors.As(err, &notWritable) || errors.As(err, &noChecksums) || errors.As(err, &mismatch) ||
		errors.As(err, &badTag) || errors.As(err, &noSig) || errors.As(err, &badSig) ||
		errors.Is(err, context.Canceled) || errors.Is(err, ErrManualMethod)
}

// unwrapPermanent returns the error a permanent marker carries.
func unwrapPermanent(err error) error {
	var p *permanentError
	if errors.As(err, &p) {
		return p.err
	}
	return err
}

// Retry runs fn until it succeeds, fails permanently, the context ends, or
// every attempt RetryPolicy allows has failed; before each new attempt it
// tells notify (when it implements RetryNotifier) and waits the policy's
// delay. Apply
// uses it for its own steps; callers use it for the network calls they make
// around Apply, such as the release check. The error returned is the last
// attempt's.
func Retry(ctx context.Context, notify Notifier, step string, fn func() error) error {
	retries, delay := RetryPolicy()
	maxAttempts := 1 + retries
	var err error
	for attempt := 1; ; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if isPermanent(err) || attempt >= maxAttempts || ctx.Err() != nil {
			return unwrapPermanent(err)
		}
		if rn, ok := notify.(RetryNotifier); ok && rn != nil {
			rn.NotifyRetry(RetryNotice{Step: step, Attempt: attempt, Max: maxAttempts, Delay: delay, Err: err})
		}
		if delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return err
			case <-t.C:
			}
		}
	}
}

// Retryable reports whether err is a failure the retries apply to, so a
// caller can tell "gave up after every attempt" from "stopped at once".
func Retryable(err error) bool {
	return err != nil && !isPermanent(err)
}

// HTTPStatus returns the HTTP status behind err when the update failed on
// one: a release asset (checksums, signature, binary) or the release check.
func HTTPStatus(err error) (int, bool) {
	var asset *statusError
	if errors.As(err, &asset) {
		return asset.status, true
	}
	var check *version.ReleaseStatusError
	if errors.As(err, &check) {
		return check.StatusCode, true
	}
	return 0, false
}
