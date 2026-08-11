package apierror

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
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
