package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/norcubeplatform/cli/internal/cmdutil"
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
		if c.Runnable() && !IsGroup(c) && c.Example == "" && c.Parent() != nil {
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
	"authcmd.go": "term.IsTerminal(cmd.OutOrStdout())", // only asks whether it is a terminal
	"config.go":  "os.Stdin, os.Stdout, os.Stderr",     // the user's editor runs on the terminal
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

// The command reference is generated from the help text, so every command
// a user can see has a one-line summary in sentence style and every flag
// says what it does. Removing a description fails here, not in the docs.
func TestHelpTextIsComplete(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		path := c.CommandPath()
		switch short := strings.TrimSpace(c.Short); {
		case short == "":
			t.Errorf("%s has no Short", path)
		case strings.HasSuffix(short, "."):
			t.Errorf("%s: Short ends with a period: %q", path, short)
		case strings.ToUpper(short[:1]) != short[:1]:
			t.Errorf("%s: Short starts in lowercase: %q", path, short)
		}
		c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden && strings.TrimSpace(f.Usage) == "" {
				t.Errorf("%s --%s has no description", path, f.Name)
			}
		})
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden && strings.TrimSpace(f.Usage) == "" {
				t.Errorf("%s --%s has no description", path, f.Name)
			}
		})
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(NewRootCmd())
}

// effectExempt are the commands whose effect depends on their arguments.
var effectExempt = map[string]string{
	"norcube api": "read or write depending on the HTTP method",
}

// Every command a user can run says what it changes (cmdutil.Reads,
// Writes or Local), so `norcube commands` can tell scripts and agents
// which commands are safe to run.
func TestEveryCommandDeclaresAnEffect(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Name() == "help" || c.Name() == "completion" {
			return
		}
		if c.Runnable() && !IsGroup(c) && c.Parent() != nil {
			switch e := cmdutil.CommandEffect(c); e {
			case cmdutil.EffectRead, cmdutil.EffectWrite, cmdutil.EffectLocal:
				if _, ok := effectExempt[c.CommandPath()]; ok {
					t.Errorf("%s declares an effect; remove it from effectExempt", c.CommandPath())
				}
			case "":
				if _, ok := effectExempt[c.CommandPath()]; !ok {
					t.Errorf("%s declares no effect: add Annotations: cmdutil.Reads(), Writes() or Local()", c.CommandPath())
				}
			default:
				t.Errorf("%s: unknown effect %q", c.CommandPath(), e)
			}
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(NewRootCmd())
}
