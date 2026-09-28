/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/term"
)

// Logging for long-running services.
//
// The CLI logs to a rotated file only, so an interactive terminal is never
// cluttered. A server in a container has no terminal and nobody reads that
// file: its logs have to reach the container's standard streams, which is
// what `kubectl logs`, `docker logs` and every log collector read. Services
// therefore also write JSON lines to stderr — never stdout, which carries
// the protocol on the stdio transports.

// logRotation is the rotation policy of the log file.
type logRotation struct {
	maxSizeMB  int
	maxBackups int
	maxAgeDays int
	compress   bool
}

// logRotationFromEnv reads the rotation policy. CHATCLI_LOG_MAX_SIZE_MB,
// CHATCLI_LOG_MAX_BACKUPS, CHATCLI_LOG_MAX_AGE_DAYS and CHATCLI_LOG_COMPRESS
// are what the Instance CRD's spec.features.logRotation sets; without them
// the defaults stay the ones the logger always used.
func logRotationFromEnv(defaultSizeMB int) logRotation {
	r := logRotation{maxSizeMB: defaultSizeMB, maxBackups: 3, maxAgeDays: 28, compress: true}
	if n, ok := positiveEnvInt("CHATCLI_LOG_MAX_SIZE_MB"); ok {
		r.maxSizeMB = n
	}
	if v := strings.TrimSpace(os.Getenv("CHATCLI_LOG_MAX_BACKUPS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			r.maxBackups = n
		}
	}
	if n, ok := positiveEnvInt("CHATCLI_LOG_MAX_AGE_DAYS"); ok {
		r.maxAgeDays = n
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CHATCLI_LOG_COMPRESS")), "false") {
		r.compress = false
	}
	return r
}

func positiveEnvInt(key string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// InitializeServiceLogger is InitializeLogger for long-running services
// (`chatcli server`, `chatcli gateway`): the same rotated file, plus JSON
// lines on stderr when serviceStderrLogging says so.
func InitializeServiceLogger() (*zap.Logger, error) {
	logger, err := InitializeLogger()
	if err != nil {
		return nil, err
	}
	if !serviceStderrLogging(term.IsTerminal(int(os.Stderr.Fd()))) {
		return logger, nil
	}
	return logger.WithOptions(zap.WrapCore(func(c zapcore.Core) zapcore.Core {
		return zapcore.NewTee(c, stderrJSONCore(c))
	})), nil
}

// serviceStderrLogging decides whether a service also logs to stderr.
// CHATCLI_LOG_STDERR=true|false forces it. Otherwise it is on when stderr
// is not a terminal — a container, a systemd unit, a pipe — and the
// development console (CHATCLI_ENV=dev) is not already printing the logs.
func serviceStderrLogging(stderrIsTerminal bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_LOG_STDERR"))) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	env := strings.ToLower(os.Getenv("CHATCLI_ENV"))
	if env == "" {
		env = strings.ToLower(GetEnvOrDefault("ENV", "prod"))
	}
	if env != "prod" {
		return false
	}
	return !stderrIsTerminal
}

// stderrJSONCore writes the same entries as base, as JSON lines on stderr,
// at base's level.
func stderrJSONCore(base zapcore.Core) zapcore.Core {
	enc := zap.NewProductionEncoderConfig()
	enc.EncodeTime = zapcore.ISO8601TimeEncoder
	return zapcore.NewCore(zapcore.NewJSONEncoder(enc), zapcore.Lock(zapcore.AddSync(os.Stderr)), base)
}
