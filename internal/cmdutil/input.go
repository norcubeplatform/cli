package cmdutil

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// ItemSource says where a command reads a list of items (domains, names,
// keys) from: positional arguments, a file (--file), or standard input
// ("-" as the only argument or --file -).
type ItemSource struct {
	Args []string
	// File is the --file value; "-" reads standard input.
	File string
	// Stdin is the command's input stream (cmd.InOrStdin()).
	Stdin io.Reader
	// Max caps how many items are accepted (0 = no cap); more is a usage
	// error naming the cap, so a script never sends a silently cut list.
	Max int
	// What names the items in messages ("domains").
	What string
}

// ReadItems returns the items of src: one per argument or per line, with
// surrounding space trimmed, blank lines and lines starting with "#"
// skipped, and duplicates removed (first occurrence wins, order kept).
// Arguments and a file can't be combined.
func ReadItems(src ItemSource) ([]string, error) {
	what := src.What
	if what == "" {
		what = "items"
	}
	args := src.Args
	file := src.File
	if len(args) == 1 && args[0] == "-" {
		args, file = nil, "-"
	}
	if len(args) > 0 && file != "" {
		return nil, clierr.Usage(fmt.Errorf("pass %s as arguments or with --file, not both", what))
	}

	var raw []string
	switch {
	case len(args) > 0:
		raw = args
	case file != "":
		r, closeFn, err := openInput(file, src.Stdin)
		if err != nil {
			return nil, err
		}
		defer closeFn()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			raw = append(raw, sc.Text())
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read %s: %w", inputName(file), err)
		}
	default:
		return nil, clierr.Usage(fmt.Errorf("no %s given: pass them as arguments, with --file <path>, or with --file - to read standard input", what))
	}

	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		item := strings.TrimSpace(line)
		if item == "" || strings.HasPrefix(item, "#") || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, clierr.Usage(fmt.Errorf("no %s found in %s", what, inputName(file)))
	}
	if src.Max > 0 && len(out) > src.Max {
		return nil, clierr.Usage(fmt.Errorf("%d %s given, at most %d per request: split the list", len(out), what, src.Max))
	}
	return out, nil
}

func openInput(file string, stdin io.Reader) (io.Reader, func(), error) {
	if file == "-" {
		if stdin == nil {
			return nil, nil, errors.New("no standard input to read from")
		}
		return stdin, func() {}, nil
	}
	f, err := os.Open(file) // #nosec G304 -- a path the user passed on purpose
	if err != nil {
		return nil, nil, clierr.Usage(err)
	}
	return f, func() { _ = f.Close() }, nil
}

func inputName(file string) string {
	switch file {
	case "":
		return "the arguments"
	case "-":
		return "standard input"
	default:
		return file
	}
}
