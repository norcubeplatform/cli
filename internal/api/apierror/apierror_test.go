package apierror

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/clierr"
)

func respFor(method, rawurl string, status int) *http.Response {
	u, _ := url.Parse(rawurl)
	return &http.Response{
		StatusCode: status,
		Request:    &http.Request{Method: method, URL: u},
	}
}

func TestFormatPrefersTypedBody(t *testing.T) {
	err := Format("snapdb", respFor("GET", "https://api.test/x", 400),
		[]byte(`raw body`), nil, &Typed{Msg: "invalid cursor", Type: "INVALID_PAYLOAD"})
	want := "snapdb GET https://api.test/x: invalid cursor (INVALID_PAYLOAD, 400)"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestFormatFallsBackToRawBody(t *testing.T) {
	err := Format("langsync", respFor("POST", "https://api.test/y", 502), []byte("bad gateway"))
	if got := err.Error(); got != "langsync POST https://api.test/y: status 502: bad gateway" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatTruncatesLongBodies(t *testing.T) {
	long := strings.Repeat("x", 2000)
	err := Format("snapdb", respFor("GET", "https://api.test/z", 500), []byte(long))
	if len(err.Error()) > 700 {
		t.Fatalf("body not truncated: %d chars", len(err.Error()))
	}
	if !strings.HasSuffix(err.Error(), "…") {
		t.Fatal("truncation marker missing")
	}
}

func TestFormatEmptyBodyAndNilResponse(t *testing.T) {
	if got := Format("snapdb", respFor("GET", "https://api.test", 500), nil).Error(); !strings.Contains(got, "empty body") {
		t.Fatalf("empty body not noted: %q", got)
	}
	// nil response must not panic and still produce something sane.
	if got := Format("snapdb", nil, []byte("boom")).Error(); !strings.Contains(got, "<unknown url>") {
		t.Fatalf("nil response mishandled: %q", got)
	}
}

func TestFormatProblem(t *testing.T) {
	body := []byte(`{"type":"https://docs.norcube.com/errors/validation-failed","title":"Validation failed","status":400,
		"detail":"The request has invalid fields.","code":"VALIDATION_FAILED",
		"errors":[{"pointer":"/http/timeoutMs","rule":"range","params":{"min":1000,"max":30000},"detail":"must be between 1000 and 30000"}]}`)
	err := Format("monitor", respFor("POST", "https://api.test/monitors", 400), body)
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a clierr.Error: %T", err)
	}
	if ce.Code != clierr.CodeValidation || ce.Type != "VALIDATION_FAILED" {
		t.Errorf("code %q type %q", ce.Code, ce.Type)
	}
	if want := "The request has invalid fields. (/http/timeoutMs: must be between 1000 and 30000)"; ce.Message != want {
		t.Errorf("message %q", ce.Message)
	}
	if d, ok := ce.Details.(map[string]any); !ok || d["errors"] == nil {
		t.Errorf("details %#v", ce.Details)
	}
}

func TestFormatProblemScopeAndForbidden(t *testing.T) {
	scope := []byte(`{"title":"Forbidden","status":403,"detail":"This token can't change monitors.","code":"FORBIDDEN","reason":"INSUFFICIENT_SCOPE"}`)
	err := Format("monitor", respFor("DELETE", "https://api.test/monitors/m", 403), scope)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != clierr.CodeInsufficientScope || ce.ExitCode() != clierr.ExitAccess {
		t.Fatalf("scope refusal: %+v", err)
	}
	plain := []byte(`{"title":"Forbidden","status":403,"detail":"You don't have access to this in Norcube Monitor.","code":"FORBIDDEN"}`)
	err = Format("monitor", respFor("GET", "https://api.test/monitors", 403), plain)
	if !errors.As(err, &ce) || ce.Code != clierr.CodePermissionDenied || ce.Details != nil {
		t.Fatalf("permission refusal: %+v", ce)
	}
	if !strings.Contains(err.Error(), "(FORBIDDEN, 403)") {
		t.Errorf("text %q", err.Error())
	}
}
