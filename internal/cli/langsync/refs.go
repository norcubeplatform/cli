package langsync

import (
	"errors"
	"fmt"
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
