/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// RunStorage executes the `storage` subcommand — the /storage command
// without a REPL, for cron jobs and scripts:
//
//	chatcli storage [store]                        inventory
//	chatcli storage prune [store] [--apply] [--json]  dry run, or removal with --apply
//
// No LLM provider is needed, so the subcommand never boots one.
func RunStorage(ctx context.Context, args []string, logger *zap.Logger) error {
	return runStorageWith(ctx, args, "", logger, os.Stdout)
}

// runStorageWith is the boot-free body of RunStorage; root overrides the
// state root (tests), "" meaning ~/.chatcli.
func runStorageWith(ctx context.Context, args []string, root string, logger *zap.Logger, w io.Writer) error {
	opts := cli.StorageOptions{Root: root, TTL: cli.SessionTTL(), Logger: logger}
	asJSON, prune := false, false
	for _, a := range args {
		switch strings.ToLower(a) {
		case "prune":
			prune = true
			opts.Burst = true
		case "--apply", "apply", "--yes", "-y":
			opts.Apply = true
		case "--dry-run", "dry-run", "-n":
			opts.Apply = false
		case "--json", "json":
			asJSON = true
		case "help", "-h", "--help":
			fmt.Fprintln(w, i18n.T("storage.cmd.usage"))
			return nil
		default:
			if opts.Only != "" || !cli.IsStorageStore(a) {
				return fmt.Errorf("%s", i18n.T("storage.unknown_store", a, strings.Join(cli.StorageStoreNames(), ", ")))
			}
			opts.Only = a
		}
	}
	if opts.Apply && !prune {
		// --apply without prune is a typo away from a deletion; refuse.
		return fmt.Errorf("%s", i18n.T("storage.cmd.usage"))
	}
	res, err := cli.RunStorage(ctx, opts)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	cli.RenderStorage(w, res, prune)
	return nil
}
