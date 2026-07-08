/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * JSON output for the `spec` subcommand — a normalized, machine-readable
 * inventory so the agent can chain the result (pick an endpoint, drive @http)
 * without re-parsing the markdown report.
 */
package plugins

import (
	"encoding/json"
	"sort"
	"strings"
)

type specInventoryJSON struct {
	Title           string            `json:"title"`
	Version         string            `json:"version,omitempty"`
	Format          string            `json:"format"`
	SpecURL         string            `json:"specURL"`
	Servers         []string          `json:"servers,omitempty"`
	SecuritySchemes map[string]string `json:"securitySchemes,omitempty"`
	GlobalSecurity  string            `json:"globalSecurity,omitempty"`
	Models          []string          `json:"models,omitempty"`
	Total           int               `json:"totalEndpoints"`
	Shown           int               `json:"shownEndpoints"`
	Endpoints       []normEndpoint    `json:"endpoints"`
}

type normEndpoint struct {
	Method       string      `json:"method"`
	Path         string      `json:"path"`
	OperationID  string      `json:"operationId,omitempty"`
	Summary      string      `json:"summary,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	Deprecated   bool        `json:"deprecated,omitempty"`
	Parameters   []normParam `json:"parameters,omitempty"`
	RequestTypes []string    `json:"requestContentTypes,omitempty"`
	ResponseCode []string    `json:"responseCodes,omitempty"`
	Security     string      `json:"security,omitempty"`
}

type normParam struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

func renderSpecJSON(doc *oasDoc, format, specURL, filter, tag string, limit int) string {
	res := newRefResolver(doc)
	inv := specInventoryJSON{
		Title:          doc.Info.Title,
		Version:        doc.Info.Version,
		Format:         format,
		SpecURL:        specURL,
		GlobalSecurity: globalSecurity(doc),
		Models:         sortedNames(schemaComponentNames(doc)),
	}
	for _, s := range doc.Servers {
		if s.URL != "" {
			inv.Servers = append(inv.Servers, s.URL)
		}
	}
	if schemes := securitySchemeMap(doc); len(schemes) > 0 {
		inv.SecuritySchemes = schemes
	}

	filter = strings.ToLower(filter)
	for _, p := range sortedKeys(pathKeys(doc.Paths)) {
		if filter != "" && !strings.Contains(strings.ToLower(p), filter) {
			continue
		}
		item := doc.Paths[p]
		for _, mo := range item.operations() {
			if tag != "" && !hasTag(mo.Op.Tags, tag) {
				continue
			}
			inv.Total++
			if inv.Shown >= limit {
				continue
			}
			inv.Shown++
			inv.Endpoints = append(inv.Endpoints, normalizeEndpoint(res, p, mo.Method, mo.Op, item.Parameters))
		}
	}
	b, _ := json.MarshalIndent(inv, "", "  ")
	return string(b)
}

func normalizeEndpoint(res *refResolver, path, method string, op *oasOperation, pathParams []oasParameter) normEndpoint {
	e := normEndpoint{
		Method:      method,
		Path:        path,
		OperationID: op.OperationID,
		Summary:     opSummary(op),
		Tags:        op.Tags,
		Deprecated:  op.Deprecated,
		Security:    operationSecurity(op),
	}
	for _, raw := range append(append([]oasParameter{}, pathParams...), op.Parameters...) {
		p := res.parameter(raw)
		if p.Name == "" {
			continue
		}
		e.Parameters = append(e.Parameters, normParam{
			Name:     p.Name,
			In:       p.In,
			Type:     paramType(p),
			Required: p.Required || strings.EqualFold(p.In, "path"),
		})
	}
	if rb := res.requestBody(op.RequestBody); rb != nil {
		e.RequestTypes = sortedNames(mediaKeys(rb.Content))
	}
	if len(op.Responses) > 0 {
		e.ResponseCode = sortedResponseCodes(op.Responses)
	}
	return e
}

func securitySchemeMap(doc *oasDoc) map[string]string {
	schemes := doc.Components.SecuritySchemes
	if len(schemes) == 0 {
		schemes = doc.SecurityDefinitions
	}
	if len(schemes) == 0 {
		return nil
	}
	out := make(map[string]string, len(schemes))
	for name, s := range schemes {
		out[name] = describeSecurityScheme(s)
	}
	return out
}

func sortedNames(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
