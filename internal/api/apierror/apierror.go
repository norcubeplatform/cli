// Package apierror renders non-2xx API responses into error messages
// that are actually useful for debugging. One implementation shared by
// every service command tree (snapdb, langsync, ...), so the format
// stays identical across the CLI:
//
//   - always includes the request method + URL, so users can tell
//     whether they're pointing at prod or a local backend;
//   - prefers the server's typed JSON error body when one matched;
//   - otherwise falls back to the raw response body (truncated), which
//     is the only thing that surfaces 5xx errors from proxies and LBs.
package apierror

import (
	"fmt"
	"net/http"
)

// maxRawBody bounds how much of an untyped response body ends up in the
// error message.
const maxRawBody = 500

// Typed is the service-agnostic view of a typed API error body. Each
// service package converts its generated ResponseAPIError into this.
type Typed struct {
	Msg  string
	Type string
}

// Format builds the standard "service METHOD URL: detail" error. The
// first non-nil typed body wins; with none, the raw body is used.
func Format(service string, resp *http.Response, body []byte, typed ...*Typed) error {
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
			return fmt.Errorf("%s %s %s: %s", service, method, url, formatTyped(*t, status))
		}
	}

	msg := string(body)
	if len(msg) > maxRawBody {
		msg = msg[:maxRawBody] + "…"
	}
	if msg == "" {
		return fmt.Errorf("%s %s %s: status %d (empty body)", service, method, url, status)
	}
	return fmt.Errorf("%s %s %s: status %d: %s", service, method, url, status, msg)
}

func formatTyped(e Typed, status int) string {
	switch {
	case e.Msg != "" && e.Type != "":
		return fmt.Sprintf("%s (%s, %d)", e.Msg, e.Type, status)
	case e.Msg != "":
		return fmt.Sprintf("%s (%d)", e.Msg, status)
	default:
		return fmt.Sprintf("status %d", status)
	}
}
