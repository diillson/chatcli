/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testOpenAPIJSON = `{
  "openapi": "3.0.1",
  "info": {"title": "Pet API", "version": "1.2.3", "description": "A demo pet API."},
  "servers": [{"url": "https://api.pets.example.com/v1"}],
  "security": [{"bearer": []}],
  "components": {
    "securitySchemes": {
      "apiKey": {"type": "apiKey", "in": "header", "name": "X-API-Key"},
      "bearer": {"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}
    },
    "schemas": {
      "Tag": {"type": "object", "properties": {"id": {"type": "integer"}, "name": {"type": "string"}}},
      "Pet": {"type": "object", "required": ["name"], "properties": {
        "id": {"type": "integer", "format": "int64"},
        "name": {"type": "string"},
        "status": {"type": "string", "enum": ["available", "pending", "sold"]},
        "tags": {"type": "array", "items": {"$ref": "#/components/schemas/Tag"}}
      }},
      "PetList": {"type": "object", "properties": {
        "total": {"type": "integer"},
        "items": {"type": "array", "items": {"$ref": "#/components/schemas/Pet"}}
      }}
    }
  },
  "paths": {
    "/pets": {
      "get": {
        "operationId": "listPets",
        "summary": "List all pets",
        "tags": ["pets"],
        "parameters": [
          {"name": "limit", "in": "query", "required": false, "schema": {"type": "integer", "format": "int32", "minimum": 1, "maximum": 100}}
        ],
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/PetList"}}}}}
      },
      "post": {
        "operationId": "createPet",
        "summary": "Create a pet",
        "tags": ["pets"],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pet"}}}},
        "responses": {"201": {"description": "created"}},
        "security": [{"bearer": []}]
      }
    },
    "/pets/{petId}": {
      "parameters": [{"name": "petId", "in": "path", "required": true, "schema": {"type": "string"}}],
      "get": {"operationId": "getPet", "summary": "Get a pet by id", "tags": ["pets"], "responses": {"200": {"description": "ok"}}}
    }
  }
}`

const testOpenAPIYAML = `openapi: 3.0.0
info:
  title: YAML API
  version: "0.1"
paths:
  /health:
    get:
      summary: Liveness probe
      responses:
        "200":
          description: healthy
`

func newSpecServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testOpenAPIJSON))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Server", "nginx/1.25.3")
			w.Header().Set("X-Powered-By", "Express")
			w.Header().Set("X-RateLimit-Limit", "100")
			w.Header().Set("X-RateLimit-Remaining", "99")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func run(t *testing.T, args string) string {
	t.Helper()
	out, err := NewBuiltinAPIExplorerPlugin().Execute(context.Background(), []string{args})
	if err != nil {
		t.Fatalf("execute %s: %v", args, err)
	}
	return out
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q\n---\n%s", w, out)
		}
	}
}

func TestAPIExplorerDiscover(t *testing.T) {
	srv := newSpecServer(t)
	out := run(t, `{"cmd":"discover","args":{"url":"`+srv.URL+`"}}`)
	mustContain(t, out,
		"Specification found", "OpenAPI 3.0.1", "/pets", "List all pets",
		"nginx/1.25.3", "Rate limiting", "### pets")
}

func TestAPIExplorerDiscoverJSON(t *testing.T) {
	srv := newSpecServer(t)
	out := run(t, `{"cmd":"discover","args":{"url":"`+srv.URL+`","format":"json"}}`)
	var r reconResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("discover json not valid: %v\n%s", err, out)
	}
	if r.SpecFormat != "OpenAPI 3.0.1" || r.EndpointCount != 3 {
		t.Errorf("unexpected inventory: format=%q endpoints=%d", r.SpecFormat, r.EndpointCount)
	}
}

