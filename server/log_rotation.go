/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"os"
	"strconv"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// LogRotationConfig holds log rotation settings.
type LogRotationConfig struct {
	// FilePath is the log file path. Empty means stdout only.
	FilePath string
	// MaxSizeMB is the maximum size in megabytes before rotation.
	MaxSizeMB int
	// MaxBackups is the maximum number of old log files to retain.
	MaxBackups int
	// MaxAgeDays is the maximum age in days before a log file is deleted.
	MaxAgeDays int
	// Compress determines whether rotated files are gzipped.
	Compress bool
}

// DefaultLogRotationConfig returns production defaults from environment variables.
func DefaultLogRotationConfig() LogRotationConfig {
	cfg := LogRotationConfig{
		FilePath:   os.Getenv("CHATCLI_LOG_FILE"),
		MaxSizeMB:  100,
		MaxBackups: 5,
		MaxAgeDays: 30,
		Compress:   true,
	}

	if v := os.Getenv("CHATCLI_LOG_MAX_SIZE_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxSizeMB = n
		}
	}
	if v := os.Getenv("CHATCLI_LOG_MAX_BACKUPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.MaxBackups = n
		}
	}
	if v := os.Getenv("CHATCLI_LOG_MAX_AGE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxAgeDays = n
		}
	}
	if v := os.Getenv("CHATCLI_LOG_COMPRESS"); v == "false" {
		cfg.Compress = false
	}

	return cfg
}

// NewRotatingLogger creates a zap.Logger with log rotation support.
// If FilePath is empty, returns a standard production logger (stdout).
func NewRotatingLogger(cfg LogRotationConfig) (*zap.Logger, error) {
	if cfg.FilePath == "" {
		return zap.NewProduction()
	}

	lj := &lumberjack.Logger{
		Filename:   cfg.FilePath,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "timestamp"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	// Write to both file (with rotation) and stdout
	fileCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(lj),
		zap.InfoLevel,
	)
	stdoutCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout),
		zap.InfoLevel,
	)

	core := zapcore.NewTee(fileCore, stdoutCore)
	return zap.New(core, zap.AddCaller()), nil
}
