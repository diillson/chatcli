/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * OpenAPI 2.0 / 3.x document model + $ref resolver + renderers for
 * @api-explorer.
 *
 * Tolerant by design: the goal is to map the API surface for a reader, not to
 * validate. Every struct carries both json and yaml tags so one set of types
 * decodes specs served in either format, and unknown fields are ignored.
 *
 * Unlike a naive parser, this DOES resolve $ref pointers (into components /
 * definitions) with cycle detection and a depth bound, so an endpoint deep-dive
 * shows the real model fields — enums, required flags, nested objects — instead
 * of an opaque "#/components/schemas/Pet". allOf/oneOf/anyOf are flattened for
 * display. That resolution is the feature that turns a spec dump into an
 * actually-usable API map.
 */
package plugins

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	oasMaxSchemaDepth = 5  // how deep $ref/object recursion renders
	oasMaxSchemaProps = 60 // properties rendered per object level
)

type oasDoc struct {
	OpenAPI  string                 `json:"openapi" yaml:"openapi"`
	Swagger  string                 `json:"swagger" yaml:"swagger"`
	Info     oasInfo                `json:"info" yaml:"info"`
	Servers  []oasServer            `json:"servers" yaml:"servers"`
	Host     string                 `json:"host" yaml:"host"`
	BasePath string                 `json:"basePath" yaml:"basePath"`
	Schemes  []string               `json:"schemes" yaml:"schemes"`
	Tags     []oasTag               `json:"tags" yaml:"tags"`
	Paths    map[string]oasPathItem `json:"paths" yaml:"paths"`
	Webhooks map[string]oasPathItem `json:"webhooks" yaml:"webhooks"` // 3.1
	Security []map[string][]string  `json:"security" yaml:"security"` // global

	Components   oasComponents    `json:"components" yaml:"components"`
	ExternalDocs *oasExternalDocs `json:"externalDocs" yaml:"externalDocs"`

	// 2.0 top-level component maps.
	Definitions         map[string]oasSchema         `json:"definitions" yaml:"definitions"`
	Parameters          map[string]oasParameter      `json:"parameters" yaml:"parameters"`
	Responses           map[string]oasResponse       `json:"responses" yaml:"responses"`
	SecurityDefinitions map[string]oasSecurityScheme `json:"securityDefinitions" yaml:"securityDefinitions"`
}

type oasInfo struct {
	Title       string `json:"title" yaml:"title"`
	Version     string `json:"version" yaml:"version"`
	Description string `json:"description" yaml:"description"`
}

type oasServer struct {
	URL         string `json:"url" yaml:"url"`
	Description string `json:"description" yaml:"description"`
}

type oasTag struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
}

type oasExternalDocs struct {
	URL         string `json:"url" yaml:"url"`
	Description string `json:"description" yaml:"description"`
}

type oasComponents struct {
	Schemas         map[string]oasSchema         `json:"schemas" yaml:"schemas"`
	Parameters      map[string]oasParameter      `json:"parameters" yaml:"parameters"`
	RequestBodies   map[string]oasRequestBody    `json:"requestBodies" yaml:"requestBodies"`
	Responses       map[string]oasResponse       `json:"responses" yaml:"responses"`
	SecuritySchemes map[string]oasSecurityScheme `json:"securitySchemes" yaml:"securitySchemes"`
}

type oasSecurityScheme struct {
	Type             string                 `json:"type" yaml:"type"`
	Scheme           string                 `json:"scheme" yaml:"scheme"`
	In               string                 `json:"in" yaml:"in"`
	Name             string                 `json:"name" yaml:"name"`
	BearerFormat     string                 `json:"bearerFormat" yaml:"bearerFormat"`
	Description      string                 `json:"description" yaml:"description"`
	Flow             string                 `json:"flow" yaml:"flow"`
	AuthorizationURL string                 `json:"authorizationUrl" yaml:"authorizationUrl"`
	TokenURL         string                 `json:"tokenUrl" yaml:"tokenUrl"`
	Flows            map[string]interface{} `json:"flows" yaml:"flows"`
	OpenIDConnectURL string                 `json:"openIdConnectUrl" yaml:"openIdConnectUrl"`
}

