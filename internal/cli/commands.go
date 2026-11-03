package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/norcubeplatform/cli/internal/buildinfo"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/registry"
)

// commandsSchemaVersion is the version of the `norcube commands` document.
// Within a version changes are additive only: new fields, new values of an
// enumerated field (such as effect), new commands. Removing or renaming a
// field, or changing its type or meaning, needs a new version.
const commandsSchemaVersion = 1

// commandTree is what `norcube commands -o json` prints.
type commandTree struct {
	SchemaVersion int    `json:"schemaVersion"`
	CLIVersion    string `json:"cliVersion"`
	// OutputFormats are the values of -o, which every command's result
	// supports (DESIGN.md P3).
	OutputFormats []string `json:"outputFormats"`
	// GlobalFlags are the root's flags, which every command inherits.
	// They are listed once here instead of on every command.
	GlobalFlags []flagInfo    `json:"globalFlags"`
	Commands    []commandInfo `json:"commands"`
}

type commandInfo struct {
	// Command is the full invocation, Path the same without the binary
	// name ("norcube backup jobs list", ["backup", "jobs", "list"]).
	Command string   `json:"command"`
	Path    []string `json:"path"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Short   string   `json:"short"`
	Long    string   `json:"long,omitempty"`
	// Usage is the usage line; Args the part of it after the command name
	// ("<datasource-id> [job-id]").
	Usage   string `json:"usage"`
	Args    string `json:"args,omitempty"`
	Example string `json:"example,omitempty"`
	// Group commands only hold subcommands; running one shows its help.
	Group bool `json:"group"`
	// Hidden, RenamedTo and Deprecated appear only with --all (or when the
	// command was named in the filter).
	Hidden     bool   `json:"hidden,omitempty"`
	RenamedTo  string `json:"renamedTo,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
	// Service is the Norcube service the command talks to; absent for
	// commands that are local or reach several services.
	Service *serviceInfo `json:"service,omitempty"`
	// Effect is read, write or local (cmdutil.Effect); absent when it
	// depends on the arguments (`api`) or for groups.
	Effect string `json:"effect,omitempty"`
	// Confirm: the command asks before it acts on a terminal and needs
	// --yes without one. Wait: it takes --wait. Paginated: it takes
	// --cursor, --all-pages and --max-items.
	Confirm   bool `json:"confirm"`
	Wait      bool `json:"wait"`
	Paginated bool `json:"paginated"`
	// OutputFormats are the -o values that shape the result; absent for
	// groups, cobra's completion scripts and commands that print no
	// result.
	OutputFormats []string `json:"outputFormats,omitempty"`
	// Flags are the command's own flags; InheritedFlags those it inherits
	// from a parent other than the root (the root's are GlobalFlags).
	Flags          []flagInfo `json:"flags"`
	InheritedFlags []flagInfo `json:"inheritedFlags,omitempty"`
}

type serviceInfo struct {
	// Key is the registry key (config endpoints, --endpoint-url).
	Key  string `json:"key"`
	Name string `json:"name"`
	// Audience is the OAuth audience of the access tokens it accepts.
	Audience string `json:"audience,omitempty"`
}

type flagInfo struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	// Type is the flag's value type as the flag library names it
	// (string, bool, int, duration, stringArray, ...).
	Type        string   `json:"type"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Repeatable  bool     `json:"repeatable"`
	Enum        []string `json:"enum,omitempty"`
	Hidden      bool     `json:"hidden,omitempty"`
	Deprecated  string   `json:"deprecated,omitempty"`
}

var allOutputFormats = []string{output.FormatTable, output.FormatJSON, output.FormatYAML, output.FormatText}

func newCommandsCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "commands [command path]",
		Short: "List every command, its flags and behaviour, for people, scripts and agents",
		Long: `Prints the CLI's whole command tree without signing in or using the
