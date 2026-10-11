/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package version

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// semverRe is the strict Semantic Versioning 2.0.0 grammar (semver.org),
// with an optional leading "v". A release tag is spliced into download URLs
// and into the go install argument, so anything outside this grammar (path
// separators, "..", whitespace, empty fields) is rejected at ingestion.
var semverRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// maxSemverLen bounds the tag length; real tags are a dozen characters.
const maxSemverLen = 128

// IsValidTag reports whether tag is a strict semantic version, with or
// without the leading "v" that release tags carry.
func IsValidTag(tag string) bool {
	return len(tag) <= maxSemverLen && semverRe.MatchString(tag)
}

// semverParts splits a strict semver into its numeric core and prerelease.
// ok is false for anything IsValidTag rejects or whose numbers overflow.
func semverParts(v string) (core [3]int, pre string, ok bool) {
	m := semverRe.FindStringSubmatch(v)
	if m == nil || len(v) > maxSemverLen {
		return core, "", false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return core, "", false
		}
		core[i] = n
	}
	return core, m[4], true
}

// comparePrerelease orders two prerelease strings per semver 2.0.0 §11:
// an empty prerelease ranks above any non-empty one; identifiers compare
// numerically when both are numeric, numeric ranks below alphanumeric, and
// otherwise ASCII order applies; a longer list wins a common prefix.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareIdentifier(as[i], bs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}

func compareIdentifier(a, b string) int {
	an, aErr := strconv.ParseUint(a, 10, 64)
	bn, bErr := strconv.ParseUint(b, 10, 64)
	switch {
	case aErr == nil && bErr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// maxReleaseBody bounds the release-check response. The GitHub payload for
// one release is a few KiB; anything past 1 MiB is not a release document.
const maxReleaseBody = 1 << 20

// errInvalidReleaseTag reports a release whose tag is not strict semver.
var errInvalidReleaseTag = errors.New("release check: tag_name is not a valid semantic version")

// decodeReleaseInfo reads at most maxReleaseBody bytes, decodes the release
// and validates its tag at ingestion, so a malformed tag never reaches the
// cache, the update comparison or a download URL.
func decodeReleaseInfo(r io.Reader) (ReleaseInfo, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxReleaseBody+1))
	if err != nil {
		return ReleaseInfo{}, err
	}
	if len(body) > maxReleaseBody {
		return ReleaseInfo{}, fmt.Errorf("release check: response exceeds %d bytes", maxReleaseBody)
	}
	var info ReleaseInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return ReleaseInfo{}, err
	}
	if !IsValidTag(info.TagName) {
		return ReleaseInfo{}, errInvalidReleaseTag
	}
	return info, nil
}
