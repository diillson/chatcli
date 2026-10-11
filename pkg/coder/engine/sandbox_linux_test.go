//go:build linux

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"strings"
	"testing"
)

func TestBuildBwrapArgs(t *testing.T) {
	ws := "/work/ws"

	work := strings.Join(buildBwrapArgs(SandboxWorkspace, ws), " ")
	if !strings.Contains(work, "--ro-bind / /") {
		t.Fatalf("workspace bwrap must bind root read-only:\n%s", work)
	}
	if !strings.Contains(work, "--bind-try /work/ws /work/ws") {
		t.Fatalf("workspace must be bound read-write:\n%s", work)
	}
	if strings.Contains(work, "--unshare-net") {
		t.Fatal("workspace mode must NOT unshare the network")
	}

	strict := strings.Join(buildBwrapArgs(SandboxStrict, ws), " ")
	if !strings.Contains(strict, "--unshare-net") {
		t.Fatalf("strict mode must unshare the network:\n%s", strict)
	}
}

func TestWrapWithSandbox_LinuxShape(t *testing.T) {
	name, args, _ := wrapWithSandbox(SandboxWorkspace, "/ws", "sh", "-c", "echo hi")
	if name == sandboxBinary {
		if args[len(args)-3] != "sh" || args[len(args)-2] != "-c" || args[len(args)-1] != "echo hi" {
			t.Fatalf("shell trio must be the tail of the bwrap args: %v", args)
		}
	}
}

// TestBuildBwrapArgs_ProtectsChatcliState: ~/.chatcli is re-bound read-only
// after the writable binds, so a workspace at $HOME cannot expose it.
func TestBuildBwrapArgs_ProtectsChatcliState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	args := strings.Join(buildBwrapArgs(SandboxWorkspace, home), " ")
	ro := "--ro-bind-try " + home + "/.chatcli " + home + "/.chatcli"
	rw := "--bind-try " + home + " " + home
	if !strings.Contains(args, ro) {
		t.Fatalf("chatcli state must be bound read-only:\n%s", args)
	}
	if strings.Index(args, ro) < strings.Index(args, rw) {
		t.Fatalf("the read-only bind must come after the writable workspace bind:\n%s", args)
	}
}
