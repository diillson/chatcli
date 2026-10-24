/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Package vault writes an Obsidian-compatible vault: one Markdown note per
 * item, YAML front matter, [[wikilinks]] between notes. It knows nothing
 * about where the notes come from — the caller renders Note values — and
 * owns only the discipline of putting them on disk safely:
 *
 *   - incremental: a manifest records the hash of every file the vault
 *     wrote, so an unchanged note is never rewritten (Obsidian, sync tools
 *     and git see no churn);
 *   - edit-preserving: a generated note the person changed by hand (its
 *     bytes no longer match the manifest) is kept, reported, and only
 *     overwritten with Force — the vault is theirs to annotate;
 *   - owned-files-only: stale notes are removed only if the vault wrote
 *     them and they are unmodified; files it never wrote are never touched;
 *   - safe paths: every note path is relative, cleaned, confined to the
 *     vault directory, and de-duplicated case-insensitively (macOS and
 *     Windows file systems fold case);
 *   - atomic writes with private permissions (0600 files, 0700 dirs).
 */
package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/diillson/chatcli/pkg/flock"
	"github.com/diillson/chatcli/utils"
)

// ManifestDir is the vault-internal directory holding the manifest. Obsidian
// ignores dot-directories, so it never shows up as a note.
const ManifestDir = ".chatcli"

const (
	manifestFile    = "manifest.json"
	manifestVersion = 1
	filePerm        = 0o600
	dirPerm         = 0o700
	// MaxNoteBytes bounds one rendered note; larger bodies are cut with a
	// visible marker (a multi-megabyte note is unusable in an editor).
	MaxNoteBytes = 1 << 20
)

// Field is one front-matter entry. Exactly one of Value/List is used: List
// renders as a YAML list (aliases, tags), Value as a scalar.
type Field struct {
	Key    string
	Value  string
	List   []string
	IsList bool
}

// Note is one Markdown note of the vault.
type Note struct {
	// Path is the note path relative to the vault, with '/' separators and
	// the .md extension ("Projects/Orbital.md").
	Path string
	// Key identifies the source revision (e.g. a fingerprint): when it is
	// unchanged from the previous run and the file is intact, the note is
	// not even rendered again. Empty disables the shortcut.
	Key string
	// Front is the YAML front matter, in order.
	Front []Field
	// Body is the Markdown after the front matter.
	Body string
	// BodyFunc, when set and Body is empty, produces the body lazily — only
	// when the note actually has to be rendered (its Key changed or the
	// file is missing/torn). Lets a caller describe thousands of large
	// documents without assembling the unchanged ones.
	BodyFunc func() string
}

// Report says what one Sync did.
type Report struct {
	Dir       string
	Written   int
	Unchanged int
	Removed   int
	// Kept lists generated notes changed by hand and therefore not
	// overwritten (or not removed), relative to the vault.
	Kept []string
	// Skipped lists notes rejected for an unsafe path.
	Skipped  []string
	Duration time.Duration
}

type manifestEntry struct {
	Hash string `json:"hash"`
	Key  string `json:"key,omitempty"`
}

type manifest struct {
	Version     int                      `json:"version"`
	GeneratedAt time.Time                `json:"generated_at"`
	Files       map[string]manifestEntry `json:"files"`
}

// Options tune Sync.
type Options struct {
	// Force overwrites (and removes) generated notes changed by hand.
	Force bool
	// Context, when set, stops the sync between notes once done; the
	// manifest still records every note written so far.
	Context context.Context
}