func TestAPIExplorerSpecAndFilter(t *testing.T) {
	srv := newSpecServer(t)
	specURL := srv.URL + "/openapi.json"

	out := run(t, `{"cmd":"spec","args":{"url":"`+specURL+`"}}`)
	mustContain(t, out, "Pet API 1.2.3", "Security schemes", "API key in header `X-API-Key`",
		"HTTP bearer (JWT)", "Endpoints (3)", "Default auth (global)", "Models (3)")

	out = run(t, `{"cmd":"spec","args":{"url":"`+specURL+`","filter":"/pets/"}}`)
	if !strings.Contains(out, "Endpoints (1)") {
		t.Errorf("filter did not narrow to 1 endpoint\n---\n%s", out)
	}
}

func TestAPIExplorerSpecJSON(t *testing.T) {
	srv := newSpecServer(t)
	out := run(t, `{"cmd":"spec","args":{"url":"`+srv.URL+`/openapi.json","format":"json"}}`)
	var inv specInventoryJSON
	if err := json.Unmarshal([]byte(out), &inv); err != nil {
		t.Fatalf("spec json not valid: %v\n%s", err, out)
	}
	if inv.Total != 3 || len(inv.Endpoints) != 3 {
		t.Fatalf("expected 3 endpoints, got total=%d shown=%d", inv.Total, len(inv.Endpoints))
	}
	// Find POST /pets and check its normalized shape.
	var found bool
	for _, e := range inv.Endpoints {
		if e.Method == "POST" && e.Path == "/pets" {
			found = true
			if e.OperationID != "createPet" || len(e.RequestTypes) == 0 || e.Security == "" {
				t.Errorf("POST /pets normalized wrong: %+v", e)
			}
		}
	}
	if !found {
		t.Errorf("POST /pets missing from json inventory")
	}
}

func TestAPIExplorerEndpointRefResolution(t *testing.T) {
	srv := newSpecServer(t)
	specURL := srv.URL + "/openapi.json"

	// POST /pets — request body $ref Pet must expand to its fields + enum.
	out := run(t, `{"cmd":"endpoint","args":{"url":"`+specURL+`","path":"/pets","method":"post"}}`)
	mustContain(t, out, "POST /pets", "createPet", "Request body", "name: string *required*",
		"status: string", "enum: available, pending, sold", "tags: array<Tag>")

	// GET /pets — response $ref PetList must expand, recursing into Pet.
	out = run(t, `{"cmd":"endpoint","args":{"url":"`+specURL+`","path":"/pets","method":"get"}}`)
	mustContain(t, out, "items: array<Pet>", "total: integer", "limit", "range: 1..100")

	// Path-level parameter merges into the operation.
	out = run(t, `{"cmd":"endpoint","args":{"url":"`+specURL+`","path":"/pets/{petId}"}}`)
	if !strings.Contains(out, "`petId` (path, string) **required**") {
		t.Errorf("path-level parameter not merged\n---\n%s", out)
	}
}

// TestAPIExplorerDiscoverViaHTML exercises the Swagger-UI HTML → spec-URL path.
func TestAPIExplorerDiscoverViaHTML(t *testing.T) {
	mux := http.NewServeMux()
	// The machine spec lives at a NON-standard path only the HTML knows.
	mux.HandleFunc("/internal/api.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testOpenAPIJSON))
	})
	swaggerHTML := `<!DOCTYPE html><html><body><div id="swagger-ui"></div>
<script>const ui = SwaggerUIBundle({ url: "/internal/api.json", dom_id: '#swagger-ui' });</script>
</body></html>`
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/docs" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(swaggerHTML))
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := run(t, `{"cmd":"discover","args":{"url":"`+srv.URL+`"}}`)
	mustContain(t, out, "Specification found", "/internal/api.json", "embedded in", "/pets")
}

func TestAPIExplorerParseYAMLSpec(t *testing.T) {
	doc, format, ok := tryParseOAS([]byte(testOpenAPIYAML), "text/yaml")
	if !ok {
		t.Fatal("YAML spec not recognized")
	}
	if format != "OpenAPI 3.0.0" {
		t.Errorf("format = %q, want OpenAPI 3.0.0", format)
	}
	if _, ok := doc.Paths["/health"]; !ok {
		t.Errorf("YAML path /health not parsed; paths=%v", doc.Paths)
	}
}

