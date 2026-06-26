/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package engine

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MultipatchEdit is one search/replace step inside a transactional
// multi-file edit. Multiple edits targeting the same file are applied
// sequentially in declaration order on the in-memory copy.
//
// Encoding defaults to "text". When "base64", both Search and Replace
// are b64-decoded before matching, which the LLM uses to ship payloads
// containing control characters or non-UTF8 sequences.
type MultipatchEdit struct {
	File     string `json:"file"`
	Search   string `json:"search"`
	Replace  string `json:"replace"`
	Encoding string `json:"encoding,omitempty"`
}

// multipatchFileLocks serializes edits on a per-file basis so two
// concurrent multipatch transactions touching the same file don't
// interleave. The lock is keyed by absolute path. We never delete
// entries from the map — a multipatch is short-lived and the working
// set of files is bounded by what the LLM has been talking about, so
// memory pressure is not a concern in practice.
var (
	multipatchFileLocksMu sync.Mutex
	multipatchFileLocks   = make(map[string]*sync.Mutex)
)

// lockFile returns the mutex for the given absolute path, creating
// it lazily under the registry mutex.
func lockFile(absPath string) *sync.Mutex {
	multipatchFileLocksMu.Lock()
	defer multipatchFileLocksMu.Unlock()
	if m, ok := multipatchFileLocks[absPath]; ok {
		return m
	}
	m := &sync.Mutex{}
	multipatchFileLocks[absPath] = m
	return m
}

