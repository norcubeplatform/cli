package main

import (
	"encoding/json"
	"slices"
	"strings"
)

// excludeOperations drops every operation tagged with one of tags (and path
// items left empty), then the component schemas nothing kept refers to. A
// service's spec can describe routes that aren't for organization members
// (superadmin tools, webhooks); they stay out of the checked-in spec and the
// generated client.
func excludeOperations(spec []byte, tags []string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(spec, &doc); err != nil {
		return nil, err
	}
	paths, _ := doc["paths"].(map[string]any)
	for p, item := range paths {
		ops, _ := item.(map[string]any)
		for method, op := range ops {
			o, ok := op.(map[string]any)
			if !ok {
				continue
			}
			opTags, _ := o["tags"].([]any)
			for _, t := range opTags {
				if s, ok := t.(string); ok && slices.Contains(tags, s) {
					delete(ops, method)
					break
				}
			}
		}
		if !hasOperation(ops) {
			delete(paths, p)
		}
	}
	pruneSchemas(doc)
	return json.MarshalIndent(doc, "", "  ")
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func hasOperation(item map[string]any) bool {
	for k := range item {
		if slices.Contains(httpMethods, k) {
			return true
		}
	}
	return false
}

// pruneSchemas removes component schemas that no path refers to, directly or
// through other schemas.
func pruneSchemas(doc map[string]any) {
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	if schemas == nil {
		return
	}
	const prefix = "#/components/schemas/"
	used := map[string]bool{}
	var queue []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok && strings.HasPrefix(ref, prefix) {
				name := strings.TrimPrefix(ref, prefix)
				if !used[name] {
					used[name] = true
					queue = append(queue, name)
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(doc["paths"])
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		walk(schemas[name])
	}
	for name := range schemas {
		if !used[name] {
			delete(schemas, name)
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
