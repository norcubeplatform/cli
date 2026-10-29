package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/norcubeplatform/cli/internal/cli"
)

// linkBase is the docs URL the reference is served under; links between
// pages are absolute so they work with and without trailing slashes.
var linkBase = "/platform/cli-reference"

func pageURL(c *cobra.Command) string {
	if name := mdxPage(c); name != "index" {
		return linkBase + "/" + name
	}
	return linkBase
}

// generatedNotice heads every generated page. The docs site keeps it as an
// MDX comment, so it never renders.
const generatedNotice = "{/* Generated from the CLI's help text by tools/gendocs. Do not edit: change the command in the CLI repository and run `make docs`. */}"

// mdxPage is the file name (without .mdx) of a command's reference page:
// the command path without the root, joined by dashes; "index" for the
// root.
func mdxPage(c *cobra.Command) string {
	parts := strings.Fields(c.CommandPath())[1:]
	if len(parts) == 0 {
		return "index"
	}
	return strings.Join(parts, "-")
}

// documented reports whether a command gets a page: everything a user can
// see in help, except cobra's own help command.
func documented(c *cobra.Command) bool {
	return !c.Hidden && c.Name() != "help"
}

// genMDX writes one MDX page per command plus a meta.json for the sidebar
// into dir, and removes pages of commands that no longer exist.
func genMDX(root *cobra.Command, dir, base string) error {
	linkBase = strings.TrimRight(base, "/")
	root.DisableAutoGenTag = true
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var pages []string
	var walk func(c *cobra.Command) error
	walk = func(c *cobra.Command) error {
		if !documented(c) {
			return nil
		}
		name := mdxPage(c)
		pages = append(pages, name)
		if err := os.WriteFile(filepath.Join(dir, name+".mdx"), []byte(renderMDX(c)), 0o644); err != nil { // #nosec G306 -- docs source
			return err
		}
		for _, sub := range c.Commands() {
			if err := walk(sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return err
	}

	// Pages of removed commands would otherwise linger in the docs.
	keep := map[string]bool{}
	for _, p := range pages {
		keep[p+".mdx"] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".mdx") && !keep[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}

	sort.Strings(pages[1:]) // index first, then alphabetical
	meta := map[string]any{
		"title":       "CLI reference",
		"description": "Every norcube command, generated from the CLI",
		"pages":       pages,
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), append(raw, '\n'), 0o644) // #nosec G306 -- docs source
}

func renderMDX(c *cobra.Command) string {
	var b bytes.Buffer
	title := c.CommandPath()
	fmt.Fprintf(&b, "---\ntitle: %s\ndescription: %s\n---\n\n%s\n\n", yamlString(title), yamlString(c.Short), generatedNotice)

	if long := strings.TrimSpace(c.Long); long != "" {
		b.WriteString(renderLong(long))
		b.WriteString("\n\n")
	} else if c.Short != "" {
		b.WriteString(escapeProse(c.Short) + ".\n\n")
	}

	b.WriteString("## Usage\n\n```bash\n")
	if c.Runnable() && !cli.IsGroup(c) {
		b.WriteString(c.UseLine() + "\n")
	}
	if c.HasAvailableSubCommands() {
		b.WriteString(c.CommandPath() + " <command> [flags]\n")
	}
	b.WriteString("```\n\n")

	if len(c.Aliases) > 0 {
		fmt.Fprintf(&b, "Also available as: %s.\n\n", codeList(c.Aliases))
	}

	if ex := strings.TrimRight(c.Example, " \n"); ex != "" {
		b.WriteString("## Examples\n\n```bash\n")
		for _, line := range strings.Split(ex, "\n") {
			b.WriteString(strings.TrimPrefix(line, "  ") + "\n")
		}
		b.WriteString("```\n\n")
	}

	var subs []*cobra.Command
	for _, s := range c.Commands() {
		if documented(s) {
			subs = append(subs, s)
		}
	}
	if len(subs) > 0 {
		b.WriteString("## Commands\n\n| Command | Description |\n| --- | --- |\n")
		for _, s := range subs {
			fmt.Fprintf(&b, "| [`%s`](%s) | %s |\n", s.Name(), pageURL(s), escapeCell(s.Short))
		}
		b.WriteString("\n")
	}

	local := c.LocalNonPersistentFlags()
	if c.HasParent() {
		local.AddFlagSet(c.PersistentFlags())
	}
	if t := flagTable(local); t != "" {
		b.WriteString("## Flags\n\n" + t + "\n")
	}
	if c.HasParent() && c.InheritedFlags().HasAvailableFlags() {
		fmt.Fprintf(&b, "Every command also takes the [global flags](%s#global-flags): `--profile`, `--org`, `-o`, `--query`, `--debug` and more.\n\n", linkBase)
	}
	if !c.HasParent() {
		if t := flagTable(c.PersistentFlags()); t != "" {
			b.WriteString("## Global flags\n\n" + t + "\n")
		}
	}

	if c.HasParent() {
		p := c.Parent()
		fmt.Fprintf(&b, "## See also\n\n- [`%s`](%s): %s\n", p.CommandPath(), pageURL(p), escapeCell(p.Short))
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderLong turns help text into Markdown: indented blocks (aligned key
// lists, command lines) become text code blocks so they keep their
// layout; everything else is prose.
func renderLong(long string) string {
	var out []string
	var block []string
	flush := func() {
		if len(block) == 0 {
			return
		}
		out = append(out, "```text")
		out = append(out, block...)
		out = append(out, "```")
		block = nil
	}
	for _, line := range strings.Split(long, "\n") {
		if strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "" {
			block = append(block, strings.TrimPrefix(line, "  "))
			continue
		}
		flush()
		out = append(out, escapeProse(line))
	}
	flush()
	return strings.Join(out, "\n")
}

func flagTable(fs *pflag.FlagSet) string {
	var rows []string
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		name := "`--" + f.Name + "`"
		if f.Shorthand != "" {
			name = "`-" + f.Shorthand + "`, " + name
		}
		def := ""
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "[]" && f.DefValue != "0" {
			def = "`" + f.DefValue + "`"
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %s |", name, def, escapeCell(f.Usage)))
	})
	if len(rows) == 0 {
		return ""
	}
	return "| Flag | Default | Description |\n| --- | --- | --- |\n" + strings.Join(rows, "\n") + "\n"
}

func codeList(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return strings.Join(out, ", ")
}

// escapeProse makes help text safe for MDX: outside inline code, `<`, `{`
// and `}` would start JSX or an expression.
func escapeProse(s string) string {
	var b strings.Builder
	inCode := false
	for _, r := range s {
		switch {
		case r == '`':
			inCode = !inCode
			b.WriteRune(r)
		case inCode:
			b.WriteRune(r)
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '{':
			b.WriteString("\\{")
		case r == '}':
			b.WriteString("\\}")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeCell also keeps a table cell on one line and its pipes literal.
func escapeCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(escapeProse(s), "|", "\\|")
}

func yamlString(s string) string {
	raw, _ := json.Marshal(s) // a JSON string is a valid YAML scalar
	return string(raw)
}
