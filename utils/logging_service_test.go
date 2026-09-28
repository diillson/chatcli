/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import "testing"

func TestServiceStderrLogging(t *testing.T) {
	cases := []struct {
		name     string
		force    string
		env      string
		terminal bool
		want     bool
	}{
		{"container without a terminal", "", "", false, true},
		{"interactive terminal keeps the file only", "", "", true, false},
		{"dev console already prints", "", "dev", false, false},
		{"forced on in a terminal", "true", "", true, true},
		{"forced off in a container", "false", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CHATCLI_LOG_STDERR", c.force)
			t.Setenv("CHATCLI_ENV", c.env)
			t.Setenv("ENV", "")
			if got := serviceStderrLogging(c.terminal); got != c.want {
				t.Fatalf("serviceStderrLogging(%v) = %v, want %v", c.terminal, got, c.want)
			}
		})
	}
}

func TestLogRotationFromEnv(t *testing.T) {
	for _, k := range []string{"CHATCLI_LOG_MAX_SIZE_MB", "CHATCLI_LOG_MAX_BACKUPS", "CHATCLI_LOG_MAX_AGE_DAYS", "CHATCLI_LOG_COMPRESS"} {
		t.Setenv(k, "")
	}
	if got := logRotationFromEnv(100); got != (logRotation{maxSizeMB: 100, maxBackups: 3, maxAgeDays: 28, compress: true}) {
		t.Fatalf("defaults changed: %+v", got)
	}

	t.Setenv("CHATCLI_LOG_MAX_SIZE_MB", "10")
	t.Setenv("CHATCLI_LOG_MAX_BACKUPS", "0")
	t.Setenv("CHATCLI_LOG_MAX_AGE_DAYS", "7")
	t.Setenv("CHATCLI_LOG_COMPRESS", "false")
	if got := logRotationFromEnv(100); got != (logRotation{maxSizeMB: 10, maxBackups: 0, maxAgeDays: 7, compress: false}) {
		t.Fatalf("the Instance logRotation settings were not applied: %+v", got)
	}

	t.Setenv("CHATCLI_LOG_MAX_SIZE_MB", "-1")
	t.Setenv("CHATCLI_LOG_MAX_AGE_DAYS", "junk")
	if got := logRotationFromEnv(100); got.maxSizeMB != 100 || got.maxAgeDays != 28 {
		t.Fatalf("invalid values must keep the defaults: %+v", got)
	}
}
