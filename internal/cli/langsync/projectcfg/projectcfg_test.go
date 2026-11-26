package projectcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProject writes a .langsync.json with one namespace whose dir is
// "locales" and whose context_file is contextFile, and returns its path.
func writeProject(t *testing.T, contextFile string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "locales", "context"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":1,"namespaces":[{"namespace":"web","dir":"locales","format":"flat-json","default_local_language":"en","context_file":` +
		quote(contextFile) + `}]}`
	path := filepath.Join(root, Filename)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func TestLoadRefusesAContextFileTheLanguageScanWouldRead(t *testing.T) {
	path := writeProject(t, "locales/context.json")
	_, err := Load(path)
	want := filepath.Join(filepath.Dir(path), "locales", "context.json")
	if err == nil || !strings.Contains(err.Error(), "context_file "+want) || !strings.Contains(err.Error(), "read as a language") {
		t.Fatalf("got %v, want a refusal naming %s", err, want)
	}

	// The same directory reached another way is still the same directory.
	root := filepath.Dir(path)
	if err := os.Symlink(filepath.Join(root, "locales"), filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks: %v", err)
	}
	cfg := `{"version":1,"namespaces":[{"namespace":"web","dir":"locales","format":"flat-json","default_local_language":"en","context_file":"alias/en-context.json"}]}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("a symlinked dir should be refused too, got %v", err)
	}
}

func TestLoadAcceptsContextFilesTheScanSkips(t *testing.T) {
	for _, cf := range []string{"locales/context/en.json", "locales/.context.json", "locales/context.txt", "docs/keys.json"} {
		path := writeProject(t, cf)
		f, err := Load(path)
		if err != nil {
			t.Fatalf("%s: %v", cf, err)
		}
		want := filepath.Join(filepath.Dir(path), filepath.FromSlash(cf))
		if got := f.ResolveContextFile(path, f.Namespaces[0]); got != want {
			t.Fatalf("%s resolved to %s, want %s", cf, got, want)
		}
	}
}

func TestLoadRefusesABlankContextFile(t *testing.T) {
	if _, err := Load(writeProject(t, "  ")); err == nil || !strings.Contains(err.Error(), "context_file must not be blank") {
		t.Fatalf("got %v", err)
	}
}

func TestReadContexts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	missing := filepath.Join(dir, "nope.json")
	if _, err := ReadContexts(missing); err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing file: %v", err)
	}

	for name, body := range map[string]string{
		"broken.json":   `{"a": `,
		"number.json":   `{"a": 1}`,
		"nested.json":   `{"a": {"b": "c"}}`,
		"array.json":    `["a"]`,
		"list-val.json": `{"a": [1]}`,
	} {
		p := write(name, body)
		if _, err := ReadContexts(p); err == nil || !strings.Contains(err.Error(), p) {
			t.Fatalf("%s: got %v, want an error naming the file", name, err)
		}
	}

	// 1,000 code points is the most, counted as characters, not bytes.
	atLimit := strings.Repeat("č", MaxContextChars)
	tooLong := strings.Repeat("a", MaxContextChars+1)
	p := write("ok.json", `{"title": "`+atLimit+`", "cleared": ""}`)
	got, err := ReadContexts(p)
	if err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	if got["title"] != atLimit || got["cleared"] != "" || len(got) != 2 {
		t.Fatalf("read %v", got)
	}
	p = write("long.json", `{"ok": "fine", "checkout.total": "`+tooLong+`"}`)
	if _, err := ReadContexts(p); err == nil || !strings.Contains(err.Error(), `"checkout.total"`) || !strings.Contains(err.Error(), p) {
		t.Fatalf("too long: %v", err)
	}
}
