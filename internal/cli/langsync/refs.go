package langsync

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// parseTermID reads a term id argument (terms have numeric ids).
func parseTermID(s string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || id <= 0 {
		return 0, clierr.Usage(fmt.Errorf("term id must be a positive integer (got %q)", s))
	}
	return id, nil
}

// promptRef reads a --prompt / --default-prompt value: a custom prompt
// id, or "none" (the empty id, which clears a namespace's default).
func promptRef(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.EqualFold(s, "none"):
		return "", nil
	case s == "":
		return "", clierr.Usage(errors.New(`prompt id must not be empty (use "none" to remove the default prompt)`))
	}
	return s, nil
}

// optionalPrompt is promptRef for flags where "none" makes no sense: an
// unset flag sends nothing, so the namespace's default prompt applies.
func optionalPrompt(changed bool, s string) (*string, error) {
	if !changed {
		return nil, nil
	}
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "none") {
		return nil, clierr.Usage(errors.New("--prompt needs a custom prompt id (leave it out to use the namespace's default prompt)"))
	}
	return &s, nil
}

// maxTextInput caps text read from a file or stdin (a prompt or a
// translation), well above anything the API accepts.
const maxTextInput = 1 << 20

// readText returns value, or the contents of path when path is set ("-"
// is stdin). A single trailing newline is dropped, so `echo ... |` works.
func readText(stdin io.Reader, value, path, what string) (string, error) {
	if path == "" {
		return value, nil
	}
	r := stdin
	if path != "-" {
		f, err := os.Open(path) // #nosec G304 -- the user names the file
		if err != nil {
			return "", clierr.Usage(fmt.Errorf("read %s: %w", what, err))
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxTextInput+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", what, err)
	}
	if len(b) > maxTextInput {
		return "", clierr.Usage(fmt.Errorf("%s is larger than %d bytes", what, maxTextInput))
	}
	s := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	return s, nil
}
