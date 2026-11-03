//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"os"
	"os/exec"
)

// execRestart starts bin as a child that inherits the console, waits for it
// and leaves with its exit code: Windows has no exec(2), and a parent that
// returned to the shell right away would fight the child for the console.
func execRestart(bin string, argv, env []string) error {
	args := []string{}
	if len(argv) > 1 {
		args = argv[1:]
	}
	cmd := exec.Command(bin, args...) //#nosec G204 -- bin is the installed chatcli resolved from argv[0]/PATH
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		os.Exit(0)
	case errors.As(err, &exitErr):
		os.Exit(exitErr.ExitCode())
	}
	return err
}
