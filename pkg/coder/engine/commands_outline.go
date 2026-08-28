/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * commands_outline.go — code-structure orientation for the coder engine.
 *
 * `outline` renders one file's symbol skeleton (declarations + line numbers)
 * and `map` renders a token-budgeted structural map of a whole tree — the
 * "repo map" every large-codebase agent needs to orient itself without
 * reading whole files. Go files are parsed natively with go/ast (exact
 * signatures, zero dependencies); other languages get a pattern-based
 * outline covering the common declaration forms.
 */
package engine

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultMapBudget bounds the map output in characters: enough structure to
// orient, small enough to not flood a turn.
const DefaultMapBudget = 8000

// maxOutlineEntries bounds a single-file outline.
const maxOutlineEntries = 300

// handleOutline renders the symbol skeleton of one file.
func (e *Engine) handleOutline(args []string) error {
	fs := flag.NewFlagSet("outline", flag.ContinueOnError)
	file := fs.String("file", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*file) == "" {
		return fmt.Errorf("--file requerido")
	}
	path := expandPath(*file)
	entries, lang, err := outlineFile(path)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		e.printf("%s: no declarations recognized (%s outline)\n", *file, lang)
		return nil
	}
	e.printf("Outline of %s (%s, %d declaration(s)):\n", *file, lang, len(entries))
	for i, en := range entries {
		if i >= maxOutlineEntries {
			e.printf("… and %d more.\n", len(entries)-i)
			break
		}
		e.printf("%s\n", en)
	}
	return nil
}

// handleMap renders a budgeted structural map of a directory tree: files
// ranked by how much structure they export, each with its declaration
// skeleton, until the character budget is spent.
func (e *Engine) handleMap(args []string) error {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	budget := fs.Int("budget", DefaultMapBudget, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	root := expandPath(*dir)

	type fileMap struct {
		rel     string
		entries []string
		weight  int
	}
	var files []fileMap
	ignore := map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true}

	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() {
			if (strings.HasPrefix(name, ".") && p != root) || ignore[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if !isMappableSource(name) || info.Size() > 1<<20 {
			return nil
		}
		entries, _, err := outlineFile(p)
		if err != nil || len(entries) == 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		files = append(files, fileMap{rel: filepath.ToSlash(rel), entries: entries, weight: len(entries)})
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if len(files) == 0 {
		e.printf("No source files with recognizable structure under %s\n", *dir)
		return nil
	}

	// Most structure first; path as the deterministic tiebreak.
	sort.Slice(files, func(i, j int) bool {
		if files[i].weight != files[j].weight {
			return files[i].weight > files[j].weight
		}
		return files[i].rel < files[j].rel
	})

	if *budget <= 0 {
		*budget = DefaultMapBudget
	}
	spent := 0
	rendered := 0
	e.printf("Repo map of %s (%d file(s) with structure, budget %d chars):\n", *dir, len(files), *budget)
	for _, f := range files {
		block := "\n" + f.rel + ":\n  " + strings.Join(f.entries, "\n  ") + "\n"
		if spent+len(block) > *budget {
			break
		}
		e.printf("%s", block)
		spent += len(block)
		rendered++
	}
	if rendered < len(files) {
		e.printf("\n… %d more file(s) beyond the budget — run outline on specific files, or raise --budget.\n", len(files)-rendered)
	}
	return nil
}

// isMappableSource reports whether a filename is worth outlining for the map.
func isMappableSource(name string) bool {
	if strings.HasSuffix(name, "_test.go") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".java", ".rb", ".rs", ".kt", ".cs", ".php":
		return true
	}
	return false
}

// outlineFile dispatches per language: exact go/ast outlines for Go, pattern
// outlines for the rest. Returns the rendered entries and the parser used.
func outlineFile(path string) ([]string, string, error) {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		entries, err := outlineGo(path)
		return entries, "go/ast", err
	}
	entries, err := outlineGeneric(path)
	return entries, "pattern", err
}

// outlineGo renders a Go file's declarations with exact signatures.
func outlineGo(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []string
	add := func(pos token.Pos, text string) {
		out = append(out, fmt.Sprintf("L%d %s", fset.Position(pos).Line, text))
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			recv := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv = "(" + typeText(d.Recv.List[0].Type) + ") "
			}
			add(d.Pos(), "func "+recv+d.Name.Name+funcSignature(d.Type))
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					add(s.Pos(), kind+" "+s.Name.Name)
				case *ast.ValueSpec:
					if d.Tok == token.CONST || d.Tok == token.VAR {
						for _, n := range s.Names {
							if n.Name == "_" {
								continue
							}
							add(n.Pos(), strings.ToLower(d.Tok.String())+" "+n.Name)
						}
					}
				}
			}
		}
	}
	return out, nil
}

// funcSignature renders a compact parameter/result signature.
func funcSignature(t *ast.FuncType) string {
	params := fieldListText(t.Params)
	results := fieldListText(t.Results)
	sig := "(" + params + ")"
	if results != "" {
		if strings.Contains(results, ",") {
			sig += " (" + results + ")"
		} else {
			sig += " " + results
		}
	}
	return sig
}

func fieldListText(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		t := typeText(f.Type)
		if n := len(f.Names); n > 1 {
			t = fmt.Sprintf("%s ×%d", t, n)
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, ", ")
}

// typeText renders a type expression compactly (best effort — enough for a
// map, not a pretty-printer).
func typeText(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeText(t.X)
	case *ast.SelectorExpr:
		return typeText(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + typeText(t.Elt)
	case *ast.MapType:
		return "map[" + typeText(t.Key) + "]" + typeText(t.Value)
	case *ast.FuncType:
		return "func(" + fieldListText(t.Params) + ")"
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.Ellipsis:
		return "..." + typeText(t.Elt)
	case *ast.ChanType:
		return "chan " + typeText(t.Value)
	case *ast.IndexExpr:
		return typeText(t.X) + "[" + typeText(t.Index) + "]"
	default:
		return "?"
	}
}

// genericDeclRe matches the common top-level declaration forms across the
// mainstream languages the map supports (def/class/function/interface/etc.).
var genericDeclRe = regexp.MustCompile(`^\s*(?:export\s+)?(?:public\s+|private\s+|protected\s+|static\s+|abstract\s+|final\s+|async\s+)*` +
	`(def |class |function |interface |enum |struct |trait |impl |module |fn |const |type |var |let )`)

// outlineGeneric renders a pattern-based outline for non-Go sources: any
// line opening a recognizable declaration, with its line number.
func outlineGeneric(path string) ([]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path expanded by the engine helper, workspace-relative
	if err != nil {
		return nil, err
	}
	var out []string
	for i, line := range strings.Split(string(data), "\n") {
		if len(out) >= maxOutlineEntries {
			break
		}
		if genericDeclRe.MatchString(line) {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 120 {
				trimmed = trimmed[:120] + "…"
			}
			out = append(out, fmt.Sprintf("L%d %s", i+1, trimmed))
		}
	}
	return out, nil
}
