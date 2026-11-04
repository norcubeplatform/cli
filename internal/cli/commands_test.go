package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

// commandsEnv is a profile without a session whose every endpoint points at
// a server that counts requests: `commands` must never reach it.
func commandsEnv(t *testing.T) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	isolatedConfig(t, func(f *config.File) {
		p := f.Profile(config.DefaultProfileName)
		p.Environment = config.EnvCustom
		p.Endpoints = map[string]string{}
		for _, key := range []string{config.ServiceAuth, config.ServiceSnapDB, config.ServiceLangsync, config.ServiceDomainradar, config.ServiceBilling, config.ServicePulsecheck} {
			p.Endpoints[key] = srv.URL
		}
	})
	t.Setenv("NORCUBE_TOKEN", "")
	t.Setenv("NORCUBE_NO_UPDATE_CHECK", "1")
	t.Cleanup(func() {
		if n := hits.Load(); n != 0 {
			t.Errorf("`commands` made %d requests; it must work offline", n)
		}
	})
}

func commandsJSON(t *testing.T, args ...string) commandTree {
	t.Helper()
	out, stderr, err := run(t, append([]string{"commands", "-o", "json"}, args...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr is not empty: %q", stderr)
	}
	var tree commandTree
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return tree
}

// The schema of a small, stable subtree is pinned in every format. A diff
// here is a change of the documented schema: additive only within
// schemaVersion 1.
func TestGoldenCommands(t *testing.T) {
	for format, ext := range goldenFormats {
		t.Run(format, func(t *testing.T) {
			commandsEnv(t)
			out, stderr, err := run(t, "commands", "billing", "-o", format, "--no-pager")
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			checkGolden(t, "commands-billing."+ext, out, "http://unused")
		})
	}
}

func TestCommandsSchemaVersion(t *testing.T) {
	commandsEnv(t)
	tree := commandsJSON(t)
	if tree.SchemaVersion != 1 {
		t.Errorf("schemaVersion %d, want 1", tree.SchemaVersion)
	}
	if tree.CLIVersion == "" || len(tree.GlobalFlags) == 0 || len(tree.OutputFormats) != 4 {
		t.Errorf("header incomplete: version %q, %d global flags, formats %v", tree.CLIVersion, len(tree.GlobalFlags), tree.OutputFormats)
	}
}

// visiblePaths walks a fresh tree the way help does: everything but hidden,
// deprecated and cobra's help command.
func visiblePaths(c *cobra.Command, out map[string]bool) {
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Deprecated != "" || sub.Name() == "help" {
			continue
		}
		out[sub.CommandPath()] = true
		visiblePaths(sub, out)
	}
}

// Every command help shows is in the tree, and nothing else is (apart from
// cobra's completion commands, which exist only once the CLI executes).
func TestCommandsListsEveryCommand(t *testing.T) {
	commandsEnv(t)
	want := map[string]bool{}
	visiblePaths(NewRootCmd(), want)
	got := map[string]bool{}
	for _, c := range commandsJSON(t).Commands {
		got[c.Command] = true
		if !want[c.Command] && c.Path[0] != "completion" {
			t.Errorf("%s is listed but not visible in help", c.Command)
		}
		if c.Hidden || c.RenamedTo != "" {
			t.Errorf("%s is hidden or renamed but listed without --all", c.Command)
		}
	}
	for path := range want {
		if !got[path] {
			t.Errorf("%s is missing from `commands`", path)
		}
	}
}

func TestCommandsAllIncludesHiddenAndRenamed(t *testing.T) {
	commandsEnv(t)
	find := func(tree commandTree, path string) *commandInfo {
		for i := range tree.Commands {
			if tree.Commands[i].Command == path {
				return &tree.Commands[i]
			}
		}
		return nil
	}
	if c := find(commandsJSON(t), "norcube backup list"); c != nil {
		t.Errorf("renamed command listed without --all: %+v", c)
	}
	c := find(commandsJSON(t, "--all"), "norcube backup list")
	if c == nil {
		t.Fatal("--all does not list the renamed `backup list`")
	}
	if !c.Hidden || c.RenamedTo != "norcube backup jobs list" {
		t.Errorf("hidden %v, renamedTo %q", c.Hidden, c.RenamedTo)
	}
	for _, c := range commandsJSON(t, "--all").Commands {
		if c.Name == "help" || strings.HasPrefix(c.Name, "__") {
			t.Errorf("%s: cobra internals are never listed", c.Command)
		}
	}
}