// Sync makes dir hold exactly the given notes (plus anything the person
// added). Notes are processed in path order; a failure to write one note
// is returned after the remaining notes were attempted, so one bad file
// never leaves the rest of the vault stale.
func Sync(dir string, notes []Note, opt Options) (Report, error) {
	start := time.Now()
	if strings.TrimSpace(dir) == "" {
		return Report{Dir: dir}, errors.New("vault: empty directory")
	}
	if err := os.MkdirAll(filepath.Join(dir, ManifestDir), dirPerm); err != nil {
		return Report{Dir: dir}, fmt.Errorf("vault: create %s: %w", dir, err)
	}
	// One sync per vault at a time across processes (two terminals, the
	// gateway): otherwise B, holding A's stale manifest, would take A's
	// fresh notes for hand edits and freeze them for good.
	unlock := flock.Lock(filepath.Join(dir, ManifestDir, manifestFile))
	defer unlock()
	prev := loadManifest(dir)
	r := &syncRun{
		dir:  dir,
		opt:  opt,
		rep:  Report{Dir: dir},
		prev: prev,
		// Case-folded view of what the vault owns: on macOS and Windows a
		// note whose path changed only in case is the same file.
		prevFold: foldIndex(prev),
		next:     manifest{Version: manifestVersion, GeneratedAt: time.Now().UTC(), Files: make(map[string]manifestEntry, len(notes))},
	}
	notes = dedupePaths(notes)
	canceled := false
	for i, n := range notes {
		if opt.Context != nil && opt.Context.Err() != nil {
			r.carryOver(notes[i:])
			canceled = true
			break
		}
		r.syncNote(n)
	}
	if canceled {
		// A canceled run cannot tell stale from not-yet-visited: it keeps
		// ownership of everything and removes nothing.
		for rel, e := range r.prev.Files {
			if _, live := r.next.Files[rel]; !live {
				r.next.Files[rel] = e
			}
		}
		r.errs = append(r.errs, opt.Context.Err())
	} else {
		r.removeStale()
	}
	sort.Strings(r.rep.Kept)
	if err := saveManifest(dir, r.next); err != nil {
		r.errs = append(r.errs, err)
	}
	r.rep.Duration = time.Since(start)
	return r.rep, errors.Join(r.errs...)
}

// syncRun is the state of one Sync.
type syncRun struct {
	dir        string
	opt        Options
	rep        Report
	prev, next manifest
	prevFold   map[string]string // lower-cased path → path in prev
	errs       []error
}

func foldIndex(m manifest) map[string]string {
	out := make(map[string]string, len(m.Files))
	for rel := range m.Files {
		out[strings.ToLower(rel)] = rel
	}
	return out
}

// carryOver keeps the manifest entries of notes a canceled run never
// reached, so they are not mistaken for removed notes.
func (r *syncRun) carryOver(rest []Note) {
	for _, n := range rest {
		if rel, ok := SafeRel(n.Path); ok {
			if e, had := r.prev.Files[rel]; had {
				r.next.Files[rel] = e
			}
		}
	}
}

// syncNote brings one note's file up to date.
func (r *syncRun) syncNote(n Note) {
	rel, ok := SafeRel(n.Path)
	if !ok {
		r.rep.Skipped = append(r.rep.Skipped, n.Path)
		return
	}
	abs := filepath.Join(r.dir, filepath.FromSlash(rel))
	old, had := r.prev.Files[rel]
	if !had {
		// Same file under another case (a renamed title): ours.
		if prevRel, ok := r.prevFold[strings.ToLower(rel)]; ok {
			old, had = r.prev.Files[prevRel], true
		}
	}
	onDisk, readErr := os.ReadFile(abs) // #nosec G304 -- confined to the vault dir by SafeRel
	exists := readErr == nil
	diskHash := ""
	if exists {
		diskHash = hashBytes(onDisk)
	}
	// A zero-length file where the vault wrote content is a torn write
	// (notes skip fsync, see writeNote), not an edit: rewrite it.
	torn := had && exists && len(onDisk) == 0
	if had && exists && diskHash != old.Hash && !torn && !r.opt.Force {
		r.rep.Kept = append(r.rep.Kept, rel) // edited by hand
		r.next.Files[rel] = old              // keep tracking our last version
		return
	}
	if had && exists && n.Key != "" && n.Key == old.Key && diskHash == old.Hash {
		r.next.Files[rel] = old
		r.rep.Unchanged++
		return
	}
	data := Render(n)
	h := hashBytes(data)
	if exists && h == diskHash {
		// Identical bytes: ours, possibly from a run that was cut off
		// before its manifest landed — adopt it.
		r.next.Files[rel] = manifestEntry{Hash: h, Key: n.Key}
		r.rep.Unchanged++
		return
	}
	if !had && exists && len(onDisk) > 0 {
		// A file the vault never wrote sits where this note goes: it is
		// the person's and is never overwritten — not even with Force,
		// which only reclaims the vault's OWN notes.
		r.rep.Kept = append(r.rep.Kept, rel)
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), dirPerm); err != nil {
		r.errs = append(r.errs, fmt.Errorf("vault: %s: %w", rel, err))
		return
	}
	if err := writeNote(abs, data); err != nil {
		r.errs = append(r.errs, fmt.Errorf("vault: %s: %w", rel, err))
		return
	}
	r.next.Files[rel] = manifestEntry{Hash: h, Key: n.Key}
	r.rep.Written++
}

