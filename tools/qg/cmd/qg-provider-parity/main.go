/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
// qg-provider-parity is the Floor 15 entry point: for every LLM provider
// declared in llm/catalog/catalog.go, verify that all touch points
// across the codebase reference it. Catches half-integrated providers
// that compile and pass tests but break in production (missing env
// redaction, missing /config section, missing operator enum, etc).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diillson/chatcli/tools/qg/providerparity"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qg-provider-parity:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("qg-provider-parity", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		root        = fs.String("root", ".", "repo root")
		catalog     = fs.String("catalog", "llm/catalog/catalog.go", "path to catalog source")
		markdownOut = fs.String("markdown", "", "optional markdown report output path")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	providers, err := providerparity.LoadProviders(*root + "/" + *catalog)
	if err != nil {
		return err
	}

	points := providerparity.DefaultTouchPoints()
	ex := providerparity.DefaultExemptions()
	violations, err := providerparity.Check(*root, providers, points, ex)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "providers=%d\n", len(providers))
	fmt.Fprintf(stdout, "touch_points=%d\n", len(points))
	fmt.Fprintf(stdout, "violations=%d\n", len(violations))

	if *markdownOut != "" {
		md := formatMarkdown(providers, violations)
		if err := os.WriteFile(*markdownOut, []byte(md), 0o600); err != nil {
			return fmt.Errorf("write markdown: %w", err)
		}
	}

	if len(violations) == 0 {
		return nil
	}
	return fmt.Errorf("provider parity: %d missing touch point(s)", len(violations))
}

func formatMarkdown(providers []providerparity.Provider, violations []providerparity.Violation) string {
	var b strings.Builder
	b.WriteString("## Provider parity\n\n")
	b.WriteString(fmt.Sprintf("Checked %d providers against the touch-point matrix.\n\n", len(providers)))

	if len(violations) == 0 {
		b.WriteString("- ✅ Every provider is wired through every touch point.\n")
		return b.String()
	}

	b.WriteString("### Missing touch points\n\n")
	b.WriteString("| Provider | File | Description |\n|---|---|---|\n")
	for _, v := range violations {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", v.Provider, v.Path, v.Description)
	}
	b.WriteString("\n")
	b.WriteString("Fix by adding the expected pattern to the listed file(s). The parity matrix")
	b.WriteString(" lives in `tools/qg/providerparity/providers.go` — edit there to add or remove checks.\n")
	return b.String()
}
