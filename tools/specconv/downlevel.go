package main

import (
	"encoding/json"
	"slices"
)

// downlevel rewrites an OpenAPI 3.1 document as OpenAPI 3.0, the version
// the pinned oapi-codegen reads. A 3.0 document passes through unchanged
// except for the "$schema" clean-up, which applies to both.
//
// 3.1 schemas are JSON Schema 2020-12. The constructs services emit that
// 3.0 has no syntax for are rewritten to their 3.0 equivalent:
//
//   - "type": ["string", "null"] becomes "type": "string" with
//     "nullable": true (a list of several non-null types can't be said in
//     3.0, so the type is left out and the field is untyped);
//   - a numeric "exclusiveMinimum"/"exclusiveMaximum" becomes
//     "minimum"/"maximum" plus the boolean form;
//   - "const" becomes a one-value "enum";
//   - a schema's "examples" list becomes "example" (its first entry);
//     the "examples" map of a media type or parameter is a 3.0 construct
//     and is left alone;
//   - the read-only "$schema" property huma adds to every object, which is
//     no field a client sends or reads, is dropped (with its "required"
//     entry);
//   - the 3.1-only top-level "webhooks" and "jsonSchemaDialect" are
//     dropped: they describe nothing a client calls.
func downlevel(raw []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	doc["openapi"] = "3.0.3"
	delete(doc, "webhooks")
	delete(doc, "jsonSchemaDialect")
	walk(doc)
	return json.Marshal(doc)
}

func walk(v any) {
	switch t := v.(type) {
	case map[string]any:
		downlevelSchema(t)
		for _, child := range t {
			walk(child)
		}
	case []any:
		for _, child := range t {
			walk(child)
		}
	}
}

// downlevelSchema rewrites one node if it uses 3.1-only schema syntax.
// Nodes that aren't schemas are untouched: every rule keys on a field
// whose 3.1 form can't appear outside a schema.
func downlevelSchema(s map[string]any) {
	if types, ok := s["type"].([]any); ok {
		var nonNull []any
		for _, x := range types {
			if x != "null" {
				nonNull = append(nonNull, x)
			}
		}
		switch {
		case len(nonNull) == 1:
			s["type"] = nonNull[0]
		default:
			delete(s, "type")
		}
		if len(nonNull) < len(types) {
			s["nullable"] = true
		}
	}
	for _, k := range [][2]string{{"exclusiveMinimum", "minimum"}, {"exclusiveMaximum", "maximum"}} {
		if n, ok := s[k[0]].(float64); ok {
			s[k[1]] = n
			s[k[0]] = true
		}
	}
	if c, ok := s["const"]; ok {
		s["enum"] = []any{c}
		delete(s, "const")
	}
	if ex, ok := s["examples"].([]any); ok {
		if len(ex) > 0 {
			s["example"] = ex[0]
		}
		delete(s, "examples")
	}
	if props, ok := s["properties"].(map[string]any); ok {
		if p, ok := props["$schema"].(map[string]any); ok && p["readOnly"] == true {
			delete(props, "$schema")
			if req, ok := s["required"].([]any); ok {
				req = slices.DeleteFunc(req, func(x any) bool { return x == "$schema" })
				if len(req) == 0 {
					delete(s, "required")
				} else {
					s["required"] = req
				}
			}
		}
	}
}
