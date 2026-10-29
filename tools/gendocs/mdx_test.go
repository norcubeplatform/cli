package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/cli"
)

func generate(t *testing.T, dir string) map[string]string {
	t.Helper()
	if err := genMDX(cli.NewRootCmd(), dir, "/platform/cli-reference"); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	return files
}

func TestEveryVisibleCommandHasAPage(t *testing.T) {
	dir := t.TempDir()
	files := generate(t, dir)
	var meta struct{ Pages []string }
	if err := json.Unmarshal([]byte(files["meta.json"]), &meta); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, p := range meta.Pages {
		listed[p] = true
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" {
			if _, ok := files[mdxPage(c)+".mdx"]; ok && c.Hidden {
				t.Errorf("hidden %s has a page", c.CommandPath())
			}
			return
		}
		page := mdxPage(c)
		body, ok := files[page+".mdx"]
		if !ok || !listed[page] {
			t.Errorf("%s: no page or not in meta.json", c.CommandPath())
		}
		if !strings.Contains(body, generatedNotice) {
			t.Errorf("%s: no generated notice", page)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cli.NewRootCmd())
	if len(meta.Pages) != len(files)-1 {
		t.Errorf("meta lists %d pages, %d files written", len(meta.Pages), len(files)-1)
	}
	if meta.Pages[0] != "index" {
		t.Errorf("first page %q", meta.Pages[0])
	}
}

func TestGenerationIsDeterministicAndPrunes(t *testing.T) {
	dir := t.TempDir()
	first := generate(t, dir)
	stale := filepath.Join(dir, "removed-command.mdx")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := generate(t, dir)
	if len(first) != len(second) {
		t.Fatalf("%d files, then %d", len(first), len(second))
	}
	for name, body := range first {
		if second[name] != body {
			t.Errorf("%s changed between two runs", name)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the page of a removed command was kept")
	}
}

func TestEscapeProse(t *testing.T) {
	got := escapeProse("Use <id> or {lang}; keep `<code> {x}` as is")
	want := "Use &lt;id&gt; or \\{lang\\}; keep `<code> {x}` as is"
	if got != want {
		t.Errorf("got %q", got)
	}
}
