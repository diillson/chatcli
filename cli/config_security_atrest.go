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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/atrest"
)

// resealReport counts one reseal walk.
type resealReport struct {
	Files, Rewritten, Lines int
	Errors                  []string
}

// atRestStoreDirs lists the directories whose files the reseal walk covers,
// under the active state root (tenant root under the gateway).
func (cli *ChatCLI) atRestStoreDirs() []string {
	root := cli.stateRoot
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".chatcli")
	}
	dirs := []string{
		filepath.Join(root, "sessions"),
		filepath.Join(root, "transcripts"),
		filepath.Join(root, "memory"),
		filepath.Join(root, "contexts"),
		filepath.Join(root, "ccr"),
		filepath.Join(root, "costs"),
	}
	if cli.sessionManager != nil && cli.sessionManager.sessionsDir != "" {
		dirs[0] = cli.sessionManager.sessionsDir
	}
	if cli.contextHandler != nil {
		if mgr := cli.contextHandler.GetManager(); mgr != nil && mgr.Storage != nil {
			dirs[3] = mgr.Storage.GetStoragePath()
		}
	}
	return dirs
}

// resealAtRestStores rewrites every store file under the state root with
// the current key: plaintext files become sealed, files sealed with a
// retired key (CHATCLI_ENCRYPTION_KEY_PREVIOUS) move to the current one.
// Line-oriented stores (transcripts, exports) are handled per line.
func (cli *ChatCLI) resealAtRestStores() resealReport {
	var rep resealReport
	for _, dir := range cli.atRestStoreDirs() {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			switch {
			case strings.HasSuffix(name, ".tmp"), strings.HasSuffix(name, ".corrupt"), strings.Contains(name, ".corrupt-"):
				return nil
			case strings.HasSuffix(name, ".jsonl"):
				rep.Files++
				n, rerr := atrest.ResealLines(path, sealedLinePrefix)
				if rerr != nil {
					rep.Errors = append(rep.Errors, path+": "+rerr.Error())
					return nil
				}
				if n > 0 {
					rep.Rewritten++
					rep.Lines += n
				}
			case strings.HasSuffix(name, ".json"), strings.HasSuffix(name, ".ccr"), strings.HasSuffix(name, ".enc"):
				rep.Files++
				changed, rerr := atrest.ResealFile(path)
				if rerr != nil {
					rep.Errors = append(rep.Errors, path+": "+rerr.Error())
					return nil
				}
				if changed {
					rep.Rewritten++
				}
			}
			return nil
		})
	}
	return rep
}

// configSecurityReseal is /config security reseal.
func (cli *ChatCLI) configSecurityReseal() {
	if !atrest.Enabled() {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.reseal_no_key"), ColorYellow))
		return
	}
	rep := cli.resealAtRestStores()
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
		return
	}
	fmt.Println(colorize("  "+i18n.T("sec.cmd.verify_audit_broken", rep.BrokenAt, rep.Err, rep.Chained), ColorRed))
}

// renderAtRestStatus prints the encryption-at-rest rows of /config security.
func (cli *ChatCLI) renderAtRestStatus(p string) {
	kv(p, atrest.EnvKey, presence(os.Getenv(atrest.EnvKey)))
	if atrest.Enabled() {
		kv(p, i18n.T("cfg.kv.sec.atrest_key"), atrest.KeyFingerprint())
	}
	previous := 0
	for _, s := range strings.Split(os.Getenv(atrest.EnvPreviousKeys), ",") {
		if strings.TrimSpace(s) != "" {
			previous++
		}
	}
	kv(p, atrest.EnvPreviousKeys, fmt.Sprintf("%d", previous))
	kv(p, i18n.T("cfg.kv.sec.atrest_covers"), i18n.T("cfg.kv.sec.atrest_covers_list"))
}
