/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config security reseal and verify-audit: key rotation for encryption at
 * rest and integrity check of the hash-chained audit trail.
 */
package cli

import (
	"errors"
	"fmt"
	"github.com/diillson/chatcli/pkg/auditchain"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
	"golang.org/x/term"
)

// resealReport counts one reseal walk.
type resealReport struct {
	Files, Rewritten, Lines int
	// Deferred counts files left for a later pass: modified too recently
	// (a live writer may still be on them) or replaced while being resealed.
	Deferred int
	Errors   []string
}

// atRestStore is one store directory and the kind of store it holds,
// which decides the files the reseal walk may rewrite there.
type atRestStore struct {
	Dir, Kind string
}

// Store kinds (the default directory names under the state root).
const (
	storeSessions    = "sessions"
	storeTranscripts = "transcripts"
	storeMemory      = "memory"
	storeContexts    = "contexts"
	storeCCR         = "ccr"
	storeCosts       = "costs"
	storeAttachments = attachmentsDirName
	storeParked      = "parked"
)

// atRestStoreDirs lists the store directories the reseal walk covers,
// under the active state root (tenant root under the gateway).
func (cli *ChatCLI) atRestStoreDirs() []atRestStore {
	root := cli.atRestRoot()
	sessions := filepath.Join(root, storeSessions)
	if cli.sessionManager != nil && cli.sessionManager.sessionsDir != "" {
		sessions = cli.sessionManager.sessionsDir
	}
	contexts := filepath.Join(root, storeContexts)
	if cli.contextHandler != nil {
		if mgr := cli.contextHandler.GetManager(); mgr != nil && mgr.Storage != nil {
			contexts = mgr.Storage.GetStoragePath()
		}
	}
	stores := []atRestStore{
		{sessions, storeSessions},
		{filepath.Join(root, storeTranscripts), storeTranscripts},
		{filepath.Join(root, storeMemory), storeMemory},
		{contexts, storeContexts},
		{filepath.Join(root, storeCCR), storeCCR},
		{filepath.Join(root, storeCosts), storeCosts},
		{filepath.Join(root, storeAttachments), storeAttachments},
	}
	if dir, err := park.Dir(); err == nil {
		stores = append(stores, atRestStore{dir, storeParked})
	}
	// Tenant roots the gateway created under this root: the same stores per
	// principal. Each file's binding names its tenant (atrest.SealAt).
	if entries, err := os.ReadDir(filepath.Join(root, "tenants")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			tr := filepath.Join(root, "tenants", e.Name())
			for _, kind := range []string{storeSessions, storeTranscripts, storeMemory, storeContexts, storeCCR, storeCosts, storeAttachments, storeParked} {
				stores = append(stores, atRestStore{filepath.Join(tr, kind), kind})
			}
		}
	}
	return stores
}

// sealableIn reports whether a file of this store is one its own reader
// opens sealed — an allowlist per store, so a file someone else keeps there
// (an export, a note, a repository) is never turned into ciphertext. The
// daily budget stays plaintext: it is read before any key is needed, and a
// budget that could not be read would reset to zero.
func sealableIn(kind, name string) bool {
	if strings.HasPrefix(name, ".") || resealSkipped(name) {
		return false
	}
	ext := filepath.Ext(name)
	switch kind {
	case storeSessions, storeContexts, storeParked:
		return ext == ".json"
	case storeTranscripts:
		return ext == ".jsonl"
	case storeMemory:
		return ext == ".json" || ext == ".md" || strings.HasPrefix(name, "MEMORY.md.bak")
	case storeCCR:
		return ext == ".ccr"
	case storeCosts:
		return ext == ".json" && name != dailySpendFile && !strings.HasSuffix(name, "-export.json")
	case storeAttachments:
		return true
	}
	return false
}

// walkStore visits the regular files of one store, never descending into
// hidden directories.
func walkStore(st atRestStore, visit func(path string, d os.DirEntry)) {
	_ = filepath.WalkDir(st.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != st.Dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			visit(path, d)
		}
		return nil
	})
}

// atRestRoot is the state root the stores live under.
func (cli *ChatCLI) atRestRoot() string {
	if cli.stateRoot != "" {
		return cli.stateRoot
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".chatcli")
}

