// Command gendocs generates shell completions and man pages for release
// archives. GoReleaser runs it as a before-hook (see .goreleaser.yaml); the
// output directories are gitignored and rebuilt on every release.
package main

import (
	"log"
	"os"

	"github.com/spf13/cobra/doc"

	"github.com/norcubeplatform/cli/internal/cli"
)

func main() {
	root := cli.NewRootCmd()

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
