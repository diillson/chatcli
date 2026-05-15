// qg-cover-total prints the total coverage percentage of a Go cover
// profile, computed purely from the profile data (no source resolution).
//
// Why: `go tool cover -func=PROFILE` requires every package referenced in
// the profile to be resolvable from the current module's go.mod. When the
// profile spans multiple Go modules (chatcli's root + operator/), no
// single working directory satisfies that constraint and `go tool cover`
// errors on the operator packages without emitting the `total:` line that
// scripts/qg/coverage-ratchet.sh needs to parse.
//
// Output: a single line `XX.X` (percent, one decimal) on stdout. No
// trailing newline is added if the caller doesn't want one.
//
// Usage: qg-cover-total -profile coverage.out
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/diillson/chatcli/tools/qg/diffcover"
)

func main() {
	profilePath := flag.String("profile", "coverage.out", "path to go coverprofile")
	flag.Parse()

	f, err := os.Open(*profilePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qg-cover-total:", err)
		os.Exit(2)
	}
	defer func() { _ = f.Close() }()

	profile, err := diffcover.ParseProfile(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qg-cover-total:", err)
		os.Exit(2)
	}

	_, _, pct := profile.TotalCoverage()
	fmt.Printf("%.1f\n", pct)
}
