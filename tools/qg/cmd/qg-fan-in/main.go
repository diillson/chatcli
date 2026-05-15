/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// qg-fan-in counts how many packages in the module import each of the
// packages reachable from a list of files. Used by Floor 5 (scope budget)
// to surface the blast radius of a PR: a 200 LOC change to a package
// imported by 30 others is more risky than a 1000 LOC change to a
// leaf package no one imports.
//
// Output (per line): `<package>\t<importers>`. Total is appended as
// `TOTAL\t<sum>` on the last line.
//
// Implementation: shell out to `go list -f '{{.ImportPath}} {{join
// .Imports "\n"}}'` to get the project's import graph, then count how
// many packages list each candidate as an import.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	files := flag.String("files", "", "newline-separated list of file paths (`-` reads stdin)")
	moduleRoot := flag.String("module", "", "module path to strip (e.g. github.com/diillson/chatcli); auto-detected from go.mod if empty")
	flag.Parse()

	paths, err := loadFiles(*files)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qg-fan-in:", err)
		os.Exit(2)
	}

	root := *moduleRoot
	if root == "" {
		root, err = detectModulePath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "qg-fan-in:", err)
			os.Exit(2)
		}
	}

	pkgs := pkgsFromFiles(paths)
	if len(pkgs) == 0 {
		fmt.Println("TOTAL\t0")
		return
	}

	graph, err := importGraph()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qg-fan-in:", err)
		os.Exit(2)
	}

	total := 0
	for _, p := range pkgs {
		full := root + "/" + p
		n := graph[full]
		fmt.Printf("%s\t%d\n", p, n)
		total += n
	}
	fmt.Printf("TOTAL\t%d\n", total)
}

// loadFiles returns the file list from a path or stdin if "-".
func loadFiles(src string) ([]string, error) {
	if src == "-" || src == "" {
		var out []string
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				out = append(out, line)
			}
		}
		return out, sc.Err()
	}
	// #nosec G304 -- src is the operator-controlled -files flag of a CI tool;
	// reading the caller-supplied path is the tool's purpose.
	data, err := os.ReadFile(filepath.Clean(src))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// pkgsFromFiles dedupes parent-directories of the input file paths.
// `cli/cli.go` and `cli/agent_mode.go` collapse to `cli`.
func pkgsFromFiles(files []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, f := range files {
		dir := filepath.Dir(f)
		if dir == "." {
			continue
		}
		if _, ok := seen[dir]; !ok {
			seen[dir] = struct{}{}
			out = append(out, dir)
		}
	}
	return out
}

// importGraph counts how many packages in the current module list each
// other package as an import. Returns a map keyed by full import path.
func importGraph() (map[string]int, error) {
	// `go list -deps=false ./...` yields packages in the current module;
	// the format string emits one line per import edge.
	cmd := exec.Command("go", "list", "-f", "{{.ImportPath}}\t{{range .Imports}}{{.}} {{end}}", "./...")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		imports := strings.Fields(parts[1])
		for _, imp := range imports {
			counts[imp]++
		}
	}
	return counts, nil
}

// detectModulePath reads the module declaration from ./go.mod.
func detectModulePath() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}
