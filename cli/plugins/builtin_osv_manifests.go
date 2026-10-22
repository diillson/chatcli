/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_osv_manifests.go — the lockfiles @osv reads beyond the first
 * four: yarn (v1 and berry), pnpm, Poetry, Pipfile, Bundler, Composer and
 * Maven. Each parser yields the pinned (ecosystem, name, version) triples
 * OSV.dev answers for; a lockfile pins exact versions, so no resolution
 * is guessed. Directory scans find every manifest below a path (skipping
 * dependency trees and VCS folders), so a monorepo is one call.
 */
package plugins

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// osvManifestNames are the file names a directory scan looks for, in the
// order a single-manifest resolution tries them.
var osvManifestNames = []string{
	"go.mod", "requirements.txt", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
	"poetry.lock", "Pipfile.lock", "Gemfile.lock", "composer.lock", "pom.xml", "Cargo.lock",
}

// osvManifestSkipDirs are never descended into on a directory scan.
var osvManifestSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "target": true, "dist": true, "build": true, ".venv": true, "venv": true}

// osvMaxManifests bounds a directory scan.
const osvMaxManifests = 25

// findManifests lists every supported manifest under dir (sorted), at most
// osvMaxManifests.
func findManifests(dir string) []string {
	known := map[string]bool{}
	for _, n := range osvManifestNames {
		known[strings.ToLower(n)] = true
	}
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if p != dir && (osvManifestSkipDirs[info.Name()] || strings.HasPrefix(info.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if known[strings.ToLower(info.Name())] {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	if len(out) > osvMaxManifests {
		out = out[:osvMaxManifests]
	}
	return out
}

// yarnEntryRe matches a yarn.lock entry header: "name@range", "name@range":
// or name@range, with optional quotes, possibly several comma-separated.
var yarnEntryRe = regexp.MustCompile(`^"?((?:@[^@/"]+/)?[^@/"\s]+)@`)

// parseYarnLock reads yarn.lock v1 ("name@^1.0.0:" blocks with a version
// line) and berry ("name@npm:^1.0.0": with version: x).
func parseYarnLock(data []byte) []osvDep {
	var deps []osvDep
	seen := map[string]bool{}
	var current string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			current = ""
			if m := yarnEntryRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				current = m[1]
			}
			continue
		}
		t := strings.TrimSpace(line)
		if current != "" && strings.HasPrefix(t, "version") {
			v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "version:"), "version")), `"`)
			key := current + "@" + v
			if v != "" && !seen[key] {
				seen[key] = true
				deps = append(deps, osvDep{Ecosystem: "npm", Name: current, Version: v})
			}
			current = ""
		}
	}
	return deps
}

// pnpmPackageRe matches a pnpm-lock.yaml packages key: /name@version or
// /name@version(peer): or name@version: (v9).
var pnpmPackageRe = regexp.MustCompile(`^\s{2}['"]?/?((?:@[^@/'"]+/)?[^@/'"\s]+)@([^(:'"\s]+)`)

// parsePnpmLock reads the packages section of pnpm-lock.yaml.
func parsePnpmLock(data []byte) []osvDep {
	var deps []osvDep
	seen := map[string]bool{}
	inPackages := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" {
			inPackages = strings.HasPrefix(line, "packages:") || strings.HasPrefix(line, "snapshots:")
			continue
		}
		if !inPackages {
			continue
		}
		if m := pnpmPackageRe.FindStringSubmatch(line); m != nil {
			key := m[1] + "@" + m[2]
			if !seen[key] {
				seen[key] = true
				deps = append(deps, osvDep{Ecosystem: "npm", Name: m[1], Version: m[2]})
			}
		}
	}
	return deps
}