network. A table lists each command with its summary; -o json or -o yaml
prints a versioned document with everything the help pages say plus what a
script or AI agent needs to plan a call: flags with types, defaults and
allowed values, whether the command changes anything (effect), asks for
confirmation (--yes), can wait for a job (--wait) or pages (--cursor), and
which service and OAuth audience it uses.

Give a command path to narrow the tree to it and the commands below it.
Hidden commands and old names of renamed commands are left out unless
--all is given. The document's schemaVersion only changes for a breaking
change; within a version, fields and commands are only ever added.`,
		Example: `  norcube commands
  norcube commands backup
  norcube commands -o json
  norcube commands backup jobs -o json --query "commands[?effect=='write'].command"
  norcube commands -o json --query 'commands[?confirm].command'`,
		Annotations: cmdutil.Reads(),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			start, err := findCommandPath(cmd.Root(), args)
			if err != nil {
				return nil, err
			}
			tree := buildCommandTree(start, all)
			return output.View[commandInfo]{
				Value: tree,
				Table: output.Table[commandInfo]{
					Headers: []string{"COMMAND", "DESCRIPTION"},
					Rows: func(c commandInfo) []string {
						desc := c.Short
						if c.RenamedTo != "" {
							desc = "Renamed to " + c.RenamedTo
						}
						return []string{c.Command, desc}
					},
					Items: tree.Commands,
					Style: output.NoStyle,
				},
			}, nil
		}),
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include hidden commands and the old names of renamed commands")
	return cmd
}

// findCommandPath resolves the words of a command path (names or
// aliases) from root. Each argument may hold several words.
func findCommandPath(root *cobra.Command, args []string) (*cobra.Command, error) {
	c := root
	for _, word := range strings.Fields(strings.Join(args, " ")) {
		next := childNamed(c, word)
		if next == nil {
			msg := fmt.Sprintf("no command %q under %q", word, c.CommandPath())
			if s := c.SuggestionsFor(word); len(s) > 0 {
				msg += "; did you mean " + strings.Join(s, ", ") + "?"
			}
			return nil, clierr.Usage(fmt.Errorf("%s (run `%s commands` for the whole tree)", msg, root.Name()))
		}
		c = next
	}
	return c, nil
}

func childNamed(c *cobra.Command, word string) *cobra.Command {
	for _, sub := range c.Commands() {
		if listedCommand(sub, true) && (sub.Name() == word || sub.HasAlias(word)) {
			return sub
		}
	}
	return nil
}

// listedCommand reports whether c belongs in the tree: never cobra's help
// command or its internal completion entry points, hidden and deprecated
// commands only with all.
func listedCommand(c *cobra.Command, all bool) bool {
	if c.Name() == "help" || strings.HasPrefix(c.Name(), "__") {
		return false
	}
	return all || (!c.Hidden && c.Deprecated == "")
}

// buildCommandTree describes start and every listed command below it (the
// root itself is described by globalFlags, not as a command), sorted by
// path so the output is the same on every run.
func buildCommandTree(start *cobra.Command, all bool) commandTree {
	root := start.Root()
	tree := commandTree{
		SchemaVersion: commandsSchemaVersion,
		CLIVersion:    buildinfo.Version,
		OutputFormats: allOutputFormats,
		GlobalFlags:   describeFlags(root.PersistentFlags(), nil, all),
		Commands:      []commandInfo{},
	}
	global := map[string]bool{}
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) { global[f.Name] = true })

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != root {
			tree.Commands = append(tree.Commands, describeCommand(c, global, all))
		}
		for _, sub := range c.Commands() {
			if listedCommand(sub, all) {
				walk(sub)
			}
		}
	}
	walk(start)
	slices.SortFunc(tree.Commands, func(a, b commandInfo) int { return slices.Compare(a.Path, b.Path) })
	return tree
}

func describeCommand(c *cobra.Command, global map[string]bool, all bool) commandInfo {
	path := strings.Fields(c.CommandPath())[1:]
	group := IsGroup(c) || !c.Runnable()
	info := commandInfo{
		Command:    c.CommandPath(),
		Path:       path,
		Name:       c.Name(),
		Aliases:    c.Aliases,
		Short:      c.Short,
		Long:       strings.TrimSpace(c.Long),
		Usage:      c.UseLine(),
		Example:    dedent(c.Example),
		Group:      group,
		Hidden:     c.Hidden,
		RenamedTo:  c.Annotations[cmdutil.AnnotationRenamedTo],
		Deprecated: c.Deprecated,
		Service:    commandService(c),
		Effect:     string(cmdutil.CommandEffect(c)),
		Flags:      describeFlags(c.LocalFlags(), nil, all),
	}
	if _, args, ok := strings.Cut(c.Use, " "); ok {
		info.Args = strings.TrimSpace(args)
	}
	if group {
		info.Usage = c.CommandPath() + " <command>"
	}
	info.InheritedFlags = describeFlags(c.InheritedFlags(), global, all)
	if len(info.InheritedFlags) == 0 {
		info.InheritedFlags = nil
	}
	has := func(name string) bool { return c.Flags().Lookup(name) != nil }
	info.Confirm = has("yes")
	info.Wait = has("wait")
	info.Paginated = has("cursor") && has("all-pages")
	if !group && !fromCobraCompletion(c) && c.Annotations[cmdutil.AnnotationNoResult] == "" {
		info.OutputFormats = allOutputFormats
	}
	return info
}

// fromCobraCompletion reports whether c is cobra's own `completion`
// command or one of its subcommands, which print shell scripts.
func fromCobraCompletion(c *cobra.Command) bool {
	for ; c.HasParent(); c = c.Parent() {
		if c.Name() == "completion" && !c.Parent().HasParent() {
			return true
		}
	}
	return false
}

// commandService is the service a command talks to: an explicit
// cmdutil.AnnotationService on it or a parent, else the registry entry
// whose command group the command belongs to.
func commandService(c *cobra.Command) *serviceInfo {
	var top *cobra.Command
	for p := c; p.HasParent(); p = p.Parent() {
		if key := p.Annotations[cmdutil.AnnotationService]; key != "" {
			if s, ok := registry.Get(key); ok {
				return &serviceInfo{Key: s.Key, Name: s.Name, Audience: s.Audience}
			}
			return nil
		}
		top = p
	}
	if top == nil {
		return nil
	}
	for _, s := range registry.All() {
		if s.Command != "" && s.Command == top.Name() {
			return &serviceInfo{Key: s.Key, Name: s.Name, Audience: s.Audience}
		}
	}
	return nil
}

// describeFlags lists the flags of fs in name order, leaving out --help
// (every command has it), the names in skip, and hidden or deprecated
// flags unless all.
func describeFlags(fs *pflag.FlagSet, skip map[string]bool, all bool) []flagInfo {
	out := []flagInfo{}
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Name == "help" || skip[f.Name] || (!all && (f.Hidden || f.Deprecated != "")) {
			return
		}
		typ := f.Value.Type()
		info := flagInfo{
			Name:        f.Name,
			Shorthand:   f.Shorthand,
			Type:        typ,
			Description: f.Usage,
			Required:    slices.Contains(f.Annotations[cobra.BashCompOneRequiredFlag], "true"),
			Repeatable:  strings.HasSuffix(typ, "Array") || strings.HasSuffix(typ, "Slice") || typ == "count",
			Enum:        cmdutil.EnumValues(f),
			Hidden:      f.Hidden,
			Deprecated:  f.Deprecated,
		}
		if f.DefValue != "[]" {
			info.Default = f.DefValue
		}
		out = append(out, info)
	})
	return out
}

// dedent removes the indentation every line of an example shares (help
// output indents examples by two spaces).
func dedent(s string) string {
	lines := strings.Split(strings.TrimRight(s, " \n"), "\n")
	indent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for i, l := range lines {
		if len(l) >= indent && indent > 0 {
			lines[i] = l[indent:]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