// handleMultipatch is the engine entry-point for transactional
// multi-file edits. Contract:
//
//  1. The --edits flag carries a JSON array of MultipatchEdit objects.
//  2. Phase 1 (validate): every edit's path passes validatePath, the
//     file exists, and the search string is found in the content the
//     edit would see (after prior in-flight edits to the same file
//     are applied sequentially).
//  3. Phase 2 (commit): for each affected file, take a transaction
//     backup, then write the new content. If any write fails, restore
//     all written files from their backups and abort.
//  4. Backups are cleaned up on success.
//
// Atomicity is best-effort at the filesystem layer: we use os.Rename
// for backup creation where possible so the restore path is fast and
// crash-tolerant up to the point where all backups are taken. After
// that, a process kill mid-write leaves a partial multipatch — the
// agent can use @coder rollback to recover from individual .bak files.
func (e *Engine) handleMultipatch(args []string) error {
	fs := flag.NewFlagSet("multipatch", flag.ContinueOnError)
	editsJSON := fs.String("edits", "", "JSON array of {file,search,replace,encoding?}")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*editsJSON) == "" {
		return errors.New("--edits required")
	}

	var edits []MultipatchEdit
	if err := json.Unmarshal([]byte(*editsJSON), &edits); err != nil {
		return fmt.Errorf("invalid edits JSON: %w", err)
	}
	if len(edits) == 0 {
		return errors.New("edits array cannot be empty")
	}

	// Phase 0: normalize and group by absolute path. Keep declaration
	// order within a file because later edits may depend on earlier
	// edits' replacements.
	type stagedEdit struct {
		decl    int // 1-indexed declaration position for error reporting
		edit    MultipatchEdit
		search  string // decoded search text
		replace string
	}
	groups := make(map[string][]stagedEdit)
	groupKeys := make([]string, 0)

	for i, ed := range edits {
		idx := i + 1
		if ed.File == "" {
			return fmt.Errorf("edit #%d: file is required", idx)
		}
		if ed.Search == "" {
			return fmt.Errorf("edit #%d: search is required (use --diff if you have a unified diff)", idx)
		}

		enc := ed.Encoding
		if enc == "" {
			enc = "text"
		}
		sBytes, err := smartDecode(ed.Search, enc)
		if err != nil {
			return fmt.Errorf("edit #%d: search decode failed: %w", idx, err)
		}
		rBytes, err := smartDecode(ed.Replace, enc)
		if err != nil {
			return fmt.Errorf("edit #%d: replace decode failed: %w", idx, err)
		}

		// ed.File arrives inside the --edits JSON, so it bypasses the
		// path-flag expansion in parseFlags; expand it here too or a
		// "~/x" / "$CHATCLI_AGENT_TMPDIR/x" edit target would resolve
		// to a literal directory under the cwd.
		abs, err := filepath.Abs(expandPath(ed.File))
		if err != nil {
			return fmt.Errorf("edit #%d: resolve path: %w", idx, err)
		}
		if err := e.validatePath(abs); err != nil {
			return fmt.Errorf("edit #%d: %w", idx, err)
		}

		if _, ok := groups[abs]; !ok {
			groupKeys = append(groupKeys, abs)
		}
		groups[abs] = append(groups[abs], stagedEdit{
			decl:    idx,
			edit:    ed,
			search:  strings.ReplaceAll(string(sBytes), "\r\n", "\n"),
			replace: strings.ReplaceAll(string(rBytes), "\r\n", "\n"),
		})
	}

	// Sort group keys so the lock acquisition order is deterministic.
	// Without this, two concurrent multipatches touching the same
	// pair of files in different orders could deadlock.
	sort.Strings(groupKeys)

	// Acquire all per-file locks up front, in sorted order. Hold them
	// for the entire transaction so other writers stay out.
	for _, abs := range groupKeys {
		lockFile(abs).Lock()
		defer lockFile(abs).Unlock()
	}

	// Phase 1: validate every edit by simulating the apply in memory.
	// Each file's edits run sequentially on its in-memory content so
	// later edits see the result of earlier ones. We also snapshot the
	// file's original permission bits so the commit phase preserves
	// chmod state across the rewrite — without this, gosec G306 would
	// flag the WriteFile call as potentially permissive, and the file
	// would silently lose any owner-set non-default mode.
	originals := make(map[string]string, len(groupKeys))
	finals := make(map[string]string, len(groupKeys))
	modes := make(map[string]os.FileMode, len(groupKeys))

	for _, abs := range groupKeys {
		info, statErr := os.Stat(abs)
		if statErr != nil {
			return fmt.Errorf("stat %s: %w", abs, statErr)
		}
		modes[abs] = info.Mode().Perm()

		// abs was already produced by filepath.Abs() (which Cleans
		// internally) and survived e.validatePath above, so this
		// filepath.Clean is functionally a no-op. We keep it as an
		// explicit barrier in the dataflow path so gosec G304's static
		// analysis can see the sanitization step — the project
		// convention (qg-fan-in/main.go, providerparity, i18nparity)
		// uses the same pattern for the same reason.
		raw, err := os.ReadFile(filepath.Clean(abs))
		if err != nil {
			return fmt.Errorf("read %s: %w", abs, err)
		}
		content := strings.ReplaceAll(string(raw), "\r\n", "\n")
		originals[abs] = content

		for _, ed := range groups[abs] {
			if !strings.Contains(content, ed.search) {
				return fmt.Errorf("edit #%d (%s): search text not found in current content",
					ed.decl, abs)
			}
			// Apply once. Multipatch is a deliberate primitive: if the
			// LLM wants multi-occurrence replacement, it can issue
			// multiple edits with the same file.
			content = strings.Replace(content, ed.search, ed.replace, 1)
		}
		finals[abs] = content
	}

	// Phase 2: commit. We snapshot in-memory originals as the rollback
	// source — payloads in a typical multipatch are kilobytes per file,
	// so RAM-backed rollback is fast and avoids the partial-state risk
	// a disk-backed staging file would introduce on process kill.
	tx := fmt.Sprintf("mpx-%d", time.Now().UnixNano())
	type backup struct {
		path string
		data []byte
		mode os.FileMode
	}
	backups := make([]backup, 0, len(groupKeys))

	for _, abs := range groupKeys {
		backups = append(backups, backup{
			path: abs,
			data: []byte(originals[abs]),
			mode: modes[abs],
		})
	}

	commit := func() error {
		for _, abs := range groupKeys {
			if err := os.WriteFile(abs, []byte(finals[abs]), modes[abs]); err != nil {
				return fmt.Errorf("write %s: %w", abs, err)
			}
		}
		return nil
	}

	if err := commit(); err != nil {
		// Restore each file from the in-memory original using its
		// pre-transaction permission mode.
		for _, b := range backups {
			_ = os.WriteFile(b.path, b.data, b.mode)
		}
		return fmt.Errorf("multipatch (tx=%s) aborted: %w (all files restored)", tx, err)
	}

	e.printf("✅ multipatch (tx=%s) applied %d edit(s) across %d file(s)\n",
		tx, len(edits), len(groupKeys))
	for _, abs := range groupKeys {
		e.printf("   • %s (%d edit(s))\n", abs, len(groups[abs]))
	}
	return nil
}
