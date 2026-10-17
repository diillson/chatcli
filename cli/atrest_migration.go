/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * One-time migration of the local stores to encryption at rest: an
 * installation that ran before encryption was on by default holds every
 * session, transcript, memory and context in plaintext; this seals them.
 */

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"golang.org/x/term"
)

const (
	// atRestMarkerName records, under the state root, the fingerprint of
	// the key the stores were last fully sealed with.
	atRestMarkerName = ".atrest-sealed"
	// atRestSkipRecent defers files modified this recently: a live writer
	// (another ChatCLI window) may still be on them, and it seals them
	// itself on its next save.
	atRestSkipRecent = 10 * time.Minute
)

var atRestUnavailableOnce sync.Once

// warnAtRestUnavailable tells the user, once per process, that the
// encryption key could not be read: new writes go to disk in plaintext and
// sealed stores do not open until it can.
func warnAtRestUnavailable(err error) {
	atRestUnavailableOnce.Do(func() {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		fmt.Fprintln(os.Stderr, colorize("⚠ "+i18n.T("atrest.unavailable.warning", reason), ColorYellow))
	})
}

// atRestMigration is the outcome of the last background migration.
type atRestMigration struct {
	mu      sync.Mutex
	running bool
	done    bool
	report  resealReport
	at      time.Time
}

// snapshot returns a copy of the migration state.
func (m *atRestMigration) snapshot() (running, done bool, rep resealReport, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running, m.done, m.report, m.at
}

// atRestMarkerPath is the marker file for this state root.
func (cli *ChatCLI) atRestMarkerPath() string {
	return filepath.Join(cli.atRestRoot(), atRestMarkerName)
}

// atRestSealedWithCurrentKey reports whether the stores were already fully
// sealed with the current key.
func (cli *ChatCLI) atRestSealedWithCurrentKey() bool {
	fp := atrest.KeyFingerprint()
	if fp == "" {
		return false
	}
	data, err := os.ReadFile(cli.atRestMarkerPath()) // #nosec G304 -- fixed marker name under the state root
	return err == nil && strings.TrimSpace(string(data)) == fp
}

// startAtRestMigration seals, in the background, every store still in
// plaintext (or under a retired key). It runs when encryption at rest is on
// and the marker does not name the current key, so an upgraded installation
// is migrated once and a rotated key is picked up. Each file is replaced
// atomically, so an interrupted run loses nothing and the next start
// resumes; the marker is written only after a pass with nothing deferred
// and no error.
//
// Only the default key runs it. A key from CHATCLI_ENCRYPTION_KEY (a
// server, the operator, a user who opted in before) keeps the behavior it
// always had: writers seal what they save, and /config security reseal
// seals the rest on request. A server's stores are never rewritten behind
// the operator's back.
func (cli *ChatCLI) startAtRestMigration() {
	src, _, err := atrest.KeyStatus()
	if src == atrest.KeySourceUnavailable {
		warnAtRestUnavailable(err)
		return
	}
	if src != atrest.KeySourceProvider || cli.atRestSealedWithCurrentKey() {
		return
	}
	m := &cli.atRestMigrationState
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	dirs, history, marker := cli.atRestStoreDirs(), cli.historyPath(), cli.atRestMarkerPath()
	go func() {
		rep := resealStores(dirs, history, atRestSkipRecent, true)
		// The marker goes first: once the state reads done, a status check
		// must already see it.
		if rep.Deferred == 0 && len(rep.Errors) == 0 {
			_ = utils.AtomicWriteFile(marker, []byte(atrest.KeyFingerprint()+"\n"), 0o600)
		}
		m.mu.Lock()
		m.running, m.done, m.report, m.at = false, true, rep, time.Now()
		m.mu.Unlock()
		if cli.logger != nil {
			cli.logger.Info("encryption at rest: local stores sealed",
				zap.Int("files", rep.Files), zap.Int("rewritten", rep.Rewritten),
				zap.Int("lines", rep.Lines), zap.Int("deferred", rep.Deferred), zap.Int("errors", len(rep.Errors)))
			for _, e := range rep.Errors {
				cli.logger.Warn("encryption at rest: store not sealed", zap.String("detail", e))
			}
		}
	}()
}

// recoveryHintMarker records, under the state root, that the recovery-key
// notice was shown, so it appears once per installation.
const recoveryHintMarker = ".atrest-recovery-hinted"

// printRecoveryKeyNotice tells the user, once, that their data is now
// encrypted with a key only the OS keychain holds, and how to keep a copy:
// a reinstalled system or a new machine would otherwise leave every sealed
// store unreadable. Interactive REPL only, and only for the default key
// (a key from CHATCLI_ENCRYPTION_KEY is already one the user holds).
func (cli *ChatCLI) printRecoveryKeyNotice() {
	if src, _, _ := atrest.KeyStatus(); src != atrest.KeySourceProvider {
		return
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) { //#nosec G115 -- a file descriptor fits in int
		return
	}
	marker := filepath.Join(cli.atRestRoot(), recoveryHintMarker)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	fmt.Println(colorize("  🔒 "+i18n.T("atrest.recovery.notice"), ColorCyan))
	fmt.Println(colorize("     "+i18n.T("atrest.recovery.notice_hint"), ColorGray))
	fmt.Println()
	_ = utils.AtomicWriteFile(marker, []byte("shown\n"), 0o600)
}
