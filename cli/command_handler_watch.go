/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/k8s"
)

// handleWatchCommand routes /watch subcommands: start, stop, status.
func (ch *CommandHandler) handleWatchCommand(userInput string) {
	args := strings.Fields(userInput)

	// /watch alone or /watch status
	if len(args) < 2 || args[1] == "status" {
		ch.handleWatchStatusCommand()
		return
	}

	switch args[1] {
	case "start":
		ch.handleWatchStartCommand(args[2:])
	case "stop":
		ch.handleWatchStopCommand()
	default:
		fmt.Println(colorize(i18n.T("watch.usage.header"), ColorYellow))
		fmt.Println(colorize(i18n.T("watch.usage.start"), ColorYellow))
		fmt.Println(colorize(i18n.T("watch.usage.stop"), ColorYellow))
		fmt.Println(colorize(i18n.T("watch.usage.status"), ColorYellow))
	}
}

// handleWatchStartCommand starts a K8s watcher in background from interactive mode.
func (ch *CommandHandler) handleWatchStartCommand(args []string) {
	if ch.cli.isWatching {
		fmt.Println(colorize(i18n.T("watch.error.already_running"), ColorYellow))
		return
	}

	// Parse flags (same manual pattern as /connect)
	cfg := k8s.WatchConfig{
		Namespace:   "default",
		Interval:    30 * time.Second,
		Window:      2 * time.Hour,
		MaxLogLines: 100,
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--deployment":
			if i+1 < len(args) {
				i++
				cfg.Deployment = args[i]
			}
		case "--kind":
			if i+1 < len(args) {
				i++
				cfg.Kind = args[i]
			}
		case "--namespace":
			if i+1 < len(args) {
				i++
				cfg.Namespace = args[i]
			}
		case "--interval":
			if i+1 < len(args) {
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					fmt.Println(colorize(i18n.T("watch.error.invalid_flag", "--interval", err.Error()), ColorYellow))
					return
				}
				cfg.Interval = d
			}
		case "--window":
			if i+1 < len(args) {
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					fmt.Println(colorize(i18n.T("watch.error.invalid_flag", "--window", err.Error()), ColorYellow))
					return
				}
				cfg.Window = d
			}
		case "--max-log-lines":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Println(colorize(i18n.T("watch.error.invalid_flag", "--max-log-lines", err.Error()), ColorYellow))
					return
				}
				cfg.MaxLogLines = n
			}
		case "--kubeconfig":
			if i+1 < len(args) {
				i++
				cfg.Kubeconfig = args[i]
			}
		default:
			fmt.Println(colorize(i18n.T("watch.error.unknown_flag", args[i]), ColorYellow))
			fmt.Println(colorize(i18n.T("watch.usage.start"), ColorYellow))
			return
		}
	}

	if cfg.Deployment == "" {
		fmt.Println(colorize(i18n.T("watch.error.deployment_required"), ColorYellow))
		fmt.Println(colorize(i18n.T("watch.usage.start"), ColorYellow))
		return
	}

	fmt.Println(colorize(i18n.T("watch.status.starting", cfg.Deployment, cfg.Namespace), ColorCyan))
	fmt.Println(colorize(i18n.T("watch.status.config", cfg.Interval, cfg.Window, cfg.MaxLogLines), ColorCyan))

	if err := ch.cli.StartWatcher(cfg); err != nil {
		fmt.Println(colorize(i18n.T("watch.error.start_failed", err.Error()), ColorYellow))
		return
	}

	fmt.Println(colorize(i18n.T("watch.status.started"), ColorGreen))
	fmt.Println(colorize(i18n.T("watch.hint.status_stop"), ColorGreen))
}

// handleWatchStopCommand stops the running K8s watcher.
func (ch *CommandHandler) handleWatchStopCommand() {
	if !ch.cli.isWatching {
		fmt.Println(colorize(i18n.T("watch.error.not_running"), ColorYellow))
		return
	}

	ch.cli.StopWatcher()
	fmt.Println(colorize(i18n.T("watch.status.stopped"), ColorGreen))
}

// handleWatchStatusCommand displays the current K8s watcher status.
func (ch *CommandHandler) handleWatchStatusCommand() {
	// Check local watcher first
	if ch.cli.isWatching {
		if ch.cli.watchStatusFunc != nil {
			status := ch.cli.watchStatusFunc()
			fmt.Println(colorize(i18n.T("watch.status.active_with_info", status), ColorCyan))
		} else {
			fmt.Println(colorize(i18n.T("watch.status.active_no_info"), ColorCyan))
		}
		return
	}

	// If connected to remote, query server's watcher status
	if ch.cli.isRemote {
		if rc, ok := ch.cli.Client.(*remote.Client); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ws, err := rc.GetWatcherStatus(ctx)
			if err != nil {
				fmt.Println(colorize(i18n.T("watch.error.remote_query_failed", err.Error()), ColorYellow))
				return
			}
			if !ws.Active {
				fmt.Println(colorize(i18n.T("watch.status.remote_inactive"), ColorYellow))
				return
			}
			fmt.Println(colorize(i18n.T("watch.status.remote_active", ws.StatusSummary), ColorCyan))
			fmt.Println(colorize(i18n.T("watch.status.remote_details",
				ws.Namespace, ws.Deployment, ws.PodCount, ws.AlertCount, ws.SnapshotCount), ColorCyan))
			return
		}
	}

	fmt.Println(colorize(i18n.T("watch.status.inactive"), ColorYellow))
}