// parsePoetryLock reads [[package]] blocks of poetry.lock.
func parsePoetryLock(data []byte) []osvDep {
	var deps []osvDep
	var name, version string
	flush := func() {
		if name != "" && version != "" {
			deps = append(deps, osvDep{Ecosystem: "PyPI", Name: name, Version: version})
		}
		name, version = "", ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		switch {
		case t == "[[package]]":
			flush()
		case strings.HasPrefix(t, "[") && t != "[[package]]":
			// another table ([package.extras], [metadata]…): keep name/version
		case strings.HasPrefix(t, "name = "):
			name = strings.Trim(strings.TrimPrefix(t, "name = "), `"`)
		case strings.HasPrefix(t, "version = "):
			version = strings.Trim(strings.TrimPrefix(t, "version = "), `"`)
		}
	}
	flush()
	return deps
}

// parsePipfileLock reads the default and develop sections of Pipfile.lock.
func parsePipfileLock(data []byte) ([]osvDep, error) {
	var lock struct {
		Default map[string]struct {
			Version string `json:"version"`
		} `json:"default"`
		Develop map[string]struct {
			Version string `json:"version"`
		} `json:"develop"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	var deps []osvDep
	for _, section := range []map[string]struct {
		Version string `json:"version"`
	}{lock.Default, lock.Develop} {
		names := make([]string, 0, len(section))
		for n := range section {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			v := strings.TrimPrefix(strings.TrimSpace(section[n].Version), "==")
			if v != "" {
				deps = append(deps, osvDep{Ecosystem: "PyPI", Name: n, Version: v})
			}
		}
	}
	return deps, nil
}

// gemSpecRe matches a Gemfile.lock specs line: "    name (version)".
var gemSpecRe = regexp.MustCompile(`^    ([A-Za-z0-9_.\-]+) \(([^)\s]+)\)$`)

// parseGemfileLock reads the specs of every GEM section in Gemfile.lock.
func parseGemfileLock(data []byte) []osvDep {
	var deps []osvDep
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if m := gemSpecRe.FindStringSubmatch(sc.Text()); m != nil {
			key := m[1] + "@" + m[2]
			if !seen[key] {
				seen[key] = true
				deps = append(deps, osvDep{Ecosystem: "RubyGems", Name: m[1], Version: m[2]})
			}
		}
	}
	return deps
}

// parseComposerLock reads packages and packages-dev of composer.lock.
func parseComposerLock(data []byte) ([]osvDep, error) {
	var lock struct {
		Packages []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packages"`
		PackagesDev []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packages-dev"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	var deps []osvDep
	for _, p := range append(lock.Packages, lock.PackagesDev...) {
		v := strings.TrimPrefix(strings.TrimSpace(p.Version), "v")
		if p.Name != "" && v != "" {
			deps = append(deps, osvDep{Ecosystem: "Packagist", Name: p.Name, Version: v})
		}
	}
	return deps, nil
}

// parsePomXML reads the dependencies of a pom.xml, resolving ${property}
// versions from the pom's own <properties>; a dependency without a
// literal version (managed by a parent or a BOM) is skipped.
func parsePomXML(data []byte) ([]osvDep, error) {
	var pom struct {
		Properties struct {
			Any []xmlKV `xml:",any"`
		} `xml:"properties"`
		Dependencies struct {
			Dependency []struct {
				GroupID    string `xml:"groupId"`
				ArtifactID string `xml:"artifactId"`
				Version    string `xml:"version"`
			} `xml:"dependency"`
		} `xml:"dependencies"`
	}
	if err := xml.Unmarshal(data, &pom); err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, kv := range pom.Properties.Any {
		props[kv.XMLName.Local] = strings.TrimSpace(kv.Value)
	}
	var deps []osvDep
	for _, d := range pom.Dependencies.Dependency {
		v := strings.TrimSpace(d.Version)
		if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
			v = props[strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}")]
		}
		if v == "" || strings.ContainsAny(v, "${}[],()") || d.GroupID == "" || d.ArtifactID == "" {
			continue
		}
		deps = append(deps, osvDep{Ecosystem: "Maven", Name: d.GroupID + ":" + d.ArtifactID, Version: v})
	}
	return deps, nil
}

// xmlKV is one <properties> child.
type xmlKV struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}
