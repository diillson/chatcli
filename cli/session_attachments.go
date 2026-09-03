/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /context attach state persisted with the session. Attachments lived only
 * in the process: a restart, a /session load on another surface or the
 * gateway daemon picking up a bound session all lost every attached
 * context. Saved sessions now carry the attachment records and loading one
 * re-attaches them under the session's own key.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// contextSessionKey is the key /context attach records are bound under —
// the same derivation the chat and agent prompt builders use.
func (cli *ChatCLI) contextSessionKey() string {
	if cli.currentSessionName == "" {
		return "default"
	}
	return cli.currentSessionName
}

// sessionAttachments snapshots the live attachment records for saving.
func (cli *ChatCLI) sessionAttachments() []models.SessionAttachment {
	if cli.contextHandler == nil || cli.contextHandler.manager == nil {
		return nil
	}
	records := cli.contextHandler.manager.AttachedRecords(cli.contextSessionKey())
	if len(records) == 0 {
		return nil
	}
	out := make([]models.SessionAttachment, 0, len(records))
	for _, r := range records {
		out = append(out, models.SessionAttachment{
			ContextID:      r.ContextID,
			Priority:       r.Priority,
			SelectedChunks: r.SelectedChunks,
			RetrievalTopK:  r.RetrievalTopK,
		})
	}
	return out
}

// applySessionAttachments re-attaches a loaded session's contexts under
// key. Contexts already attached are left alone; contexts that no longer
// exist are skipped with a log line — a stale record must never block the
// load.
func (cli *ChatCLI) applySessionAttachments(sd *models.SessionData, key string) {
	if sd == nil || len(sd.Attachments) == 0 || cli.contextHandler == nil || cli.contextHandler.manager == nil {
		return
	}
	mgr := cli.contextHandler.manager
	for _, a := range sd.Attachments {
		if strings.TrimSpace(a.ContextID) == "" {
			continue
		}
		err := mgr.AttachContextWithOptions(key, a.ContextID, ctxmgr.AttachOptions{
			Priority:       a.Priority,
			SelectedChunks: a.SelectedChunks,
			RetrievalTopK:  a.RetrievalTopK,
		})
		if err != nil && cli.logger != nil {
			cli.logger.Debug("session attachment not restored",
				zap.String("context", a.ContextID), zap.String("session", key), zap.Error(err))
		}
	}
}
