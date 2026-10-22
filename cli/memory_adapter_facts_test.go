/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"
)

func TestMemoryAdapter_ListPreviewAndForgetByID(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	mgr := cli.memoryStore.Manager()
	if !mgr.RememberFact("The deploy runs on Fridays", "project") || !mgr.RememberFact("User prefers tabs over spaces", "preference") {
		t.Fatal("seed facts")
	}
	a := &memoryPluginAdapter{cli: cli}

	out, err := a.ListFacts("", "", 0)
	if err != nil || !strings.Contains(out, "deploy runs on Fridays") || !strings.Contains(out, "tabs over spaces") {
		t.Fatalf("list all = %q %v", out, err)
	}
	out, err = a.ListFacts("", "preference", 10)
	if err != nil || strings.Contains(out, "Fridays") || !strings.Contains(out, "[preference]") {
		t.Fatalf("list by category = %q %v", out, err)
	}
	out, err = a.ListFacts("fridays", "", 10)
	if err != nil || !strings.Contains(out, "Fridays") || strings.Contains(out, "tabs") {
		t.Fatalf("list by match = %q %v", out, err)
	}
	if out, err = a.ListFacts("nothing-like-this", "", 10); err != nil || !strings.Contains(out, "mem.tool.list.none") && !strings.Contains(strings.ToLower(out), "no ") {
		t.Fatalf("list with no hit = %q %v", out, err)
	}
	if out, err = a.ListFacts("", "", 1); err != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("list with limit 1 shows one fact under the header = %q %v", out, err)
	}

	out, err = a.ForgetPreview("tabs")
	if err != nil || !strings.Contains(out, "tabs over spaces") {
		t.Fatalf("preview = %q %v", out, err)
	}
	if len(mgr.Facts.GetAll()) != 2 {
		t.Fatal("preview must not remove anything")
	}
	if _, err = a.ForgetPreview("zzz"); err != nil {
		t.Fatalf("preview with no hit is not an error: %v", err)
	}

	var id string
	for _, f := range mgr.Facts.GetAll() {
		if strings.Contains(f.Content, "tabs") {
			id = f.ID
		}
	}
	if _, err = a.ForgetByID("no-such-id"); err == nil {
		t.Fatal("unknown id must error")
	}
	// Ids are content hashes: add facts until two share a first character,
	// then that character is an ambiguous prefix.
	ambiguous := ""
	for i := 0; i < 64 && ambiguous == ""; i++ {
		mgr.RememberFact("filler fact number "+strings.Repeat("x", i), "project")
		seen := map[byte]bool{}
		for _, f := range mgr.Facts.GetAll() {
			if seen[f.ID[0]] {
				ambiguous = f.ID[:1]
				break
			}
			seen[f.ID[0]] = true
		}
	}
	if ambiguous == "" {
		t.Fatal("could not build an ambiguous prefix")
	}
	if _, err = a.ForgetByID(ambiguous); err == nil {
		t.Fatal("a prefix shared by several ids is ambiguous and must error")
	}
	before := len(mgr.Facts.GetAll())
	out, err = a.ForgetByID(shortFactID(id))
	if err != nil || !strings.Contains(out, "tabs over spaces") {
		t.Fatalf("forget by id = %q %v", out, err)
	}
	if n := len(mgr.Facts.GetAll()); n != before-1 {
		t.Fatalf("exactly one fact must be removed: before %d, after %d", before, n)
	}
	if mgr.FactsMatching("tabs") != nil {
		t.Fatal("removed fact must not match anymore")
	}

	none := &memoryPluginAdapter{cli: &ChatCLI{}}
	for _, call := range []func() (string, error){
		func() (string, error) { return none.ListFacts("", "", 0) },
		func() (string, error) { return none.ForgetPreview("x") },
		func() (string, error) { return none.ForgetByID("x") },
	} {
		if _, err := call(); err == nil {
			t.Fatal("memory disabled must error")
		}
	}
	if shortFactID("abcdefghijklmnop") != "abcdefghij" || shortFactID("abc") != "abc" {
		t.Fatal("short id is ten characters at most")
	}
}