// resealSkipped reports files the walk never rewrites in any store:
// temporaries, quarantined copies, locks, OS litter and the daily budget.
func resealSkipped(name string) bool {
	switch {
	case strings.HasSuffix(name, ".tmp"), strings.HasSuffix(name, ".corrupt"), strings.Contains(name, ".corrupt-"),
		strings.HasSuffix(name, ".lock"), name == ".DS_Store", name == dailySpendFile:
		return true
	}
	return false
}

// resealAtRestStores rewrites every store file under the state root with
// the current key: plaintext files become sealed, files sealed with a
// retired key (CHATCLI_ENCRYPTION_KEY_PREVIOUS) move to the current one.
// Line-oriented stores (transcripts, the prompt history) are handled per
// line. Files modified within skipRecent are deferred: a live writer may
// still be on them, and it seals them itself on its next save.
func (cli *ChatCLI) resealAtRestStores(skipRecent time.Duration) resealReport {
	return resealStores(cli.atRestStoreDirs(), cli.historyPath(), skipRecent, false)
}

// historyPath is the prompt history file, "" when there is none.
func (cli *ChatCLI) historyPath() string {
	if cli.historyManager == nil {
		return ""
	}
	return cli.historyManager.historyFile
}

// resealStores is the walk itself, over a precomputed list of directories
// and the history file, so it can run in the background without reading
// the ChatCLI fields the gateway swaps per tenant.
//
// plaintextOnly is the migration pass: a file that already carries the
// sealed header (or a journal whose every line is sealed) is skipped
// without being decrypted, so a pass over an already sealed installation
// costs a header read per file. The full pass (/config security reseal)
// opens everything, to move files off a retired key.
func resealStores(stores []atRestStore, history string, skipRecent time.Duration, plaintextOnly bool) resealReport {
	var rep resealReport
	cutoff := time.Now().Add(-skipRecent)
	recent := func(info os.FileInfo) bool { return skipRecent > 0 && info.ModTime().After(cutoff) }
	for _, st := range stores {
		walkStore(st, func(path string, d os.DirEntry) {
			if staleAtomicTemp(d, cutoff, skipRecent) {
				// A temp an interrupted atomic write left behind (a process
				// that exited mid-write): the original beside it is intact.
				_ = os.Remove(path)
				return
			}
			if !sealableIn(st.Kind, d.Name()) {
				return
			}
			// Already sealed first: a file its writer sealed a minute ago
			// is done, not deferred.
			if plaintextOnly && alreadySealed(path, d.Name()) {
				rep.Files++
				return
			}
			if info, ierr := d.Info(); ierr == nil && recent(info) {
				rep.Deferred++
				return
			}
			rep.Files++
			if strings.HasSuffix(d.Name(), ".jsonl") {
				n, lerr := atrest.SealLines(path, sealedLinePrefix, true)
				rep.countLines(path, n, lerr)
				return
			}
			changed, rerr := atrest.ResealFile(path)
			rep.count(path, changed, rerr)
		})
	}
	if history != "" {
		if info, err := os.Lstat(history); err == nil && info.Mode().IsRegular() {
			switch {
			case plaintextOnly && allLinesSealed(history, historySealedPrefix):
				rep.Files++
			case recent(info):
				rep.Deferred++
			default:
				rep.Files++
				// The prompt history is sealed per line without a path
				// binding: that is how its writer seals it.
				n, lerr := atrest.SealLines(history, historySealedPrefix, false)
				rep.countLines(history, n, lerr)
			}
		}
	}
	return rep
}

// atomicTempName matches the temps utils.AtomicWriteFile creates:
// <name>.<random digits>.tmp.
var atomicTempName = regexp.MustCompile(`\.[0-9]+\.tmp$`)

// staleAtomicTemp reports an atomic-write temp older than the deferral
// window: no writer keeps one open that long, so it is an orphan.
func staleAtomicTemp(d os.DirEntry, cutoff time.Time, skipRecent time.Duration) bool {
	if skipRecent <= 0 || !atomicTempName.MatchString(d.Name()) {
		return false
	}
	info, err := d.Info()
	return err == nil && info.ModTime().Before(cutoff)
}

