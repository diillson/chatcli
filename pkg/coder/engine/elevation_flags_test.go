/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExec_ModelAllowUnsafeIgnored: --allow-unsafe passed in the tool call
// (by a model or a remote caller) no longer lifts the dangerous-pattern
// block; the command does not run.
func TestExec_ModelAllowUnsafeIgnored(t *testing.T) {
	t.Setenv(AllowUnsafeEnv, "")
	e, _, _, dir := newGuardEngine(t)
	marker := filepath.Join(dir, "PWNED")
	payload := "eval \"touch " + marker + "\""

	for _, sub := range []string{"exec", "test"} {
		for _, flagForm := range [][]string{{"--allow-unsafe"}, {"--allow-unsafe=true"}, {"-allow-unsafe"}} {
			args := append([]string{"--cmd", payload, "--dir", dir}, flagForm...)
			err := e.Execute(context.Background(), sub, args)
			if err == nil {
				t.Fatalf("%s %v ran an unsafe command", sub, flagForm)
			}
			if !strings.Contains(err.Error(), AllowUnsafeEnv) {
				t.Fatalf("the refusal must name the operator setting, got: %v", err)
			}
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the unsafe payload executed")
	}
}

// TestExec_ModelAllowSudoIgnored: --allow-sudo from the tool call does not
// permit sudo; only CHATCLI_AGENT_ALLOW_SUDO does.
func TestExec_ModelAllowSudoIgnored(t *testing.T) {
	t.Setenv(AllowUnsafeEnv, "")
	t.Setenv(AllowSudoEnv, "")
	e, _, _, dir := newGuardEngine(t)
	if err := e.Execute(context.Background(), "exec", []string{"--allow-sudo", "--cmd", "echo sudo-check", "--dir", dir}); err == nil {
		t.Fatal("a tool-supplied --allow-sudo lifted the sudo block")
	}
}

// TestExec_ElevationFlagDoesNotSwallowNextArg: the ignored boolean flag must
// not consume the following argument as its value.
func TestExec_ElevationFlagDoesNotSwallowNextArg(t *testing.T) {
	e, out, _, dir := newGuardEngine(t)
	if err := e.Execute(context.Background(), "exec", []string{"--allow-unsafe", "--cmd", "echo still-parsed", "--dir", dir}); err != nil {
		t.Fatalf("harmless command failed: %v", err)
	}
	if !strings.Contains(out.String(), "still-parsed") {
		t.Fatalf("--cmd after the ignored flag was lost: %q", out.String())
	}
}

// TestExec_OperatorSettingsLiftBlocks: the operator settings are the only
// way to lift the blocks.
func TestExec_OperatorSettingsLiftBlocks(t *testing.T) {
	e, out, _, dir := newGuardEngine(t)

	// "echo ... rm -rf ..." matches the dangerous pattern but is harmless.
	harmlessMatch := "echo rm -rf nothing-here"
	t.Setenv(AllowUnsafeEnv, "")
	if err := e.Execute(context.Background(), "exec", []string{"--cmd", harmlessMatch, "--dir", dir}); err == nil {
		t.Fatal("the pattern must be blocked without the operator setting")
	}
	t.Setenv(AllowUnsafeEnv, "true")
	if err := e.Execute(context.Background(), "exec", []string{"--cmd", harmlessMatch, "--dir", dir}); err != nil {
		t.Fatalf("%s=true must lift the block: %v", AllowUnsafeEnv, err)
	}
	if !strings.Contains(out.String(), "nothing-here") {
		t.Fatalf("command did not run: %q", out.String())
	}

	t.Setenv(AllowUnsafeEnv, "")
	t.Setenv(AllowSudoEnv, "true")
	if err := checkUnsafeCommand("echo sudo-check"); err != nil {
		t.Fatalf("%s=true must permit sudo: %v", AllowSudoEnv, err)
	}
}

// TestMetadata_ElevationFlagsNotAdvertised: the model is never told about
// flags it cannot use.
func TestMetadata_ElevationFlagsNotAdvertised(t *testing.T) {
	if s := GetSchema() + GetMetadata().Usage; strings.Contains(s, "allow-unsafe") || strings.Contains(s, "allow-sudo") {
		t.Fatal("the schema still advertises an elevation flag")
	}
}
