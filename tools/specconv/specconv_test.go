package main

import (
	"context"
	"encoding/json"
	"testing"
)

// A small service document as huma writes it (OpenAPI 3.1).
const humaDoc = `{
  "openapi": "3.1.0",
  "info": {"title": "Example", "version": "1.0.0"},
  "jsonSchemaDialect": "https://spec.openapis.org/oas/3.1/dialect/base",
  "paths": {
    "/widgets/{id}": {
      "get": {
        "operationId": "getWidget",
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string", "format": "uuid"}}],
        "responses": {
          "200": {"description": "OK", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}},
          "default": {"description": "Error", "content": {"application/problem+json": {"schema": {"$ref": "#/components/schemas/ErrorModel"}}}}
        },
        "security": [{"bearer": []}]
      }
    }
  },
  "components": {
    "securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}},
    "schemas": {
      "Widget": {
        "type": "object", "additionalProperties": false,
        "required": ["$schema", "id", "kind"],
        "properties": {
          "$schema": {"type": "string", "format": "uri", "readOnly": true},
          "id": {"type": "string", "examples": ["w-1"]},
          "kind": {"const": "widget"},
          "note": {"type": ["string", "null"]},
          "score": {"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 10}
        }
      },
      "ErrorModel": {"type": "object", "properties": {"title": {"type": "string"}}}
    }
  }
}`

func TestOpenAPI31IsBroughtDownTo30(t *testing.T) {
	v3, err := toOpenAPI30([]byte(humaDoc))
	if err != nil {
		t.Fatal(err)
	}
	stripOperationSecurity(v3)
	ensurePathParameters(v3)

	if v3.OpenAPI != "3.0.3" {
		t.Errorf("openapi = %q, want 3.0.3", v3.OpenAPI)
	}
	// The result must be a valid 3.0 document: what oapi-codegen reads.
	if err := v3.Validate(context.Background()); err != nil {
		t.Fatalf("not a valid OpenAPI 3.0 document: %v", err)
	}

	w := v3.Components.Schemas["Widget"].Value
	if _, ok := w.Properties["$schema"]; ok {
		t.Error("huma's read-only $schema property was kept")
	}
	if len(w.Required) != 2 {
		t.Errorf("required = %v, want [id kind]", w.Required)
	}
	if note := w.Properties["note"].Value; !note.Type.Is("string") || !note.Nullable {
		t.Errorf("note: type %v nullable %v, want string, nullable", note.Type, note.Nullable)
	}
	if kind := w.Properties["kind"].Value; len(kind.Enum) != 1 || kind.Enum[0] != "widget" {
		t.Errorf("kind enum = %v, want [widget]", kind.Enum)
	}
	if id := w.Properties["id"].Value; id.Example != "w-1" {
		t.Errorf("id example = %v, want w-1", id.Example)
	}
	score := w.Properties["score"].Value
	// 3.0 form: the bound in minimum/maximum, exclusiveness as a boolean.
	if score.Min == nil || *score.Min != 0 || score.ExclusiveMin.Bool == nil || !*score.ExclusiveMin.Bool || score.ExclusiveMin.Value != nil ||
		score.Max == nil || *score.Max != 10 || score.ExclusiveMax.Bool == nil || !*score.ExclusiveMax.Bool || score.ExclusiveMax.Value != nil {
		t.Errorf("score bounds: min %v excl %+v, max %v excl %+v", score.Min, score.ExclusiveMin, score.Max, score.ExclusiveMax)
	}
}

func TestSwagger20StillConverts(t *testing.T) {
	doc := `{"swagger": "2.0", "info": {"title": "E", "version": "1"}, "basePath": "/app/v1",
	  "paths": {"/things/:id": {"get": {"operationId": "getThing", "produces": ["application/json"],
	    "parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
	    "responses": {"200": {"description": "OK", "schema": {"$ref": "#/definitions/Thing"}}}}}},
	  "definitions": {"Thing": {"type": "object", "properties": {"id": {"type": "string"}}}}}`
	v3, err := toOpenAPI30([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	normalizeFiberPathParams(v3)
	if v3.Paths.Value("/things/{id}") == nil {
		t.Errorf("Fiber path not normalized; paths: %v", v3.Paths.InMatchingOrder())
	}
	if err := v3.Validate(context.Background()); err != nil {
		t.Fatalf("not a valid OpenAPI 3.0 document: %v", err)
	}
}

func TestRefusesUnknownDocuments(t *testing.T) {
	for _, doc := range []string{`{"openapi": "2.5.0"}`, `{"paths": {}}`, `not json`} {
		if _, err := toOpenAPI30([]byte(doc)); err == nil {
			t.Errorf("%s was accepted", doc)
		}
	}
}

// Media-type "examples" maps are 3.0 constructs and survive.
func TestMediaExamplesMapIsKept(t *testing.T) {
	doc := `{"openapi": "3.1.0", "info": {"title": "E", "version": "1"}, "paths": {"/a": {"get": {"operationId": "a",
	  "responses": {"200": {"description": "OK", "content": {"application/json": {
	    "schema": {"type": "string"}, "examples": {"one": {"value": "x"}}}}}}}}}}`
	out, err := downlevel([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	media := v["paths"].(map[string]any)["/a"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)
	if _, ok := media["examples"].(map[string]any); !ok {
		t.Errorf("media examples map was changed: %v", media["examples"])
	}
}