// unsealStores rewrites every sealed store file as plaintext (encryption
// turned off with CHATCLI_ENCRYPTION_KEY=off; the key still opens them).
func unsealStores(stores []atRestStore, history string) resealReport {
	var rep resealReport
	for _, st := range stores {
		walkStore(st, func(path string, d os.DirEntry) {
			if !sealableIn(st.Kind, d.Name()) {
				return
			}
			rep.Files++
			if strings.HasSuffix(d.Name(), ".jsonl") {
				n, lerr := atrest.UnsealLines(path, sealedLinePrefix, true)
				rep.countLines(path, n, lerr)
				return
			}
			changed, uerr := atrest.UnsealFile(path)
			rep.count(path, changed, uerr)
		})
	}
	if history != "" {
		if info, err := os.Lstat(history); err == nil && info.Mode().IsRegular() {
			rep.Files++
			n, lerr := atrest.UnsealLines(history, historySealedPrefix, false)
			rep.countLines(history, n, lerr)
		}
	}
	return rep
}

// alreadySealed reports, without decrypting, whether a store file is
// sealed: the header for a whole-file store, every line for a journal.
func alreadySealed(path, name string) bool {
	if strings.HasSuffix(name, ".jsonl") {
		return allLinesSealed(path, sealedLinePrefix)
	}
	f, err := os.Open(path) // #nosec G304 -- store path enumerated from the state root
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 32)
	n, _ := io.ReadFull(f, head)
	return atrest.IsEncrypted(head[:n])
}

// allLinesSealed reports whether every non-blank line carries prefix.
func allLinesSealed(path, prefix string) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- store path enumerated from the state root
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, prefix) {
			return false
		}
	}
	return true
}

// count records one whole-file reseal.
func (rep *resealReport) count(path string, changed bool, err error) {
	switch {
	case errors.Is(err, atrest.ErrChangedDuringReseal):
		rep.Files--
		rep.Deferred++
	case err != nil:
		rep.Errors = append(rep.Errors, path+": "+err.Error())
	case changed:
		rep.Rewritten++
	}
}

// countLines records one line-oriented reseal.
func (rep *resealReport) countLines(path string, n int, err error) {
	rep.count(path, n > 0, err)
	if err == nil {
		rep.Lines += n
	}
}

// configSecurityReseal is /config security reseal.
func (cli *ChatCLI) configSecurityReseal() {
	if src, _, _ := atrest.KeyStatus(); src == atrest.KeySourceOff {
		// Encryption turned off: the reseal writes the stores back in
		// plaintext, the way to an older version that cannot read them.
		rep := unsealStores(cli.atRestStoreDirs(), cli.historyPath())
		_ = os.Remove(cli.atRestMarkerPath())
		fmt.Println(colorize("  "+i18n.T("sec.cmd.unseal_done", rep.Rewritten, rep.Files, rep.Lines), ColorGreen))
		for _, e := range rep.Errors {
			fmt.Println(colorize("    "+i18n.T("sec.cmd.reseal_error", e), ColorYellow))
		}
		return
	}
	if !atrest.Enabled() {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.reseal_off"), ColorYellow))
		return
	}
	rep := cli.resealAtRestStores(0)
	if len(rep.Errors) == 0 && rep.Deferred == 0 {
		_ = utils.AtomicWriteFile(cli.atRestMarkerPath(), []byte(atrest.KeyFingerprint()+"\n"), 0o600)
	}
	fmt.Println(colorize("  "+i18n.T("sec.cmd.reseal_done", rep.Rewritten, rep.Files, rep.Lines, atrest.KeyFingerprint()), ColorGreen))
	for _, e := range rep.Errors {
		fmt.Println(colorize("    "+i18n.T("sec.cmd.reseal_error", e), ColorYellow))
	}
	if len(rep.Errors) == 0 && os.Getenv(atrest.EnvPreviousKeys) != "" {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.reseal_retire_hint", atrest.EnvPreviousKeys), ColorGray))
	}
}