type oasPathItem struct {
	Ref         string         `json:"$ref" yaml:"$ref"`
	Summary     string         `json:"summary" yaml:"summary"`
	Description string         `json:"description" yaml:"description"`
	Parameters  []oasParameter `json:"parameters" yaml:"parameters"`

	Get     *oasOperation `json:"get" yaml:"get"`
	Put     *oasOperation `json:"put" yaml:"put"`
	Post    *oasOperation `json:"post" yaml:"post"`
	Delete  *oasOperation `json:"delete" yaml:"delete"`
	Options *oasOperation `json:"options" yaml:"options"`
	Head    *oasOperation `json:"head" yaml:"head"`
	Patch   *oasOperation `json:"patch" yaml:"patch"`
	Trace   *oasOperation `json:"trace" yaml:"trace"`
}

type methodOp struct {
	Method string
	Op     *oasOperation
}

// operations returns the defined operations in canonical HTTP order.
func (pi oasPathItem) operations() []methodOp {
	all := []methodOp{
		{"GET", pi.Get}, {"POST", pi.Post}, {"PUT", pi.Put}, {"PATCH", pi.Patch},
		{"DELETE", pi.Delete}, {"HEAD", pi.Head}, {"OPTIONS", pi.Options}, {"TRACE", pi.Trace},
	}
	out := all[:0]
	for _, mo := range all {
		if mo.Op != nil {
			out = append(out, mo)
		}
	}
	return out
}

type oasOperation struct {
	OperationID string                 `json:"operationId" yaml:"operationId"`
	Summary     string                 `json:"summary" yaml:"summary"`
	Description string                 `json:"description" yaml:"description"`
	Tags        []string               `json:"tags" yaml:"tags"`
	Parameters  []oasParameter         `json:"parameters" yaml:"parameters"`
	RequestBody *oasRequestBody        `json:"requestBody" yaml:"requestBody"`
	Responses   map[string]oasResponse `json:"responses" yaml:"responses"`
	Security    []map[string][]string  `json:"security" yaml:"security"`
	Deprecated  bool                   `json:"deprecated" yaml:"deprecated"`
	Consumes    []string               `json:"consumes" yaml:"consumes"`
	Produces    []string               `json:"produces" yaml:"produces"`
}

type oasParameter struct {
	Ref         string        `json:"$ref" yaml:"$ref"`
	Name        string        `json:"name" yaml:"name"`
	In          string        `json:"in" yaml:"in"`
	Description string        `json:"description" yaml:"description"`
	Required    bool          `json:"required" yaml:"required"`
	Deprecated  bool          `json:"deprecated" yaml:"deprecated"`
	Type        string        `json:"type" yaml:"type"`
	Schema      *oasSchema    `json:"schema" yaml:"schema"`
	Example     interface{}   `json:"example" yaml:"example"`
	Enum        []interface{} `json:"enum" yaml:"enum"` // 2.0 inline
}

type oasRequestBody struct {
	Ref         string                  `json:"$ref" yaml:"$ref"`
	Description string                  `json:"description" yaml:"description"`
	Required    bool                    `json:"required" yaml:"required"`
	Content     map[string]oasMediaType `json:"content" yaml:"content"`
}

type oasMediaType struct {
	Schema  *oasSchema  `json:"schema" yaml:"schema"`
	Example interface{} `json:"example" yaml:"example"`
}

type oasResponse struct {
	Ref         string                  `json:"$ref" yaml:"$ref"`
	Description string                  `json:"description" yaml:"description"`
	Content     map[string]oasMediaType `json:"content" yaml:"content"`
	Schema      *oasSchema              `json:"schema" yaml:"schema"` // 2.0
	Headers     map[string]oasParameter `json:"headers" yaml:"headers"`
}

