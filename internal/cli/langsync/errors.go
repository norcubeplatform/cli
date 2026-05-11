package langsync

import (
	"fmt"
	"net/http"

	"github.com/norcubeplatform/cli/internal/api/langsync"
)

// apiError turns a non-2xx response into an error message that surfaces
// the URL, status, and either a typed JSON error body or the raw body
// when no typed shape matched. Mirrors snapdb's helper of the same name.
func apiError(resp *http.Response, body []byte, typed ...*langsync.ResponseAPIError) error {
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

	for _, t := range typed {
		if t != nil {
			return fmt.Errorf("langsync %s %s: %s", method, url, formatTyped(*t, status))
		}
	}

	msg := string(body)
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	if msg == "" {
		return fmt.Errorf("langsync %s %s: status %d (empty body)", method, url, status)
	}
	return fmt.Errorf("langsync %s %s: status %d: %s", method, url, status, msg)
}

func formatTyped(e langsync.ResponseAPIError, status int) string {
	switch {
	case e.Msg != "" && e.Type != "":
		return fmt.Sprintf("%s (%s, %d)", e.Msg, e.Type, status)
	case e.Msg != "":
		return fmt.Sprintf("%s (%d)", e.Msg, status)
	default:
		return fmt.Sprintf("status %d", status)
	}
}
