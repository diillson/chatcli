/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /context refresh and /context watch: incremental re-indexing of a
 * context from its recorded source paths, on demand or driven by the
 * filesystem watcher. Watcher outcomes are queued as notices the REPL
 * prints at its next tick (never from the watcher goroutine).
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
)

// SetRefreshNotifier installs the sink for watcher-driven refresh notices.
func (h *ContextHandler) SetRefreshNotifier(fn func(string)) {
	st := h.watchState()
	st.mu.Lock()
	st.notify = fn
	st.mu.Unlock()
}

// watchState returns the handler's watch state, creating it on first use.
func (h *ContextHandler) watchState() *contextWatchState {
	h.watchOnce.Do(func() {
		if h.watch == nil {
			h.watch = &contextWatchState{}
		}
	})
	return h.watch
}

// Close stops the watcher (session end / tenant release).
func (h *ContextHandler) Close() {
	st := h.watchState()
	st.mu.Lock()
	w := st.watcher
	st.watcher = nil
	st.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

func (h *ContextHandler) contextWatcher() *ctxmgr.ContextWatcher {
	st := h.watchState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.watcher == nil {
		st.watcher = ctxmgr.NewContextWatcher(h.manager, h.logger, func(name string, rep ctxmgr.RefreshReport, err error) {
			st.mu.Lock()
			notify := st.notify
			st.mu.Unlock()
			if notify == nil {
				return
			}
			if err != nil {
				notify(i18n.T("context.watch.refresh_failed", name, err.Error()))
				return
			}
			notify(i18n.T("context.refresh.done", name, rep.Changed, rep.Added, rep.Removed))
		})
	}
	return st.watcher
}

// handleRefresh is /context refresh <name>.
func (h *ContextHandler) handleRefresh(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.refresh.usage"))
	}
	name := args[0]
	_, rep, err := h.manager.RefreshContext(ctx, name)
	if err != nil {
		if errors.Is(err, ctxmgr.ErrNoSourcePaths) {
			return fmt.Errorf("%s", i18n.T("context.refresh.no_sources", name))
		}
		return err
	}
	if !rep.Dirty() {
		fmt.Println(colorize("  "+i18n.T("context.refresh.unchanged", name, rep.Unchanged), ColorGray))
		return nil
	}
	fmt.Println(colorize("  "+i18n.T("context.refresh.done", name, rep.Changed, rep.Added, rep.Removed), ColorGreen))
	if rep.Resealed {
		// A refresh that moved zero files still did work, and the work
		// costs embeddings: say so instead of printing three zeros.
		fmt.Println(colorize("  "+i18n.T("context.refresh.resealed", name), ColorCyan))
	}
	return nil
}

// handleWatch is /context watch <name> [off] | /context watch list |
// /context unwatch <name>.
func (h *ContextHandler) handleWatch(sub string, args []string) error {
	if sub == "unwatch" {
		if len(args) < 1 {
			return fmt.Errorf("%s", i18n.T("context.watch.usage"))
		}
		return h.stopWatch(args[0])
	}
	if len(args) < 1 || strings.EqualFold(args[0], "list") {
		names := h.contextWatcher().Watching()
		if len(names) == 0 {
			fmt.Println(colorize("  "+i18n.T("context.watch.none"), ColorGray))
			return nil
		}
		fmt.Println(colorize("  "+i18n.T("context.watch.list", strings.Join(names, ", ")), ColorCyan))
		return nil
	}
	name := args[0]
	if len(args) > 1 && (strings.EqualFold(args[1], "off") || strings.EqualFold(args[1], "stop")) {
		return h.stopWatch(name)
	}
	if err := h.contextWatcher().Watch(name); err != nil {
		switch {
		case errors.Is(err, ctxmgr.ErrContextNotFound):
			return fmt.Errorf("%s", i18n.T("context.attach.error.not_found", name))
		case errors.Is(err, ctxmgr.ErrNoSourcePaths):
			return fmt.Errorf("%s", i18n.T("context.refresh.no_sources", name))
		}
		return err
	}
	paths, _ := h.manager.SourcePathsOf(name)
	fmt.Println(colorize("  "+i18n.T("context.watch.started", name, strings.Join(paths, ", ")), ColorGreen))
	return nil
}

func (h *ContextHandler) stopWatch(name string) error {
	if !h.contextWatcher().Unwatch(name) {
		return fmt.Errorf("%s", i18n.T("context.watch.not_watching", name))
	}
	fmt.Println(colorize("  "+i18n.T("context.watch.stopped", name), ColorGray))
	return nil
}