type oasSchema struct {
	Ref         string               `json:"$ref" yaml:"$ref"`
	Type        string               `json:"type" yaml:"type"`
	Format      string               `json:"format" yaml:"format"`
	Title       string               `json:"title" yaml:"title"`
	Description string               `json:"description" yaml:"description"`
	Items       *oasSchema           `json:"items" yaml:"items"`
	Enum        []interface{}        `json:"enum" yaml:"enum"`
	Default     interface{}          `json:"default" yaml:"default"`
	Example     interface{}          `json:"example" yaml:"example"`
	Properties  map[string]oasSchema `json:"properties" yaml:"properties"`
	Required    []string             `json:"required" yaml:"required"`
	AllOf       []*oasSchema         `json:"allOf" yaml:"allOf"`
	OneOf       []*oasSchema         `json:"oneOf" yaml:"oneOf"`
	AnyOf       []*oasSchema         `json:"anyOf" yaml:"anyOf"`
	Nullable    bool                 `json:"nullable" yaml:"nullable"`
	Deprecated  bool                 `json:"deprecated" yaml:"deprecated"`
	ReadOnly    bool                 `json:"readOnly" yaml:"readOnly"`
	WriteOnly   bool                 `json:"writeOnly" yaml:"writeOnly"`
	Minimum     *float64             `json:"minimum" yaml:"minimum"`
	Maximum     *float64             `json:"maximum" yaml:"maximum"`
	MinLength   *int                 `json:"minLength" yaml:"minLength"`
	MaxLength   *int                 `json:"maxLength" yaml:"maxLength"`
	Pattern     string               `json:"pattern" yaml:"pattern"`
}

// ---------------------------------------------------------------------------
// $ref resolver.
// ---------------------------------------------------------------------------

// refResolver walks $ref pointers into the document's component maps with cycle
// detection. It is bound to one doc for the duration of a render.
type refResolver struct {
	doc *oasDoc
}

func newRefResolver(doc *oasDoc) *refResolver { return &refResolver{doc: doc} }

// schema resolves a schema $ref (local pointers only; external refs are left
// as their name). Returns the resolved schema and whether resolution happened.
func (r *refResolver) schema(ref string) (*oasSchema, bool) {
	name := localRefName(ref, "schemas", "definitions")
	if name == "" {
		return nil, false
	}
	if s, ok := r.doc.Components.Schemas[name]; ok {
		return &s, true
	}
	if s, ok := r.doc.Definitions[name]; ok {
		return &s, true
	}
	return nil, false
}

func (r *refResolver) parameter(p oasParameter) oasParameter {
	if p.Ref == "" {
		return p
	}
	name := localRefName(p.Ref, "parameters", "parameters")
	if resolved, ok := r.doc.Components.Parameters[name]; ok {
		return resolved
	}
	if resolved, ok := r.doc.Parameters[name]; ok {
		return resolved
	}
	return p
}

func (r *refResolver) requestBody(rb *oasRequestBody) *oasRequestBody {
	if rb == nil || rb.Ref == "" {
		return rb
	}
	name := localRefName(rb.Ref, "requestBodies", "")
	if resolved, ok := r.doc.Components.RequestBodies[name]; ok {
		return &resolved
	}
	return rb
}

func (r *refResolver) response(resp oasResponse) oasResponse {
	if resp.Ref == "" {
		return resp
	}
	name := localRefName(resp.Ref, "responses", "responses")
	if resolved, ok := r.doc.Components.Responses[name]; ok {
		return resolved
	}
	if resolved, ok := r.doc.Responses[name]; ok {
		return resolved
	}
	return resp
}

// localRefName extracts the component name from a local $ref, accepting the
// 3.x (#/components/<kind>/Name) and 2.0 (#/<kind2>/Name) shapes.
func localRefName(ref, kind, kind2 string) string {
	if ref == "" || !strings.HasPrefix(ref, "#/") {
		return ""
	}
	tail := refName(ref)
	if strings.Contains(ref, "/components/"+kind+"/") {
		return tail
	}
	if kind2 != "" && strings.Contains(ref, "/"+kind2+"/") {
		return tail
	}
	return ""
}

