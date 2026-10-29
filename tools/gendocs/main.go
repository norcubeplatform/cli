// Command gendocs turns the CLI's help text into documentation, so the
// command tree is the only place flags and examples are written down.
//
// Without flags it writes shell completions and man pages for release
// archives; GoReleaser runs it as a before-hook (see .goreleaser.yaml) and
// the output directories are gitignored. With -mdx DIR it writes the
// command reference for the docs site instead (`make docs`).
package main

import (
	"flag"
	"log"
	"os"

	"github.com/spf13/cobra/doc"

	"github.com/norcubeplatform/cli/internal/cli"
)

func main() {
	mdxDir := flag.String("mdx", "", "write the MDX command reference into this directory instead")
	base := flag.String("base", "/platform/cli-reference", "docs URL the MDX reference is served under")
	flag.Parse()
	root := cli.NewRootCmd()

	if *mdxDir != "" {
		if err := genMDX(root, *mdxDir, *base); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := os.MkdirAll("completions", 0o755); err != nil {
		log.Fatal(err)
	}
	if err := root.GenBashCompletionFileV2("completions/norcube.bash", true); err != nil {
		log.Fatal(err)
	}
	if err := root.GenZshCompletionFile("completions/_norcube"); err != nil {
		log.Fatal(err)
	}
	if err := root.GenFishCompletionFile("completions/norcube.fish", true); err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll("manpages", 0o755); err != nil {
		log.Fatal(err)
	}
	header := &doc.GenManHeader{Title: "NORCUBE", Section: "1", Source: "Norcube CLI"}
	if err := doc.GenManTree(root, header, "manpages"); err != nil {
		log.Fatal(err)
	}
}
