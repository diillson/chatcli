/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

// Every builtin tool carries a semantic version, and the version moves with
// what the model sees. cli/testdata/builtin_versions.json records, per tool,
// its version and a hash of its surface (name, description, usage, schema).
// When a surface changes, the version must change with it:
//
//   - patch: wording, examples, a fix in behavior the model relies on
//   - minor: a new subcommand, argument or capability
//   - major: a subcommand or argument removed or renamed, a changed contract
//
// Then refresh the file with UPDATE_BUILTIN_VERSIONS=1 go test ./cli -run TestBuiltinVersions.
const builtinVersionsFile = "testdata/builtin_versions.json"

type builtinVersion struct {
	Version string `json:"version"`
	Surface string `json:"surface"`
}

var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

func builtinSurface(p plugins.Plugin) string {
	h := sha256.Sum256([]byte(p.Name() + "\x00" + p.Description() + "\x00" + p.Usage() + "\x00" + p.Schema()))
	return hex.EncodeToString(h[:8])
}

func currentBuiltinVersions(t *testing.T) map[string]builtinVersion {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_AGENT_MODEL_TOOL", "")
	t.Setenv("CHATCLI_AGENT_DASH_TOOL", "")
	m, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("plugin manager: %v", err)
	}
	registerCoreBuiltins(m)
	out := map[string]builtinVersion{}
	for _, p := range m.GetPlugins() {
		out[p.Name()] = builtinVersion{Version: p.Version(), Surface: builtinSurface(p)}
	}
	return out
}

// semverLess reports whether a precedes b.
func semverLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

func TestBuiltinVersions(t *testing.T) {
	current := currentBuiltinVersions(t)
	if os.Getenv("UPDATE_BUILTIN_VERSIONS") == "1" {
		data, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(builtinVersionsFile, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(builtinVersionsFile)
	if err != nil {
		t.Fatalf("%s: %v (create it with UPDATE_BUILTIN_VERSIONS=1)", builtinVersionsFile, err)
	}
	var recorded map[string]builtinVersion
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatalf("%s: %v", builtinVersionsFile, err)
	}

	names := make([]string, 0, len(current))
	for n := range current {
		names = append(names, n)
	}
	sort.Strings(names)
	var problems []string
	for _, name := range names {
		cur := current[name]
		if !semverRe.MatchString(cur.Version) {
			problems = append(problems, fmt.Sprintf("%s: version %q is not MAJOR.MINOR.PATCH", name, cur.Version))
			continue
		}
		rec, known := recorded[name]
		switch {
		case !known:
			problems = append(problems, fmt.Sprintf("%s: new builtin, not recorded", name))
		case semverLess(cur.Version, rec.Version):
			problems = append(problems, fmt.Sprintf("%s: version went back from %s to %s", name, rec.Version, cur.Version))
		case cur.Surface != rec.Surface && cur.Version == rec.Version:
			problems = append(problems, fmt.Sprintf("%s: description, usage or schema changed but the version is still %s: bump it (patch for wording, minor for a new argument or subcommand, major for a removed or renamed one)", name, cur.Version))
		case cur != rec:
			problems = append(problems, fmt.Sprintf("%s: %s is now %s, not recorded", name, rec.Version, cur.Version))
		}
	}
	for name := range recorded {
		if _, ok := current[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s: recorded but no longer registered", name))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("builtin versions out of date:\n  %s\nFix the version where asked, then run UPDATE_BUILTIN_VERSIONS=1 go test ./cli -run TestBuiltinVersions to record it.",
			strings.Join(problems, "\n  "))
	}
}

// The surface hash only depends on what the model sees: the same plugin
// built twice hashes the same, so the check is not flaky.
func TestBuiltinSurfaceIsStable(t *testing.T) {
	a := currentBuiltinVersions(t)
	b := currentBuiltinVersions(t)
	for name, v := range a {
		if b[name] != v {
			t.Errorf("%s: surface not stable across builds (%+v vs %+v)", name, v, b[name])
		}
	}
}

func TestSemverLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.0.0", "1.0.1", true}, {"1.2.0", "1.10.0", true}, {"2.0.0", "1.9.9", false}, {"1.1.0", "1.1.0", false}} {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
