// Package config provides configuration management for ChatCLI with
// environment variable loading, versioned migration, and runtime defaults.
//
// Configuration is loaded from .env files, environment variables, and
// command-line flags. The ConfigManager supports versioned schema migration
// to handle upgrades between ChatCLI versions without losing user settings.
//
// # Defaults
//
// Default values for all providers, timeouts, buffer sizes, and feature
// flags are defined in defaults.go. These can be overridden via environment
// variables or .env files.
package config
