// specconv reads a service's API document and writes the OpenAPI 3.0 JSON
// that the pinned oapi-codegen generates clients from, on stdout.
//
// It accepts both formats the Norcube services publish: Swagger 2.0 (swag)
// is converted to OpenAPI 3.0, and OpenAPI 3.1 (huma) is brought down to
// 3.0 (downlevel.go), so every client is generated from the same input
// format. The same clean-ups apply to both, then the excluded tags are
// removed.
package main

import (
	"encoding/json"
	"flag"
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
	excludeTags := flag.String("exclude-tags", "", "comma-separated tags whose operations are left out (with the schemas only they use)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: specconv [-exclude-tags a,b] <swagger-2.0-or-openapi-3.x.json>")
		os.Exit(2)
	}

	in, err := os.Open(flag.Arg(0))
	must(err)
	defer in.Close()

	raw, err := io.ReadAll(in)
	must(err)

	v3, err := toOpenAPI30(raw)
	must(err)

	normalizeFiberPathParams(v3)
	stripOperationSecurity(v3)
	ensurePathParameters(v3)

	out, err := json.MarshalIndent(v3, "", "  ")
	must(err)
	if tags := splitList(*excludeTags); len(tags) > 0 {
		out, err = excludeOperations(out, tags)
		must(err)
	}

	_, err = os.Stdout.Write(out)
	must(err)
}

// toOpenAPI30 reads a Swagger 2.0, OpenAPI 3.0 or OpenAPI 3.1 document as
// OpenAPI 3.0.
func toOpenAPI30(raw []byte) (*openapi3.T, error) {
	var head struct {
		Swagger string `json:"swagger"`
		OpenAPI string `json:"openapi"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	switch {
	case head.Swagger == "2.0":
		var v2 openapi2.T
		if err := json.Unmarshal(raw, &v2); err != nil {
			return nil, err
		}
		return openapi2conv.ToV3(&v2)
	case strings.HasPrefix(head.OpenAPI, "3.0."), strings.HasPrefix(head.OpenAPI, "3.1."):
		down, err := downlevel(raw)
		if err != nil {
			return nil, err
		}
		// The loader resolves internal references (written back as $ref);
		// a service document has no business pointing at external files.
		loader := openapi3.NewLoader()
		loader.IsExternalRefsAllowed = false
		return loader.LoadFromData(down)
	default:
		return nil, fmt.Errorf("not a Swagger 2.0 or OpenAPI 3.0/3.1 document (swagger: %q, openapi: %q)", head.Swagger, head.OpenAPI)
	}
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
	fiberParamRE = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)
	pathParamRE  = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
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
