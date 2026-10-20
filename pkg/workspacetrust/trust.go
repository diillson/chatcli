/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Workspace trust.
 *
 * A cloned repository is attacker-controlled input: its ./.env (endpoints,
 * TLS switches, MCP commands), .chatcli/hooks.json (shell commands) and
 * coder_policy.json load only from a trusted workspace. Trust is keyed by the
 * directory's resolved path plus a SHA-256 over those files' contents,
 * persisted in ~/.chatcli/trusted_workspaces.json: changing any covered file
 * lapses it.
 */
package workspacetrust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/pkg/flock"
	"github.com/diillson/chatcli/pkg/stateroot"
	"github.com/diillson/chatcli/utils"
)

// StoreFileName is the trust store under ~/.chatcli.
const StoreFileName = "trusted_workspaces.json"

// coveredFiles are the project-relative files that can run code or change
// ChatCLI's security posture. Forward slashes: they are converted per OS.
var coveredFiles = []string{".env", ".chatcli/hooks.json", "coder_policy.json"}

func init() {
	// config cannot import this package (utils imports config), so the
	// dotenv discovery consults trust through this gate.
	config.SetWorkspaceTrustGate(Allowed)
}

// Inspection is the trust state of one directory.
type Inspection struct {
	Dir         string   // resolved path of the directory
	Files       []string // covered files present, project-relative
	Fingerprint string   // SHA-256 over the covered files' contents
	Trusted     bool     // a trust decision matches Fingerprint
}

// NeedsDecision reports whether the directory has covered files that may
// not be loaded until the user trusts it.
func (i Inspection) NeedsDecision() bool { return len(i.Files) > 0 && !i.Trusted }

// Record is one trusted workspace in the store.
type Record struct {
	Path        string    `json:"path"`
	Fingerprint string    `json:"fingerprint"`
	Files       []string  `json:"files"`
	TrustedAt   time.Time `json:"trusted_at"`
}

type storeFile struct {
	Version    int      `json:"version"`
	Workspaces []Record `json:"workspaces"`
}

// session holds the decisions taken in this process, keyed by resolved path:
// the fingerprint the user answered for and the answer. A "No" keeps every
// covered file out for the rest of the run without asking again; a "Yes"
// still applies when the store could not be written.
var (
	sessionMu sync.Mutex
	session   = map[string]sessionDecision{}
)

type sessionDecision struct {
	fingerprint string
	allow       bool
}

// realDir resolves dir to an absolute path with symlinks evaluated.
func realDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// Inspect computes the trust state of dir.
func Inspect(dir string) (Inspection, error) {
	resolved, err := realDir(dir)
	if err != nil {
		return Inspection{}, err
	}
	ins := Inspection{Dir: resolved}
	h := sha256.New()
	for _, rel := range coveredFiles {
		path := filepath.Join(resolved, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		ins.Files = append(ins.Files, rel)
		fmt.Fprintf(h, "%s\x00", rel)
		if !info.Mode().IsRegular() {
			// Never read a FIFO or a device: hash the kind instead, so the
			// fingerprint still changes when the file is replaced.
			fmt.Fprintf(h, "nonregular:%s\x00", info.Mode().Type())
			continue
		}
		if err := hashFile(h, path); err != nil {
			return Inspection{}, err
		}
		h.Write([]byte{0})
	}
	ins.Fingerprint = hex.EncodeToString(h.Sum(nil))
	ins.Trusted = decided(ins)
	return ins, nil
}

func hashFile(w io.Writer, path string) error {
	f, err := os.Open(path) // #nosec G304 -- a covered file of the inspected workspace, hashed only
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}

// decided reports whether a decision of this run or the store trusts ins.
func decided(ins Inspection) bool {
	sessionMu.Lock()
	d, ok := session[ins.Dir]
	sessionMu.Unlock()
	if ok && d.fingerprint == ins.Fingerprint {
		return d.allow
	}
	recs, err := List()
	if err != nil {
		return false
	}
	for _, r := range recs {
		if r.Path == ins.Dir {
			return r.Fingerprint == ins.Fingerprint
		}
	}
	return false
}

// Allowed reports whether dir is a trusted workspace whose covered files may
// be loaded. Any error fails closed.
func Allowed(dir string) bool {
	ins, err := Inspect(dir)
	return err == nil && ins.Trusted
}

// StorePath returns the trust store location.
func StorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateroot.Under(home), StoreFileName), nil
}

// List returns the trusted workspaces.
func List() ([]Record, error) {
	path, err := StorePath()
	if err != nil {
		return nil, err
	}
	return readStore(path)
}

func readStore(path string) ([]Record, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed location under the user's home
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sf.Workspaces, nil
}

// update applies fn to the store under a cross-process lock.
func update(fn func([]Record) []Record) error {
	path, err := StorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock := flock.Lock(path)
	defer unlock()
	recs, err := readStore(path)
	if err != nil {
		return err
	}
	recs = fn(recs)
	sort.Slice(recs, func(i, j int) bool { return recs[i].Path < recs[j].Path })
	data, err := json.MarshalIndent(storeFile{Version: 1, Workspaces: recs}, "", "  ")
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(path, append(data, '\n'), 0o600)
}

// Trust records ins as trusted, for this run and in the store. The
// fingerprint recorded is the one the user saw: if a file changed since, the
// record does not match and the user is asked again.
func Trust(ins Inspection) error {
	sessionMu.Lock()
	session[ins.Dir] = sessionDecision{fingerprint: ins.Fingerprint, allow: true}
	sessionMu.Unlock()
	return update(func(recs []Record) []Record {
		out := recs[:0]
		for _, r := range recs {
			if r.Path != ins.Dir {
				out = append(out, r)
			}
		}
		return append(out, Record{
			Path:        ins.Dir,
			Fingerprint: ins.Fingerprint,
			Files:       append([]string(nil), ins.Files...),
			TrustedAt:   time.Now().UTC(),
		})
	})
}

// Deny keeps ins untrusted for the rest of this run.
func Deny(ins Inspection) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	session[ins.Dir] = sessionDecision{fingerprint: ins.Fingerprint}
}

// Revoke removes dir from the store and from this run's decisions. It
// reports whether a record existed.
func Revoke(dir string) (bool, error) {
	resolved, err := realDir(dir)
	if err != nil {
		// A directory deleted since it was trusted: match its stored path.
		if resolved, err = filepath.Abs(dir); err != nil {
			return false, err
		}
	}
	sessionMu.Lock()
	delete(session, resolved)
	sessionMu.Unlock()
	removed := false
	err = update(func(recs []Record) []Record {
		out := recs[:0]
		for _, r := range recs {
			if r.Path == resolved {
				removed = true
				continue
			}
			out = append(out, r)
		}
		return out
	})
	return removed, err
}

// ResetSessionForTest forgets the decisions of this run. Test-only.
func ResetSessionForTest() {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	session = map[string]sessionDecision{}
}
