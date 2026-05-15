/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
// qg-i18n-parity verifies cross-locale and code↔locale parity for the
// chatcli i18n catalog. Used by the Quality Gate Floor 10.
//
// Checks performed:
//
//	1. Locale parity   — every key appears in every JSON locale file.
//	2. Usage→locales   — every `i18n.T("literal")` call references a key
//	                      defined in at least one locale (so the runtime
//	                      can fall back).
//	3. Optional orphan — keys in locales never referenced from Go code
//	                      (informational; not enforced by default).
//
// Output to stdout is key=value pairs for the wrapper script.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diillson/chatcli/tools/qg/i18nparity"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qg-i18n-parity:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("qg-i18n-parity", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		localesDir  = fs.String("locales-dir", "i18n/locales", "directory containing *.json locale files")
		sourceRoot  = fs.String("source-root", ".", "root directory to scan for i18n.T calls")
		markdownOut = fs.String("markdown", "", "optional markdown report output path")
		excludes    stringSliceFlag
	)
	fs.Var(&excludes, "exclude", "directory prefix to skip while scanning (may repeat)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(excludes) == 0 {
		excludes = stringSliceFlag{"vendor", "tools/docgen", "proto", "node_modules", ".git"}
	}

	locales, err := i18nparity.LoadLocales(*localesDir)
	if err != nil {
		return err
	}
	missing := i18nparity.MissingByLocale(locales)

	usages, err := i18nparity.ScanUsages(*sourceRoot, []string(excludes))
	if err != nil {
		return err
	}
	unknown := i18nparity.UnknownUsages(usages, locales)

	totalMissing := 0
	for _, ks := range missing {
		totalMissing += len(ks)
	}

	fmt.Fprintf(stdout, "locales=%d\n", len(locales))
	fmt.Fprintf(stdout, "usages=%d\n", len(usages))
	fmt.Fprintf(stdout, "missing_keys=%d\n", totalMissing)
	fmt.Fprintf(stdout, "unknown_usages=%d\n", len(unknown))

	if *markdownOut != "" {
		md := formatMarkdown(locales, missing, unknown)
		if err := os.WriteFile(*markdownOut, []byte(md), 0o600); err != nil {
			return fmt.Errorf("write markdown: %w", err)
		}
	}

	if totalMissing == 0 && len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("i18n parity failed: %d cross-locale missing key(s), %d unknown usage(s)",
		totalMissing, len(unknown))
}

func formatMarkdown(locales []i18nparity.Locale, missing map[string][]string, unknown []i18nparity.UsageRef) string {
	var b strings.Builder
	b.WriteString("## i18n parity\n\n")

	if len(missing) == 0 {
		b.WriteString("- ✅ All locales contain the same key set.\n")
	} else {
		b.WriteString("### Cross-locale missing keys\n\n")
		b.WriteString("| Locale | Missing |\n|---|---|\n")
		for _, l := range locales {
			ks, ok := missing[l.Name]
			if !ok {
				continue
			}
			preview := ks
			if len(preview) > 5 {
				preview = ks[:5]
			}
			extra := ""
			if len(ks) > 5 {
				extra = fmt.Sprintf(" _(+%d more)_", len(ks)-5)
			}
			fmt.Fprintf(&b, "| `%s` | `%s`%s |\n", l.Name, strings.Join(preview, "`, `"), extra)
		}
		b.WriteString("\n")
	}

	if len(unknown) == 0 {
		b.WriteString("- ✅ All `i18n.T()` keys exist in at least one locale.\n")
	} else {
		b.WriteString("### Unknown i18n.T keys\n\n")
		b.WriteString("These call sites reference keys not defined in any locale. The")
		b.WriteString(" runtime will display the literal key instead of a translation.\n\n")
		b.WriteString("| Key | File:Line |\n|---|---|\n")
		shown := unknown
		if len(shown) > 25 {
			shown = shown[:25]
		}
		for _, u := range shown {
			fmt.Fprintf(&b, "| `%s` | `%s:%d` |\n", u.Key, u.File, u.Line)
		}
		if len(unknown) > 25 {
			fmt.Fprintf(&b, "\n_(+%d more)_\n", len(unknown)-25)
		}
	}
	return b.String()
}
