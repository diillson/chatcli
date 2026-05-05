/*
 * Helpers shared across action executors.
 */
package action

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

func asString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func asInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}

func asBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func asStringMap(m map[string]any, key string) map[string]string {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch mm := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(mm))
		for k, v := range mm {
			out[k] = v
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(mm))
		for k, v := range mm {
			out[k] = fmt.Sprint(v)
		}
		return out
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// payloadInt64 reads an int-shaped payload field with cross-encoding
// tolerance (JSON numbers come back as float64; Go literals are int).
func payloadInt64(action scheduler.Action, key string) int64 {
	if action.Payload == nil {
		return 0
	}
	v, ok := action.Payload[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// payloadDuration parses a duration string field, falling back to def
// when missing or unparseable.
func payloadDuration(action scheduler.Action, key string, def time.Duration) time.Duration {
	if action.Payload == nil {
		return def
	}
	if s, ok := action.Payload[key].(string); ok {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	return def
}

// payloadStringMap reads an object-shaped payload field as
// map[string]string with the same coercion rules as asStringMap.
func payloadStringMap(action scheduler.Action, key string) map[string]string {
	return asStringMap(action.Payload, key)
}
