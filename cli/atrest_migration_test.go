/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

// writeOld writes a plaintext store file dated an hour back, the way an
// installation that ran before encryption was on left it.
func writeOld(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// useDefaultKey stands in for the keychain-derived key ChatCLI installs at
// startup (main_atrest.go): no CHATCLI_ENCRYPTION_KEY, a provider instead.
func useDefaultKey(t *testing.T, secret string) {
	t.Helper()
	t.Setenv(atrest.EnvKey, "")
	atrest.SetKeyProvider(func() (string, string, error) { return secret, "keychain", nil })
	t.Cleanup(func() { atrest.SetKeyProvider(nil) })
}

func waitMigration(t *testing.T, cli *ChatCLI) resealReport {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if running, done, rep, _ := cli.atRestMigrationState.snapshot(); done && !running {
			return rep
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the migration did not finish")
	return resealReport{}
}

func isSealedFile(t *testing.T, path string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return atrest.IsEncrypted(raw)
}

// An installation upgraded from a plaintext version is sealed once, in the
// background, and everything keeps reading.
func TestAtRestMigration_SealsAnUpgradedInstallation(t *testing.T) {
	root := t.TempDir()
	parkDir := filepath.Join(root, "parked")
	park.SetBaseDir(parkDir)
	t.Cleanup(func() { park.SetBaseDir("") })
	t.Setenv("HISTORY_FILE", filepath.Join(root, "history"))
	t.Setenv(atrest.EnvKey, "")

	session := filepath.Join(root, "sessions", "old.json")
	memoryIdx := filepath.Join(root, "memory", "memory_index.json")
	daily := filepath.Join(root, "memory", "202609", "20260901.md")
	journal := filepath.Join(root, "transcripts", "tr-old.jsonl")
	budget := filepath.Join(root, "costs", dailySpendFile)
	recent := filepath.Join(root, "costs", "live.json")
	orphan := filepath.Join(root, "costs", "x.json.123456.tmp")
	writeOld(t, session, `{"name":"old"}`)
	writeOld(t, memoryIdx, `{"facts":[]}`)
	writeOld(t, daily, "## 10:00\n\nnote\n")
	writeOld(t, journal, "{\"kind\":\"msg\",\"message\":{\"role\":\"user\",\"content\":\"hi\"}}\n")
	writeOld(t, budget, `{"date":"2026-09-01","spent_usd":1.5}`)
	writeOld(t, orphan, "garbage")
	writeOld(t, filepath.Join(root, "history"), "first prompt\nsecond prompt\n")
	if err := os.WriteFile(recent, []byte(`{"live":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	snap := &park.Snapshot{Token: "parktoken01", History: []models.Message{{Role: "user", Content: "parked"}}}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(parkDir, "parktoken01.json"), old, old)
	useDefaultKey(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root, historyManager: NewHistoryManager(zap.NewNop())}
	cli.startAtRestMigration()
	rep := waitMigration(t, cli)

	for _, p := range []string{session, memoryIdx, daily, filepath.Join(parkDir, "parktoken01.json")} {
		if !isSealedFile(t, p) {
			t.Errorf("%s was not sealed", p)
		}
	}
	if info, err := os.Stat(session); err != nil || time.Since(info.ModTime()) < 30*time.Minute {
		t.Error("sealing changed the session's modification time")
	}
	if tr := readFileString(t, journal); !strings.HasPrefix(tr, sealedLinePrefix) {
		t.Error("the journal was not sealed")
	}
	if msgs, err := readTranscript(journal); err != nil || len(msgs) != 1 {
		t.Errorf("the sealed journal does not read: %v %v", msgs, err)
	}
	if h := readFileString(t, filepath.Join(root, "history")); strings.Contains(h, "first prompt") {
		t.Error("the prompt history was not sealed")
	}
	if hist, err := cli.historyManager.LoadHistory(); err != nil || len(hist) != 2 || hist[1] != "second prompt" {
		t.Errorf("the sealed history does not load: %v %v", hist, err)
	}
	if got, err := park.Load("parktoken01"); err != nil || got.History[0].Content != "parked" {
		t.Errorf("the sealed park snapshot does not load: %v", err)
	}
	if isSealedFile(t, budget) {
		t.Error("the daily budget was sealed: it must stay readable before any key")
	}
	if isSealedFile(t, recent) {
		t.Error("a file modified moments ago was rewritten under a possible live writer")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("the stale atomic-write temp was not removed")
	}
	if rep.Deferred != 1 || len(rep.Errors) != 0 {
		t.Errorf("report = %+v", rep)
	}
	if cli.atRestSealedWithCurrentKey() {
		t.Error("the marker was written while a file was still deferred")
	}

	// The deferred file ages; the next start finishes and records the key.
	_ = os.Chtimes(recent, old, old)
	cli2 := &ChatCLI{logger: zap.NewNop(), stateRoot: root, historyManager: NewHistoryManager(zap.NewNop())}
	cli2.startAtRestMigration()
	rep = waitMigration(t, cli2)
	if !isSealedFile(t, recent) || rep.Deferred != 0 || !cli2.atRestSealedWithCurrentKey() {
		t.Fatalf("second pass did not finish: %+v", rep)
	}

	// Once the marker names the current key, a start does no work.
	cli3 := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	cli3.startAtRestMigration()
	if running, done, _, _ := cli3.atRestMigrationState.snapshot(); running || done {
		t.Error("a sealed installation was walked again")
	}
}

// The migration pass never decrypts or rewrites a sealed file.
func TestAtRestMigration_SkipsSealedFilesWithoutRewriting(t *testing.T) {
	root := t.TempDir()
	park.SetBaseDir(filepath.Join(root, "parked"))
	t.Cleanup(func() { park.SetBaseDir("") })
	useDefaultKey(t, "aaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccddddaaaabbbbccccdddd")
	path := filepath.Join(root, "sessions", "s.json")
	sealed, err := atrest.SealAt(path, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	writeOld(t, path, string(sealed))
	before, _ := os.Stat(path)

	rep := resealStores([]atRestStore{{filepath.Join(root, "sessions"), storeSessions}}, "", time.Minute, true)
	after, _ := os.Stat(path)
	if rep.Rewritten != 0 || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("a sealed file was rewritten: %+v", rep)
	}
}

// With encryption off nothing runs.
func TestAtRestMigration_OffDoesNothing(t *testing.T) {
	root := t.TempDir()
	t.Setenv(atrest.EnvKey, "off")
	writeOld(t, filepath.Join(root, "sessions", "s.json"), `{"a":1}`)
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	cli.startAtRestMigration()
	if running, done, _, _ := cli.atRestMigrationState.snapshot(); running || done {
		t.Fatal("the migration ran with encryption off")
	}
	if isSealedFile(t, filepath.Join(root, "sessions", "s.json")) {
		t.Fatal("a store was sealed with encryption off")
	}
}

// Turning encryption off and resealing writes every store back in
// plaintext: the way back to a version that cannot read sealed stores.
func TestAtRestReseal_OffWritesPlaintextBack(t *testing.T) {
	root := t.TempDir()
	park.SetBaseDir(filepath.Join(root, "parked"))
	t.Cleanup(func() { park.SetBaseDir("") })
	histPath := filepath.Join(root, "history")
	t.Setenv("HISTORY_FILE", histPath)
	key := "unseal-key-unseal-key-unseal-key-00"
	t.Setenv(atrest.EnvKey, key)
	session := filepath.Join(root, "sessions", "s.json")
	journal := filepath.Join(root, "transcripts", "t.jsonl")
	writeOld(t, session, `{"a":1}`)
	writeOld(t, journal, "{\"kind\":\"msg\"}\n")
	writeOld(t, histPath, "a prompt\n")
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root, historyManager: NewHistoryManager(zap.NewNop())}
	if rep := cli.resealAtRestStores(0); len(rep.Errors) != 0 || !isSealedFile(t, session) {
		t.Fatalf("seal pass: %+v", rep)
	}

	// "off" still opens with a provider; here the variable held the key,
	// so the retired key goes in the previous list.
	t.Setenv(atrest.EnvKey, "off")
	t.Setenv(atrest.EnvPreviousKeys, key)
	atrest.SetKeyProvider(func() (string, string, error) { return key, "keychain", nil })
	t.Cleanup(func() { atrest.SetKeyProvider(nil) })
	cli.configSecurityReseal()
	if isSealedFile(t, session) || readFileString(t, session) != `{"a":1}` {
		t.Fatal("the session was not written back in plaintext")
	}
	if strings.HasPrefix(readFileString(t, journal), sealedLinePrefix) {
		t.Fatal("the journal was not written back in plaintext")
	}
	if readFileString(t, histPath) != "a prompt\n" {
		t.Fatalf("history = %q", readFileString(t, histPath))
	}
}

// A key from CHATCLI_ENCRYPTION_KEY (a server, the operator) never starts
// the background rewrite: the server's stores change only on request.
func TestAtRestMigration_EnvKeyLeavesStoresAlone(t *testing.T) {
	root := t.TempDir()
	t.Setenv(atrest.EnvKey, "server-key-server-key-server-key-00")
	writeOld(t, filepath.Join(root, "sessions", "s.json"), `{"a":1}`)
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	cli.startAtRestMigration()
	if running, done, _, _ := cli.atRestMigrationState.snapshot(); running || done {
		t.Fatal("the migration ran under an env key")
	}
	if isSealedFile(t, filepath.Join(root, "sessions", "s.json")) {
		t.Fatal("a store was rewritten under an env key")
	}
}

// The walk seals only what each store's reader opens sealed: an export the
// user asked for, a repository kept inside memory, a foreign file in a
// store directory are never turned into ciphertext.
func TestAtRestReseal_SealsOnlyKnownStoreFiles(t *testing.T) {
	root := t.TempDir()
	park.SetBaseDir(filepath.Join(root, "parked"))
	t.Cleanup(func() { park.SetBaseDir("") })
	useDefaultKey(t, "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe")
	keep := map[string]string{
		"costs/s1-export.json":        `{"export":true}`,
		"costs/" + dailySpendFile:     `{"spent_usd":1}`,
		"memory/.git/objects/ab/cdef": "blob",
		"memory/notes.txt":            "foreign",
		"sessions/readme.txt":         "foreign",
		"transcripts/other.log":       "foreign",
	}
	seal := map[string]string{
		"costs/s1.json":                `{"a":1}`,
		"memory/MEMORY.md":             "# memory",
		"memory/202609/20260901.md":    "note",
		"memory/pending/seg-1.json":    `{"p":1}`,
		"attachments/20260901-a-b.png": "png",
	}
	for rel, c := range keep {
		writeOld(t, filepath.Join(root, rel), c)
	}
	for rel, c := range seal {
		writeOld(t, filepath.Join(root, rel), c)
	}
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	if rep := cli.resealAtRestStores(0); len(rep.Errors) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for rel := range keep {
		if isSealedFile(t, filepath.Join(root, rel)) {
			t.Errorf("%s was sealed", rel)
		}
	}
	for rel := range seal {
		if !isSealedFile(t, filepath.Join(root, rel)) {
			t.Errorf("%s was not sealed", rel)
		}
	}
}

// From a key of your own back to the keychain: use-keychain reseals every
// store with the default key, after which the variable is not needed.
func TestAtRestUseKeychain_MovesStoresOffTheVariable(t *testing.T) {
	root := t.TempDir()
	park.SetBaseDir(filepath.Join(root, "parked"))
	t.Cleanup(func() { park.SetBaseDir("") })
	own := "my-own-key-my-own-key-my-own-key-00"
	t.Setenv(atrest.EnvKey, own)
	t.Setenv(atrest.EnvPreviousKeys, "")
	session := filepath.Join(root, "sessions", "s.json")
	sealed, err := atrest.SealAt(session, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	writeOld(t, session, string(sealed))
	atrest.SetKeyProvider(func() (string, string, error) {
		return "abababababababababababababababababababababababababababababababab", "keychain", nil
	})
	t.Cleanup(func() { atrest.SetKeyProvider(nil) })

	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	cli.configSecurityUseKeychain()

	// The variable is gone for this process; drop the retired key too and
	// the store must open with the keychain key alone.
	t.Setenv(atrest.EnvKey, "")
	t.Setenv(atrest.EnvPreviousKeys, "")
	raw, _ := os.ReadFile(session)
	if plain, err := atrest.OpenAt(session, raw); err != nil || string(plain) != `{"a":1}` {
		t.Fatalf("the store does not open with the keychain key alone: %v", err)
	}
	if !cli.atRestSealedWithCurrentKey() {
		t.Error("the marker does not name the keychain key")
	}
}

// A new machine: the old keychain key is gone, a new one is in place, and
// the recovery key from the old machine moves every store onto it.
func TestAtRestRestoreRecoveryKey_MovesStoresToThisMachine(t *testing.T) {
	root := t.TempDir()
	park.SetBaseDir(filepath.Join(root, "parked"))
	t.Cleanup(func() { park.SetBaseDir("") })
	t.Setenv(atrest.EnvPreviousKeys, "")
	oldMachine := "1212121212121212121212121212121212121212121212121212121212121212"
	useDefaultKey(t, oldMachine)
	session := filepath.Join(root, "sessions", "s.json")
	sealed, err := atrest.SealAt(session, []byte(`{"old":true}`))
	if err != nil {
		t.Fatal(err)
	}
	writeOld(t, session, string(sealed))
	recovery, err := atrest.RecoveryKey()
	if err != nil {
		t.Fatal(err)
	}
	file, err := atrest.SealRecoveryKey(recovery, "a passphrase long enough", atrest.KeyFingerprint())
	if err != nil {
		t.Fatal(err)
	}

	newMachine := "3434343434343434343434343434343434343434343434343434343434343434"
	atrest.SetKeyProvider(func() (string, string, error) { return newMachine, "keychain", nil })
	key, err := atrest.OpenRecoveryKey(file, "a passphrase long enough")
	if err != nil {
		t.Fatal(err)
	}
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	cli.restoreWithRecoveryKey(key)

	t.Setenv(atrest.EnvKey, "")
	t.Setenv(atrest.EnvPreviousKeys, "")
	raw, _ := os.ReadFile(session)
	if plain, err := atrest.OpenAt(session, raw); err != nil || string(plain) != `{"old":true}` {
		t.Fatalf("the store does not open with this machine's key: %v", err)
	}
}

// The /config security recovery-key --save and --restore flows end to end,
// with the passphrase prompts answered by the test.
func TestRecoveryKeyFile_SaveAndRestoreCommands(t *testing.T) {
	root := t.TempDir()
	park.SetBaseDir(filepath.Join(root, "parked"))
	t.Cleanup(func() { park.SetBaseDir("") })
	t.Setenv(atrest.EnvPreviousKeys, "")
	useDefaultKey(t, "5656565656565656565656565656565656565656565656565656565656565656")
	session := filepath.Join(root, "sessions", "s.json")
	sealed, err := atrest.SealAt(session, []byte(`{"kept":true}`))
	if err != nil {
		t.Fatal(err)
	}
	writeOld(t, session, string(sealed))

	origInteractive, origRead := stdinIsInteractive, readPassphrase
	t.Cleanup(func() { stdinIsInteractive, readPassphrase = origInteractive, origRead })
	stdinIsInteractive = func() bool { return true }
	answers := []string{"a passphrase long enough", "a passphrase long enough"}
	readPassphrase = func(string) (string, error) {
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	cli := &ChatCLI{logger: zap.NewNop(), stateRoot: root}
	file := filepath.Join(t.TempDir(), "recovery.json")
	cli.routeRecoveryKey([]string{"--save", file})
	if info, err := os.Stat(file); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("recovery file not written privately: %v", err)
	}
	before, _ := os.ReadFile(file)
	answers = []string{"a passphrase long enough", "a passphrase long enough"}
	cli.routeRecoveryKey([]string{"--save", file})
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("an existing recovery file was overwritten")
	}

	// Mismatched passphrases write nothing.
	other := filepath.Join(t.TempDir(), "other.json")
	answers = []string{"a passphrase long enough", "a different passphrase!"}
	cli.routeRecoveryKey([]string{"--save", other})
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("a recovery file was written with mismatched passphrases")
	}

	// New machine: a different keychain key; restore moves the store onto it.
	atrest.SetKeyProvider(func() (string, string, error) {
		return "7878787878787878787878787878787878787878787878787878787878787878", "keychain", nil
	})
	answers = []string{"wrong passphrase here"}
	cli.routeRecoveryKey([]string{"--restore", file})
	answers = []string{"a passphrase long enough"}
	cli.routeRecoveryKey([]string{"--restore", file})
	t.Setenv(atrest.EnvKey, "")
	t.Setenv(atrest.EnvPreviousKeys, "")
	raw, _ := os.ReadFile(session)
	if plain, err := atrest.OpenAt(session, raw); err != nil || string(plain) != `{"kept":true}` {
		t.Fatalf("restore did not move the store onto this machine's key: %v", err)
	}

	cli.routeRecoveryKey([]string{"--bogus"})
	stdinIsInteractive = func() bool { return false }
	cli.routeRecoveryKey([]string{"--save", filepath.Join(t.TempDir(), "x.json")})
	cli.routeRecoveryKey([]string{"--restore", filepath.Join(t.TempDir(), "missing.json")})
}
