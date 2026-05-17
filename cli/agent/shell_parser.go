/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ShellSegment represents one shell command — a single invocation that is
// not itself a pipeline or compound statement. Two `cat` commands separated
// by `|`, `&&`, `||`, or `;` produce two ShellSegments. Each segment is the
// granularity at which we apply per-command dangerous-pattern matching and
// inline-code classification.
//
// The Full string is the literal text of just this segment (no operators);
// Cmd is the first word (the program name), and Args is everything after
// (including flags). Quoting is preserved in Full but stripped from Args
// so callers can pattern-match flag values directly.
type ShellSegment struct {
	Cmd      string
	Args     []string
	Full     string
	HasPipe  bool // true if this segment is part of a pipeline
	Position int  // 0-based position in the top-level command sequence
}

// ParseShellSegments splits a shell command line into segments using a real
// shell parser (mvdan.cc/sh/v3/syntax). Compared to splitting by "|" or
// strings.Fields, this is robust against:
//
//   - Quoted operators:    `echo "a | b" | grep a`       → 2 segments, not 3
//   - Heredocs:            `cat <<EOF\n|||\nEOF`         → 1 segment
//   - Escaped pipes:       `printf 'a\\|b\\|c'`           → 1 segment
//   - Subshells:           `(cd /tmp && ls)`              → emits inner cmds
//   - Background:          `sleep 1 & echo done`          → 2 segments
//
// On parse failure the function falls back to a permissive single-segment
// result (the whole line as one segment). Callers must treat the segments
// as a best-effort decomposition, not a security boundary on their own —
// the dangerous-pattern matcher still runs against the full line as a
// belt-and-suspenders measure.
func ParseShellSegments(line string) []ShellSegment {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(trimmed), "")
	if err != nil {
		// Couldn't parse — return whole line as one segment so callers can
		// still run their regex matchers. Returning nil would silently bypass
		// the dangerous-pattern check.
		return []ShellSegment{singleSegment(trimmed, false, 0)}
	}

	var out []ShellSegment
	var position int
	for _, stmt := range file.Stmts {
		walkStmt(stmt, false, &position, &out)
	}
	if len(out) == 0 {
		out = append(out, singleSegment(trimmed, false, 0))
	}
	return out
}

// walkStmt recursively flattens a statement into its constituent simple
// commands, recording whether each lives inside a pipeline so callers can
// apply different policies to piped right-hand sides (pure stdin consumers
// like `grep`, `jq` are typically safe even when their input is from a
// privileged source).
func walkStmt(stmt *syntax.Stmt, insidePipe bool, position *int, out *[]ShellSegment) {
	if stmt == nil {
		return
	}
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		segment := segmentFromCall(cmd, insidePipe, *position)
		*out = append(*out, segment)
		*position++
	case *syntax.BinaryCmd:
		// && || are not pipes; only Pipe sets insidePipe=true on the right.
		pipeRight := insidePipe || cmd.Op == syntax.Pipe || cmd.Op == syntax.PipeAll
		walkStmt(cmd.X, insidePipe, position, out)
		walkStmt(cmd.Y, pipeRight, position, out)
	case *syntax.Block:
		for _, inner := range cmd.Stmts {
			walkStmt(inner, insidePipe, position, out)
		}
	case *syntax.Subshell:
		for _, inner := range cmd.Stmts {
			walkStmt(inner, insidePipe, position, out)
		}
	case *syntax.IfClause:
		// IfClause has Cond + Then + (optional Else, recursively itself).
		// We walk every branch — security policy applies across all paths.
		for branch := cmd; branch != nil; branch = branch.Else {
			for _, inner := range branch.Cond {
				walkStmt(inner, insidePipe, position, out)
			}
			for _, inner := range branch.Then {
				walkStmt(inner, insidePipe, position, out)
			}
		}
	case *syntax.WhileClause, *syntax.ForClause, *syntax.CaseClause:
		// These structures contain inner statements but the syntax package
		// already walks them via stmt.Cmd recursion if we re-emit via
		// Walk. For the security path we only need to surface the inner
		// CallExprs that may carry dangerous commands.
		syntax.Walk(stmt, func(n syntax.Node) bool {
			if call, ok := n.(*syntax.CallExpr); ok && call != stmt.Cmd {
				*out = append(*out, segmentFromCall(call, insidePipe, *position))
				*position++
			}
			return true
		})
	default:
		// Unknown node — capture as raw segment so it isn't silently dropped.
		printer := syntax.NewPrinter()
		var buf strings.Builder
		_ = printer.Print(&buf, stmt)
		*out = append(*out, singleSegment(strings.TrimSpace(buf.String()), insidePipe, *position))
		*position++
	}
}

