/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func depSet(deps []osvDep) map[string]string {
	m := map[string]string{}
	for _, d := range deps {
		m[d.Ecosystem+"/"+d.Name] = d.Version
	}
	return m
}

func TestParseYarnLock_V1AndBerry(t *testing.T) {
	v1 := "# yarn lockfile v1\n\n\"@babel/core@^7.0.0\", \"@babel/core@^7.1.0\":\n  version \"7.2.0\"\n  resolved \"x\"\n\nlodash@^4.17.0:\n  version \"4.17.21\"\n"
	got := depSet(parseYarnLock([]byte(v1)))
	if got["npm/@babel/core"] != "7.2.0" || got["npm/lodash"] != "4.17.21" || len(got) != 2 {
		t.Fatalf("yarn v1 = %v", got)
	}
	berry := "__metadata:\n  version: 6\n\n\"minimist@npm:^1.2.6\":\n  version: 1.2.8\n  resolution: \"minimist@npm:1.2.8\"\n"
	if got := depSet(parseYarnLock([]byte(berry))); got["npm/minimist"] != "1.2.8" {
		t.Fatalf("yarn berry = %v", got)
	}
}

func TestParsePnpmLock(t *testing.T) {
	lock := "lockfileVersion: '6.0'\n\ndependencies:\n  lodash:\n    specifier: ^4\n    version: 4.17.21\n\npackages:\n\n  /lodash@4.17.21:\n    resolution: {integrity: sha512-x}\n\n  /@types/node@18.0.0(typescript@5.0.0):\n    resolution: {integrity: sha512-y}\n"
	got := depSet(parsePnpmLock([]byte(lock)))
	if got["npm/lodash"] != "4.17.21" || got["npm/@types/node"] != "18.0.0" || len(got) != 2 {
		t.Fatalf("pnpm = %v", got)
	}
	v9 := "lockfileVersion: '9.0'\n\npackages:\n\n  minimist@1.2.8:\n    resolution: {integrity: sha512-z}\n"
	if got := depSet(parsePnpmLock([]byte(v9))); got["npm/minimist"] != "1.2.8" {
		t.Fatalf("pnpm v9 = %v", got)
	}
}

func TestParsePoetryPipfileGemfileComposerPom(t *testing.T) {
	poetry := "[[package]]\nname = \"requests\"\nversion = \"2.19.0\"\n\n[package.extras]\nsecurity = []\n\n[[package]]\nname = \"urllib3\"\nversion = \"1.26.0\"\n\n[metadata]\nlock-version = \"2.0\"\n"
	if got := depSet(parsePoetryLock([]byte(poetry))); got["PyPI/requests"] != "2.19.0" || got["PyPI/urllib3"] != "1.26.0" || len(got) != 2 {
		t.Fatalf("poetry = %v", got)
	}
	pipfile := `{"default":{"requests":{"version":"==2.19.0"}},"develop":{"pytest":{"version":"==7.0.0"}}}`
	deps, err := parsePipfileLock([]byte(pipfile))
	if got := depSet(deps); err != nil || got["PyPI/requests"] != "2.19.0" || got["PyPI/pytest"] != "7.0.0" {
		t.Fatalf("pipfile = %v %v", got, err)
	}
	if _, err := parsePipfileLock([]byte("{bad")); err == nil {
		t.Fatal("bad Pipfile.lock must error")
	}
	gemfile := "GEM\n  remote: https://rubygems.org/\n  specs:\n    rack (2.2.3)\n    rails (7.0.0)\n      actionpack (= 7.0.0)\n\nPLATFORMS\n  ruby\n\nDEPENDENCIES\n  rails\n"
	if got := depSet(parseGemfileLock([]byte(gemfile))); got["RubyGems/rack"] != "2.2.3" || got["RubyGems/rails"] != "7.0.0" || len(got) != 2 {
		t.Fatalf("gemfile = %v", got)
	}
	composer := `{"packages":[{"name":"monolog/monolog","version":"v2.0.0"}],"packages-dev":[{"name":"phpunit/phpunit","version":"9.5.0"}]}`
	deps, err = parseComposerLock([]byte(composer))
	if got := depSet(deps); err != nil || got["Packagist/monolog/monolog"] != "2.0.0" || got["Packagist/phpunit/phpunit"] != "9.5.0" {
		t.Fatalf("composer = %v %v", got, err)
	}
	pom := `<project><properties><log4j.version>2.14.1</log4j.version></properties><dependencies>
<dependency><groupId>org.apache.logging.log4j</groupId><artifactId>log4j-core</artifactId><version>${log4j.version}</version></dependency>
<dependency><groupId>com.google.guava</groupId><artifactId>guava</artifactId><version>30.0-jre</version></dependency>
<dependency><groupId>org.managed</groupId><artifactId>bom-managed</artifactId></dependency>
<dependency><groupId>org.range</groupId><artifactId>ranged</artifactId><version>[1.0,2.0)</version></dependency>
</dependencies></project>`
	deps, err = parsePomXML([]byte(pom))
	got := depSet(deps)
	if err != nil || got["Maven/org.apache.logging.log4j:log4j-core"] != "2.14.1" || got["Maven/com.google.guava:guava"] != "30.0-jre" || len(got) != 2 {
		t.Fatalf("pom = %v %v", got, err)
	}
	if _, err := parsePomXML([]byte("<project")); err == nil {
		t.Fatal("bad pom must error")
	}
}

