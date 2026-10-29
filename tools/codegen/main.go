// codegen regenerates the typed API clients listed in
// internal/registry/services.yaml: for every service with a `package`, it
// converts the Swagger 2.0 spec from the backend monorepo to OpenAPI 3
// (specs/<package>.openapi.json) and runs a pinned oapi-codegen with
// internal/api/<package>/oapi-codegen.yaml.
//
// Usage: go run ./tools/codegen -mono ../backend-mono [-only snapdb,langsync]
//
// -mono is the backend monorepo checkout, or any directory with the same
// apps/<app>/docs/<file> layout (a published spec artifact).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/norcubeplatform/cli/internal/registry"
)

// oapiCodegen is pinned so every contributor and CI generate the same code.
// Bump it deliberately and regenerate every client in the same commit.
const oapiCodegen = "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.0"

func main() {
	mono := flag.String("mono", "../backend-mono", "backend monorepo (or spec artifact) directory")
	only := flag.String("only", "", "comma-separated packages to regenerate (default: all)")
	list := flag.Bool("list", false, "print the packages and their spec paths, then exit")
	flag.Parse()

	want := map[string]bool{}
	for _, p := range strings.Split(*only, ",") {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}

	failed := false
	for _, s := range registry.All() {
		if s.Package == "" || (len(want) > 0 && !want[s.Package]) {
			continue
		}
		if *list {
			fmt.Printf("%s\t%s\n", s.Package, s.Spec.Path())
			continue
		}
		if err := generate(*mono, s); err != nil {
			fmt.Fprintf(os.Stderr, "codegen %s: %v\n", s.Package, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func generate(mono string, s registry.Service) error {
	spec := filepath.Join(mono, filepath.FromSlash(s.Spec.Path()))
	if _, err := os.Stat(spec); err != nil {
		return fmt.Errorf("spec not found (set MONO to the backend monorepo or a spec artifact): %w", err)
	}
	cfg := filepath.Join("internal", "api", s.Package, "oapi-codegen.yaml")
	if _, err := os.Stat(cfg); err != nil {
		return fmt.Errorf("missing %s: copy it from another package and adjust package and output", cfg)
	}

	fmt.Fprintf(os.Stderr, ">> %s: %s → specs/%s.openapi.json\n", s.Package, s.Spec.Path(), s.Package)
	excluded, err := excludedTags(cfg)
	if err != nil {
		return err
	}
	var converted bytes.Buffer
	conv := exec.Command("go", "run", "./tools/swagger2openapi", "-exclude-tags", strings.Join(excluded, ","), spec) // #nosec G204 -- paths from the embedded manifest
	conv.Stdout, conv.Stderr = &converted, os.Stderr
	if err := conv.Run(); err != nil {
		return fmt.Errorf("convert: %w", err)
	}
	out := filepath.Join("specs", s.Package+".openapi.json")
	if err := os.WriteFile(out, converted.Bytes(), 0o644); err != nil { // #nosec G306 -- a checked-in source file
		return err
	}

	fmt.Fprintf(os.Stderr, ">> %s: generating client\n", s.Package)
	gen := exec.Command("go", "run", oapiCodegen, "--config", cfg, out) // #nosec G204 -- pinned tool, paths from the manifest
	gen.Stdout, gen.Stderr = os.Stderr, os.Stderr
	if err := gen.Run(); err != nil {
		return fmt.Errorf("oapi-codegen: %w", err)
	}
	return nil
}

// excludedTags reads output-options.exclude-tags from a package's
// oapi-codegen.yaml: the converter leaves those operations out of the
// checked-in spec too, not only out of the generated client.
func excludedTags(cfgPath string) ([]string, error) {
	raw, err := os.ReadFile(cfgPath) // #nosec G304 -- a path inside the repository
	if err != nil {
		return nil, err
	}
	var cfg struct {
		OutputOptions struct {
			ExcludeTags []string `yaml:"exclude-tags"`
		} `yaml:"output-options"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", cfgPath, err)
	}
	return cfg.OutputOptions.ExcludeTags, nil
}
