/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// watchConfigFile represents the YAML config file structure.
type watchConfigFile struct {
	Targets         []WatchTarget `yaml:"targets"`
	Interval        string        `yaml:"interval"`
	Window          string        `yaml:"window"`
	MaxLogLines     int           `yaml:"maxLogLines"`
	MaxContextChars int           `yaml:"maxContextChars"`
}

// LoadMultiWatchConfig loads a MultiWatchConfig from a YAML file.
func LoadMultiWatchConfig(path string) (*MultiWatchConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read watch config file: %w", err)
	}

	var file watchConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("failed to parse watch config file: %w", err)
	}

	if len(file.Targets) == 0 {
		return nil, fmt.Errorf("watch config file has no targets")
	}

	cfg := &MultiWatchConfig{
		Targets:         file.Targets,
		MaxLogLines:     file.MaxLogLines,
		MaxContextChars: file.MaxContextChars,
	}

	if file.Interval != "" {
		d, err := time.ParseDuration(file.Interval)
		if err != nil {
			return nil, fmt.Errorf("invalid interval %q: %w", file.Interval, err)
		}
		cfg.Interval = d
	} else {
		cfg.Interval = 30 * time.Second
	}

	if file.Window != "" {
		d, err := time.ParseDuration(file.Window)
		if err != nil {
			return nil, fmt.Errorf("invalid window %q: %w", file.Window, err)
		}
		cfg.Window = d
	} else {
		cfg.Window = 2 * time.Hour
	}

	if cfg.MaxLogLines <= 0 {
		cfg.MaxLogLines = 100
	}
	if cfg.MaxContextChars <= 0 {
		cfg.MaxContextChars = 32000
	}

	for i, t := range cfg.Targets {
		if t.Deployment == "" {
			return nil, fmt.Errorf("target[%d]: deployment is required", i)
		}
		if t.Namespace == "" {
			cfg.Targets[i].Namespace = "default"
		}
		if t.MetricsPath == "" && t.MetricsPort > 0 {
			cfg.Targets[i].MetricsPath = "/metrics"
		}
	}

	return cfg, nil
}

// SingleTargetToMulti converts a legacy single-target WatchConfig to a MultiWatchConfig.
func SingleTargetToMulti(cfg WatchConfig) MultiWatchConfig {
	return MultiWatchConfig{
		Targets: []WatchTarget{
			{
				Deployment: cfg.Deployment,
				Namespace:  cfg.Namespace,
			},
		},
		Interval:        cfg.Interval,
		Window:          cfg.Window,
		MaxLogLines:     cfg.MaxLogLines,
		Kubeconfig:      cfg.Kubeconfig,
		MaxContextChars: 32000,
	}
}
