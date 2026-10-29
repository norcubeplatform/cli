package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/cmdutil"
)

// Renamed commands keep working under their old names, stay out of help,
// and only mention the rename when NORCUBE_DEPRECATION_WARNINGS=1.
func TestRenamedCommandsKeepWorking(t *testing.T) {
	for _, args := range [][]string{
		{"backup", "jobs", "list"},
		{"backup", "list"},
		{"snapdb", "list"},
	} {
		signIn(t)
		stdout, stderr, err := run(t, append(args, "-o", "json")...)
		if err != nil {
			t.Fatalf("%v: %v (%s)", args, err, stderr)
		}
		if strings.TrimSpace(stdout) != "[]" {
			t.Errorf("%v: stdout %q", args, stdout)
		}
		if strings.Contains(stderr, "Note:") {
			t.Errorf("%v: rename note without %s: %q", args, cmdutil.EnvDeprecationWarnings, stderr)
		}
	}

	t.Setenv(cmdutil.EnvDeprecationWarnings, "1")
	signIn(t)
	_, stderr, err := run(t, "backup", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "is now `norcube backup jobs list`") {
		t.Errorf("no rename note: %q", stderr)
	}
	_, stderr, _ = run(t, "backup", "jobs", "list", "-o", "json")
	if strings.Contains(stderr, "Note:") {
		t.Errorf("the new name printed a rename note: %q", stderr)
	}
}

func TestHelpShowsTheNewNames(t *testing.T) {
	help, _, err := run(t, "backup", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jobs", "datasources", "restore-tests", "policies"} {
		if !strings.Contains(help, "  "+want+" ") {
			t.Errorf("backup help lacks %q:\n%s", want, help)
		}
	}
	for _, old := range []string{"\n  list ", "\n  download "} {
		if strings.Contains(help, old) {
			t.Errorf("backup help still lists the old %q", strings.TrimSpace(old))
		}
	}
}

// `rm` must never resolve to a destructive command.
func TestRmAliasIsNotDestructive(t *testing.T) {
	root := NewRootCmd()
	cmd, _, err := root.Find([]string{"langsync", "languages", "rm"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "remove" {
		t.Errorf("`languages rm` runs %q, want remove", cmd.Name())
	}
	walkAliases(t, root)
}

// walkAliases fails when two siblings share a name or alias, which makes
// the command an alias resolves to depend on registration order.
func walkAliases(t *testing.T, parent *cobra.Command) {
	t.Helper()
	seen := map[string]string{}
	for _, c := range parent.Commands() {
		for _, n := range append([]string{c.Name()}, c.Aliases...) {
			if prev, ok := seen[n]; ok {
				t.Errorf("%s: %q is used by both %s and %s", parent.CommandPath(), n, prev, c.Name())
			}
			seen[n] = c.Name()
		}
		walkAliases(t, c)
	}
}
