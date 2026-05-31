/*
 * JSON schema describing the @ask args envelope. Two consumers.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *   - the plugin's Schema() (text-mode / XML providers), injected into the
 *     agent system prompt so the model knows the arg shape;
 *   - the native ToolDefinition Parameters block (cli/agent/workers), which
 *     references the same structure.
 */
package ask

import "encoding/json"

// ParametersJSON returns the JSON-Schema "parameters" object as raw JSON.
// Exposed as a concrete type (no interface{} on the public signature) so
// callers in other packages can decode it into their own structures.
func ParametersJSON() json.RawMessage {
	b, _ := json.Marshal(paramsMap())
	return b
}

// paramsMap builds the JSON-Schema "parameters" object for the native ask_user
// tool definition. Unexported: the map[string]interface{} shape stays internal.
func paramsMap() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"questions": map[string]interface{}{
				"type":        "array",
				"minItems":    1,
				"maxItems":    MaxQuestions,
				"description": "1 to 6 questions to ask the user.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"header": map[string]interface{}{
							"type":        "string",
							"description": "Short label for the question (1-3 words), e.g. 'Database'.",
						},
						"question": map[string]interface{}{
							"type":        "string",
							"description": "The full question text shown to the user.",
						},
						"multiSelect": map[string]interface{}{
							"type":        "boolean",
							"description": "Allow selecting multiple options (default false).",
						},
						"options": map[string]interface{}{
							"type":        "array",
							"minItems":    1,
							"maxItems":    MaxOptions,
							"description": "The choices. The user can also type a free-text 'Other' answer.",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"label":       map[string]interface{}{"type": "string", "description": "The choice label."},
									"description": map[string]interface{}{"type": "string", "description": "Optional one-line explanation of the choice."},
								},
								"required": []string{"label"},
							},
						},
					},
					"required": []string{"header", "question", "options"},
				},
			},
		},
		"required": []string{"questions"},
	}
}

// SchemaJSON returns a compact JSON description for the plugin Schema() used by
// the text-mode prompt builder.
func SchemaJSON() string {
	schema := map[string]interface{}{
		"argsFormat": `JSON envelope {"questions":[{header, question, multiSelect?, options:[{label, description?}]}]}`,
		"parameters": paramsMap(),
		"examples": []string{
			`{"questions":[{"header":"Database","question":"Which database should I use?","options":[{"label":"Postgres","description":"Relational, ACID"},{"label":"SQLite","description":"Embedded, zero-config"}]}]}`,
			`{"questions":[{"header":"Targets","question":"Which environments to deploy?","multiSelect":true,"options":[{"label":"staging"},{"label":"prod"}]}]}`,
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}
