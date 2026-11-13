/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package scheduler

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
)

// jobTagsKey scopes the tags of the firing job carried on a context.
type jobTagsKey struct{}

// WithJobTags returns ctx carrying the tags of the job whose action runs
// under it. The dispatcher installs it for every action, so what a job
// starts can recover what its creator recorded on it — a job is created
// under one context and fires, much later, under the dispatcher's.
func WithJobTags(ctx context.Context, tags map[string]string) context.Context {
	if len(tags) == 0 {
		return ctx
	}
	return context.WithValue(ctx, jobTagsKey{}, tags)
}

// JobTagsFrom returns the tags of the job firing under ctx, nil when ctx
// is not a job's. The map is the job's own copy: read it, do not write.
func JobTagsFrom(ctx context.Context) map[string]string {
	tags, _ := ctx.Value(jobTagsKey{}).(map[string]string)
	return tags
}

// tagsCopy snapshots the job's tags under its lock.
func (j *Job) tagsCopy() map[string]string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.Tags) == 0 {
		return nil
	}
	out := make(map[string]string, len(j.Tags))
	for k, v := range j.Tags {
		out[k] = v
	}
	return out
}

// storedJobID is what a job id may look like before it is used as a file
// name: ids come from snapshots on disk here, not only from this process.
var storedJobID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// StoredJob reads a job from the store on disk, whether or not this
// process ever loaded it. The data directory is shared by every chatcli of
// the user, and each keeps in memory only what it enqueued or replayed at
// start: a job another process created is unknown to Query yet durable
// here, and fires when a scheduler next replays the store. Read-only: it
// never quarantines or rewrites a record. ErrJobNotFound means the store
// holds no such job; any other error means this process cannot tell.
func (s *Scheduler) StoredJob(id JobID) (*Job, error) {
	if s == nil || s.wal == nil {
		return nil, errors.New("scheduler: no store to read")
	}
	if !storedJobID.MatchString(string(id)) {
		return nil, ErrJobNotFound
	}
	j, err := s.wal.Read(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// ReservedTagPrefix is the namespace of the job tags chatcli sets itself.
// They carry trust — whose state a job acts on when it fires — so no input
// may set one: not a tool call, not a --tag, in any spelling. They reach a
// job only through WithTrustedJobTags.
const ReservedTagPrefix = "chatcli."

// reservedTag reports whether key is in the reserved namespace, however
// it is cased or padded.
func reservedTag(key string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), ReservedTagPrefix)
}

// trustedTagsKey scopes the reserved tags of the job being created.
type trustedTagsKey struct{}

// WithTrustedJobTags returns ctx carrying reserved tags for the job created
// under it. Only tags in the reserved namespace are kept. The context is
// the one channel for them: a caller in this process sets it from what it
// knows about the request, never from the request's own input.
func WithTrustedJobTags(ctx context.Context, tags map[string]string) context.Context {
	kept := map[string]string{}
	for k, v := range tags {
		if reservedTag(k) && v != "" {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		return ctx
	}
	return context.WithValue(ctx, trustedTagsKey{}, kept)
}

// applyTrustedTags puts the reserved tags ctx carries on job.
func applyTrustedTags(ctx context.Context, job *Job) {
	tags, _ := ctx.Value(trustedTagsKey{}).(map[string]string)
	for k, v := range tags {
		if job.Tags == nil {
			job.Tags = map[string]string{}
		}
		job.Tags[k] = v
	}
}
