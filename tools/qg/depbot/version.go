/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package depbot

import (
	"regexp"
	"strconv"
	"strings"
)

// version is the subset of semver the dependency policy needs: the numeric
// triple and whether a pre-release part is present. Go pseudo-versions
// (v0.0.0-20260101000000-abcdef123456) count as pre-releases, which is
// exactly how the policy wants to treat them.
//
// Hand-rolled on purpose: golang.org/x/mod is not a dependency of the root
// module and a tool that polices dependency changes should not add one.
type version struct {
	major, minor, patch int
	pre                 string
}

var versionRE = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

// parseVersion accepts v1.2.3, 1.2.3, v1.2, v1 and the pre-release / build
// suffixed forms. Missing components are zero, so an action pinned as
// "v0.7" compares correctly against "v0.7.1".
func parseVersion(s string) (version, bool) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return version{}, false
	}
	v := version{pre: m[4]}
	for i, dst := range []*int{&v.major, &v.minor, &v.patch} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return version{}, false
		}
		*dst = n
	}
	return v, true
}

// compare returns -1, 0 or 1. A release sorts after any pre-release of the
// same triple; two pre-releases of the same triple compare as strings,
// which orders Go pseudo-versions by their timestamp.
func (v version) compare(o version) int {
	for _, p := range [][2]int{{v.major, o.major}, {v.minor, o.minor}, {v.patch, o.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.pre == o.pre:
		return 0
	case v.pre == "":
		return 1
	case o.pre == "":
		return -1
	}
	return strings.Compare(v.pre, o.pre)
}
