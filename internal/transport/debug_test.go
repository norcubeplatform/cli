package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDebugTraceRedactsCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "jds_refresh_token=cookie-secret; HttpOnly")
		w.Header().Set("X-Request-Id", "req-42")
		_, _ = w.Write([]byte(`{"access_token":"at-secret","refresh_token":"rt-secret","expires_in":300,
			"url":"https://bucket.example/obj?X-Amz-Signature=sig-secret&X-Amz-Expires=60",
			"uri":"postgres://app:pw-secret@db.example:5432/app"}`))
	}))
	defer srv.Close()

	var trace bytes.Buffer
	cfg := Defaults()
	cfg.Debug = &trace
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"form-secret"}, "audience": {"snapdb-api"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/oauth/token?token=query-secret&page=2", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("Cookie", "jds_refresh_token=cookie-secret")
	req.Header.Set("Apikey", "key-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	_ = resp.Body.Close()

	out := trace.String()
	for _, secret := range []string{"header-secret", "cookie-secret", "key-secret", "form-secret", "query-secret", "at-secret", "rt-secret", "sig-secret", "pw-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("trace leaks %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{"--> #1 POST", "Authorization: Bearer [REDACTED]", "page=2", "audience=snapdb-api", "<-- #1 200 OK", "request-id=req-42", `"expires_in":300`, "X-Amz-Expires=60"} {
		if !strings.Contains(out, want) {
			t.Errorf("trace lacks %q:\n%s", want, out)
		}
	}
	// The caller still gets the untouched body.
	if !strings.Contains(body.String(), "at-secret") {
		t.Error("the trace consumed the response body")
	}
}

func TestDebugOffByDefault(t *testing.T) {
	c, err := New(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Transport.(*retryTransport).base.(*debugTransport); ok {
		t.Error("debug trace on without --debug")
	}
}

func TestRedactBodySummarizesOtherTypes(t *testing.T) {
	if got := RedactBody("application/octet-stream", make([]byte, 10)); !strings.Contains(got, "10 bytes") {
		t.Errorf("got %q", got)
	}
}
