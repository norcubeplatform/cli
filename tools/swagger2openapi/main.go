// swagger2openapi reads a Swagger 2.0 JSON document and writes the equivalent
// OpenAPI 3.0 JSON to stdout. This is a glue tool: the Norcube backend
// services emit Swagger 2.0 via `swag`, but oapi-codegen only ingests
// OpenAPI 3.x, so we run every spec through this converter before codegen.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: swagger2openapi <swagger-2.0.json>")
		os.Exit(2)
	}

	in, err := os.Open(os.Args[1])
	must(err)
	defer in.Close()

	raw, err := io.ReadAll(in)
	must(err)

	var v2 openapi2.T
	must(json.Unmarshal(raw, &v2))

	v3, err := openapi2conv.ToV3(&v2)
	must(err)

	normalizeFiberPathParams(v3)
	stripOperationSecurity(v3)
	ensurePathParameters(v3)

	out, err := json.MarshalIndent(v3, "", "  ")
	must(err)

	_, err = os.Stdout.Write(out)
	must(err)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// normalizeFiberPathParams rewrites Fiber-style path placeholders (`:name`) to
// OpenAPI-style placeholders (`{name}`) in the spec's path keys. Some Norcube
// services use Fiber's syntax in their @Router annotations, which `swag`
// passes through unchanged, but oapi-codegen requires OpenAPI syntax.
var (
	fiberParamRE   = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)
	pathParamRE    = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// ensurePathParameters walks every operation and adds a declaration for any
// path placeholder ({foo}) that's missing from the operation's Parameters
// list. Norcube's `swag` annotations sometimes only `@Param` the last
// placeholder, which trips oapi-codegen with "N positional parameters but
// spec has M declared". Filling in the missing ones unblocks codegen
// without losing type information for the params that *were* declared.
func ensurePathParameters(doc *openapi3.T) {
	if doc == nil || doc.Paths == nil {
		return
	}
	for pathKey, item := range doc.Paths.Map() {
		if item == nil {
			continue
		}
		needed := pathParamRE.FindAllStringSubmatch(pathKey, -1)
		if len(needed) == 0 {
			continue
		}
		for _, op := range item.Operations() {
			if op == nil {
				continue
			}
			have := map[string]bool{}
			for _, p := range op.Parameters {
				if p == nil || p.Value == nil {
					continue
				}
				if p.Value.In == "path" {
					have[p.Value.Name] = true
				}
			}
			for _, m := range needed {
				name := m[1]
				if have[name] {
					continue
				}
				op.Parameters = append(op.Parameters, &openapi3.ParameterRef{
					Value: &openapi3.Parameter{
						Name:     name,
						In:       "path",
						Required: true,
						Schema:   &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
					},
				})
			}
		}
	}
}

// stripOperationSecurity removes per-operation `security` blocks. Norcube
// services aren't consistent about security-scheme names across @Security
// annotations vs the top-level securityDefinitions, which trips up
// oapi-codegen. The generated client doesn't enforce auth anyway — we
// inject Bearer tokens via the http.Client transport — so dropping these
// blocks is purely a codegen cleanup.
func stripOperationSecurity(doc *openapi3.T) {
	if doc == nil || doc.Paths == nil {
		return
	}
	for _, item := range doc.Paths.Map() {
		if item == nil {
			continue
		}
		for _, op := range item.Operations() {
			op.Security = nil
		}
	}
	doc.Components.SecuritySchemes = nil
	doc.Security = nil
}

func normalizeFiberPathParams(doc *openapi3.T) {
	if doc == nil || doc.Paths == nil {
		return
	}
	rewrites := map[string]string{}
	for k := range doc.Paths.Map() {
		if !strings.Contains(k, ":") {
			continue
		}
		nk := fiberParamRE.ReplaceAllString(k, "{$1}")
		if nk != k {
			rewrites[k] = nk
		}
	}
	for old, fresh := range rewrites {
		item := doc.Paths.Value(old)
		doc.Paths.Delete(old)
		// If the OpenAPI-form key already has operations (because some
		// handlers use `{}` in @Router while others use `:`), merge
		// instead of overwriting — otherwise the second Set clobbers
		// whatever was there.
		if existing := doc.Paths.Value(fresh); existing != nil {
			mergePathItem(existing, item)
		} else {
			doc.Paths.Set(fresh, item)
		}
	}
}

// mergePathItem copies operations from src into dst without overwriting
// existing ones on dst. Per HTTP, only one operation per (path, method)
// is allowed; if both items define the same method we keep dst (assumed
// to be the OpenAPI-form, more recent).
func mergePathItem(dst, src *openapi3.PathItem) {
	if src.Get != nil && dst.Get == nil {
		dst.Get = src.Get
	}
	if src.Put != nil && dst.Put == nil {
		dst.Put = src.Put
	}
	if src.Post != nil && dst.Post == nil {
		dst.Post = src.Post
	}
	if src.Delete != nil && dst.Delete == nil {
		dst.Delete = src.Delete
	}
	if src.Patch != nil && dst.Patch == nil {
		dst.Patch = src.Patch
	}
	if src.Head != nil && dst.Head == nil {
		dst.Head = src.Head
	}
	if src.Options != nil && dst.Options == nil {
		dst.Options = src.Options
	}
	if src.Trace != nil && dst.Trace == nil {
		dst.Trace = src.Trace
	}
	dst.Parameters = append(dst.Parameters, src.Parameters...)
}