// refName returns the tail component of a $ref pointer.
func refName(ref string) string {
	if ref == "" {
		return ""
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// typeName renders a schema as a short human-readable type token (one line, no
// recursion) — used for parameter tables and endpoint listings.
func (s *oasSchema) typeName() string {
	if s == nil {
		return "?"
	}
	if s.Ref != "" {
		return refName(s.Ref)
	}
	if len(s.AllOf) > 0 {
		return joinSchemaNames(s.AllOf, " & ")
	}
	if len(s.OneOf) > 0 {
		return joinSchemaNames(s.OneOf, " | ")
	}
	if len(s.AnyOf) > 0 {
		return joinSchemaNames(s.AnyOf, " | ")
	}
	switch s.Type {
	case "array":
		return "array<" + s.Items.typeName() + ">"
	case "":
		if len(s.Properties) > 0 {
			return "object"
		}
		return "any"
	default:
		if s.Format != "" {
			return s.Type + "(" + s.Format + ")"
		}
		return s.Type
	}
}

func joinSchemaNames(list []*oasSchema, sep string) string {
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.typeName())
	}
	return strings.Join(names, sep)
}

// constraintSuffix renders enum/default/range/pattern info as a compact
// bracketed tail, or "" if the schema carries none.
func (s *oasSchema) constraintSuffix() string {
	if s == nil {
		return ""
	}
	var parts []string
	if len(s.Enum) > 0 {
		parts = append(parts, "enum: "+joinValues(s.Enum, 8))
	}
	if s.Default != nil {
		parts = append(parts, "default: "+valueString(s.Default))
	}
	if s.Minimum != nil || s.Maximum != nil {
		lo, hi := "", ""
		if s.Minimum != nil {
			lo = strconv.FormatFloat(*s.Minimum, 'g', -1, 64)
		}
		if s.Maximum != nil {
			hi = strconv.FormatFloat(*s.Maximum, 'g', -1, 64)
		}
		parts = append(parts, "range: "+lo+".."+hi)
	}
	if s.MinLength != nil || s.MaxLength != nil {
		lo, hi := "", ""
		if s.MinLength != nil {
			lo = strconv.Itoa(*s.MinLength)
		}
		if s.MaxLength != nil {
			hi = strconv.Itoa(*s.MaxLength)
		}
		parts = append(parts, "len: "+lo+".."+hi)
	}
	if s.Pattern != "" {
		parts = append(parts, "pattern: "+firstLine(s.Pattern))
	}
	if s.Nullable {
		parts = append(parts, "nullable")
	}
	if s.ReadOnly {
		parts = append(parts, "readOnly")
	}
	if s.WriteOnly {
		parts = append(parts, "writeOnly")
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, "; ") + "]"
}

func joinValues(vals []interface{}, limit int) string {
	out := make([]string, 0, len(vals))
	for i, v := range vals {
		if i >= limit {
			out = append(out, "…")
			break
		}
		out = append(out, valueString(v))
	}
	return strings.Join(out, ", ")
}

func valueString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", t)
	}
}

// ---------------------------------------------------------------------------
// Overview rendering.
// ---------------------------------------------------------------------------

func specTitle(doc *oasDoc) string {
	if doc.Info.Title != "" {
		if doc.Info.Version != "" {
			return doc.Info.Title + " " + doc.Info.Version
		}
		return doc.Info.Title
	}
	return "API"
}

// renderSpecSummary renders the overview + endpoint listing (grouped by tag),
// honoring an optional path-substring filter, tag filter and endpoint cap.
func renderSpecSummary(doc *oasDoc, filter, tag string, limit int) string {
	var b strings.Builder

	if doc.Info.Description != "" {
		b.WriteString(firstLine(doc.Info.Description))
		b.WriteString("\n\n")
	}
	if servers := serverList(doc); servers != "" {
		fmt.Fprintf(&b, "**Servers:** %s\n\n", servers)
	}
	if sec := renderSecuritySchemes(doc); sec != "" {
		b.WriteString(sec)
		b.WriteString("\n")
	}
	if g := globalSecurity(doc); g != "" {
		fmt.Fprintf(&b, "**Default auth (global):** %s\n\n", g)
	}
	if models := modelList(doc); models != "" {
		b.WriteString(models)
		b.WriteString("\n")
	}
	if doc.ExternalDocs != nil && doc.ExternalDocs.URL != "" {
		fmt.Fprintf(&b, "**Docs:** %s\n\n", doc.ExternalDocs.URL)
	}

	total, shown, listing := renderEndpointListing(doc, filter, tag, limit)
	fmt.Fprintf(&b, "## Endpoints (%d)\n\n", total)
	b.WriteString(listing)
	if total > shown {
		fmt.Fprintf(&b, "\n… %d more not shown (raise limit or narrow with filter/tag).\n", total-shown)
	}
	if wh := renderWebhooks(doc); wh != "" {
		b.WriteString("\n")
		b.WriteString(wh)
	}
	return b.String()
}

