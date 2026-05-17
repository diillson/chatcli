/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaPlugin wraps minimalPlugin with a configurable JSONSchema().
// Used to exercise the validator without depending on the production
// plugins' schemas (which can churn independently).
type schemaPlugin struct {
	minimalPlugin
	name   string
	schema string
}

func (p schemaPlugin) Name() string       { return p.name }
func (p schemaPlugin) JSONSchema() string { return p.schema }

// TestValidateArgs_AcceptsValidObject is the happy path.
func TestValidateArgs_AcceptsValidObject(t *testing.T) {
	ResetValidatorCache()
	p := schemaPlugin{
		name: "@test1",
		schema: `{
			"type": "object",
			"properties": {"name": {"type": "string"}},
			"required": ["name"]
		}`,
	}
	require.NoError(t, ValidateArgs(p, `{"name":"chatcli"}`))
}

// TestValidateArgs_RejectsMissingRequired pins the field-level
// failure path. The error should name the offending location.
func TestValidateArgs_RejectsMissingRequired(t *testing.T) {
	ResetValidatorCache()
	p := schemaPlugin{
		name: "@test2",
		schema: `{
			"type": "object",
			"properties": {"name": {"type": "string"}},
			"required": ["name"]
		}`,
	}
	err := ValidateArgs(p, `{}`)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidArgs),
		"validation errors must wrap ErrInvalidArgs for downstream classification")
}

// TestValidateArgs_RejectsWrongType ensures type mismatches surface.
func TestValidateArgs_RejectsWrongType(t *testing.T) {
	ResetValidatorCache()
	p := schemaPlugin{
		name: "@test3",
		schema: `{
			"type": "object",
			"properties": {"count": {"type": "integer"}}
		}`,
	}
	err := ValidateArgs(p, `{"count":"five"}`)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidArgs))
}

// TestValidateArgs_LegacyPluginIsNoOp pins the additive contract: a
// plugin that doesn't implement JSONSchemaAware bypasses validation
// entirely.
func TestValidateArgs_LegacyPluginIsNoOp(t *testing.T) {
	ResetValidatorCache()
	assert.NoError(t, ValidateArgs(minimalPlugin{}, `{"any":"thing"}`))
	assert.NoError(t, ValidateArgs(minimalPlugin{}, ``))
	assert.NoError(t, ValidateArgs(minimalPlugin{}, `not even json`))
}

// TestValidateArgs_EmptyArgsTreatedAsEmptyObject lets a plugin with
// no required fields accept a no-args call.
func TestValidateArgs_EmptyArgsTreatedAsEmptyObject(t *testing.T) {
	ResetValidatorCache()
	p := schemaPlugin{
		name: "@test4",
		schema: `{
			"type": "object",
			"properties": {"opt": {"type": "string"}}
		}`,
	}
	require.NoError(t, ValidateArgs(p, ``))
	require.NoError(t, ValidateArgs(p, `{}`))
}

// TestValidateArgs_MalformedJSONIsInvalidArgs covers the parse path:
// LLM emits invalid JSON, validator returns InvalidArgs without
// invoking the schema engine.
func TestValidateArgs_MalformedJSONIsInvalidArgs(t *testing.T) {
	ResetValidatorCache()
	p := schemaPlugin{
		name: "@test5",
		schema: `{
			"type": "object",
			"properties": {"x": {"type": "string"}}
		}`,
	}
	err := ValidateArgs(p, `{broken`)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidArgs))
	assert.Contains(t, err.Error(), "malformed JSON")
}

// TestValidateArgs_NilPluginIsNoOp documents the defensive guard.
func TestValidateArgs_NilPluginIsNoOp(t *testing.T) {
	ResetValidatorCache()
	assert.NoError(t, ValidateArgs(nil, `{"x":1}`))
}

// TestValidateArgs_EmptySchemaIsNoOp pins the "schema text is empty"
// path so a plugin can dynamically disable validation by returning
// empty without removing the JSONSchema method.
func TestValidateArgs_EmptySchemaIsNoOp(t *testing.T) {
	ResetValidatorCache()
	p := schemaPlugin{name: "@test6", schema: ""}
	assert.NoError(t, ValidateArgs(p, `{"x":1}`))
}

