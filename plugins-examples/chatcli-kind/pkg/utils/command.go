package utils

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

const (
	ShortTimeout    = 30 * time.Second
	DefaultTimeout  = 5 * time.Minute
	ExtendedTimeout = 10 * time.Minute
)

func RunCommand(name string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), fmt.Errorf("command timed out after %v", timeout)
	}

	return out.String(), err
}
