/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package scheduler

import "context"

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