// configSecurityVerifyAudit is /config security verify-audit [path].
func (cli *ChatCLI) configSecurityVerifyAudit(args []string) {
	path := os.Getenv(AuditLogPathEnv)
	if len(args) > 0 {
		path = args[0]
	}
	if path == "" {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_no_path", AuditLogPathEnv), ColorYellow))
		return
	}
	rep, err := VerifyAuditChain(path)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_error", err), ColorRed))
		return
	}
	if rep.Intact() {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_ok", rep.Entries, rep.Chained, rep.Legacy), ColorGreen))
	} else {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_broken", rep.BrokenAt, rep.Err, rep.Chained), ColorRed))
	}
	if rep.Sealed > 0 {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_sealed", rep.Sealed), ColorGray))
	}
	if rep.RotatedFrom != "" {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_rotated_from", rep.RotatedFrom), ColorGray))
	}
	if rep.Torn {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_torn"), ColorYellow))
	}
	if rotated := auditchain.Rotated(path); len(rotated) > 0 {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_rotated_files", len(rotated)), ColorGray))
	}
}

// renderAtRestStatus prints the encryption-at-rest rows of /config security.
func (cli *ChatCLI) renderAtRestStatus(p string) {
	kv(p, i18n.T("cfg.kv.sec.atrest_state"), atRestStateLabel())
	kv(p, atrest.EnvKey, presence(os.Getenv(atrest.EnvKey)))
	if atrest.Enabled() {
		kv(p, i18n.T("cfg.kv.sec.atrest_key"), atrest.KeyFingerprint())
		kv(p, i18n.T("cfg.kv.sec.atrest_migration"), cli.atRestMigrationLabel())
		if src, _, _ := atrest.KeyStatus(); src == atrest.KeySourceProvider {
			kv(p, i18n.T("cfg.kv.sec.atrest_recovery"), colorize(i18n.T("cfg.val.sec.atrest_recovery"), ColorCyan))
		}
	}
	previous := 0
	for _, s := range strings.Split(os.Getenv(atrest.EnvPreviousKeys), ",") {
		if strings.TrimSpace(s) != "" {
			previous++
		}
	}
	kv(p, atrest.EnvPreviousKeys, fmt.Sprintf("%d", previous))
	kv(p, i18n.T("cfg.kv.sec.atrest_covers"), i18n.T("cfg.val.sec.atrest_covers_all"))
	kv(p, i18n.T("cfg.kv.sec.tenant_shared"), i18n.T("cfg.kv.sec.tenant_shared_list"))
	if locked := memory.LockedStores(); len(locked) > 0 {
		names := make([]string, 0, len(locked))
		for _, l := range locked {
			names = append(names, l.Store)
		}
		kv(p, i18n.T("cfg.kv.sec.atrest_locked"), colorize(i18n.T("cfg.val.sec.atrest_locked", strings.Join(names, ", ")), ColorRed))
	}
}

// atRestStateLabel says whether stores are sealed and where the key lives.
func atRestStateLabel() string {
	src, where, err := atrest.KeyStatus()
	switch src {
	case atrest.KeySourceEnv:
		return colorize(i18n.T("cfg.val.sec.atrest_env"), ColorGreen)
	case atrest.KeySourceProvider:
		if where == "file" {
			return colorize(i18n.T("cfg.val.sec.atrest_file", auth.KeyFilePath()), ColorYellow)
		}
		return colorize(i18n.T("cfg.val.sec.atrest_keychain"), ColorGreen)
	case atrest.KeySourceOff:
		return colorize(i18n.T("cfg.val.sec.atrest_off"), ColorYellow)
	case atrest.KeySourceUnavailable:
		return colorize(i18n.T("cfg.val.sec.atrest_unavailable", err.Error()), ColorRed)
	default:
		return colorize(i18n.T("cfg.val.sec.atrest_none"), ColorYellow)
	}
}

// atRestMigrationLabel reports whether every store is sealed with the
// current key, or how far the background migration got.
func (cli *ChatCLI) atRestMigrationLabel() string {
	if cli.atRestSealedWithCurrentKey() {
		return colorize(i18n.T("cfg.val.sec.atrest_migration_done"), ColorGreen)
	}
	running, done, rep, _ := cli.atRestMigrationState.snapshot()
	switch {
	case running:
		return i18n.T("cfg.val.sec.atrest_migration_running")
	case done:
		return colorize(i18n.T("cfg.val.sec.atrest_migration_partial", rep.Rewritten, rep.Deferred, len(rep.Errors)), ColorYellow)
	default:
		return colorize(i18n.T("cfg.val.sec.atrest_migration_pending"), ColorYellow)
	}
}

