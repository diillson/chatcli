/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseProfile(t *testing.T) {
	cases := []struct {
		in   string
		want Profile
		ok   bool
	}{
		{"", ProfileDefault, true},
		{"default", ProfileDefault, true},
		{"balanced", ProfileDefault, true},
		{"conservative", ProfileConservative, true},
		{"safe", ProfileConservative, true},
		{"aggressive", ProfileAggressive, true},
		{"max", ProfileAggressive, true},
		{"bogus", ProfileDefault, false},
	}
	for _, c := range cases {
		got, ok := ParseProfile(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseProfile(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// searchPayload builds a grep-style payload with many matches across many
// files, big enough to exercise every profile's caps.
func searchPayload() string {
	var b strings.Builder
	for f := 0; f < 40; f++ {
		for m := 0; m < 15; m++ {
			fmt.Fprintf(&b, "internal/pkg%02d/file.go:%d: occurrence of the searched symbol in context\n", f, m+10)
		}
	}
	return b.String()
}

// TestProfilesOrderSavings verifies the profile contract on a real payload:
// aggressive keeps less than default, default less than conservative, and all
// three remain reversible.
func TestProfilesOrderSavings(t *testing.T) {
	payload := searchPayload()
	sizes := map[Profile]int{}
	for _, p := range []Profile{ProfileConservative, ProfileDefault, ProfileAggressive} {
		l := NewLayer(Config{Mode: ModeLossyWithCCR, Profile: p, Threshold: 100, Store: NewMemoryStore()})
		out, res := l.CompressToolOutput("@search", payload)
		if !res.Reversible {
			t.Fatalf("profile %v produced an irreversible result", p)
		}
		for _, key := range ExtractKeys(out) {
			if _, ok := l.Recall(key); !ok {
				t.Fatalf("profile %v emitted unrecallable marker %s", p, key)
			}
		}
		sizes[p] = res.CompressedSize
	}
	if !(sizes[ProfileAggressive] < sizes[ProfileDefault] && sizes[ProfileDefault] < sizes[ProfileConservative]) {
		t.Fatalf("profile ordering violated: aggressive=%d default=%d conservative=%d",
			sizes[ProfileAggressive], sizes[ProfileDefault], sizes[ProfileConservative])
	}
}

// TestSetProfileSwapsRouterLive verifies the runtime switch: the same layer
// produces different reductions before and after SetProfile, with no rebuild.
func TestSetProfileSwapsRouterLive(t *testing.T) {
	payload := searchPayload()
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Threshold: 100, Store: NewMemoryStore()})

	_, resDefault := l.CompressToolOutput("@search", payload)

	l.SetProfile(ProfileAggressive)
	if l.Profile() != ProfileAggressive {
		t.Fatalf("Profile() = %v after SetProfile(aggressive)", l.Profile())
	}
	_, resAggressive := l.CompressToolOutput("@search", payload)

	if resAggressive.CompressedSize >= resDefault.CompressedSize {
		t.Fatalf("aggressive (%d bytes) did not reduce more than default (%d bytes) after live switch",
			resAggressive.CompressedSize, resDefault.CompressedSize)
	}
}

// TestSetThresholdLive verifies the runtime threshold: raising it above the
// payload size turns compression into passthrough, lowering re-engages it.
func TestSetThresholdLive(t *testing.T) {
	payload := searchPayload()
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Threshold: 100, Store: NewMemoryStore()})

	if _, res := l.CompressToolOutput("@search", payload); res.Strategy == "passthrough" {
		t.Fatal("payload above threshold was not compressed")
	}

	l.SetThreshold(len(payload) + 1)
	if l.Threshold() != len(payload)+1 {
		t.Fatalf("Threshold() = %d after SetThreshold", l.Threshold())
	}
	if _, res := l.CompressToolOutput("@search", payload); res.Strategy != "passthrough" {
		t.Fatalf("payload below the raised threshold was compressed (strategy=%s)", res.Strategy)
	}

	l.SetThreshold(100)
	if _, res := l.CompressToolOutput("@search", payload); res.Strategy == "passthrough" {
		t.Fatal("lowering the threshold did not re-engage compression")
	}
}
