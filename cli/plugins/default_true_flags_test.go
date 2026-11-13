/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"strings"
	"testing"
)

// An option that is on by default could only be turned off with false, and
// false is the one value the agent loop drops when it flattens a call. Each
// of these has a positive spelling now; the tests feed the flattened argv.

func TestDiagramDefaultsCanBeTurnedOff(t *testing.T) {
	base, err := parseDiagramArgs([]string{"gomod", "--root", "."})
	if err != nil {
		t.Fatal(err)
	}
	if !base.InternalOnly || !base.Cluster {
		t.Fatalf("defaults changed: internalOnly=%v cluster=%v", base.InternalOnly, base.Cluster)
	}
	got, err := parseDiagramArgs([]string{"gomod", "--allDeps", "--root", ".", "--ungrouped"})
	if err != nil {
		t.Fatal(err)
	}
	if got.InternalOnly || got.Cluster {
		t.Fatalf("allDeps/ungrouped ignored: internalOnly=%v cluster=%v", got.InternalOnly, got.Cluster)
	}
	// Raw JSON with the old names keeps working.
	got, err = parseDiagramArgs([]string{`{"root":".","internalOnly":false,"cluster":false,"dot":"digraph{}"}`})
	if err != nil {
		t.Fatal(err)
	}
	if got.InternalOnly || got.Cluster {
		t.Fatalf("raw JSON false ignored: %+v", got)
	}
}

func TestDocsFlattenDefaultsCanBeTurnedOff(t *testing.T) {
	base, err := parseDocsFlattenArgs([]string{"--url", "https://example.com/docs"})
	if err != nil {
		t.Fatal(err)
	}
	if !base.SameHost || !base.StripFrontMatter {
		t.Fatalf("defaults changed: sameHost=%v stripFrontMatter=%v", base.SameHost, base.StripFrontMatter)
	}
	got, err := parseDocsFlattenArgs([]string{"--crossHost", "--keepFrontMatter", "--url", "https://example.com/docs"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SameHost || got.StripFrontMatter {
		t.Fatalf("crossHost/keepFrontMatter ignored: sameHost=%v stripFrontMatter=%v", got.SameHost, got.StripFrontMatter)
	}
}

// A true boolean arrives as a bare flag; it used to be stored as an empty
// string and read as false.
func TestDocsFlattenBareFlagIsTrue(t *testing.T) {
	got, err := parseDocsFlattenArgs([]string{"--confirmImages", "--root", "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ConfirmImages {
		t.Fatal("confirmImages given as a bare flag was read as false")
	}
	if got, err = parseDocsFlattenArgs([]string{"--root", "docs"}); err != nil || got.ConfirmImages {
		t.Fatalf("confirmImages defaulted to true: %+v %v", got, err)
	}
	// A flag followed by its value is unchanged.
	if got, err = parseDocsFlattenArgs([]string{"--root", "docs", "--format", "jsonl"}); err != nil || got.Format != "jsonl" || got.Root != "docs" {
		t.Fatalf("valued flags: %+v %v", got, err)
	}
}

func TestImageModelsCatalogOnly(t *testing.T) {
	if imageCatalogOnly(`{"provider":"openai"}`) {
		t.Fatal("a plain models call is not catalog-only")
	}
	for _, inner := range []string{`{"catalogOnly":true}`, `{"catalog_only":"true"}`, `{"offline":true}`} {
		if !imageCatalogOnly(inner) {
			t.Errorf("%s was not read as catalog-only", inner)
		}
	}
	// The flattened call reaches the same field.
	cmd, inner, err := parseImageInvocation([]string{"models", "--catalogOnly", "--provider", "openai"})
	if err != nil || cmd != "models" || !imageCatalogOnly(inner) {
		t.Fatalf("flattened models call: cmd=%q inner=%s err=%v", cmd, inner, err)
	}
}

// What the model is taught must be the form that works.
func TestDefaultTrueFlagsAreTaughtInTheirPositiveForm(t *testing.T) {
	for name, text := range map[string]string{
		"diagram":      NewBuiltinDiagramPlugin().Usage() + NewBuiltinDiagramPlugin().Schema(),
		"docs-flatten": NewBuiltinDocsFlattenPlugin().Usage() + NewBuiltinDocsFlattenPlugin().Schema(),
		"image":        NewBuiltinImagePlugin().Usage() + NewBuiltinImagePlugin().Schema(),
	} {
		for _, gone := range []string{`"internalOnly":false`, "Default: true.", "(default: true)", "(default true); false"} {
			if strings.Contains(text, gone) {
				t.Errorf("@%s still teaches %q", name, gone)
			}
		}
	}
}
