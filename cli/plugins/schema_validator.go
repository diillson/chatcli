/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// JSONSchemaAware is implemented by plugins that ship a JSON Schema
// (draft-2020-12) describing their input. The schema is the LLM-facing
// contract: when the model emits arguments the validator rejects, the
// orchestrator can fail fast with a clear "InvalidArgs" code instead
// of letting a type assertion panic deep inside the plugin.
//
// Plugins that do NOT implement this interface bypass validation and
// keep working — Item 5 is purely additive. External plugins,
// MCP-sourced tools, and legacy builtins that have not migrated all
// continue to dispatch via the existing path.
//
// The JSON Schema returned must be a valid draft-2020-12 document.
// Empty string is treated as "no schema" (same as not implementing
// the interface).
type JSONSchemaAware interface {
	JSONSchema() string
}

// validatorCache memoizes compiled schemas by plugin name. The
// santhosh-tekuri/jsonschema compiler is expensive at first use
// (regex-heavy resolver); caching makes the second call essentially
// free. Plugins re-register at startup (chatcli has no live plugin
// reload mid-process), so the cache never gets stale during a session.
var (
	validatorCacheMu sync.RWMutex
	validatorCache   = make(map[string]*jsonschema.Schema)
)

// ErrInvalidArgs is the sentinel returned when args fail schema
// validation. Callers (the agent loop) translate this into a
// ToolResult with IsError=true, ErrorCode=InvalidArgs so the LLM
// sees a clean failure surface and can retry with corrected input.
var ErrInvalidArgs = errors.New("invalid arguments")

// ValidateArgs runs the plugin's JSON Schema against the raw args
// payload. Returns:
//
//   - nil when the plugin does not implement JSONSchemaAware (legacy
//     plugins keep working unchanged).
//   - nil when the schema validates successfully.
//   - wrapped ErrInvalidArgs with a descriptive message naming the
//     offending JSON path when validation fails.
//   - a non-InvalidArgs error when the schema itself fails to compile
//     (programmer bug; should never reach production).
//
// rawJSON is the LLM-emitted argument blob. We do not attempt to
// JSON-validate the chatcli @coder envelope shape (`{"cmd":...,"args":{...}}`)
// here because plugins choose their own input shape via the schema.
func ValidateArgs(plugin Plugin, rawJSON string) error {
	if plugin == nil {
		return nil
	}
	sa, ok := plugin.(JSONSchemaAware)
	if !ok {
		return nil
	}
	schemaText := strings.TrimSpace(sa.JSONSchema())
	if schemaText == "" {
		return nil
	}
	rawJSON = strings.TrimSpace(rawJSON)
	if rawJSON == "" {
		// Empty args are valid only when the schema's required array
		// is empty. Feed an empty object so the schema validator can
		// make that call.
		rawJSON = "{}"
	}

	compiled, err := compileSchema(plugin.Name(), schemaText)
	if err != nil {
		return fmt.Errorf("%s: schema compile failed: %w", plugin.Name(), err)
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(rawJSON), &parsed); err != nil {
		return fmt.Errorf("%w: malformed JSON: %v", ErrInvalidArgs, err)
	}

	if err := compiled.Validate(parsed); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidArgs, formatValidationError(err))
	}
	return nil
}

// compileSchema loads or compiles the plugin's schema, caching the
// result. Cache miss is the only case that pays the compile cost.
func compileSchema(pluginName, schemaText string) (*jsonschema.Schema, error) {
	validatorCacheMu.RLock()
	cached, ok := validatorCache[pluginName]
	validatorCacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	compiler := jsonschema.NewCompiler()
	resourceID := "chatcli://plugin/" + pluginName + "/schema.json"
	if err := compiler.AddResource(resourceID, strings.NewReader(schemaText)); err != nil {
		return nil, fmt.Errorf("add resource: %w", err)
	}
	compiled, err := compiler.Compile(resourceID)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}

	validatorCacheMu.Lock()
	validatorCache[pluginName] = compiled
	validatorCacheMu.Unlock()
	return compiled, nil
}

// formatValidationError turns the library's structured error into a
// single concise line the model can act on. The full error tree is
// detailed but verbose; for a CLI tool, naming the first violating
// path is what the LLM needs to fix its next call.
func formatValidationError(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		// Walk down to the deepest cause that has a location.
		cause := ve
		for len(cause.Causes) > 0 {
			cause = cause.Causes[0]
		}
		path := strings.TrimSpace(cause.InstanceLocation)
		if path == "" {
			path = "(root)"
		}
		msg := strings.TrimSpace(cause.Message)
		if msg == "" {
			msg = "constraint failed"
		}
		return fmt.Sprintf("at %s: %s", path, msg)
	}
	return err.Error()
}

// ResetValidatorCache clears the compiled schema cache. Used by tests
// to ensure a plugin's schema is re-compiled across cases that mutate
// it via fixtures. Production code never calls this.
func ResetValidatorCache() {
	validatorCacheMu.Lock()
	defer validatorCacheMu.Unlock()
	validatorCache = make(map[string]*jsonschema.Schema)
}
