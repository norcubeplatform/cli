package cmdutil

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

func TestReadItems(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "domains.txt")
	if err := os.WriteFile(file, []byte("# list\nb.com\n\n  a.com  \nb.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		src  ItemSource
		want []string
	}{
		{"arguments", ItemSource{Args: []string{"a.com", "b.com", "a.com"}}, []string{"a.com", "b.com"}},
		{"file", ItemSource{File: file}, []string{"b.com", "a.com"}},
		{"stdin via --file -", ItemSource{File: "-", Stdin: strings.NewReader("x.io\ny.io\n")}, []string{"x.io", "y.io"}},
		{"stdin via a single -", ItemSource{Args: []string{"-"}, Stdin: strings.NewReader("z.dev\n")}, []string{"z.dev"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadItems(c.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestReadItemsRefusesBadInput(t *testing.T) {
	cases := []struct {
		name string
		src  ItemSource
	}{
		{"nothing", ItemSource{}},
		{"arguments and file", ItemSource{Args: []string{"a.com"}, File: "x"}},
		{"only comments", ItemSource{File: "-", Stdin: strings.NewReader("# nothing\n\n")}},
		{"over the cap", ItemSource{Args: []string{"a", "b", "c"}, Max: 2}},
		{"missing file", ItemSource{File: filepath.Join(t.TempDir(), "nope")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ReadItems(c.src); !clierr.IsUsage(err) {
				t.Fatalf("got %v, want a usage error", err)
			}
		})
	}
}