// segmentFromCall builds a ShellSegment from a syntax.CallExpr. The Full
// field round-trips through the syntax printer so command substitution and
// quoting are preserved exactly as written.
func segmentFromCall(call *syntax.CallExpr, insidePipe bool, position int) ShellSegment {
	printer := syntax.NewPrinter()
	var buf strings.Builder
	_ = printer.Print(&buf, call)
	full := strings.TrimSpace(buf.String())

	var cmd string
	args := make([]string, 0, len(call.Args))
	for i, word := range call.Args {
		lit := wordLiteral(word)
		if i == 0 {
			cmd = lit
			continue
		}
		args = append(args, lit)
	}
	return ShellSegment{
		Cmd:      cmd,
		Args:     args,
		Full:     full,
		HasPipe:  insidePipe,
		Position: position,
	}
}

// wordLiteral extracts the literal string value of a syntax.Word, joining
// its parts. Quoted parts are joined without their quote characters so
// regex matching against flag values like `-c "print(1)"` works against
// the bare content.
func wordLiteral(word *syntax.Word) string {
	if word == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					b.WriteString(lit.Value)
				}
			}
		default:
			// Command substitution, parameter expansion, etc. — keep as raw text.
			printer := syntax.NewPrinter()
			var sub strings.Builder
			_ = printer.Print(&sub, part)
			b.WriteString(sub.String())
		}
	}
	return b.String()
}

// singleSegment is the fallback constructor used when parsing fails or a
// non-CallExpr statement is encountered. It treats the raw text as one
// opaque command so pattern matching still works.
func singleSegment(text string, hasPipe bool, position int) ShellSegment {
	fields := strings.Fields(text)
	var cmd string
	var args []string
	if len(fields) > 0 {
		cmd = fields[0]
		args = fields[1:]
	}
	return ShellSegment{
		Cmd:      cmd,
		Args:     args,
		Full:     text,
		HasPipe:  hasPipe,
		Position: position,
	}
}

// IsInlineCodeInvocation returns true when the segment looks like
// `interpreter -flag inline-code` for one of the languages we analyze
// dynamically. The expected exec flag (-c, -e, -r) is returned so callers
// can find the next arg = the inline source.
func (s ShellSegment) IsInlineCodeInvocation() (lang string, flagPos int, ok bool) {
	cmd := strings.ToLower(baseName(s.Cmd))
	expectedFlag, ok := inlineCodeFlags[cmd]
	if !ok {
		return "", 0, false
	}
	for i, a := range s.Args {
		if a == expectedFlag {
			return cmd, i, true
		}
	}
	return "", 0, false
}

// InlineSource returns the inline source code passed via -c / -e / -r.
// Assumes IsInlineCodeInvocation returned true with the given flag pos.
func (s ShellSegment) InlineSource(flagPos int) string {
	if flagPos+1 >= len(s.Args) {
		return ""
	}
	return s.Args[flagPos+1]
}

// baseName strips a directory prefix from an executable path so
// `/usr/bin/python3` and `python3` are treated identically.
func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// inlineCodeFlags lists the interpreters we recognize as accepting inline
// source via a flag. Used by IsInlineCodeInvocation to detect the input
// shape before handing the source over to the risk classifier. The flag
// values match the interpreters' standard CLIs and have not changed in
// decades — they are safe to keep as a hardcoded table.
var inlineCodeFlags = map[string]string{
	"python":  "-c",
	"python2": "-c",
	"python3": "-c",
	"perl":    "-e",
	"ruby":    "-e",
	"node":    "-e",
	"nodejs":  "-e",
	"php":     "-r",
	"lua":     "-e",
}

// pureStdinConsumers is the allowlist of right-of-pipe commands that have
// no side effects: they read stdin, transform, write stdout. Listed here so
// the validator can short-circuit "safe | safe" pipelines like `ls | jq`
// without forcing the user through a security prompt.
//
// We do not include commands that *can* be dangerous depending on flags
// (e.g. `tee` writes files — only `tee /dev/null` is in the safe set, and
// is handled separately by checking the flag).
var pureStdinConsumers = map[string]struct{}{
	"grep":   {},
	"egrep":  {},
	"fgrep":  {},
	"rg":     {},
	"jq":     {},
	"yq":     {},
	"awk":    {},
	"gawk":   {},
	"head":   {},
	"tail":   {},
	"cut":    {},
	"sort":   {},
	"uniq":   {},
	"wc":     {},
	"column": {},
	"pr":     {},
	"nl":     {},
	"fold":   {},
	"tr":     {},
	"rev":    {},
	"cat":    {},
	"less":   {},
	"more":   {},
}

// IsPureStdinConsumer returns true when the segment is a known no-side-effect
// transformer that is safe regardless of who is feeding it data. Used by the
// classifier to decide that `<anything> | jq .` should not be elevated to
// dangerous just because the left-hand side touches files.
func (s ShellSegment) IsPureStdinConsumer() bool {
	if _, ok := pureStdinConsumers[strings.ToLower(baseName(s.Cmd))]; ok {
		return true
	}
	if strings.ToLower(baseName(s.Cmd)) == "sed" {
		// sed without -i (in-place) is read-only.
		for _, a := range s.Args {
			if a == "-i" || strings.HasPrefix(a, "-i") {
				return false
			}
		}
		return true
	}
	return false
}