// sameFile reports whether two vault paths name one file (case-folding
// file systems).
func (r *syncRun) sameFile(a, b string) bool {
	fa, errA := os.Stat(filepath.Join(r.dir, filepath.FromSlash(a)))
	fb, errB := os.Stat(filepath.Join(r.dir, filepath.FromSlash(b)))
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// removeStale deletes notes the vault wrote before and no longer
// produces — unless they were edited by hand (kept, still tracked so Force
// can clean them later).
func (r *syncRun) removeStale() {
	liveFold := make(map[string]string, len(r.next.Files))
	for rel := range r.next.Files {
		liveFold[strings.ToLower(rel)] = rel
	}
	stale := make([]string, 0)
	for rel := range r.prev.Files {
		if _, live := r.next.Files[rel]; live {
			continue
		}
		if liveRel, ok := liveFold[strings.ToLower(rel)]; ok && r.sameFile(rel, liveRel) {
			// Only the case changed and the file system folds case: the
			// old path IS the note just written — removing it would delete
			// that note.
			continue
		}
		stale = append(stale, rel)
	}
	sort.Strings(stale)
	for _, rel := range stale {
		if contains(r.rep.Kept, rel) {
			continue
		}
		abs := filepath.Join(r.dir, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs) // #nosec G304 -- manifest paths were validated when written
		if err != nil {
			continue // already gone
		}
		if hashBytes(data) != r.prev.Files[rel].Hash && !r.opt.Force {
			r.rep.Kept = append(r.rep.Kept, rel)
			r.next.Files[rel] = r.prev.Files[rel]
			continue
		}
		if err := os.Remove(abs); err != nil {
			r.errs = append(r.errs, fmt.Errorf("vault: remove %s: %w", rel, err))
			r.next.Files[rel] = r.prev.Files[rel]
			continue
		}
		r.rep.Removed++
		pruneEmptyDirs(r.dir, filepath.Dir(abs))
	}
}

// Render produces the note's bytes: front matter, then body.
func Render(n Note) []byte {
	var b strings.Builder
	if len(n.Front) > 0 {
		b.WriteString("---\n")
		for _, f := range n.Front {
			key := yamlKey(f.Key)
			if key == "" {
				continue
			}
			if f.IsList {
				if len(f.List) == 0 {
					continue
				}
				b.WriteString(key + ":\n")
				for _, v := range f.List {
					b.WriteString("  - " + yamlScalar(v) + "\n")
				}
				continue
			}
			b.WriteString(key + ": " + yamlScalar(f.Value) + "\n")
		}
		b.WriteString("---\n\n")
	}
	raw := n.Body
	if raw == "" && n.BodyFunc != nil {
		raw = n.BodyFunc()
	}
	body := strings.TrimRight(raw, "\n") + "\n"
	if b.Len()+len(body) > MaxNoteBytes {
		keep := MaxNoteBytes - b.Len() - 64
		if keep < 0 {
			keep = 0
		}
		body = cutUTF8(body, keep) + "\n\n> … truncated by chatcli (note exceeded 1 MiB)\n"
	}
	b.WriteString(body)
	return []byte(b.String())
}

// SafeRel validates and normalizes a note path: relative, '/'-separated,
// no "..", no empty or dot segments, ends in .md, outside the manifest dir.
func SafeRel(p string) (string, bool) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return "", false
	}
	clean := path.Clean(p)
	if clean != p || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	for _, seg := range strings.Split(clean, "/") {
		// ':' is a volume separator on Windows and invalid in file names
		// there; a leading dot hides the file (and is the manifest dir).
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || strings.ContainsRune(seg, ':') {
			return "", false
		}
	}
	if !strings.HasSuffix(strings.ToLower(clean), ".md") {
		return "", false
	}
	return clean, true
}