// configSecurityRecoveryKey is /config security recovery-key: it shows the
// secret behind the default key so the user can keep a copy outside this
// machine. The OS keychain is otherwise the only copy, and a reinstalled
// system, a reset keychain or a new machine would leave every sealed store
// unreadable. Shown only on an interactive terminal (never over ACP, MCP or
// a pipe, where it would end up in another program's log) and never logged.
func (cli *ChatCLI) configSecurityRecoveryKey() {
	if src, _, _ := atrest.KeyStatus(); src == atrest.KeySourceEnv {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_env"), ColorGray))
		return
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) { //#nosec G115 -- a file descriptor fits in int
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_not_tty"), ColorYellow))
		return
	}
	key, err := atrest.RecoveryKey()
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_unavailable", err.Error()), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_warning"), ColorYellow+ColorBold))
	fmt.Println()
	fmt.Println("    " + key)
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_howto", atrest.EnvKey), ColorGray))
}

// configSecurityUseKeychain is /config security use-keychain: the way back
// from a key of your own (CHATCLI_ENCRYPTION_KEY) to the default key in the
// OS keychain. Run while the variable is still set, it retires that key
// for this process (it keeps opening what it sealed, as a previous key),
// reseals every store with the keychain key, and tells the user to remove
// the variable. Nothing is lost if it stops halfway: files not moved yet
// still open with the old key listed in CHATCLI_ENCRYPTION_KEY_PREVIOUS.
func (cli *ChatCLI) configSecurityUseKeychain() {
	old := os.Getenv(atrest.EnvKey)
	src, where, _ := atrest.KeyStatus()
	if src == atrest.KeySourceProvider && where == "file" {
		cli.moveDefaultKeyToKeychain()
		return
	}
	if src != atrest.KeySourceEnv {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.use_keychain_not_env"), ColorGray))
		return
	}
	previous := os.Getenv(atrest.EnvPreviousKeys)
	_ = os.Unsetenv(atrest.EnvKey)
	if src, _, err := atrest.KeyStatus(); src != atrest.KeySourceProvider {
		_ = os.Setenv(atrest.EnvKey, old)
		reason := string(src)
		if err != nil {
			reason = err.Error()
		}
		fmt.Println(colorize("  "+i18n.T("sec.cmd.use_keychain_unavailable", reason), ColorYellow))
		return
	}
	retired := old
	if previous != "" {
		retired = old + "," + previous
	}
	_ = os.Setenv(atrest.EnvPreviousKeys, retired)

	rep := cli.resealAtRestStores(0)
	fmt.Println(colorize("  "+i18n.T("sec.cmd.reseal_done", rep.Rewritten, rep.Files, rep.Lines, atrest.KeyFingerprint()), ColorGreen))
	for _, e := range rep.Errors {
		fmt.Println(colorize("    "+i18n.T("sec.cmd.reseal_error", e), ColorYellow))
	}
	if len(rep.Errors) == 0 && rep.Deferred == 0 {
		_ = utils.AtomicWriteFile(cli.atRestMarkerPath(), []byte(atrest.KeyFingerprint()+"\n"), 0o600)
		fmt.Println(colorize("  "+i18n.T("sec.cmd.use_keychain_done", atrest.EnvKey), ColorCyan))
		return
	}
	fmt.Println(colorize("  "+i18n.T("sec.cmd.use_keychain_partial", atrest.EnvKey, atrest.EnvPreviousKeys), ColorYellow))
}

// routeRecoveryKey is /config security recovery-key [--save <file> |
// --restore <file>].
func (cli *ChatCLI) routeRecoveryKey(args []string) {
	if len(args) == 0 {
		cli.configSecurityRecoveryKey()
		return
	}
	if len(args) != 2 || (args[0] != "--save" && args[0] != "--restore") {
		fmt.Println(colorize("  /config security recovery-key [--save <file> | --restore <file>]", ColorYellow))
		return
	}
	path, err := utils.ExpandPath(args[1])
	if err != nil {
		path = args[1]
	}
	if !stdinIsInteractive() {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_not_tty"), ColorYellow))
		return
	}
	if args[0] == "--save" {
		cli.saveRecoveryFile(path)
		return
	}
	cli.restoreRecoveryFile(path)
}

