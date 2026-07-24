/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"runtime/debug"
	"testing"
)

// withDetectSeams instala seams de detecção e restaura no cleanup.
func withDetectSeams(t *testing.T, execPath, ldflagsVersion, moduleVersion string, buildInfoOK bool, existingFiles map[string]bool) {
	t.Helper()
	origExec, origSymlink, origBI, origLD, origExists :=
		executableFn, evalSymlinksFn, readBuildInfoFn, ldflagsVersionFn, fileExistsFn
	t.Cleanup(func() {
		executableFn, evalSymlinksFn, readBuildInfoFn, ldflagsVersionFn, fileExistsFn =
			origExec, origSymlink, origBI, origLD, origExists
	})

	executableFn = func() (string, error) { return execPath, nil }
	evalSymlinksFn = func(p string) (string, error) { return p, nil }
	ldflagsVersionFn = func() string { return ldflagsVersion }
	readBuildInfoFn = func() (*debug.BuildInfo, bool) {
		if !buildInfoOK {
			return nil, false
		}
		return &debug.BuildInfo{Main: debug.Module{Version: moduleVersion}}, true
	}
	fileExistsFn = func(path string) bool { return existingFiles[path] }
}

func TestDetectClassifiesEachChannel(t *testing.T) {
	cases := []struct {
		name           string
		execPath       string
		ldflagsVersion string
		moduleVersion  string
		buildInfoOK    bool
		files          map[string]bool
		want           Method
	}{
		{
			name:           "homebrew apple silicon",
			execPath:       "/opt/homebrew/Cellar/chatcli/1.169.5/bin/chatcli",
			ldflagsVersion: "v1.169.5", // binário do brew É o asset estampado
			want:           MethodHomebrew,
		},
		{
			name:           "homebrew linuxbrew",
			execPath:       "/home/linuxbrew/.linuxbrew/Cellar/chatcli/1.169.5/bin/chatcli",
			ldflagsVersion: "v1.169.5",
			want:           MethodHomebrew,
		},
		{
			name:           "container docker",
			execPath:       "/usr/local/bin/chatcli",
			ldflagsVersion: "v1.169.5",
			files:          map[string]bool{"/.dockerenv": true},
			want:           MethodDocker,
		},
		{
			name:           "container podman",
			execPath:       "/usr/local/bin/chatcli",
			ldflagsVersion: "v1.169.5",
			files:          map[string]bool{"/run/.containerenv": true},
			want:           MethodDocker,
		},
		{
			name:           "release binary baixado manualmente",
			execPath:       "/usr/local/bin/chatcli",
			ldflagsVersion: "v1.169.5",
			want:           MethodReleaseBinary,
		},
		{
			name:           "go install",
			execPath:       "/Users/someone/go/bin/chatcli",
			ldflagsVersion: "dev",
			moduleVersion:  "v1.169.5",
			buildInfoOK:    true,
			want:           MethodGoInstall,
		},
		{
			name:           "go install pseudo-version",
			execPath:       "/Users/someone/go/bin/chatcli",
			ldflagsVersion: "dev",
			moduleVersion:  "v1.169.6-0.20260720123456-abcdef123456",
			buildInfoOK:    true,
			want:           MethodGoInstall,
		},
		{
			name:           "build local do checkout",
			execPath:       "/Users/someone/GolandProjects/chatcli/chatcli",
			ldflagsVersion: "dev",
			moduleVersion:  "(devel)",
			buildInfoOK:    true,
			want:           MethodSourceBuild,
		},
		{
			name:           "sem build info",
			execPath:       "/somewhere/chatcli",
			ldflagsVersion: "dev",
			buildInfoOK:    false,
			want:           MethodUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDetectSeams(t, tc.execPath, tc.ldflagsVersion, tc.moduleVersion, tc.buildInfoOK, tc.files)
			info := Detect()
			if info.Method != tc.want {
				t.Fatalf("Detect() = %s, esperado %s", info.Method, tc.want)
			}
			if info.ExecPath != tc.execPath {
				t.Fatalf("ExecPath = %q, esperado %q", info.ExecPath, tc.execPath)
			}
		})
	}
}

func TestMethodCapabilities(t *testing.T) {
	if !MethodHomebrew.Automatable() || !MethodGoInstall.Automatable() || !MethodReleaseBinary.Automatable() {
		t.Fatal("canais brew/go install/release devem ser automatizáveis")
	}
	if MethodDocker.Automatable() || MethodSourceBuild.Automatable() || MethodUnknown.Automatable() {
		t.Fatal("docker/source/unknown nunca são automatizáveis")
	}
	// Homebrew é automatizável via /update, mas NUNCA silencioso em background.
	if MethodHomebrew.AutoApplicable() {
		t.Fatal("homebrew não pode ser auto-aplicável em background")
	}
	if !MethodGoInstall.AutoApplicable() || !MethodReleaseBinary.AutoApplicable() {
		t.Fatal("go install e release binary devem ser auto-aplicáveis")
	}
}

func TestIsHomebrewPathRejectsNonCellar(t *testing.T) {
	for _, p := range []string{"", "/usr/local/bin/chatcli", "/opt/homebrew/Cellar/other/1.0/bin/other"} {
		if isHomebrewPath(p) {
			t.Fatalf("isHomebrewPath(%q) = true, esperado false", p)
		}
	}
	if !isHomebrewPath("/usr/local/Cellar/chatcli/1.169.5/bin/chatcli") {
		t.Fatal("prefixo Intel /usr/local com Cellar/chatcli deve ser reconhecido")
	}
}
