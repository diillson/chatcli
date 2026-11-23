/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package depbot decides whether a Dependabot pull request may be merged
// without a human.
//
// The policy is an allowlist: a pull request is eligible only when every
// changed line is recognized as a plain version bump of something the
// repository already depends on. Anything else — a new direct dependency,
// a major version, a pre-release, a changed go/toolchain/replace
// directive, a file outside the dependency manifests — holds the pull
// request for review. The checks a human used to run by eye on every
// weekly batch live here so they run the same way every time.
package depbot

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Bump kinds.
const (
	KindGoDirect   = "go-direct"
	KindGoIndirect = "go-indirect"
	KindAction     = "action"
	KindImage      = "image"
)

// Bump is one recognized version change.
type Bump struct {
	File string `json:"file"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Policy is the verdict on a pull request diff.
type Policy struct {
	// Eligible is true when no rule objected.
	Eligible bool `json:"eligible"`
	// Reasons lists every objection, one per line of the report.
	Reasons []string `json:"reasons,omitempty"`
	Bumps   []Bump   `json:"bumps,omitempty"`
	// NeedsDepsLabel is true when the root go.mod gains require lines,
	// which Quality Gate Floor 4 only accepts under the deps:approved
	// label.
	NeedsDepsLabel bool `json:"needs_deps_label"`
}

func (p *Policy) hold(format string, args ...any) {
	p.Reasons = append(p.Reasons, fmt.Sprintf(format, args...))
}

// EvaluateDiff applies the policy to a unified diff.
func EvaluateDiff(diff string) Policy {
	var p Policy
	files := parseDiff(diff)
	if len(files) == 0 {
		p.hold("the diff is empty")
	}
	for _, f := range files {
		evaluateFile(&p, f)
	}
	p.Eligible = len(p.Reasons) == 0
	return p
}

var actionFileRE = regexp.MustCompile(`^\.github/(workflows/[^/]+|actions/.+/action)\.ya?ml$`)

func evaluateFile(p *Policy, f *fileChange) {
	if f.shape != "" {
		p.hold("%s: file is %s, a version bump only edits lines in place", f.path, f.shape)
		return
	}
	switch {
	case f.path == "go.sum" || f.path == "operator/go.sum":
		// Checksums follow go.mod; the build and vulnerability checks
		// are what validate them.
	case f.path == "go.mod" || f.path == "operator/go.mod":
		evaluateGoMod(p, f)
	case actionFileRE.MatchString(f.path):
		evaluatePairs(p, f, KindAction, parseUses)
	case strings.HasPrefix(path.Base(f.path), "Dockerfile"):
		evaluatePairs(p, f, KindImage, parseFrom)
	default:
		p.hold("%s: not a dependency manifest", f.path)
	}
}

// --- go.mod -----------------------------------------------------------

type modLine struct {
	version  string
	indirect bool
}

// requireRE matches a require entry, block or single-line form. The module
// path must contain a dot so directive keywords (go, toolchain, exclude,
// retract) can never be read as a module, and a `replace` line never
// matches because of its `=>` operand.
var requireRE = regexp.MustCompile(`^\s*(?:require\s+)?([A-Za-z0-9][A-Za-z0-9._~-]*\.[A-Za-z0-9._~/-]+)\s+(v[^\s/]+)\s*(//\s*indirect)?\s*$`)

// blockPunctuation are the lines `go mod tidy` adds or removes when it
// opens or closes a require block.
func blockPunctuation(line string) bool {
	switch strings.TrimSpace(line) {
	case "", "require (", ")":
		return true
	}
	return false
}

// collectRequires indexes require entries by module path. Lines that are
// neither a require entry nor block punctuation are returned as strays.
//
// A diff does not say which block a line sits in, so an entry inside an
// existing exclude or retract block would read as a require entry. Neither
// go.mod has such a block (TestRepositoryGoModsHaveNoExcludeOrRetract pins
// that), and opening one needs an `exclude (` line, which is a stray.
func collectRequires(lines []string) (map[string]modLine, []string) {
	mods := map[string]modLine{}
	var strays []string
	for _, l := range lines {
		if m := requireRE.FindStringSubmatch(l); m != nil {
			mods[m[1]] = modLine{version: m[2], indirect: m[3] != ""}
			continue
		}
		if !blockPunctuation(l) {
			strays = append(strays, strings.TrimSpace(l))
		}
	}
	return mods, strays
}

func evaluateGoMod(p *Policy, f *fileChange) {
	old, oldStrays := collectRequires(f.removed())
	cur, curStrays := collectRequires(f.added())
	for _, s := range append(oldStrays, curStrays...) {
		p.hold("%s: line is not a require entry: %q", f.path, s)
	}
	if f.path == "go.mod" && len(cur) > 0 {
		p.NeedsDepsLabel = true
	}
	for _, name := range sortedKeys(old) {
		if _, kept := cur[name]; !kept && !old[name].indirect {
			p.hold("%s: direct dependency %s removed", f.path, name)
		}
	}
	for _, name := range sortedKeys(cur) {
		n := cur[name]
		o, existed := old[name]
		kind := KindGoDirect
		if n.indirect {
			kind = KindGoIndirect
		}
		p.Bumps = append(p.Bumps, Bump{File: f.path, Kind: kind, Name: name, From: o.version, To: n.version})
		if !existed {
			if !n.indirect {
				p.hold("%s: new direct dependency %s %s", f.path, name, n.version)
			}
			continue
		}
		checkBump(p, f.path, name, o.version, n.version, !n.indirect)
	}
}

// checkBump applies the version rules shared by every ecosystem. strict
// adds the rules for things the repository chooses itself: no major jump
// and no pre-release. Indirect Go modules are exempt from those two — the
// upstream that requires them picks their version, pseudo-versions are
// routine there, and the build, vulnerability and dependency-review
// checks still gate them.
func checkBump(p *Policy, file, name, from, to string, strict bool) {
	o, okOld := parseVersion(from)
	n, okNew := parseVersion(to)
	if !okOld || !okNew {
		p.hold("%s: %s has a version the policy cannot read (%s -> %s)", file, name, from, to)
		return
	}
	if n.compare(o) < 0 {
		p.hold("%s: %s is downgraded (%s -> %s)", file, name, from, to)
	}
	if !strict {
		return
	}
	if n.major != o.major {
		p.hold("%s: %s changes major version (%s -> %s)", file, name, from, to)
	}
	if n.pre != "" {
		p.hold("%s: %s moves to a pre-release or pseudo-version (%s)", file, name, to)
	}
}

// --- line-for-line manifests (workflows, Dockerfiles) -----------------

// pinned is one parsed reference: what is pinned, its version, and the
// rest of the line that must not change in a bump.
type pinned struct {
	name    string
	version string
	frame   string
}

type pinParser func(line string) (pinned, bool)

// usesRE matches the repository's pinning convention for actions:
//
//	uses: owner/repo[/path]@<40-hex commit> # vX[.Y[.Z]]
var usesRE = regexp.MustCompile(`^(\s*(?:-\s+)?uses:\s+)([A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+)@[0-9a-f]{40}\s+#\s*(v?\d+(?:\.\d+){0,2})\s*$`)

func parseUses(line string) (pinned, bool) {
	m := usesRE.FindStringSubmatch(line)
	if m == nil {
		return pinned{}, false
	}
	return pinned{name: m[2], version: m[3], frame: m[1]}, true
}

// fromRE matches `FROM [--platform=x] image:tag[@sha256:digest] [AS name]`.
var fromRE = regexp.MustCompile(`^(FROM\s+(?:--platform=\S+\s+)?)(\S+):([A-Za-z0-9_.-]+)(?:@sha256:[0-9a-f]{64})?(\s+[Aa][Ss]\s+\S+)?\s*$`)

// tagRE splits an image tag into a dotted version and a variant suffix
// (1.27.4-alpine). A tag must have at least major.minor: floating or
// dotless forms such as `latest`, `nonroot` or golang's `1.27rc2` are not
// versions the policy can order.
var tagRE = regexp.MustCompile(`^(v?\d+\.\d+(?:\.\d+)?)(-[A-Za-z0-9_.-]+)?$`)

var preReleaseTagRE = regexp.MustCompile(`(?i)(rc|beta|alpha|preview|dev)\d*`)

func parseFrom(line string) (pinned, bool) {
	m := fromRE.FindStringSubmatch(line)
	if m == nil {
		return pinned{}, false
	}
	t := tagRE.FindStringSubmatch(m[3])
	if t == nil || preReleaseTagRE.MatchString(m[3]) {
		return pinned{}, false
	}
	// The variant suffix is part of the frame: 1.27.4-alpine may become
	// 1.27.5-alpine but not 1.27.5-bookworm.
	return pinned{name: m[2], version: t[1], frame: m[1] + "|" + t[2] + "|" + strings.ToLower(strings.TrimSpace(m[4]))}, true
}

// evaluatePairs handles manifests where a bump rewrites lines one for one.
// Within each hunk the i-th removed line must be the old form of the i-th
// added line.
func evaluatePairs(p *Policy, f *fileChange, kind string, parse pinParser) {
	for _, h := range f.hunks {
		if len(h.removed) != len(h.added) {
			p.hold("%s: lines were added or removed, a version bump only rewrites them", f.path)
			continue
		}
		for i := range h.added {
			o, okOld := parse(h.removed[i])
			n, okNew := parse(h.added[i])
			switch {
			case !okOld || !okNew:
				p.hold("%s: line is not a pinned version reference: %q", f.path, strings.TrimSpace(h.added[i]))
			case o.name != n.name || o.frame != n.frame:
				p.hold("%s: %s is replaced by something else: %q", f.path, o.name, strings.TrimSpace(h.added[i]))
			default:
				p.Bumps = append(p.Bumps, Bump{File: f.path, Kind: kind, Name: n.name, From: o.version, To: n.version})
				checkBump(p, f.path, n.name, o.version, n.version, true)
			}
		}
	}
}

func sortedKeys(m map[string]modLine) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