func TestAPIExplorerRejectsHTMLAsSpec(t *testing.T) {
	if _, _, ok := tryParseOAS([]byte("<!DOCTYPE html><html></html>"), "text/html"); ok {
		t.Error("HTML wrongly accepted as a spec")
	}
}

func TestAPIExplorerSecurity(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			// Dangerous: reflect the probe Origin AND allow credentials.
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Set-Cookie", "session=abc; Path=/") // no Secure/HttpOnly → flagged
		w.Header().Set("Server", "Jetty(9.4)")
		w.WriteHeader(http.StatusOK)
	})
	// OIDC discovery.
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://issuer.example.com","authorization_endpoint":"https://issuer.example.com/auth","token_endpoint":"https://issuer.example.com/token"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := run(t, `{"cmd":"security","args":{"url":"`+srv.URL+`"}}`)
	mustContain(t, out,
		"Security posture", "X-Content-Type-Options: nosniff", "CSP missing",
		"reflects an arbitrary Origin with credentials",
		"no Secure", "no HttpOnly", "OpenID Connect", "issuer.example.com")
}

func TestAPIExplorerGraphQL(t *testing.T) {
	const introspection = `{"data":{"__schema":{
      "queryType":{"name":"Query"},
      "mutationType":{"name":"Mutation"},
      "subscriptionType":null,
      "types":[
        {"kind":"OBJECT","name":"Query","fields":[
          {"name":"user","args":[{"name":"id","type":{"kind":"NON_NULL","name":null,"ofType":{"kind":"SCALAR","name":"ID"}}}],"type":{"kind":"OBJECT","name":"User","ofType":null},"isDeprecated":false}
        ]},
        {"kind":"OBJECT","name":"Mutation","fields":[
          {"name":"createUser","args":[{"name":"input","type":{"kind":"INPUT_OBJECT","name":"UserInput","ofType":null}}],"type":{"kind":"OBJECT","name":"User","ofType":null},"isDeprecated":true,"deprecationReason":"use createUserV2"}
        ]},
        {"kind":"OBJECT","name":"User","fields":[]},
        {"kind":"INPUT_OBJECT","name":"UserInput","inputFields":[{"name":"name","type":{"kind":"SCALAR","name":"String"}}]},
        {"kind":"ENUM","name":"Role","enumValues":[{"name":"ADMIN"},{"name":"USER"}]},
        {"kind":"__Directive","name":"__Directive","fields":[]}
      ]}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(introspection))
	}))
	defer srv.Close()

	out := run(t, `{"cmd":"graphql","args":{"url":"`+srv.URL+`/graphql"}}`)
	mustContain(t, out,
		"Queries (Query)", "user(id: ID!): User",
		"Mutations (Mutation)", "createUser(input: UserInput): User", "deprecated: use createUserV2",
		"Input types", "UserInput", "name: String",
		"Enums", "Role`: ADMIN | USER")
	if strings.Contains(out, "__Directive") {
		t.Errorf("introspection internal type leaked\n---\n%s", out)
	}
}

func TestParseAPIExplorerArgs(t *testing.T) {
	in, err := parseAPIExplorerArgs([]string{"https://api.example.com"})
	if err != nil {
		t.Fatalf("bare url: %v", err)
	}
	if in.Cmd != "discover" || in.URL != "https://api.example.com" {
		t.Errorf("bare url parsed as %+v", in)
	}

	in, err = parseAPIExplorerArgs([]string{"security", "--url", "https://x", "--format", "json"})
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	if in.Cmd != "security" || in.Format != "json" {
		t.Errorf("argv parsed as %+v", in)
	}

	if _, err := parseAPIExplorerArgs([]string{`{"cmd":"spec","args":{}}`}); err == nil {
		t.Error("expected error for missing url")
	}
}
