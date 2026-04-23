/*
 * Helpers shared across condition evaluators. These are intentionally
 * small and stateless so tests don't need to mock them.
 */
package condition

import (
	"fmt"
	"strings"
)

// asString reads a string field, returning "" when absent or wrong-typed.
func asString(spec map[string]any, key string) string {
	if spec == nil {
		return ""
	}
	if v, ok := spec[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// asInt reads an int field.
func asInt(spec map[string]any, key string) int {
	if spec == nil {
		return 0
	}
	if v, ok := spec[key]; ok {
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

// asStringMap reads a map[string]string field, tolerating map[string]any.
func asStringMap(spec map[string]any, key string) map[string]string {
	if spec == nil {
		return nil
	}
	v, ok := spec[key]
	if !ok {
		return nil
	}
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = fmt.Sprint(v)
		}
		return out
	}
	return nil
}

// truncate is an internal helper mirrored in scheduler.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// nonNilErr returns the first non-nil error.
func nonNilErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