// renderEndpointListing groups matching operations by their first tag.
func renderEndpointListing(doc *oasDoc, filter, tag string, limit int) (total, shown int, out string) {
	filter = strings.ToLower(filter)
	paths := sortedKeys(pathKeys(doc.Paths))

	type row struct{ path, line, tag string }
	var rows []row
	for _, p := range paths {
		if filter != "" && !strings.Contains(strings.ToLower(p), filter) {
			continue
		}
		for _, mo := range doc.Paths[p].operations() {
			if tag != "" && !hasTag(mo.Op.Tags, tag) {
				continue
			}
			total++
			if shown >= limit {
				continue
			}
			shown++
			line := fmt.Sprintf("- `%-6s %s`", mo.Method, p)
			if sum := opSummary(mo.Op); sum != "" {
				line += " — " + sum
			}
			if mo.Op.Deprecated {
				line += " _(deprecated)_"
			}
			rows = append(rows, row{p, line, firstTag(mo.Op.Tags)})
		}
	}
	if len(rows) == 0 {
		return total, shown, "_(no endpoints matched)_\n"
	}

	// Group by tag, preserving a stable tag order.
	groups := map[string][]string{}
	var order []string
	for _, r := range rows {
		if _, ok := groups[r.tag]; !ok {
			order = append(order, r.tag)
		}
		groups[r.tag] = append(groups[r.tag], r.line)
	}
	sort.Strings(order)
	var b strings.Builder
	for _, g := range order {
		if g != "" {
			fmt.Fprintf(&b, "### %s\n\n", g)
		}
		b.WriteString(strings.Join(groups[g], "\n"))
		b.WriteString("\n\n")
	}
	return total, shown, b.String()
}

