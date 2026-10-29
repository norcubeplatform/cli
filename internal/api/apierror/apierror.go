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
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/norcubeplatform/cli/internal/clierr"
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

// Format builds the standard "service METHOD URL: detail" error as a
// *clierr.Error, so the code and exit code follow the response status.
// The first non-nil typed body wins; with none, the raw body is used.
func Format(service string, resp *http.Response, body []byte, typed ...*Typed) error {
	e := &clierr.Error{Service: service, URL: "<unknown url>", Method: "?"}
	if resp != nil {
		e.HTTPStatus = resp.StatusCode
		e.RequestID = requestID(resp.Header)
		if resp.Request != nil {
			e.URL = resp.Request.URL.String()
			e.Method = resp.Request.Method
		}
	}

	for _, t := range typed {
		if t != nil {
			e.Message, e.Type = t.Msg, t.Type
			if e.Message == "" {
				e.Message = fmt.Sprintf("status %d", e.HTTPStatus)
			}
			e.Code = clierr.CodeForStatus(e.HTTPStatus, e.Type)
			return e
		}
	}

	// Every Norcube service answers errors as {"type": ..., "msg": ...},
	// also on statuses the generated client has no typed field for.
	var generic struct {
		Type string `json:"type"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(body, &generic) == nil && generic.Msg != "" {
		e.Message, e.Type = generic.Msg, generic.Type
		e.Code = clierr.CodeForStatus(e.HTTPStatus, e.Type)
		return e
	}

	msg := string(body)
	if len(msg) > maxRawBody {
		msg = msg[:maxRawBody] + "…"
	}
	e.Message = msg
	e.Code = clierr.CodeForStatus(e.HTTPStatus, "")
	return e
}

// requestID returns the id the service logged the request under, when it
// sent one back.
func requestID(h http.Header) string {
	for _, k := range []string{"X-Request-Id", "X-Request-ID", "Request-Id"} {
		if v := h.Get(k); v != "" {
			return v
		}
	}
	return ""
}
