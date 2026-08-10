/*
 * ChatCLI - Change notification for memory sub-stores
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * changeNotifier is the one-line "mark derived caches stale" seam each
 * sub-store embeds. The callback is stored atomically so notifyChanged is
 * safe to invoke while holding any store lock, and the downstream consumer
 * (the graph cache) is a bare atomic-bool store — cheap enough to fire on
 * every content mutation.
 *
 * Deliberate NON-callers (rebuild-storm guards, keep in sync with the tap
 * list in graph_cache.go): FactIndex.MarkAccessed, bare persistLocked calls,
 * ProjectTracker.Touch, UserProfileStore.RecordCommand.
 */
package memory

import "sync/atomic"

// changeNotifier is embedded by sub-stores whose content feeds derived
// caches. Zero value is inert.
type changeNotifier struct {
	fn atomic.Pointer[func()]
}

// SetOnChanged registers the notifier callback (nil detaches). The callback
// must be cheap and must not call back into the owning store.
func (c *changeNotifier) SetOnChanged(fn func()) {
	if fn == nil {
		c.fn.Store(nil)
		return
	}
	c.fn.Store(&fn)
}

// notifyChanged fires the callback if attached. Safe under any lock.
func (c *changeNotifier) notifyChanged() {
	if fn := c.fn.Load(); fn != nil {
		(*fn)()
	}
}
