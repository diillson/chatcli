/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * GraphQL schema introspection for @api-explorer.
 *
 * A GraphQL endpoint answers a single POST with its entire type system, so
 * when the OpenAPI sweep comes up empty this is the equivalent map for a
 * GraphQL API. Introspection is a pure query — it reads the schema and mutates
 * nothing — which is why the tool stays read-only despite issuing a POST.
 */
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// graphqlIntrospectionQuery asks for the schema shape: root operation type
// names, and every type's name/kind/fields. Kept lean (no deep input-type
// recursion) to bound the response on large schemas.
const graphqlIntrospectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      kind
      name
      description
      fields(includeDeprecated: true) {
        name
        description
        isDeprecated
        deprecationReason
        args { name type { kind name ofType { kind name ofType { kind name } } } }
        type { kind name ofType { kind name ofType { kind name ofType { kind name } } } }
      }
      inputFields { name type { kind name ofType { kind name ofType { kind name } } } }
      enumValues(includeDeprecated: true) { name isDeprecated }
    }
  }
}`

type graphqlType struct {
	Kind        string           `json:"kind"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Fields      []graphqlField   `json:"fields"`
	InputFields []graphqlArg     `json:"inputFields"`
	EnumValues  []graphqlEnumVal `json:"enumValues"`
}

type graphqlField struct {
	Name              string         `json:"name"`
	Args              []graphqlArg   `json:"args"`
	Type              graphqlTypeRef `json:"type"`
	IsDeprecated      bool           `json:"isDeprecated"`
	DeprecationReason string         `json:"deprecationReason"`
}

type graphqlArg struct {
	Name string         `json:"name"`
	Type graphqlTypeRef `json:"type"`
}

type graphqlEnumVal struct {
	Name         string `json:"name"`
	IsDeprecated bool   `json:"isDeprecated"`
}

type graphqlTypeRef struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	OfType *graphqlTypeRef `json:"ofType"`
}

// render resolves a (possibly wrapped NON_NULL/LIST) type reference to a name.
func (r graphqlTypeRef) render() string {
	switch r.Kind {
	case "NON_NULL":
		if r.OfType != nil {
			return r.OfType.render() + "!"
		}
	case "LIST":
		if r.OfType != nil {
			return "[" + r.OfType.render() + "]"
		}
	}
	if r.Name != "" {
		return r.Name
	}
	return r.Kind
}

