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
	"strings"

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

	// Newer services (PulseCheck) answer RFC 9457 problems: "code" is the
	// stable code, "reason" a finer cause, "detail" the sentence.
	if p, ok := parseProblem(body); ok {
		e.Message, e.Type = p.message(), p.Code
		mapped := p.Code
		if strings.EqualFold(p.Reason, "INSUFFICIENT_SCOPE") {
			mapped = p.Reason
		}
		e.Code = clierr.CodeForStatus(e.HTTPStatus, mapped)
		if d := p.details(); d != nil {
			e.Details = d
		}
		return e
	}

	// The other Norcube services answer errors as {"type": ..., "msg": ...},
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

// problem is an RFC 9457 problem document as Norcube services send it
// (application/problem+json).
type problem struct {
	Code   string            `json:"code"`
	Detail string            `json:"detail"`
	Title  string            `json:"title"`
	Reason string            `json:"reason"`
	Data   map[string]string `json:"data"`
	Errors []violation       `json:"errors"`
}

// violation is one invalid field of a VALIDATION_FAILED problem.
type violation struct {
	Pointer string         `json:"pointer"`
	Rule    string         `json:"rule"`
	Detail  string         `json:"detail"`
	Params  map[string]any `json:"params,omitempty"`
}

// maxViolations bounds how many field errors the message lists; details
// keep all of them.
const maxViolations = 5

func parseProblem(body []byte) (problem, bool) {
	var p problem
	if json.Unmarshal(body, &p) != nil || p.Code == "" || (p.Detail == "" && p.Title == "") {
		return problem{}, false
	}
	return p, true
}

// message is the detail, followed by the invalid fields ("/http/url:
// must be a public address").
func (p problem) message() string {
	msg := p.Detail
	if msg == "" {
		msg = p.Title
	}
	if len(p.Errors) == 0 {
		return msg
	}
	parts := make([]string, 0, maxViolations+1)
	for i, v := range p.Errors {
		if i == maxViolations {
			parts = append(parts, fmt.Sprintf("and %d more", len(p.Errors)-maxViolations))
			break
		}
		parts = append(parts, strings.TrimSpace(v.Pointer+": "+v.Detail))
	}
	return msg + " (" + strings.Join(parts, "; ") + ")"
}

// details are the problem's machine-readable parts for the JSON error:
// the reason, the field errors and the data, when there are any.
func (p problem) details() map[string]any {
	d := map[string]any{}
	if p.Reason != "" {
		d["reason"] = p.Reason
	}
	if len(p.Errors) > 0 {
		d["errors"] = p.Errors
	}
	if len(p.Data) > 0 {
		d["data"] = p.Data
	}
	if len(d) == 0 {
		return nil
	}
	return d
}
