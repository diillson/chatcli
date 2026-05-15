/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
// qg-diffcover computes patch coverage for the Quality Gate Floor 3.
//
// Usage:
//
//	qg-diffcover \
//	    -coverage coverage.out \
//	    -base origin/main \
//	    -threshold 60 \
//	    -markdown report.md \
//	    -strip-prefix github.com/diillson/chatcli \
//	    -strip-prefix github.com/diillson/chatcli/operator \
//	    -include '*.go' \
//	    -exclude '*_test.go' -exclude 'proto/**' -exclude 'tools/docgen/**'
//
// Exit codes:
//
//	0 — patch coverage >= threshold
//	1 — patch coverage < threshold (gate failure)
//	2 — usage / runtime error (NOT a gate verdict; treat as red)
//
// Outputs to stdout are key=value pairs that the wrapper script forwards
// into $GITHUB_OUTPUT:
//
//	percent=NN.N
//	covered=N
//	total=N
//	threshold=NN
//	uninstrumented=path/to/file.go,path/to/other.go
//	files_changed=N
//
// The markdown report is the same content the sticky comment renders;
// only written when -markdown is set.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/tools/qg/diffcover"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qg-diffcover:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("qg-diffcover", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		coveragePath   = fs.String("coverage", "coverage.out", "path to go coverprofile")
		baseRef        = fs.String("base", "origin/main", "base git ref for the diff")
		threshold      = fs.Float64("threshold", 60, "minimum patch coverage % to pass")
		markdownOut    = fs.String("markdown", "", "optional markdown report output path")
		includes       stringSliceFlag
		excludes       stringSliceFlag
		strips         stringSliceFlag
		pathThresholds stringSliceFlag
	)
	fs.Var(&includes, "include", "include path glob (may repeat)")
	fs.Var(&excludes, "exclude", "exclude path glob (may repeat)")
	fs.Var(&strips, "strip-prefix", "module-path prefix to strip from cover profile entries (may repeat)")
	fs.Var(&pathThresholds, "path-threshold", "per-path coverage requirement `pattern=PCT` (may repeat, e.g. `auth/**=80`)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(includes) == 0 {
		includes = stringSliceFlag{"*.go"}
	}

	// Parse the coverage profile (module-relative paths) and strip module
	// prefixes so the keys match `git diff` (repo-relative).
	profFile, err := os.Open(*coveragePath)
	if err != nil {
		return fmt.Errorf("open coverage: %w", err)
	}
	defer func() { _ = profFile.Close() }()

	profile, err := diffcover.ParseProfile(profFile)
	if err != nil {
		return err
	}
	if len(strips) > 0 {
		profile = profile.StripPrefixes(strips)
	}

	// Run `git diff` against the base ref and parse it. Using --unified=0
	// keeps context lines out of the way; we only want added new-file lines.
	diffBytes, err := gitDiff(*baseRef)
	if err != nil {
		return fmt.Errorf("git diff: %w", err)
	}
	diff, err := diffcover.ParseUnifiedDiff(bytes.NewReader(diffBytes))
	if err != nil {
		return err
	}

	include := func(path string) bool {
		return diffcover.MatchAny(path, includes) && diffcover.MatchNone(path, excludes)
	}
	result, uninstr := diffcover.Compute(profile, diff, include, *threshold)

	// Apply per-path thresholds AFTER the global computation. Each
	// `-path-threshold "pattern=PCT"` flag becomes one (Pattern, Threshold)
	// pair; any breach makes Result.Passed() return false.
	pathTs, perr := parsePathThresholds(pathThresholds)
	if perr != nil {
		return perr
	}
	result.PathBreaches = diffcover.ComputePathThresholds(result.Files, pathTs)

	emitKV(stdout, "percent", fmt.Sprintf("%.1f", result.Percent()))
	emitKV(stdout, "covered", fmt.Sprintf("%d", result.Covered))
	emitKV(stdout, "total", fmt.Sprintf("%d", result.Total))
	emitKV(stdout, "threshold", fmt.Sprintf("%.0f", *threshold))
	emitKV(stdout, "files_changed", fmt.Sprintf("%d", len(result.Files)))
	emitKV(stdout, "path_breaches", fmt.Sprintf("%d", len(result.PathBreaches)))
	if len(uninstr) > 0 {
		emitKV(stdout, "uninstrumented", strings.Join(uninstr, ","))
	}

	if *markdownOut != "" {
		md := result.FormatMarkdown()
		if len(uninstr) > 0 {
			md += "\n> **Warning:** these Go files in the diff had no coverage entries " +
				"(missing `-coverpkg=./...` on `go test`?):\n"
			for _, p := range uninstr {
				md += "> - `" + p + "`\n"
			}
		}
		if err := os.WriteFile(*markdownOut, []byte(md), 0o644); err != nil {
			return fmt.Errorf("write markdown: %w", err)
		}
	}

	// Uninstrumented Go files are a HARD failure — we cannot certify
	// coverage on code the tool never measured. The previous Python
	// pipeline silently reported 0% and was treated as passing.
	if len(uninstr) > 0 {
		return fmt.Errorf("uninstrumented Go files in diff (no -coverpkg=./...?): %s",
			strings.Join(uninstr, ", "))
	}

	if len(result.PathBreaches) > 0 {
		parts := make([]string, 0, len(result.PathBreaches))
		for _, b := range result.PathBreaches {
			parts = append(parts, fmt.Sprintf("%s: %.1f%% < %.0f%%", b.Pattern, b.Percent, b.Threshold))
		}
		return fmt.Errorf("per-path coverage breaches: %s", strings.Join(parts, "; "))
	}
	if !result.Passed() {
		return fmt.Errorf("patch coverage %.1f%% below threshold %.0f%%",
			result.Percent(), *threshold)
	}
	return nil
}

// parsePathThresholds parses `pattern=PCT` strings into PathThresholds.
// The PCT is read as a float so callers can ask for "80.5" if needed.
func parsePathThresholds(args []string) ([]diffcover.PathThreshold, error) {
	out := make([]diffcover.PathThreshold, 0, len(args))
	for _, a := range args {
		idx := strings.LastIndex(a, "=")
		if idx <= 0 || idx == len(a)-1 {
			return nil, fmt.Errorf("path-threshold %q: want `pattern=PCT`", a)
		}
		pct, err := strconv.ParseFloat(a[idx+1:], 64)
		if err != nil {
			return nil, fmt.Errorf("path-threshold %q: parse pct: %w", a, err)
		}
		out = append(out, diffcover.PathThreshold{Pattern: a[:idx], Threshold: pct})
	}
	return out, nil
}

func emitKV(w io.Writer, k, v string) { fmt.Fprintf(w, "%s=%s\n", k, v) }

// gitDiff shells out to `git diff <base>...HEAD --unified=0` and returns
// the raw output. Shelling out keeps us out of the libgit2/go-git
// dependency mess for a 30-line read.
func gitDiff(baseRef string) ([]byte, error) {
	// #nosec G204 — baseRef comes from the workflow config, not user input
	cmd := exec.Command("git", "diff", baseRef+"...HEAD", "--no-color", "--unified=0", "--diff-filter=AM")
	cmd.Stderr = os.Stderr
	return cmd.Output()
}
