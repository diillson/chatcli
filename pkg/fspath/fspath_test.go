/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package fspath

import (
	"runtime"
	"testing"
)

func TestWithinBoundary(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		root        string
		want        bool
		windowsOnly bool
	}{
		{name: "unix inside", target: "/home/u/proj/main.go", root: "/home/u/proj", want: true},
		{name: "unix equals root", target: "/home/u/proj", root: "/home/u/proj", want: true},
		{name: "unix outside", target: "/home/u/other/main.go", root: "/home/u/proj", want: false},
		{name: "unix sibling prefix is not inside", target: "/home/u/projext/f.go", root: "/home/u/proj", want: false},
		{name: "unix root boundary", target: "/etc/passwd", root: "/", want: true},
		{name: "trailing separator on root", target: "/home/u/proj/f.go", root: "/home/u/proj/", want: true},
		{name: "empty target", target: "", root: "/home/u", want: false},
		{name: "empty root", target: "/home/u", root: "", want: false},

		// The exact failure from the field: LLM sends forward slashes, the
		// workspace root is backslashed.
		{name: "win forward slash target", target: "C:/Users/x/deployment.yaml", root: `C:\Users\x`, want: true, windowsOnly: true},
		{name: "win backslash target", target: `C:\Users\x\deployment.yaml`, root: `C:\Users\x`, want: true, windowsOnly: true},
		{name: "win case-insensitive", target: `c:\users\X\f.yaml`, root: `C:\Users\x`, want: true, windowsOnly: true},
		{name: "win equals root mixed slashes", target: "C:/Users/x", root: `C:\Users\x`, want: true, windowsOnly: true},
		{name: "win outside", target: `C:\Users\alt\f.yaml`, root: `C:\Users\x`, want: false, windowsOnly: true},
		{name: "win sibling prefix", target: `C:\Users\xy\f.yaml`, root: `C:\Users\x`, want: false, windowsOnly: true},
		{name: "win drive root boundary", target: `C:\Users\x\f.yaml`, root: `C:\`, want: true, windowsOnly: true},
		{name: "win different drive", target: `D:\Users\x\f.yaml`, root: `C:\Users\x`, want: false, windowsOnly: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.windowsOnly && runtime.GOOS != "windows" {
				t.Skip("windows-only semantics (filepath.Clean is OS-specific)")
			}
			if got := WithinBoundary(tt.target, tt.root); got != tt.want {
				t.Errorf("WithinBoundary(%q, %q) = %v, want %v", tt.target, tt.root, got, tt.want)
			}
		})
	}
}

func TestEqual(t *testing.T) {
	if !Equal("/home/u/proj", "/home/u/proj/") {
		t.Error("trailing separator should not affect equality")
	}
	if Equal("/home/u/a", "/home/u/b") {
		t.Error("distinct paths must not be equal")
	}
	if !Equal("", "") {
		t.Error("two empty paths are equal")
	}
	if Equal("", "/home/u") {
		t.Error("empty vs non-empty must not be equal")
	}
	if runtime.GOOS == "windows" {
		if !Equal(`C:\Users\X\.kube\config`, "c:/users/x/.kube/config") {
			t.Error("windows equality must ignore case and separator style")
		}
	}
}

func TestWindowsSystemRoots(t *testing.T) {
	roots := WindowsSystemRoots()
	if runtime.GOOS != "windows" {
		if roots != nil {
			t.Errorf("expected nil on %s, got %v", runtime.GOOS, roots)
		}
		return
	}
	if len(roots) == 0 {
		t.Error("expected at least one system root on windows")
	}
}
