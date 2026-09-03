/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Organization-managed defaults.
 *
 * An operator can ship a managed.env file with the machine image or the
 * MDM profile and have every ChatCLI process on that machine honor it:
 *
 *   /etc/chatcli/managed.env                  (Linux, macOS)
 *   %ProgramData%\chatcli\managed.env         (Windows)
 *   $CHATCLI_MANAGED_CONFIG                   (explicit path, any OS)
 *
 * The file is dotenv-shaped. A plain KEY=value line is a DEFAULT: it
 * applies only when the user has not set KEY (environment or .env). A
 * line prefixed with "!" (!KEY=value) is LOCKED: it wins over whatever
 * the user set, on boot and again on every /reload, so a policy such as
 * an audit trail path or a redaction mode cannot be switched off from a
 * shell. /config shows which values came from the managed file.
 *
 * Precedence, highest first: locked managed → user environment / .env →
 * managed default → code default.
 */
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// ManagedConfigEnv points at the managed file explicitly.
const ManagedConfigEnv = "CHATCLI_MANAGED_CONFIG"

// ManagedEntry is one line of the managed file.
type ManagedEntry struct {
	Key    string
	Value  string
	Locked bool
}

// ManagedReport is the outcome of one ApplyManaged pass.
type ManagedReport struct {
	Path    string
	Present bool           // a managed file exists
	Applied []ManagedEntry // defaults that filled an unset variable
	Locked  []ManagedEntry // locked values that were enforced
	Skipped []ManagedEntry // defaults the user had already overridden
	Err     error          // read/parse failure (the file is then ignored)
}

var (
	managedMu      sync.RWMutex
	managedEntries map[string]ManagedEntry
	managedPath    string
	managedPresent bool
)

// ManagedConfigPath returns the managed file location for this process.
func ManagedConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(ManagedConfigEnv)); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "chatcli", "managed.env")
	}
	return "/etc/chatcli/managed.env"
}

// ParseManaged reads managed entries from dotenv-shaped text.
func ParseManaged(text string) []ManagedEntry {
	var out []ManagedEntry
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		locked := false
		if strings.HasPrefix(line, "!") {
			locked = true
			line = strings.TrimSpace(line[1:])
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if !validEnvKey(key) {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		val = unquote(val)
		out = append(out, ManagedEntry{Key: key, Value: val, Locked: locked})
	}
	return out
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return true
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

// ApplyManaged loads the managed file and applies it to the process
// environment: locked entries always, defaults only where the variable
// is unset. Safe to call again (reload): locked values are re-asserted,
// defaults re-filled. A missing file is not an error.
func ApplyManaged() ManagedReport {
	rep := ManagedReport{Path: ManagedConfigPath()}
	data, err := os.ReadFile(rep.Path) // #nosec G304 -- fixed system path or operator-provided CHATCLI_MANAGED_CONFIG
	if err != nil {
		if !os.IsNotExist(err) {
			rep.Err = err
		}
		managedMu.Lock()
		managedEntries, managedPath, managedPresent = nil, rep.Path, false
		managedMu.Unlock()
		return rep
	}
	rep.Present = true
	entries := ParseManaged(string(data))
	byKey := make(map[string]ManagedEntry, len(entries))
	for _, e := range entries {
		byKey[e.Key] = e
		if e.Locked {
			_ = os.Setenv(e.Key, e.Value)
			rep.Locked = append(rep.Locked, e)
			continue
		}
		if _, set := os.LookupEnv(e.Key); set {
			rep.Skipped = append(rep.Skipped, e)
			continue
		}
		_ = os.Setenv(e.Key, e.Value)
		rep.Applied = append(rep.Applied, e)
	}
	managedMu.Lock()
	managedEntries, managedPath, managedPresent = byKey, rep.Path, true
	managedMu.Unlock()
	return rep
}

// ManagedEntryFor reports whether a variable comes from the managed file
// and whether it is locked there.
func ManagedEntryFor(key string) (entry ManagedEntry, managed bool) {
	managedMu.RLock()
	defer managedMu.RUnlock()
	e, ok := managedEntries[key]
	return e, ok
}

// ManagedState returns the managed file path, whether it exists, and its
// entries sorted by key (locked first) for /config.
func ManagedState() (path string, present bool, entries []ManagedEntry) {
	managedMu.RLock()
	defer managedMu.RUnlock()
	path = managedPath
	if path == "" {
		path = ManagedConfigPath()
	}
	entries = make([]ManagedEntry, 0, len(managedEntries))
	for _, e := range managedEntries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Locked != entries[j].Locked {
			return entries[i].Locked
		}
		return entries[i].Key < entries[j].Key
	})
	return path, managedPresent, entries
}
