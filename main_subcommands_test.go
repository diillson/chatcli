/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli"
)

// dispatchedSubcommands reads the case labels of dispatchSubcommand's
// switch from the source, so the test sees exactly what main dispatches.
func dispatchedSubcommands(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "dispatchSubcommand" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						names = append(names, v)
					}
				}
			}
			return true
		})
	}
	if len(names) == 0 {
		t.Fatal("no subcommand cases found in dispatchSubcommand")
	}
	sort.Strings(names)
	return names
}

// The subcommand registry (chatcli --help, /help, generic --help) must list
// exactly what main dispatches: a new subcommand cannot ship undocumented,
// and help never advertises one that does not exist.
func TestSubcommandRegistryMatchesDispatch(t *testing.T) {
	dispatched := dispatchedSubcommands(t)
	var listed []string
	for _, s := range cli.Subcommands() {
		listed = append(listed, s.Name)
		listed = append(listed, s.Aliases...)
	}
	sort.Strings(listed)
	if strings.Join(dispatched, ",") != strings.Join(listed, ",") {
		t.Fatalf("dispatchSubcommand handles %v\nbut cli.Subcommands lists %v", dispatched, listed)
	}
}

func TestHelpRequested(t *testing.T) {
	if helpRequested(nil) {
		t.Error("nil is not a help request")
	}
}