func renderWebhooks(doc *oasDoc) string {
	if len(doc.Webhooks) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Webhooks (%d)\n\n", len(doc.Webhooks))
	for _, name := range sortedKeys(pathKeys(doc.Webhooks)) {
		for _, mo := range doc.Webhooks[name].operations() {
			fmt.Fprintf(&b, "- `%s %s`", mo.Method, name)
			if sum := opSummary(mo.Op); sum != "" {
				b.WriteString(" — " + sum)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderOperationDetail renders the full parameter/body/response breakdown of
// one operation, resolving $ref schemas and merging path-level parameters.
func renderOperationDetail(doc *oasDoc, path, method string, op *oasOperation, pathParams []oasParameter) string {
	res := newRefResolver(doc)
	var b strings.Builder
	fmt.Fprintf(&b, "## %s %s\n\n", method, path)
	if op.OperationID != "" {
		fmt.Fprintf(&b, "operationId: `%s`\n", op.OperationID)
	}
	if op.Summary != "" {
		fmt.Fprintf(&b, "%s\n", op.Summary)
	}
	if op.Description != "" && !strings.EqualFold(op.Description, op.Summary) {
		fmt.Fprintf(&b, "\n%s\n", firstLine(op.Description))
	}
	if len(op.Tags) > 0 {
		fmt.Fprintf(&b, "\ntags: %s\n", strings.Join(op.Tags, ", "))
	}
	if op.Deprecated {
		b.WriteString("\n**⚠ deprecated**\n")
	}
	b.WriteString("\n")

	b.WriteString(renderOperationParams(res, op, pathParams))
	b.WriteString(renderOperationRequestBody(res, op))
	b.WriteString(renderOperationResponses(res, op))
	b.WriteString(renderOperationSecurity(doc, op))
	return b.String()
}

func renderOperationParams(res *refResolver, op *oasOperation, pathParams []oasParameter) string {
	rawParams := append(append([]oasParameter{}, pathParams...), op.Parameters...)
	params := make([]oasParameter, 0, len(rawParams))
	for _, p := range rawParams {
		params = append(params, res.parameter(p))
	}
	if len(params) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Parameters\n\n")
	for _, group := range []string{"path", "query", "header", "cookie", "formData", "body"} {
		for _, prm := range params {
			if strings.EqualFold(prm.In, group) {
				b.WriteString(renderOneParam(res, prm))
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderOneParam(res *refResolver, prm oasParameter) string {
	req := ""
	if prm.Required || strings.EqualFold(prm.In, "path") {
		req = " **required**"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- `%s` (%s, %s)%s", prm.Name, prm.In, paramType(prm), req)
	b.WriteString(paramConstraints(prm))
	if prm.Description != "" {
		b.WriteString(" — " + firstLine(prm.Description))
	}
	b.WriteString("\n")
	if prm.Schema != nil && (prm.Schema.Ref != "" || len(prm.Schema.Properties) > 0) {
		b.WriteString(res.renderSchema(prm.Schema, 2, 0, map[string]bool{}))
	}
	return b.String()
}

func renderOperationRequestBody(res *refResolver, op *oasOperation) string {
	rb := res.requestBody(op.RequestBody)
	if rb == nil || len(rb.Content) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Request body")
	if rb.Required {
		b.WriteString(" (required)")
	}
	b.WriteString("\n\n")
	for _, ct := range sortedKeys(mediaKeys(rb.Content)) {
		mt := rb.Content[ct]
		fmt.Fprintf(&b, "- %s → %s\n", ct, mt.Schema.typeName())
		b.WriteString(res.renderSchema(mt.Schema, 2, 0, map[string]bool{}))
	}
	b.WriteString("\n")
	return b.String()
}

func renderOperationResponses(res *refResolver, op *oasOperation) string {
	if len(op.Responses) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Responses\n\n")
	for _, code := range sortedResponseCodes(op.Responses) {
		resp := res.response(op.Responses[code])
		desc := firstLine(resp.Description)
		schema, schemaObj := responseSchema(resp)
		if schema != "" {
			fmt.Fprintf(&b, "- **%s** — %s → %s\n", code, desc, schema)
		} else {
			fmt.Fprintf(&b, "- **%s** — %s\n", code, desc)
		}
		if schemaObj != nil && (schemaObj.Ref != "" || len(schemaObj.Properties) > 0) {
			b.WriteString(res.renderSchema(schemaObj, 4, 0, map[string]bool{}))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func renderOperationSecurity(doc *oasDoc, op *oasOperation) string {
	if sec := operationSecurity(op); sec != "" {
		return "### Security\n\n" + sec + "\n"
	}
	if g := globalSecurity(doc); g != "" {
		return "### Security\n\n" + g + " _(inherited global default)_\n"
	}
	return ""
}

// renderSchema recursively renders a schema's fields, resolving $ref with cycle
// detection, bounded by oasMaxSchemaDepth and oasMaxSchemaProps.
func (r *refResolver) renderSchema(s *oasSchema, indentCols, depth int, seen map[string]bool) string {
	if s == nil || depth >= oasMaxSchemaDepth {
		return ""
	}
	indent := strings.Repeat(" ", indentCols)

	// Resolve a top-level $ref, guarding cycles.
	if s.Ref != "" {
		if seen[s.Ref] {
			return fmt.Sprintf("%s- (recursive → %s)\n", indent, refName(s.Ref))
		}
		resolved, ok := r.schema(s.Ref)
		if !ok {
			return "" // external / unknown ref — the type name was already shown
		}
		seen = cloneSeen(seen)
		seen[s.Ref] = true
		s = resolved
	}

	// Flatten composition.
	if merged := r.flatten(s); merged != nil {
		s = merged
	}

	target := s
	if s.Type == "array" && s.Items != nil {
		target = s.Items
		if target.Ref != "" {
			if seen[target.Ref] {
				return fmt.Sprintf("%s- (array of recursive → %s)\n", indent, refName(target.Ref))
			}
			if resolved, ok := r.schema(target.Ref); ok {
				seen = cloneSeen(seen)
				seen[target.Ref] = true
				target = resolved
			}
		}
	}
	if len(target.Properties) == 0 {
		return ""
	}

	required := map[string]bool{}
	for _, rq := range target.Required {
		required[rq] = true
	}
	var b strings.Builder
	count := 0
	for _, name := range sortedKeys(propKeys(target.Properties)) {
		if count >= oasMaxSchemaProps {
			fmt.Fprintf(&b, "%s- … (%d more fields)\n", indent, len(target.Properties)-count)
			break
		}
		count++
		prop := target.Properties[name]
		req := ""
		if required[name] {
			req = " *required*"
		}
		fmt.Fprintf(&b, "%s- %s: %s%s%s\n", indent, name, prop.typeName(), req, prop.constraintSuffix())
		// Recurse into nested objects / arrays-of-objects.
		if prop.Ref != "" || prop.Type == "object" || len(prop.Properties) > 0 ||
			(prop.Type == "array" && prop.Items != nil && (prop.Items.Ref != "" || len(prop.Items.Properties) > 0)) {
			b.WriteString(r.renderSchema(&prop, indentCols+4, depth+1, seen))
		}
	}
	return b.String()
}

// flatten merges allOf members into one schema (shallow property union) and, for
// oneOf/anyOf, picks the first member so at least one shape is shown.
func (r *refResolver) flatten(s *oasSchema) *oasSchema {
	members := s.AllOf
	union := len(members) > 0
	if !union {
		if len(s.OneOf) > 0 {
			members = s.OneOf[:1]
		} else if len(s.AnyOf) > 0 {
			members = s.AnyOf[:1]
		}
	}
	if len(members) == 0 {
		return nil
	}
	merged := oasSchema{Type: "object", Properties: map[string]oasSchema{}}
	for k, v := range s.Properties {
		merged.Properties[k] = v
	}
	merged.Required = append(merged.Required, s.Required...)
	for _, m := range members {
		mm := m
		if m.Ref != "" {
			if resolved, ok := r.schema(m.Ref); ok {
				mm = resolved
			}
		}
		for k, v := range mm.Properties {
			merged.Properties[k] = v
		}
		merged.Required = append(merged.Required, mm.Required...)
	}
	return &merged
}

func cloneSeen(seen map[string]bool) map[string]bool {
	out := make(map[string]bool, len(seen)+1)
	for k := range seen {
		out[k] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// Security / models rendering.
// ---------------------------------------------------------------------------

func renderSecuritySchemes(doc *oasDoc) string {
	schemes := doc.Components.SecuritySchemes
	if len(schemes) == 0 {
		schemes = doc.SecurityDefinitions // 2.0
	}
	if len(schemes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**Security schemes:**\n")
	for _, name := range sortedKeys(secKeys(schemes)) {
		s := schemes[name]
		fmt.Fprintf(&b, "- `%s`: %s\n", name, describeSecurityScheme(s))
	}
	return b.String()
}

func describeSecurityScheme(s oasSecurityScheme) string {
	switch strings.ToLower(s.Type) {
	case "apikey":
		return "API key in " + s.In + " `" + s.Name + "`"
	case "http":
		if s.BearerFormat != "" {
			return "HTTP " + s.Scheme + " (" + s.BearerFormat + ")"
		}
		return "HTTP " + s.Scheme
	case "oauth2":
		if len(s.Flows) > 0 {
			return "OAuth2 (" + strings.Join(sortedKeys(flowKeys(s.Flows)), ", ") + " flow)"
		}
		if s.Flow != "" {
			return "OAuth2 (" + s.Flow + " flow)"
		}
		return "OAuth2"
	case "openidconnect":
		return "OpenID Connect (" + s.OpenIDConnectURL + ")"
	case "basic":
		return "HTTP Basic"
	default:
		if s.Type != "" {
			return s.Type
		}
		return "unspecified"
	}
}

func globalSecurity(doc *oasDoc) string { return renderSecurityRequirements(doc.Security) }

func operationSecurity(op *oasOperation) string { return renderSecurityRequirements(op.Security) }

func renderSecurityRequirements(reqs []map[string][]string) string {
	if len(reqs) == 0 {
		return ""
	}
	out := make([]string, 0, len(reqs))
	for _, req := range reqs {
		if len(req) == 0 {
			out = append(out, "(public — no auth)")
			continue
		}
		var names []string
		for name, scopes := range req {
			if len(scopes) > 0 {
				names = append(names, fmt.Sprintf("%s[%s]", name, strings.Join(scopes, " ")))
			} else {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		out = append(out, strings.Join(names, " + "))
	}
	return strings.Join(out, " OR ")
}

func modelList(doc *oasDoc) string {
	names := schemaComponentNames(doc)
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	const listCap = 40
	shown := names
	suffix := ""
	if len(names) > listCap {
		shown = names[:listCap]
		suffix = fmt.Sprintf(", … (+%d)", len(names)-listCap)
	}
	return fmt.Sprintf("**Models (%d):** %s%s\n", len(names), strings.Join(shown, ", "), suffix)
}

func schemaComponentNames(doc *oasDoc) []string {
	seen := map[string]bool{}
	names := make([]string, 0, len(doc.Components.Schemas)+len(doc.Definitions))
	for n := range doc.Components.Schemas {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range doc.Definitions {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	return names
}

// ---------------------------------------------------------------------------
// Small helpers.
// ---------------------------------------------------------------------------

func serverList(doc *oasDoc) string {
	var urls []string
	for _, s := range doc.Servers {
		if s.URL != "" {
			urls = append(urls, s.URL)
		}
	}
	if len(urls) == 0 && doc.Host != "" { // 2.0
		scheme := "https"
		if len(doc.Schemes) > 0 {
			scheme = doc.Schemes[0]
		}
		urls = append(urls, scheme+"://"+doc.Host+doc.BasePath)
	}
	return strings.Join(urls, ", ")
}

func opSummary(op *oasOperation) string {
	if op.Summary != "" {
		return firstLine(op.Summary)
	}
	if op.OperationID != "" {
		return op.OperationID
	}
	return firstLine(op.Description)
}

func paramType(p oasParameter) string {
	if p.Schema != nil {
		return p.Schema.typeName()
	}
	if p.Type != "" {
		return p.Type
	}
	return "string"
}

func paramConstraints(p oasParameter) string {
	if p.Schema != nil {
		return p.Schema.constraintSuffix()
	}
	if len(p.Enum) > 0 {
		return " [enum: " + joinValues(p.Enum, 8) + "]"
	}
	return ""
}

// responseSchema returns a one-line type token and the underlying schema object
// (for optional expansion), handling both 2.0 (.schema) and 3.x (.content).
func responseSchema(r oasResponse) (string, *oasSchema) {
	if r.Schema != nil { // 2.0
		return r.Schema.typeName(), r.Schema
	}
	for _, ct := range sortedKeys(mediaKeys(r.Content)) {
		if mt := r.Content[ct]; mt.Schema != nil {
			return mt.Schema.typeName(), mt.Schema
		}
	}
	return "", nil
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}

func firstTag(tags []string) string {
	if len(tags) > 0 {
		return tags[0]
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const maxLen = 200
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

func sortedKeys(keys []string) []string {
	sort.Strings(keys)
	return keys
}

// sortedResponseCodes orders status codes numerically, with non-numeric keys
// (e.g. "default", "2XX") last.
func sortedResponseCodes(m map[string]oasResponse) []string {
	keys := responseKeys(m)
	sort.Slice(keys, func(i, j int) bool {
		ni, ei := strconv.Atoi(keys[i])
		nj, ej := strconv.Atoi(keys[j])
		if ei == nil && ej == nil {
			return ni < nj
		}
		if ei == nil {
			return true
		}
		if ej == nil {
			return false
		}
		return keys[i] < keys[j]
	})
	return keys
}

func pathKeys(m map[string]oasPathItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mediaKeys(m map[string]oasMediaType) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func responseKeys(m map[string]oasResponse) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func propKeys(m map[string]oasSchema) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func secKeys(m map[string]oasSecurityScheme) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func flowKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
