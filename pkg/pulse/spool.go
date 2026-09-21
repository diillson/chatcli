/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// On-disk layout, one directory per process:
//
//	<root>/<instance>/meta.json                      who this process is + heartbeat
//	<root>/<instance>/events-<first seq>.ndjson      append-only segments
//
// A segment is named after the first sequence number it holds, so a reader
// with a cursor finds the right segment from the directory listing alone and
// never re-reads history it has already seen. Segments rotate by size and
// the oldest are deleted, which bounds what one process can ever write.
const (
	metaFileName  = "meta.json"
	segmentPrefix = "events-"
	segmentSuffix = ".ndjson"
	leaseFileName = "lease.json"

	dirPerm  = 0o750
	filePerm = 0o600

	// DefaultSegmentBytes and DefaultMaxSegments cap one process at
	// roughly 16 MB of spool.
	DefaultSegmentBytes = 4 << 20
	DefaultMaxSegments  = 4

	// HeartbeatInterval is how often a live process refreshes meta.json;
	// StaleAfter is how long without a refresh before readers call it dead.
	HeartbeatInterval = 5 * time.Second
	StaleAfter        = 20 * time.Second

	// DefaultRetention is how long a dead process's spool is kept.
	DefaultRetention = 24 * time.Hour
)

// Meta identifies the process behind an instance directory.
type Meta struct {
	Instance  string    `json:"instance"`
	PID       int       `json:"pid"`
	Surface   string    `json:"surface"`
	Version   string    `json:"version,omitempty"`
	WorkDir   string    `json:"workdir,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Heartbeat time.Time `json:"heartbeat"`
	Ended     bool      `json:"ended,omitempty"`
}

// Alive reports whether the process was heard from recently.
func (m Meta) Alive(now time.Time) bool {
	return !m.Ended && now.Sub(m.Heartbeat) < StaleAfter
}

// DefaultRoot is ~/.chatcli/pulse.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("pulse: resolving home: %w", err)
	}
	return filepath.Join(home, ".chatcli", "pulse"), nil
}

// Spool appends the events of one process to its instance directory.
type Spool struct {
	dir          string
	meta         Meta
	segmentBytes int64
	maxSegments  int

	file    *os.File
	w       *bufio.Writer
	written int64
}

// OpenSpool creates the instance directory and writes the first meta.json.
func OpenSpool(root string, meta Meta) (*Spool, error) {
	if meta.Instance == "" || meta.Instance != filepath.Base(meta.Instance) {
		return nil, fmt.Errorf("pulse: invalid instance %q", meta.Instance)
	}
	dir := filepath.Join(root, meta.Instance)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("pulse: creating spool: %w", err)
	}
	now := time.Now()
	if meta.StartedAt.IsZero() {
		meta.StartedAt = now
	}
	meta.Heartbeat = now
	meta.Ended = false
	s := &Spool{dir: dir, meta: meta, segmentBytes: DefaultSegmentBytes, maxSegments: DefaultMaxSegments}
	if err := s.writeMeta(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetLimits overrides the rotation bounds. Non-positive values keep the
// defaults.
func (s *Spool) SetLimits(segmentBytes int64, maxSegments int) {
	if segmentBytes > 0 {
		s.segmentBytes = segmentBytes
	}
	if maxSegments > 0 {
		s.maxSegments = maxSegments
	}
}

// Append writes events to the current segment, rotating first when it is
// full, and flushes once per batch.
func (s *Spool) Append(events ...Event) error {
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			continue // an unencodable event is dropped, never fatal
		}
		if s.file == nil || s.written+int64(len(line))+1 > s.segmentBytes {
			if err := s.rotate(ev.Seq); err != nil {
				return err
			}
		}
		n, err := s.w.Write(append(line, '\n'))
		s.written += int64(n)
		if err != nil {
			return fmt.Errorf("pulse: appending event: %w", err)
		}
	}
	if s.w != nil {
		if err := s.w.Flush(); err != nil {
			return fmt.Errorf("pulse: flushing spool: %w", err)
		}
	}
	return nil
}

func (s *Spool) rotate(firstSeq uint64) error {
	if err := s.closeSegment(); err != nil {
		return err
	}
	name := fmt.Sprintf("%s%020d%s", segmentPrefix, firstSeq, segmentSuffix)
	// #nosec G304 -- path is the spool dir this process created plus a name built from a sequence number
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("pulse: opening segment: %w", err)
	}
	s.file, s.w, s.written = f, bufio.NewWriter(f), 0

	segs := listSegments(s.dir)
	for len(segs) > s.maxSegments {
		_ = os.Remove(filepath.Join(s.dir, segs[0].name))
		segs = segs[1:]
	}
	return nil
}

func (s *Spool) closeSegment() error {
	if s.file == nil {
		return nil
	}
	flushErr := s.w.Flush()
	closeErr := s.file.Close()
	s.file, s.w = nil, nil
	return errors.Join(flushErr, closeErr)
}

// Beat refreshes the heartbeat so readers keep treating the process as live.
func (s *Spool) Beat() error {
	s.meta.Heartbeat = time.Now()
	return s.writeMeta()
}

// Close flushes the open segment and marks the process as ended.
func (s *Spool) Close() error {
	err := s.closeSegment()
	s.meta.Heartbeat = time.Now()
	s.meta.Ended = true
	return errors.Join(err, s.writeMeta())
}

func (s *Spool) writeMeta() error {
	data, err := json.Marshal(s.meta)
	if err != nil {
		return fmt.Errorf("pulse: encoding meta: %w", err)
	}
	return atomicWrite(filepath.Join(s.dir, metaFileName), data)
}

// atomicWrite is temp-file-then-rename in the same directory. The package
// is a leaf and cannot use utils.AtomicWriteFile; telemetry also does not
// need its fsync — a torn meta.json only ever costs one heartbeat.
func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("pulse: temp file: %w", err)
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr, os.Chmod(tmpName, filePerm)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pulse: writing %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pulse: replacing %s: %w", filepath.Base(path), err)
	}
	return nil
}

type segment struct {
	name     string
	firstSeq uint64
}

// listSegments returns the segments of an instance dir, oldest first.
func listSegments(dir string) []segment {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	segs := make([]segment, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, segmentPrefix) || !strings.HasSuffix(name, segmentSuffix) {
			continue
		}
		num := strings.TrimSuffix(strings.TrimPrefix(name, segmentPrefix), segmentSuffix)
		first, err := strconv.ParseUint(num, 10, 64)
		if err != nil {
			continue
		}
		segs = append(segs, segment{name: name, firstSeq: first})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].firstSeq < segs[j].firstSeq })
	return segs
}

// ListInstances returns every process that has a spool under root, live
// ones first and then most recently heard from. Unreadable or foreign
// directories are skipped.
func ListInstances(root string) []Meta {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	now := time.Now()
	out := make([]Meta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// #nosec G304 -- name comes from enumerating root, not from a caller
		data, err := os.ReadFile(filepath.Join(root, e.Name(), metaFileName))
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(data, &m) != nil || m.Instance != e.Name() {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		ai, aj := out[i].Alive(now), out[j].Alive(now)
		if ai != aj {
			return ai
		}
		return out[i].Heartbeat.After(out[j].Heartbeat)
	})
	return out
}

// ReadSince returns up to limit events of one instance with Seq greater
// than afterSeq, oldest first, and the cursor to pass next time. The
// instance is only ever compared against directories enumerated from root —
// no path is built from the argument, so a hostile value can at most fail
// to match.
func ReadSince(root, instance string, afterSeq uint64, limit int) ([]Event, uint64, error) {
	var dir string
	for _, m := range ListInstances(root) {
		if m.Instance == instance {
			dir = filepath.Join(root, m.Instance)
			break
		}
	}
	if dir == "" {
		return nil, afterSeq, fmt.Errorf("pulse: no instance %q", instance)
	}
	if limit <= 0 {
		limit = 1000
	}

	segs := listSegments(dir)
	// Skip segments that end before the cursor: segment i holds
	// [firstSeq(i), firstSeq(i+1)), so it is relevant only while the next
	// one starts after the cursor.
	start := 0
	for i := 0; i+1 < len(segs); i++ {
		if segs[i+1].firstSeq <= afterSeq+1 {
			start = i + 1
		}
	}

	next := afterSeq
	var out []Event
	for _, seg := range segs[start:] {
		// #nosec G304 -- dir and segment names both come from directory listings
		data, err := os.ReadFile(filepath.Join(dir, seg.name))
		if err != nil {
			continue // rotated away between listing and reading
		}
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var ev Event
			if json.Unmarshal(line, &ev) != nil || ev.Seq <= afterSeq {
				continue // torn last line of a live segment, or already seen
			}
			out = append(out, ev)
			next = ev.Seq
			if len(out) >= limit {
				return out, next, nil
			}
		}
	}
	return out, next, nil
}

// PruneInstances removes the spools of processes that are dead and were
// last heard from more than olderThan ago. skip names an instance that is
// never removed (the caller's own). It returns how many were removed.
func PruneInstances(root string, olderThan time.Duration, skip string) int {
	removed := 0
	for _, m := range PrunableInstances(root, olderThan, skip, time.Now()) {
		if os.RemoveAll(filepath.Join(root, m.Instance)) == nil {
			removed++
		}
	}
	return removed
}

// PrunableInstances lists what PruneInstances would remove, so an inventory
// can report it without deleting anything.
func PrunableInstances(root string, olderThan time.Duration, skip string, now time.Time) []Meta {
	var out []Meta
	for _, m := range ListInstances(root) {
		if m.Instance == skip || m.Alive(now) || now.Sub(m.Heartbeat) < olderThan {
			continue
		}
		out = append(out, m)
	}
	return out
}
