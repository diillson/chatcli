/*
 * ChatCLI - Tests for skill_handler.go Info helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * SkillHandler.Info itself is a thin orchestrator that prints to stdout;
 * the interesting logic moved to small helpers that pick the best metadata
 * source and the nil-safe field readers. Those are what we exercise here.
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/pkg/registry"
)

func TestPickFirstNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{name: "all empty", in: []string{"", "", ""}, want: ""},
		{name: "first non-empty wins", in: []string{"alpha", "bravo"}, want: "alpha"},
		{name: "skips leading empties", in: []string{"", "", "gamma"}, want: "gamma"},
		{name: "no args", in: nil, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickFirstNonEmpty(tc.in...); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoteAccessors_NilSafe(t *testing.T) {
	if remoteName(nil) != "" {
		t.Error("remoteName(nil) must return empty")
	}
	if remoteDescription(nil) != "" {
		t.Error("remoteDescription(nil) must return empty")
	}
	if remoteVersion(nil) != "" {
		t.Error("remoteVersion(nil) must return empty")
	}
	if remoteRegistryName(nil) != "" {
		t.Error("remoteRegistryName(nil) must return empty")
	}
}

func TestRemoteAccessors_ReturnFields(t *testing.T) {
	r := &registry.SkillMeta{
		Name:         "n",
		Description:  "d",
		Version:      "v",
		RegistryName: "reg",
	}
	if remoteName(r) != "n" {
		t.Error("remoteName")
	}
	if remoteDescription(r) != "d" {
		t.Error("remoteDescription")
	}
	if remoteVersion(r) != "v" {
		t.Error("remoteVersion")
	}
	if remoteRegistryName(r) != "reg" {
		t.Error("remoteRegistryName")
	}
}

func TestLocalAccessors_NilSafe(t *testing.T) {
	if localName(nil) != "" {
		t.Error("localName(nil)")
	}
	if localDescription(nil) != "" {
		t.Error("localDescription(nil)")
	}
	if localVersion(nil) != "" {
		t.Error("localVersion(nil)")
	}
	if localSource(nil) != "" {
		t.Error("localSource(nil)")
	}
}

func TestLocalAccessors_ReturnFields(t *testing.T) {
	l := &registry.InstalledSkillInfo{
		Name:        "ln",
		Description: "ld",
		Version:     "lv",
		Source:      "lsrc",
	}
	if localName(l) != "ln" {
		t.Error("localName")
	}
	if localDescription(l) != "ld" {
		t.Error("localDescription")
	}
	if localVersion(l) != "lv" {
		t.Error("localVersion")
	}
	if localSource(l) != "lsrc" {
		t.Error("localSource")
	}
}

func TestSelectRichestRemote_EmptyReturnsNil(t *testing.T) {
	if got := selectRichestRemote(nil); got != nil {
		t.Errorf("expected nil; got %v", got)
	}
	if got := selectRichestRemote([]*registry.SkillMeta{}); got != nil {
		t.Errorf("expected nil for empty slice; got %v", got)
	}
}

func TestSelectRichestRemote_PrefersSkillsShSource(t *testing.T) {
	// The exact registry name skills.sh uses is registry-internal; we
	// inspect IsSkillsShSource and find one that returns true. If the
	// canonical name shifts, this test would self-correct on rebuild.
	canonical := canonicalSkillsShRegistryName(t)
	if canonical == "" {
		t.Skip("no registry recognized as skills.sh; cannot exercise preference")
	}
	candidates := []*registry.SkillMeta{
		{RegistryName: "custom-a", Downloads: 1000},
		{RegistryName: canonical, Downloads: 1},
		{RegistryName: "custom-b", Downloads: 999},
	}
	best := selectRichestRemote(candidates)
	if best == nil || best.RegistryName != canonical {
		t.Fatalf("expected skills.sh entry to win; got %+v", best)
	}
}

func TestSelectRichestRemote_FallsBackToMostDownloads(t *testing.T) {
	candidates := []*registry.SkillMeta{
		{RegistryName: "a", Downloads: 1},
		{RegistryName: "b", Downloads: 5},
		{RegistryName: "c", Downloads: 3},
	}
	if got := selectRichestRemote(candidates); got == nil || got.RegistryName != "b" {
		t.Fatalf("expected highest-downloads to win; got %+v", got)
	}
}

func TestSelectRichestRemote_PrefersDescriptionOverEmpty(t *testing.T) {
	candidates := []*registry.SkillMeta{
		{RegistryName: "a", Downloads: 0, Description: ""},
		{RegistryName: "b", Downloads: 0, Description: "has desc"},
	}
	got := selectRichestRemote(candidates)
	if got == nil || got.RegistryName != "b" {
		t.Fatalf("expected description-bearing entry to win on equal downloads; got %+v", got)
	}
}

// canonicalSkillsShRegistryName probes registry.IsSkillsShSource with a few
// plausible names. Returns the first one that comes back true, or "" when
// the helper is too strict to recognize any string we tried.
func canonicalSkillsShRegistryName(t *testing.T) string {
	t.Helper()
	for _, name := range []string{
		"skills.sh", "Skills.sh", "skills-sh", "skills_sh", "skillssh",
	} {
		if registry.IsSkillsShSource(name) {
			return name
		}
	}
	return ""
}
