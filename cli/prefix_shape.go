/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Cache miss attribution: which part of the request changed.
 *
 * The telemetry can say THAT the prefix was lost — the request read back
 * less than the previous one left cached — but not why. Every provider's
 * prompt cache is prefix-keyed over the same three regions in the same
 * order: the tool definitions, the system blocks, then the messages. So
 * before each main-lane request ChatCLI fingerprints those regions in
 * request order and diffs the list against the previous request's. The
 * first position that differs is the attribution: the tool set, a system
 * block, or a message named by index and kind (a tool result rewritten, a
 * skills block, a turn context, the user's own text). A request whose
 * shape only grew at the end changed nothing the cache held; a miss on
 * such a request is a server-side or lifetime event, and is reported as
 * exactly that rather than blamed on the prompt.
 */
package cli

import (
	"encoding/json"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// Attribution kinds, keyed for the counters and the i18n labels.
const (
	prefixCauseNone        = ""
	prefixCauseTools       = "tools"
	prefixCauseSystem      = "system"
	prefixCauseMessage     = "message"
	prefixCauseShrunk      = "shrunk"
	prefixCauseTTL         = "ttl_promotion"
	prefixCauseUnobserved  = "unobserved"
	prefixCauseMaxEvents   = 8
	prefixCauseMaxShapeLen = 4096
)

// prefixChange names the first region of the request that differs from
// the previous request's, i.e. the point from which the cache was lost.
type prefixChange struct {
	Kind   string // one of the prefixCause* keys
	Index  int    // system block or message index, 1-based, when Kind names one
	Detail string // message kind (tool_result, skills, ...) or a count
}

// none reports whether nothing ahead of the tail changed.
func (c prefixChange) none() bool { return c.Kind == prefixCauseNone }

// key is the counter key for the attribution table: kind plus detail,
// never the position, so the same cause at different indexes adds up.
func (c prefixChange) key() string {
	if c.Detail == "" {
		return c.Kind
	}
	return c.Kind + ":" + c.Detail
}

// prefixShape is the request's fingerprint list in cache order.
type prefixShape []string

// messageKind names what a history message carries, for the attribution.
func messageKind(m models.Message) string {
	switch {
	case m.IsTurnContext():
		return "turn_context"
	case m.IsRunContext():
		return "run_context"
	case m.Meta != nil && m.Meta.SkillNames != "":
		return "skills"
	case m.Meta != nil && m.Meta.IsSummary:
		return "summary"
	}
	role := strings.ToLower(strings.TrimSpace(m.Role))
	if role == "tool" {
		return "tool_result"
	}
	if role == "" {
		return "user"
	}
	return role
}

func hash64(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// shapeOf fingerprints a request: one entry for the tool set, one per
// system block (a structured system message contributes one per part),
// one per message. Entries carry the region so a diff can name it.
func shapeOf(history []models.Message, tools []models.ToolDefinition) prefixShape {
	shape := make(prefixShape, 0, len(history)+2)
	if len(tools) > 0 {
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			params, _ := json.Marshal(t.Function.Parameters)
			names = append(names, t.Function.Name+"\x01"+t.Function.Description+"\x01"+string(params))
		}
		sort.Strings(names)
		shape = append(shape, "tools:"+hash64(names...))
	}
	for _, m := range history {
		if strings.EqualFold(m.Role, "system") {
			if len(m.SystemParts) > 0 {
				for _, p := range m.SystemParts {
					shape = append(shape, "system:"+hash64(p.Type, p.Text))
				}
				continue
			}
			shape = append(shape, "system:"+hash64(m.Content))
			continue
		}
		parts := []string{m.Role, m.Content, m.ToolCallID, strconv.Itoa(len(m.Images))}
		for _, tc := range m.ToolCalls {
			args, _ := json.Marshal(tc.Arguments)
			parts = append(parts, tc.ID, tc.Name, string(args))
		}
		shape = append(shape, "msg:"+messageKind(m)+":"+hash64(parts...))
	}
	if len(shape) > prefixCauseMaxShapeLen {
		shape = shape[len(shape)-prefixCauseMaxShapeLen:]
	}
	return shape
}

// regionIndex returns the 1-based index of entry i within its region
// (system blocks and messages are counted separately).
func (s prefixShape) regionIndex(i int) int {
	region := strings.SplitN(s[i], ":", 2)[0]
	n := 0
	for j := 0; j <= i; j++ {
		if strings.HasPrefix(s[j], region+":") {
			n++
		}
	}
	return n
}

// diffPrefixShape finds the first entry of next that differs from prev.
// A next that extends prev at the end changed nothing the cache held.
func diffPrefixShape(prev, next prefixShape) prefixChange {
	if len(prev) == 0 {
		return prefixChange{}
	}
	for i := 0; i < len(prev) && i < len(next); i++ {
		if prev[i] == next[i] {
			continue
		}
		return changeAt(next, i)
	}
	if len(next) < len(prev) {
		return prefixChange{Kind: prefixCauseShrunk, Detail: strconv.Itoa(len(prev) - len(next))}
	}
	return prefixChange{}
}

// changeAt names the region that entry i of shape belongs to.
func changeAt(shape prefixShape, i int) prefixChange {
	entry := shape[i]
	switch {
	case strings.HasPrefix(entry, "tools:"):
		return prefixChange{Kind: prefixCauseTools}
	case strings.HasPrefix(entry, "system:"):
		return prefixChange{Kind: prefixCauseSystem, Index: shape.regionIndex(i)}
	}
	kind := "user"
	if f := strings.SplitN(entry, ":", 3); len(f) == 3 {
		kind = f[1]
	}
	return prefixChange{Kind: prefixCauseMessage, Index: shape.regionIndex(i), Detail: kind}
}

// notePrefixShape fingerprints the request about to be sent on the main
// lane, compares it with the previous one, and hands the telemetry the
// cause a miss on this request would have. Called right before the
// request goes out; nil-safe.
func (cli *ChatCLI) notePrefixShape(history []models.Message, tools []models.ToolDefinition) {
	if cli == nil {
		return
	}
	next := shapeOf(history, tools)
	cli.prefixShapeMu.Lock()
	prev := cli.lastPrefixShape
	cli.lastPrefixShape = next
	cli.prefixShapeMu.Unlock()
	if cli.costTracker != nil {
		cli.costTracker.NotePrefixChange(diffPrefixShape(prev, next))
	}
}

// resetPrefixShape forgets the previous request: the conversation
// restarted, so the next request has nothing to be compared with.
func (cli *ChatCLI) resetPrefixShape() {
	if cli == nil {
		return
	}
	cli.prefixShapeMu.Lock()
	cli.lastPrefixShape = nil
	cli.prefixShapeMu.Unlock()
}

// prefixCauseLabel renders an attribution for /cost.
func prefixCauseLabel(c prefixChange) string {
	switch c.Kind {
	case prefixCauseTools:
		return i18n.T("prefix.cause.tools")
	case prefixCauseSystem:
		return i18n.T("prefix.cause.system", c.Index)
	case prefixCauseMessage:
		return i18n.T("prefix.cause.message", c.Index, prefixKindLabel(c.Detail))
	case prefixCauseShrunk:
		return i18n.T("prefix.cause.shrunk", c.Detail)
	case prefixCauseTTL:
		return i18n.T("prefix.cause.ttl_promotion")
	case prefixCauseUnobserved:
		if c.Detail == "expired" {
			return i18n.T("prefix.cause.unobserved_expired")
		}
		return i18n.T("prefix.cause.unobserved")
	}
	return c.key()
}

// prefixKindLabel renders a message kind; unknown kinds print as they are.
func prefixKindLabel(kind string) string {
	switch kind {
	case "tool_result", "turn_context", "run_context", "skills", "summary", "user", "assistant", "system":
		return i18n.T("prefix.kind." + kind)
	}
	return kind
}

// prefixOutcomeLabel renders how the prefix was lost.
func prefixOutcomeLabel(outcome string) string {
	switch outcome {
	case "miss", "expired", "rebuild":
		return i18n.T("prefix.outcome." + outcome)
	}
	return outcome
}

// parsePrefixCauseKey is the inverse of prefixChange.key for the
// counters table, which only keeps keys.
func parsePrefixCauseKey(key string) prefixChange {
	kind, detail, _ := strings.Cut(key, ":")
	c := prefixChange{Kind: kind, Detail: detail}
	if kind == prefixCauseSystem || kind == prefixCauseMessage {
		// Counters aggregate across indexes: the key carries the detail
		// only, so the label is rendered without a position.
		c.Index = 0
	}
	return c
}

// lostPrefixLines renders the attribution table and the recent events
// for /cost: nothing when no prefix was ever lost.
func lostPrefixLines(report LostPrefixReport) []string {
	if len(report.Causes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(report.Causes))
	for k := range report.Causes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if report.Causes[keys[i]] != report.Causes[keys[j]] {
			return report.Causes[keys[i]] > report.Causes[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, i18n.T("prefix.cause.count", prefixCauseLabel(parsePrefixCauseKey(k)), report.Causes[k]))
	}
	out := []string{i18n.T("cost.cmd.cache_causes", strings.Join(parts, " · "))}
	events := report.Events
	if len(events) > 5 {
		events = events[len(events)-5:]
	}
	for _, e := range events {
		out = append(out, i18n.T("cost.cmd.cache_event", e.Request, prefixOutcomeLabel(e.Outcome), prefixCauseLabel(e.Cause)))
	}
	return out
}
