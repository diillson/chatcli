/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package i18nparity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UsageRef records one i18n.T call site: the key it asks for and the
// source location for the error report.
type UsageRef struct {
	Key  string
	File string
	Line int
}

// ScanUsages walks rootDir recursively, parses every non-test .go file,
// and collects every call shaped as `i18n.T("literal-key", ...)`. Calls
// whose first argument is not a string literal are intentionally
// ignored: dynamic keys can't be statically verified and are a separate
// (manual) review concern.
//
// excludes are path prefixes (repo-relative) that the walker skips
// entirely — typical: "vendor", "tools/docgen", "proto".
func ScanUsages(rootDir string, excludes []string) ([]UsageRef, error) {
	rootDir = filepath.Clean(rootDir)

	var out []UsageRef
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rootDir, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			for _, ex := range excludes {
				if rel == ex || strings.HasPrefix(rel, ex+"/") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, ex := range excludes {
			if strings.HasPrefix(rel, ex+"/") {
				return nil
			}
		}

		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			// Tolerate parse errors — they show up in other gates (go vet,
			// build). We don't want a single broken file to short-circuit
			// the whole parity scan.
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "T" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "i18n" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, err := stripQuotes(lit.Value)
			if err != nil {
				return true
			}
			pos := fset.Position(lit.Pos())
			out = append(out, UsageRef{Key: key, File: rel, Line: pos.Line})
			return true
		})
		return nil
	})

	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// stripQuotes returns the unquoted contents of a Go string literal. Only
// handles double-quoted strings (the i18n.T convention); raw strings
// (`...`) are also accepted because the parser hands them in unchanged.
func stripQuotes(lit string) (string, error) {
	if len(lit) < 2 {
		return "", errBadString
	}
	first := lit[0]
	last := lit[len(lit)-1]
	if first != last {
		return "", errBadString
	}
	if first != '"' && first != '`' {
		return "", errBadString
	}
	return lit[1 : len(lit)-1], nil
}

var errBadString = stringErr("bad string literal")

type stringErr string

func (e stringErr) Error() string { return string(e) }

// UnknownUsages joins usage refs against the keys defined in any locale.
// Returns the refs whose key is not present in at least one locale —
// i.e. the call site will fall back to the raw key string at runtime in
// that locale.
func UnknownUsages(usages []UsageRef, locales []Locale) []UsageRef {
	known := map[string]struct{}{}
	for _, l := range locales {
		for k := range l.Keys {
			known[k] = struct{}{}
		}
	}
	out := make([]UsageRef, 0, len(usages))
	for _, u := range usages {
		if _, ok := known[u.Key]; ok {
			continue
		}
		// i18n.T() formats with substitutions: keys may end in a format
		// suffix like "%s" only in their value, not the key itself. The
		// chatcli convention has every key statically defined, so any
		// miss is a real bug.
		out = append(out, u)
	}
	return out
}
