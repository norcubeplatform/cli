package snapdb

import (
	"fmt"
	"net/http"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
)

// apiError turns a non-2xx response from the snapdb client into an error
// message that's actually useful for debugging:
//
//   - Always includes the request method + URL so users can tell whether
//     they're pointing at prod vs a local backend.
//   - Prefers the typed JSON error bodies (JSON400 / JSON500) when present.
//   - Otherwise falls back to the raw response body (truncated), which is
//     the only thing that surfaces 5xx errors from upstream proxies / LBs.
func apiError(resp *http.Response, body []byte, e400, e500 *snapdb.ResponseAPIError) error {
	url := "<unknown url>"
	method := "?"
	if resp != nil && resp.Request != nil {
		url = resp.Request.URL.String()
		method = resp.Request.Method
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	if e400 != nil {
		return fmt.Errorf("snapdb %s %s: %s", method, url, formatTyped(*e400, status))
	}
	if e500 != nil {
		return fmt.Errorf("snapdb %s %s: %s", method, url, formatTyped(*e500, status))
	}

	msg := string(body)
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	if msg == "" {
		return fmt.Errorf("snapdb %s %s: status %d (empty body)", method, url, status)
	}
	return fmt.Errorf("snapdb %s %s: status %d: %s", method, url, status, msg)
}

func formatTyped(e snapdb.ResponseAPIError, status int) string {
	switch {
	case e.Msg != "" && e.Type != "":
		return fmt.Sprintf("%s (%s, %d)", e.Msg, e.Type, status)
	case e.Msg != "":
		return fmt.Sprintf("%s (%d)", e.Msg, status)
	default:
		return fmt.Sprintf("status %d", status)
	}
}