func TestOsvScan_RecursiveMonorepo(t *testing.T) {
	defer mockOSV(t, "requests", "2.19.0")()
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("api/requirements.txt", "requests==2.19.0\n")
	mk("web/package-lock.json", `{"lockfileVersion":3,"packages":{"node_modules/lodash":{"version":"4.17.21"}}}`)
	mk("web/node_modules/dep/package-lock.json", `{"lockfileVersion":3,"packages":{"node_modules/x":{"version":"1.0.0"}}}`)
	mk("svc/Gemfile.lock", "GEM\n  specs:\n    rack (2.2.3)\n")

	found := findManifests(root)
	if len(found) != 3 {
		t.Fatalf("manifests = %v", found)
	}
	for _, f := range found {
		if strings.Contains(f, "node_modules") {
			t.Fatalf("node_modules must be skipped: %v", found)
		}
	}
	out, err := NewBuiltinOsvPlugin().Execute(context.Background(), []string{`{"cmd":"scan","args":{"path":"` + root + `","recursive":true}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3 manifest(s)") || !strings.Contains(out, "requests@2.19.0") || !strings.Contains(out, "GHSA-xxxx") || !strings.Contains(out, "Gemfile.lock") {
		t.Fatalf("recursive scan = %q", out)
	}
	// Flattened argv carries recursive as a string and must still recurse.
	out, err = NewBuiltinOsvPlugin().Execute(context.Background(), []string{"scan", "--path", root, "--recursive", "true"})
	if err != nil || !strings.Contains(out, "3 manifest(s)") {
		t.Fatalf("flat recursive = %q %v", out, err)
	}
	// A directory without a manifest says how to go deeper.
	_, err = NewBuiltinOsvPlugin().Execute(context.Background(), []string{`{"cmd":"scan","args":{"path":"` + root + `"}}`})
	if err == nil || !strings.Contains(err.Error(), "recursive:true") {
		t.Fatalf("non-recursive on a monorepo root must hint: %v", err)
	}
	out, err = NewBuiltinOsvPlugin().Execute(context.Background(), []string{`{"cmd":"scan","args":{"path":"` + t.TempDir() + `","recursive":true}}`})
	if err != nil || !strings.Contains(out, "no supported manifest below") {
		t.Fatalf("empty tree = %q %v", out, err)
	}
	if !strings.Contains(NewBuiltinOsvPlugin().Schema(), "yarn.lock") || !strings.Contains(NewBuiltinOsvPlugin().Usage(), "pom.xml") {
		t.Fatal("schema/usage must list the new manifests")
	}
}