func apiExplorerGraphQL(ctx context.Context, endpoint string) (string, error) {
	safe, err := validateWebTarget(endpoint)
	if err != nil {
		return "", fmt.Errorf("refusing %q: %w", endpoint, err)
	}

	payload, _ := json.Marshal(map[string]string{"query": graphqlIntrospectionQuery})
	reqCtx, cancel := context.WithTimeout(ctx, apiExplorerRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, safe, bytes.NewReader(payload)) //#nosec G704 -- URL validated by validateWebTarget + ssrfDialControl (metadata/link-local refused, redirects re-validated)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fallbackUserAgent)

	resp, err := webHTTPClient().Do(req) //#nosec G704 -- see annotation above
	if err != nil {
		return "", fmt.Errorf("introspection request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, apiExplorerMaxSpecBody))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("endpoint returned HTTP %d (introspection may be disabled, or this is not a GraphQL endpoint)", resp.StatusCode)
	}

	sch, err := parseGraphQLIntrospection(body)
	if err != nil {
		return "", err
	}
	return renderGraphQLSchema(safe, sch), nil
}

// graphqlSchema is the decoded __schema payload.
type graphqlSchema struct {
	QueryType        *struct{ Name string } `json:"queryType"`
	MutationType     *struct{ Name string } `json:"mutationType"`
	SubscriptionType *struct{ Name string } `json:"subscriptionType"`
	Types            []graphqlType          `json:"types"`
}

// parseGraphQLIntrospection decodes and validates the introspection response.
func parseGraphQLIntrospection(body []byte) (*graphqlSchema, error) {
	var parsed struct {
		Data struct {
			Schema graphqlSchema `json:"__schema"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("response is not GraphQL introspection JSON: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("graphql introspection returned an error: %s", parsed.Errors[0].Message)
	}
	if len(parsed.Data.Schema.Types) == 0 {
		return nil, fmt.Errorf("no schema returned (introspection is likely disabled on this endpoint)")
	}
	return &parsed.Data.Schema, nil
}

func graphqlRootName(p *struct{ Name string }) string {
	if p != nil {
		return p.Name
	}
	return ""
}

func renderGraphQLSchema(safe string, sch *graphqlSchema) string {
	byName := make(map[string]graphqlType, len(sch.Types))
	for _, t := range sch.Types {
		byName[t.Name] = t
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# GraphQL schema: %s\n\n", safe)

	renderGraphQLRoot(&b, byName, "Queries", graphqlRootName(sch.QueryType))
	renderGraphQLRoot(&b, byName, "Mutations", graphqlRootName(sch.MutationType))
	renderGraphQLRoot(&b, byName, "Subscriptions", graphqlRootName(sch.SubscriptionType))

	roots := map[string]bool{
		graphqlRootName(sch.QueryType): true, graphqlRootName(sch.MutationType): true, graphqlRootName(sch.SubscriptionType): true,
	}
	objects, scalars, enumTypes, inputTypes := classifyGraphQLTypes(sch.Types, roots)

	renderGraphQLInputs(&b, inputTypes)
	renderGraphQLEnums(&b, enumTypes)

	b.WriteString("## Types\n\n")
	writeGraphQLTypeList(&b, "Objects", objects)
	writeGraphQLTypeList(&b, "Scalars", scalars)
	return b.String()
}

// renderGraphQLRoot prints one root operation type's fields.
func renderGraphQLRoot(b *strings.Builder, byName map[string]graphqlType, title, typeName string) {
	if typeName == "" {
		return
	}
	t, ok := byName[typeName]
	if !ok || len(t.Fields) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s (%s)\n\n", title, typeName)
	for _, f := range t.Fields {
		var args []string
		for _, a := range f.Args {
			args = append(args, a.Name+": "+a.Type.render())
		}
		sig := f.Name
		if len(args) > 0 {
			sig += "(" + strings.Join(args, ", ") + ")"
		}
		line := "- `" + sig + ": " + f.Type.render() + "`"
		if f.IsDeprecated {
			line += " _(deprecated"
			if f.DeprecationReason != "" {
				line += ": " + firstLine(f.DeprecationReason)
			}
			line += ")_"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
}

// classifyGraphQLTypes buckets user-visible types, skipping introspection
// internals and the already-rendered root types.
func classifyGraphQLTypes(types []graphqlType, roots map[string]bool) (objects, scalars []string, enumTypes, inputTypes []graphqlType) {
	for _, t := range types {
		if strings.HasPrefix(t.Name, "__") || t.Name == "" {
			continue
		}
		switch t.Kind {
		case "OBJECT":
			if !roots[t.Name] {
				objects = append(objects, t.Name)
			}
		case "ENUM":
			enumTypes = append(enumTypes, t)
		case "INPUT_OBJECT":
			inputTypes = append(inputTypes, t)
		case "SCALAR":
			scalars = append(scalars, t.Name)
		}
	}
	return objects, scalars, enumTypes, inputTypes
}

func renderGraphQLInputs(b *strings.Builder, inputTypes []graphqlType) {
	if len(inputTypes) == 0 {
		return
	}
	b.WriteString("## Input types\n\n")
	sort.Slice(inputTypes, func(i, j int) bool { return inputTypes[i].Name < inputTypes[j].Name })
	for _, t := range inputTypes {
		fmt.Fprintf(b, "### %s\n\n", t.Name)
		for _, f := range t.InputFields {
			fmt.Fprintf(b, "- `%s: %s`\n", f.Name, f.Type.render())
		}
		b.WriteString("\n")
	}
}

func renderGraphQLEnums(b *strings.Builder, enumTypes []graphqlType) {
	if len(enumTypes) == 0 {
		return
	}
	b.WriteString("## Enums\n\n")
	sort.Slice(enumTypes, func(i, j int) bool { return enumTypes[i].Name < enumTypes[j].Name })
	for _, t := range enumTypes {
		var vals []string
		for _, v := range t.EnumValues {
			vals = append(vals, v.Name)
		}
		fmt.Fprintf(b, "- `%s`: %s\n", t.Name, strings.Join(vals, " | "))
	}
	b.WriteString("\n")
}

func writeGraphQLTypeList(b *strings.Builder, title string, names []string) {
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	fmt.Fprintf(b, "**%s (%d):** %s\n\n", title, len(names), strings.Join(names, ", "))
}
