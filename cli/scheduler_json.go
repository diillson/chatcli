/*
 * Tiny encoding/json wrappers so the command handler can stay import-
 * light on the json package. Kept in a separate file so the rest of
 * scheduler_command.go reads cleanly.
 */
package cli

import "encoding/json"

func marshalJSONImpl(v any) ([]byte, error) { return json.Marshal(v) }

func jsonDecodeImpl(raw string, dst any) error {
	return json.Unmarshal([]byte(raw), dst)
}