// Same tree, same bytes: sorted by path, flags by name.
func TestCommandsOutputIsDeterministic(t *testing.T) {
	commandsEnv(t)
	first, _, err := run(t, "commands", "-o", "json", "--all")
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := run(t, "commands", "-o", "json", "--all")
	if first != second {
		t.Error("two runs printed different documents")
	}
	tree := commandsJSON(t, "--all")
	if !slices.IsSortedFunc(tree.Commands, func(a, b commandInfo) int { return slices.Compare(a.Path, b.Path) }) {
		t.Error("commands are not sorted by path")
	}
	for _, c := range tree.Commands {
		if !slices.IsSortedFunc(c.Flags, func(a, b flagInfo) int { return strings.Compare(a.Name, b.Name) }) {
			t.Errorf("%s: flags are not sorted by name", c.Command)
		}
	}
}

func TestCommandsFilter(t *testing.T) {
	commandsEnv(t)
	for _, args := range [][]string{{"backup", "jobs"}, {"snapdb", "job"}, {"backup jobs"}} {
		tree := commandsJSON(t, args...)
		if len(tree.Commands) < 2 || tree.Commands[0].Command != "norcube backup jobs" {
			t.Fatalf("%v: %d commands, first %+v", args, len(tree.Commands), tree.Commands)
		}
		for _, c := range tree.Commands {
			if !slices.Equal(c.Path[:2], []string{"backup", "jobs"}) {
				t.Errorf("%v: %s is outside the subtree", args, c.Command)
			}
		}
	}
	_, _, err := run(t, "commands", "backup", "nope")
	if !clierr.IsUsage(err) {
		t.Errorf("unknown path: got %v, want a usage error", err)
	}
}

// What the tree derives from flags and annotations matches the commands.
func TestCommandsBehaviourFields(t *testing.T) {
	commandsEnv(t)
	byPath := map[string]commandInfo{}
	for _, c := range commandsJSON(t).Commands {
		byPath[c.Command] = c
	}
	checks := []struct {
		path                       string
		effect                     string
		confirm, wait, paginated   bool
		service, audience, enumFor string
	}{
		{path: "norcube backup jobs list", effect: "read", paginated: true, service: "snapdb", audience: "snapdb-api"},
		{path: "norcube backup jobs run", effect: "write", wait: true, service: "snapdb", audience: "snapdb-api"},
		{path: "norcube backup restores create", effect: "write", confirm: true, wait: true, service: "snapdb", audience: "snapdb-api", enumFor: "mode"},
		{path: "norcube billing budget set", effect: "write", confirm: true, service: "billing", audience: "billing"},
		{path: "norcube org use", effect: "local", service: "auth", audience: "auth"},
		{path: "norcube login", effect: "write", service: "auth", audience: "auth"},
		{path: "norcube config list", effect: "read"},
		{path: "norcube api"},
	}
	for _, want := range checks {
		c, ok := byPath[want.path]
		if !ok {
			t.Errorf("%s missing", want.path)
			continue
		}
		if c.Effect != want.effect || c.Confirm != want.confirm || c.Wait != want.wait || c.Paginated != want.paginated {
			t.Errorf("%s: effect %q confirm %v wait %v paginated %v", want.path, c.Effect, c.Confirm, c.Wait, c.Paginated)
		}
		var key, aud string
		if c.Service != nil {
			key, aud = c.Service.Key, c.Service.Audience
		}
		if key != want.service || aud != want.audience {
			t.Errorf("%s: service %q audience %q, want %q %q", want.path, key, aud, want.service, want.audience)
		}
		if want.enumFor != "" {
			i := slices.IndexFunc(c.Flags, func(f flagInfo) bool { return f.Name == want.enumFor })
			if i < 0 || len(c.Flags[i].Enum) == 0 {
				t.Errorf("%s --%s: no enum values", want.path, want.enumFor)
			}
		}
	}
	if c := byPath["norcube config edit"]; c.OutputFormats != nil {
		t.Errorf("config edit prints no result, but lists formats %v", c.OutputFormats)
	}
	if c := byPath["norcube backup jobs list"]; len(c.OutputFormats) != 4 {
		t.Errorf("backup jobs list formats %v", c.OutputFormats)
	}
	if c := byPath["norcube backup jobs list"]; slices.ContainsFunc(c.Flags, func(f flagInfo) bool { return f.Name == "output" || f.Name == "help" }) {
		t.Error("global flags and --help are repeated on a command")
	}
}

func TestCommandsQuery(t *testing.T) {
	commandsEnv(t)
	out, stderr, err := run(t, "commands", "billing", "-o", "text", "--query", "commands[?confirm].command")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if strings.TrimSpace(out) != "norcube billing budget set" {
		t.Errorf("got %q", out)
	}
	if _, _, err := run(t, "commands", "--query", "[?"); !clierr.IsUsage(err) {
		t.Errorf("invalid --query: got %v, want a usage error", err)
	}
}
