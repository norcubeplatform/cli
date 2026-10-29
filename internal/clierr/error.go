package clierr

import (
	"fmt"
	"net/http"
	"strings"
)

// Error codes: the stable, machine-readable reason in an Error. Scripts
// match on these (and on the exit code); messages may change wording.
const (
	CodeError             = "error"
	CodeUsage             = "usage_error"
	CodeValidation        = "validation_failed"
	CodeOrgNotFound       = "org_not_found"
	CodeNotFound          = "not_found"
	CodeLoginRequired     = "login_required"
	CodePermissionDenied  = "permission_denied"
	CodeInsufficientScope = "insufficient_scope"
	CodeRateLimited       = "rate_limited"
	CodeTimeout           = "timeout"
	CodeConflict          = "conflict"
	CodeServerError       = "server_error"
	CodeInterrupted       = "interrupted"
	// CodeJobFailed: a command waited (--wait) for a server-side job that
	// ended unsuccessfully. Exit code 1.
	CodeJobFailed = "job_failed"
)

// Error is a failure described for both people and scripts. Printed as
// text it reads like before; with -o json (or NORCUBE_ERROR_FORMAT=json)
// it is written to stderr as {"error": {...}}.
type Error struct {
	Code    string `json:"code" yaml:"code"`
	Message string `json:"message" yaml:"message"`
	Hint    string `json:"hint,omitempty" yaml:"hint,omitempty"`
	// HTTPStatus, Service, Method, URL, Type and RequestID describe the
	// API response that failed, when there was one.
	HTTPStatus int    `json:"status,omitempty" yaml:"status,omitempty"`
	Service    string `json:"service,omitempty" yaml:"service,omitempty"`
	Method     string `json:"method,omitempty" yaml:"method,omitempty"`
	URL        string `json:"url,omitempty" yaml:"url,omitempty"`
	// Type is the service's own error type (e.g. NOT_FOUND).
	Type      string `json:"type,omitempty" yaml:"type,omitempty"`
	RequestID string `json:"requestId,omitempty" yaml:"requestId,omitempty"`
	Details   any    `json:"details,omitempty" yaml:"details,omitempty"`

	// Err is the underlying error, for errors.Is/As.
	Err error `json:"-" yaml:"-"`
}

// Error is the human form. API errors keep the "service METHOD URL: detail"
// shape that tells prod from a local backend at a glance.
func (e *Error) Error() string {
	if e.Service == "" || e.URL == "" {
		return e.Message
	}
	detail := e.Message
	switch {
	case e.Type != "" && detail != "":
		detail = fmt.Sprintf("%s (%s, %d)", detail, e.Type, e.HTTPStatus)
	case detail != "" && e.HTTPStatus != 0:
		detail = fmt.Sprintf("status %d: %s", e.HTTPStatus, detail)
	case detail == "":
		detail = fmt.Sprintf("status %d (empty body)", e.HTTPStatus)
	}
	return fmt.Sprintf("%s %s %s: %s", e.Service, e.Method, e.URL, detail)
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode is the process exit code for e.
func (e *Error) ExitCode() int { return ExitFor(e.Code) }

// ExitFor maps an error code to its exit code.
func ExitFor(code string) int {
	switch code {
	case CodeUsage, CodeValidation, CodeOrgNotFound:
		return ExitUsage
	case CodeNotFound:
		return ExitNotFound
	case CodeLoginRequired:
		return ExitAuth
	case CodePermissionDenied, CodeInsufficientScope:
		return ExitAccess
	case CodeRateLimited:
		return ExitRateLimited
	case CodeTimeout:
		return ExitTimeout
	case CodeConflict:
		return ExitConflict
	case CodeInterrupted:
		return ExitInterrupted
	}
	return ExitError
}

// CodeForStatus maps an API response status (and the service's own error
// type, when it sent one) to an error code.
func CodeForStatus(status int, serviceType string) string {
	switch {
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		return CodeValidation
	case status == http.StatusUnauthorized:
		return CodeLoginRequired
	case status == http.StatusForbidden:
		if strings.EqualFold(serviceType, "INSUFFICIENT_SCOPE") {
			return CodeInsufficientScope
		}
		return CodePermissionDenied
	case status == http.StatusNotFound:
		return CodeNotFound
	case status == http.StatusConflict:
		return CodeConflict
	case status == http.StatusTooManyRequests:
		return CodeRateLimited
	case status == http.StatusRequestTimeout, status == http.StatusGatewayTimeout:
		return CodeTimeout
	case status >= 500:
		return CodeServerError
	}
	return CodeError
}
