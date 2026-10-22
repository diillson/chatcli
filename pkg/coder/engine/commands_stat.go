/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * commands_stat.go — stat answers "what is this file" before a read:
 * size, line count, whether it is text or binary, the encoding hints a
 * reader needs (CRLF, BOM, longest line), mode and modification time.
 * Cheap, so the model can size a read instead of paying for a 20k-line
 * file to learn it has 20k lines.
 */
package engine

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// statSampleBytes bounds the sniff used to decide text vs binary.
const statSampleBytes = 8192

// handleStat prints one block per file.
func (e *Engine) handleStat(args []string) error {
	fs := flag.NewFlagSet("stat", flag.ContinueOnError)
	file := fs.String("file", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	files := collectFiles(*file, fs.Args())
	if len(files) == 0 {
		return errors.New("--file is required")
	}
	for _, f := range files {
		if err := e.validatePath(f); err != nil {
			e.printf("❌ BLOCKED %s: %v\n", f, err)
			continue
		}
		out, err := statFile(f)
		if err != nil {
			e.printf("❌ ERROR STAT '%s': %v\n", f, err)
			continue
		}
		e.println(out)
	}
	return nil
}

// statFile builds the block for one path.
func statFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", path)
	if info.IsDir() {
		entries, rerr := os.ReadDir(path)
		if rerr != nil {
			return "", rerr
		}
		dirs, files := 0, 0
		for _, en := range entries {
			if en.IsDir() {
				dirs++
			} else {
				files++
			}
		}
		fmt.Fprintf(&b, "  type: directory (%d files, %d subdirectories)\n  modified: %s\n", files, dirs, info.ModTime().UTC().Format(time.RFC3339))
		return b.String(), nil
	}
	fmt.Fprintf(&b, "  size: %d bytes\n  mode: %s\n  modified: %s\n", info.Size(), info.Mode().Perm(), info.ModTime().UTC().Format(time.RFC3339))
	fh, err := os.Open(path) // #nosec G304 -- path validated by engine.validatePath
	if err != nil {
		return "", err
	}
	defer func() { _ = fh.Close() }()
	sample := make([]byte, statSampleBytes)
	n, _ := io.ReadFull(fh, sample)
	sample = sample[:n]
	if n == 0 {
		b.WriteString("  type: empty file\n")
		return b.String(), nil
	}
	if bytes.IndexByte(sample, 0) >= 0 || !utf8.Valid(bytes.TrimRight(sample, "\x80\x81\x82\x83\x84\x85\x86\x87\x88\x89\x8a\x8b\x8c\x8d\x8e\x8f\x90\x91\x92\x93\x94\x95\x96\x97\x98\x99\x9a\x9b\x9c\x9d\x9e\x9f\xa0\xa1\xa2\xa3\xa4\xa5\xa6\xa7\xa8\xa9\xaa\xab\xac\xad\xae\xaf\xb0\xb1\xb2\xb3\xb4\xb5\xb6\xb7\xb8\xb9\xba\xbb\xbc\xbd\xbe\xbf\xc0\xc1\xc2\xc3\xc4\xc5\xc6\xc7\xc8\xc9\xca\xcb\xcc\xcd\xce\xcf\xd0\xd1\xd2\xd3\xd4\xd5\xd6\xd7\xd8\xd9\xda\xdb\xdc\xdd\xde\xdf\xe0\xe1\xe2\xe3\xe4\xe5\xe6\xe7\xe8\xe9\xea\xeb\xec\xed\xee\xef\xf0\xf1\xf2\xf3\xf4\xf5\xf6\xf7\xf8\xf9\xfa\xfb\xfc\xfd\xfe\xff")) {
		b.WriteString("  type: binary (read with encoding base64, or not at all)\n")
		return b.String(), nil
	}
	if _, err := fh.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	// The scanner strips the CR of a CRLF ending, so the ending is read
	// off the sample instead.
	lines, longest, crlf := 0, 0, bytes.Contains(sample, []byte("\r\n"))
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lines++
		if l := len(sc.Bytes()); l > longest {
			longest = l
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "  type: text, %d lines, longest line %d bytes", lines, longest)
	if crlf {
		b.WriteString(", CRLF line endings")
	}
	if bytes.HasPrefix(sample, []byte{0xEF, 0xBB, 0xBF}) {
		b.WriteString(", UTF-8 BOM")
	}
	b.WriteString("\n")
	if lines > 2000 {
		fmt.Fprintf(&b, "  hint: read it in ranges (--start/--end), or outline/search it first; a whole read is capped at 2000 lines\n")
	}
	return b.String(), nil
}
