package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// startLogin runs BrowserLogin in the background and returns the login
// URL it printed plus a channel with its result.
func startLogin(t *testing.T, opts BrowserLoginOptions) (*url.URL, <-chan error, <-chan *CallbackPayload) {
	t.Helper()
	urls := make(chan string, 1)
	opts.OnURLReady = func(u string) { urls <- u }
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	errs := make(chan error, 1)
	payloads := make(chan *CallbackPayload, 1)
	go func() {
		p, err := BrowserLogin(context.Background(), opts)
		errs <- err
		payloads <- p
	}()
	select {
	case raw := <-urls:
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u, errs, payloads
	case err := <-errs:
		t.Fatalf("login failed to start: %v", err)
	}
	return nil, nil, nil
}

func callback(t *testing.T, loginURL *url.URL, method, origin string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, "http://127.0.0.1:"+loginURL.Query().Get("port")+"/callback", bytes.NewReader(b))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestBrowserLoginCORS(t *testing.T) {
	opened := false
	u, errs, payloads := startLogin(t, BrowserLoginOptions{
		WebApp:         "https://app.norcube.test/",
		BrowserOpenURL: func(string) error { opened = true; return nil },
	})
	state := u.Query().Get("state")
	good := map[string]any{"state": state, "accessToken": "a", "refreshToken": "r"}

	// Another site's preflight and POST are refused, and don't end the login.
	if resp := callback(t, u, http.MethodOptions, "https://evil.test", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign preflight: %d", resp.StatusCode)
	}
	if resp := callback(t, u, http.MethodPost, "https://evil.test", good); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign POST: %d", resp.StatusCode)
	}
	// The web app's own preflight is allowed for exactly its origin.
	resp := callback(t, u, http.MethodOptions, "https://app.norcube.test", nil)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "https://app.norcube.test" {
		t.Fatalf("web app preflight: %d, ACAO %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if resp := callback(t, u, http.MethodPost, "https://app.norcube.test", good); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("web app POST: %d", resp.StatusCode)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if p := <-payloads; p.RefreshToken != "r" {
		t.Fatalf("payload %+v", p)
	}
	if !opened {
		t.Fatal("browser was not opened")
	}
}

func TestBrowserLoginNoBrowser(t *testing.T) {
	u, errs, _ := startLogin(t, BrowserLoginOptions{
		WebApp:         "https://app.norcube.test",
		NoBrowser:      true,
		BrowserOpenURL: func(string) error { t.Error("browser opened despite NoBrowser"); return nil },
	})
	if u.Host != "app.norcube.test" || u.Path != "/cli-login" || u.Query().Get("port") == "" {
		t.Fatalf("login URL %s", u)
	}
	body := map[string]any{"state": u.Query().Get("state"), "accessToken": "a", "refreshToken": "r"}
	if resp := callback(t, u, http.MethodPost, "https://app.norcube.test", body); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestBrowserLoginRejectsRelativeWebApp(t *testing.T) {
	if _, err := BrowserLogin(context.Background(), BrowserLoginOptions{WebApp: "app.norcube.com"}); err == nil {
		t.Fatal("relative web app URL accepted")
	}
}
