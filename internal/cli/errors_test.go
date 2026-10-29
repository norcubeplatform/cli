package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/orgs"
	"github.com/norcubeplatform/cli/internal/transport"
)

func apiErr(status int, typ string) error {
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "api.example", Path: "/x"}}
	resp := &http.Response{StatusCode: status, Request: req, Header: http.Header{"X-Request-Id": {"req-1"}}}
	if typ == "" {
		return apierror.Format("backup", resp, []byte("boom"))
	}
	return apierror.Format("backup", resp, nil, &apierror.Typed{Msg: "nope", Type: typ})
}

func TestDescribeCodesAndExitCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		exit int
	}{
		{"400", apiErr(400, "VALIDATION"), clierr.CodeValidation, clierr.ExitUsage},
		{"401", apiErr(401, "UNAUTHORIZED"), clierr.CodeLoginRequired, clierr.ExitAuth},
		{"403", apiErr(403, "FORBIDDEN"), clierr.CodePermissionDenied, clierr.ExitAccess},
		{"403 scope", apiErr(403, "INSUFFICIENT_SCOPE"), clierr.CodeInsufficientScope, clierr.ExitAccess},
		{"404", apiErr(404, "NOT_FOUND"), clierr.CodeNotFound, clierr.ExitNotFound},
		{"409", apiErr(409, "CONFLICT"), clierr.CodeConflict, clierr.ExitConflict},
		{"429", apiErr(429, ""), clierr.CodeRateLimited, clierr.ExitRateLimited},
		{"504", apiErr(504, ""), clierr.CodeTimeout, clierr.ExitTimeout},
		{"500", apiErr(500, ""), clierr.CodeServerError, clierr.ExitError},
		{"not signed in", auth.ErrNotLoggedIn, clierr.CodeLoginRequired, clierr.ExitAuth},
		{"session refused", fmt.Errorf("%w: revoked", auth.ErrLoginRequired), clierr.CodeLoginRequired, clierr.ExitAuth},
		{"foreign org", &auth.OrgAccessError{Org: "acme"}, clierr.CodePermissionDenied, clierr.ExitAccess},
		{"unknown slug", clierr.Usage(&orgs.NotFoundError{Value: "acme"}), clierr.CodeOrgNotFound, clierr.ExitUsage},
		{"usage", clierr.Usage(errors.New("bad flag")), clierr.CodeUsage, clierr.ExitUsage},
		{"cobra usage", errors.New("unknown flag: --nope"), clierr.CodeUsage, clierr.ExitUsage},
		{"read timeout", &transport.TimeoutError{After: time.Minute}, clierr.CodeTimeout, clierr.ExitTimeout},
		{"deadline", fmt.Errorf("get: %w", context.DeadlineExceeded), clierr.CodeTimeout, clierr.ExitTimeout},
		{"interrupted", context.Canceled, clierr.CodeInterrupted, clierr.ExitInterrupted},
		{"other", errors.New("disk full"), clierr.CodeError, clierr.ExitError},
	}
	for _, tt := range tests {
		e := Describe(tt.err)
		if e.Code != tt.code || e.ExitCode() != tt.exit {
			t.Errorf("%s: code %q exit %d, want %q exit %d", tt.name, e.Code, e.ExitCode(), tt.code, tt.exit)
		}
	}
}

func TestPrintErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	exit := PrintError(&buf, apiErr(404, "NOT_FOUND"), true)
	if exit != clierr.ExitNotFound {
		t.Errorf("exit %d", exit)
	}
	var got struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	want := map[string]any{
		"code": "not_found", "message": "nope", "status": float64(404), "service": "backup",
		"method": "GET", "url": "https://api.example/x", "type": "NOT_FOUND", "requestId": "req-1",
	}
	for k, v := range want {
		if got.Error[k] != v {
			t.Errorf("error.%s = %v, want %v", k, got.Error[k], v)
		}
	}
}

func TestPrintErrorText(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, apiErr(404, "NOT_FOUND"), false)
	if got, want := buf.String(), "backup GET https://api.example/x: nope (NOT_FOUND, 404)\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	buf.Reset()
	PrintError(&buf, auth.ErrNotLoggedIn, false)
	if !strings.HasPrefix(buf.String(), "Not signed in") || strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("not signed in: %q", buf.String())
	}
}

func TestErrorsAsJSON(t *testing.T) {
	defer func(o string) { selectedOutput = o }(selectedOutput)
	tests := []struct {
		env, selected string
		want          bool
	}{
		{"", "table", false},
		{"", "json", true},
		{"", "yaml", true},
		{"json", "table", true},
		{"JSON", "", true},
	}
	for _, tt := range tests {
		t.Setenv(EnvErrorFormat, tt.env)
		selectedOutput = tt.selected
		if got := ErrorsAsJSON(); got != tt.want {
			t.Errorf("env %q, output %q: %v, want %v", tt.env, tt.selected, got, tt.want)
		}
	}
	for args, want := range map[string]string{
		"backup list -o json":       "json",
		"backup list --output=yaml": "yaml",
		"-ojson whoami":             "json",
		"whoami -- -o json":         "",
	} {
		if got := outputFromArgs(strings.Fields(args)); got != want {
			t.Errorf("outputFromArgs(%q) = %q, want %q", args, got, want)
		}
	}
}