// TestValidateArgs_CachedCompileOnSecondCall ensures the compiled
// schema is reused. Hard to assert directly (cache is private), so
// we exercise the happy path twice with the same plugin and assert
// both calls succeed without error.
func TestValidateArgs_CachedCompileOnSecondCall(t *testing.T) {
	ResetValidatorCache()
	p := schemaPlugin{
		name: "@cached",
		schema: `{
			"type": "object",
			"properties": {"name": {"type": "string"}},
			"required": ["name"]
		}`,
	}
	require.NoError(t, ValidateArgs(p, `{"name":"a"}`))
	require.NoError(t, ValidateArgs(p, `{"name":"b"}`))
}

// TestValidateArgs_RealReadPluginAcceptsCanonicalShape exercises the
// production schema of BuiltinReadPlugin to catch a class of "I
// shipped a schema that rejects my own valid input" bugs.
func TestValidateArgs_RealReadPluginAcceptsCanonicalShape(t *testing.T) {
	ResetValidatorCache()
	p := NewBuiltinReadPlugin()
	require.NoError(t, ValidateArgs(p, `{"file":"main.go"}`))
	require.NoError(t, ValidateArgs(p, `{"file":"main.go","from_line":10,"to_line":50}`))
	require.NoError(t, ValidateArgs(p, `{"path":"alt.go"}`))
}

// TestValidateArgs_RealReadPluginRejectsMissingFile pins the
// required-anyOf path: at least one of file/path/filepath must be
// present.
func TestValidateArgs_RealReadPluginRejectsMissingFile(t *testing.T) {
	ResetValidatorCache()
	p := NewBuiltinReadPlugin()
	err := ValidateArgs(p, `{}`)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidArgs))
}

// TestValidateArgs_RealSearchPluginAcceptsCanonicalShape mirrors the
// above for @search.
func TestValidateArgs_RealSearchPluginAcceptsCanonicalShape(t *testing.T) {
	ResetValidatorCache()
	p := NewBuiltinSearchPlugin()
	require.NoError(t, ValidateArgs(p, `{"term":"Login"}`))
	require.NoError(t, ValidateArgs(p, `{"term":"Login","dir":"./src","max_results":50}`))
}

// TestValidateArgs_RealTreePluginAcceptsEmptyAndPopulated since tree
// is the only atomic tool with no required fields.
func TestValidateArgs_RealTreePluginAcceptsEmptyAndPopulated(t *testing.T) {
	ResetValidatorCache()
	p := NewBuiltinTreePlugin()
	require.NoError(t, ValidateArgs(p, `{}`))
	require.NoError(t, ValidateArgs(p, `{"dir":"src","depth":3}`))
}

// TestValidateArgs_RealTreePluginRejectsOutOfRangeDepth pins the
// schema's numeric bounds.
func TestValidateArgs_RealTreePluginRejectsOutOfRangeDepth(t *testing.T) {
	ResetValidatorCache()
	p := NewBuiltinTreePlugin()
	err := ValidateArgs(p, `{"depth":999}`)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidArgs))
}

// TestValidateArgs_RealTodoPluginShapes covers @todo's three
// subcommand shapes via the oneOf branch.
func TestValidateArgs_RealTodoPluginShapes(t *testing.T) {
	ResetValidatorCache()
	p := NewBuiltinTodoPlugin()

	// list (no args).
	require.NoError(t, ValidateArgs(p, `{"cmd":"list"}`))

	// write with one todo.
	require.NoError(t, ValidateArgs(p,
		`{"cmd":"write","args":{"todos":[{"description":"x","status":"pending"}]}}`))

	// mark by id.
	require.NoError(t, ValidateArgs(p,
		`{"cmd":"mark","args":{"id":1,"status":"completed"}}`))
}

// TestValidateArgs_RealTodoPluginRejectsBadShapes pins the failure
// matrix: missing args, missing id, invalid status, empty todos.
func TestValidateArgs_RealTodoPluginRejectsBadShapes(t *testing.T) {
	ResetValidatorCache()
	p := NewBuiltinTodoPlugin()

	cases := []string{
		`{"cmd":"write"}`,                                       // missing args
		`{"cmd":"write","args":{"todos":[]}}`,                   // empty array
		`{"cmd":"write","args":{"todos":[{"description":""}]}}`, // empty description
		`{"cmd":"write","args":{"todos":[{"description":"x","status":"bogus"}]}}`,
		`{"cmd":"mark","args":{}}`,                            // missing id/status
		`{"cmd":"mark","args":{"id":1}}`,                      // missing status
		`{"cmd":"mark","args":{"id":0,"status":"completed"}}`, // id must be >=1
		`{"cmd":"frobnicate"}`,                                // unknown subcommand (no oneOf branch)
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			err := ValidateArgs(p, c)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidArgs), "got %v", err)
		})
	}
}
