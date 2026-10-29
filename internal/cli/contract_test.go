package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every command a user can run documents itself with an example.
func TestEveryCommandHasAnExample(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		// Command groups only answer with help (unknownSubcommand); the
		// commands under them carry the examples.
		group := c.RunE != nil && reflect.ValueOf(c.RunE).Pointer() == reflect.ValueOf(unknownSubcommand).Pointer()
		if c.Runnable() && !group && c.Example == "" && c.Parent() != nil {
			t.Errorf("%s has no Example", c.CommandPath())
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(NewRootCmd())
}

// stdout carries a command's result only, so every command's output goes
// through cmdutil.Print, which honors -o and --query. These are the
// places allowed to touch stdout directly.
var stdoutAllowed = map[string]string{
	"authcmd.go":       "term.IsTerminal(cmd.OutOrStdout())",    // only asks whether it is a terminal
	"config.go":        "os.Stdin, os.Stdout, os.Stderr",        // the user's editor runs on the terminal
	"backup/backup.go": "io.Copy(cmd.OutOrStdout(), resp.Body)", // download --file - streams the artifact
}

var stdoutWrite = regexp.MustCompile(`OutOrStdout\(\)|os\.Stdout|fmt\.Print(f|ln)?\(`)

func TestNoDirectStdoutWrites(t *testing.T) {
	src := os.DirFS(".")
	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !stdoutWrite.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if allowed, ok := stdoutAllowed[filepath.ToSlash(path)]; ok && strings.Contains(line, allowed) {
				continue
			}
			t.Errorf("%s:%d writes to stdout directly; return the value through cmdutil.Print:\n\t%s", path, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