// readPassphrase reads a passphrase from the terminal without echo; a
// variable so tests can answer the prompt.
var readPassphrase = readPassphraseFromTerminal

func readPassphraseFromTerminal(label string) (string, error) {
	fmt.Print("  " + label + ": ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd())) //#nosec G115 -- a file descriptor fits in int
	fmt.Println()
	return string(raw), err
}

// saveRecoveryFile writes the recovery key sealed with a passphrase the
// user types twice: a file that can live anywhere, since it opens nothing
// without the passphrase.
func (cli *ChatCLI) saveRecoveryFile(path string) {
	key, err := atrest.RecoveryKey()
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_unavailable", err.Error()), ColorYellow))
		return
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_file_exists", path), ColorYellow))
		return
	}
	pass, err := readPassphrase(i18n.T("sec.cmd.recovery_pass_new", atrest.MinRecoveryPassphrase))
	if err != nil {
		return
	}
	again, err := readPassphrase(i18n.T("sec.cmd.recovery_pass_again"))
	if err != nil {
		return
	}
	if pass != again {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_pass_mismatch"), ColorYellow))
		return
	}
	data, err := atrest.SealRecoveryKey(key, pass, atrest.KeyFingerprint())
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_file_failed", err.Error()), ColorRed))
		return
	}
	if err := utils.AtomicWriteFile(path, data, 0o600); err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_file_failed", err.Error()), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_file_saved", path), ColorGreen))
}

// restoreRecoveryFile opens a recovery file and moves the stores it opens
// onto this machine's keychain key: the way back after a reinstall or on a
// new machine, without the key ever being typed or shown.
func (cli *ChatCLI) restoreRecoveryFile(path string) {
	data, err := os.ReadFile(path) //#nosec G304 -- a recovery file the user named
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_file_failed", err.Error()), ColorRed))
		return
	}
	pass, err := readPassphrase(i18n.T("sec.cmd.recovery_pass"))
	if err != nil {
		return
	}
	key, err := atrest.OpenRecoveryKey(data, pass)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.recovery_file_failed", err.Error()), ColorRed))
		return
	}
	cli.restoreWithRecoveryKey(key)
}

// restoreWithRecoveryKey moves the stores the recovery key opens onto the
// active key: this machine's keychain key, or a key of your own when one
// is set.
func (cli *ChatCLI) restoreWithRecoveryKey(key string) {
	if src, _, _ := atrest.KeyStatus(); src == atrest.KeySourceEnv {
		// A key of your own is active: keep it, and let the recovered key
		// open what it sealed while the stores move onto the active key.
		retired := key
		if prev := os.Getenv(atrest.EnvPreviousKeys); prev != "" {
			retired = key + "," + prev
		}
		_ = os.Setenv(atrest.EnvPreviousKeys, retired)
		cli.configSecurityReseal()
		return
	}
	_ = os.Setenv(atrest.EnvKey, key)
	cli.configSecurityUseKeychain()
}

// moveDefaultKeyToKeychain is use-keychain for the default key while it
// still lives in the key file: the key moves into the OS keychain as it is,
// so nothing sealed with it changes.
func (cli *ChatCLI) moveDefaultKeyToKeychain() {
	moved, err := moveFileKeyToKeychain()
	switch {
	case err != nil:
		fmt.Println(colorize("  "+i18n.T("sec.cmd.keyfile_move_failed", err.Error()), ColorYellow))
	case moved:
		fmt.Println(colorize("  "+i18n.T("sec.cmd.keyfile_moved", auth.KeyFilePath()), ColorGreen))
	default:
		fmt.Println(colorize("  "+i18n.T("sec.cmd.use_keychain_not_env"), ColorGray))
	}
}

// moveFileKeyToKeychain is auth.MoveFileKeyToKeychain; a variable for tests.
var moveFileKeyToKeychain = auth.MoveFileKeyToKeychain