// FileName turns a title into a portable file-name segment: characters
// Obsidian, Windows or link syntax reject are replaced, whitespace
// collapsed, length bounded (in bytes, on a rune boundary). Never empty.
func FileName(title string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(title) {
		switch {
		case strings.ContainsRune(`/\:*?"<>|#^[]`, r), r < 0x20, r == 0x7f:
			r = ' '
		case unicode.IsSpace(r):
			r = ' '
		}
		if r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	name := strings.Trim(b.String(), ". ")
	name = cutUTF8(name, 96)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "untitled"
	}
	if isReservedWindowsName(name) {
		name += "_"
	}
	return name
}

// Tag turns a label into an Obsidian tag: letters, digits, '_', '-', '/'
// only (others become '-'), not purely numeric. Empty when nothing is left.
func Tag(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '/' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	t := strings.Trim(b.String(), "-/")
	if t == "" {
		return ""
	}
	if _, err := strconv.Atoi(t); err == nil {
		return "n" + t // Obsidian tags cannot be purely numeric
	}
	return t
}

// Link renders a wikilink to a note path, with label when it differs from
// the file name: [[Projects/Orbital|Orbital]].
func Link(notePath, label string) string {
	target := strings.TrimSuffix(notePath, ".md")
	label = strings.TrimSpace(strings.NewReplacer("[", "(", "]", ")", "|", "/", "\n", " ").Replace(label))
	if label == "" || label == path.Base(target) {
		return "[[" + target + "]]"
	}
	return "[[" + target + "|" + label + "]]"
}

// --- internals ---

func dedupePaths(notes []Note) []Note {
	sorted := append([]Note(nil), notes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	seen := make(map[string]int)
	for i := range sorted {
		key := strings.ToLower(sorted[i].Path)
		if n, dup := seen[key]; dup {
			seen[key] = n + 1
			base := strings.TrimSuffix(sorted[i].Path, ".md")
			sorted[i].Path = fmt.Sprintf("%s (%d).md", base, n+1)
			continue
		}
		seen[key] = 1
	}
	return sorted
}

func loadManifest(dir string) manifest {
	m := manifest{Files: map[string]manifestEntry{}}
	data, err := os.ReadFile(filepath.Join(dir, ManifestDir, manifestFile)) // #nosec G304 -- fixed name under the vault dir
	if err != nil {
		return m
	}
	var onDisk manifest
	if json.Unmarshal(data, &onDisk) != nil || onDisk.Files == nil {
		// A corrupt manifest forgets ownership: every existing file is
		// then treated as the person's and kept — the safe direction.
		return m
	}
	for rel, e := range onDisk.Files {
		if clean, ok := SafeRel(rel); ok && clean == rel {
			m.Files[rel] = e
		}
	}
	return m
}

func saveManifest(dir string, m manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := utils.AtomicWriteFile(filepath.Join(dir, ManifestDir, manifestFile), data, filePerm); err != nil {
		return fmt.Errorf("vault: write manifest: %w", err)
	}
	return nil
}

// writeNote replaces a note atomically (temp file + rename, so a reader
// never sees a half-written note) WITHOUT fsync: every note is derived and
// re-rendered on the next sync, and fsync per file (F_FULLFSYNC on macOS)
// would turn a first sync of thousands of notes into minutes of I/O. The
// manifest, which records ownership, is the one file written durably.
func writeNote(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func pruneEmptyDirs(root, dir string) {
	root = filepath.Clean(root)
	for d := filepath.Clean(dir); d != root && strings.HasPrefix(d, root+string(filepath.Separator)); d = filepath.Dir(d) {
		if err := os.Remove(d); err != nil { // fails (and stops) on a non-empty dir
			return
		}
	}
}

func hashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func yamlKey(k string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(k) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// yamlScalar renders a string as a YAML scalar that always parses back to
// the same string: plain when unambiguous, double-quoted (JSON escaping is
// valid YAML) otherwise.
func yamlScalar(v string) string {
	if v == "" {
		return `""`
	}
	plain := true
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		plain = false
	}
	if plain {
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			plain = false
		}
	}
	if plain {
		first := v[0]
		if strings.ContainsRune("-?:,[]{}#&*!|>'\"%@` ", rune(first)) || strings.HasSuffix(v, " ") ||
			strings.Contains(v, ": ") || strings.Contains(v, " #") || strings.ContainsAny(v, "\n\r\t") {
			plain = false
		}
	}
	if plain {
		return v
	}
	q, _ := json.Marshal(v)
	return string(q)
}

func cutUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func isReservedWindowsName(name string) bool {
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}
