/*
 * Helpers shared across action executors.
 */
package action

import (
	"fmt"
	"strings"
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
