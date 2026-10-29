package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pkg/browser"

	"github.com/norcubeplatform/cli/internal/transport"
)

// CallbackPayload is what the /cli-login frontend page POSTs to our loopback
// server. Field names mirror the JSON shape returned by /auth/cli/exchange,
// passed through unchanged by the page.
type CallbackPayload struct {
	State            string       `json:"state"`
	AccessToken      string       `json:"accessToken"`
	RefreshToken     string       `json:"refreshToken"`
	AccessExpiresIn  int          `json:"accessExpiresIn"`
	RefreshExpiresIn int          `json:"refreshExpiresIn"`
	User             User         `json:"user"`
	DefaultOrg       Organization `json:"defaultOrganization"`
}

type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatarUrl"`
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// BrowserLoginOptions configures BrowserLogin.
type BrowserLoginOptions struct {
	WebApp         string        // base URL of the web app, e.g. https://app.norcube.com
	Timeout        time.Duration // how long to wait for the user to complete the flow
	BrowserOpenURL func(string) error
	OnURLReady     func(string) // called with the URL just before opening the browser, for printing
	// NoBrowser skips opening a browser: the caller prints the URL (via
	// OnURLReady) for the user to open anywhere, e.g. over SSH.
	NoBrowser bool
}

// BrowserLogin runs the full browser-based login handshake:
//
//  1. Bind a loopback HTTP server on a random localhost port.
//  2. Open the user's browser to <WebApp>/cli-login?port=<P>&state=<S>.
//  3. Wait for the page to POST {state, tokens...} to http://127.0.0.1:P/callback.
//  4. Validate state, return the payload.
//
// The caller is responsible for persisting the returned tokens.
func BrowserLogin(ctx context.Context, opts BrowserLoginOptions) (*CallbackPayload, error) {
	if opts.WebApp == "" {
		return nil, errors.New("web app URL is required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.BrowserOpenURL == nil {
		opts.BrowserOpenURL = browser.OpenURL
	}

	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	allowedOrigin, err := originOf(opts.WebApp)
	if err != nil {
		return nil, err
	}

	// Bind to :0 so the OS picks a free port. The web app posts to
	// http://127.0.0.1:<port>, so it must be IPv4 loopback.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("can't open a local port on 127.0.0.1 for the sign-in callback: %w. "+
			"Browser login needs IPv4 loopback; check that the loopback interface is up and that no firewall blocks local ports", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	resultCh := make(chan *CallbackPayload, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// CORS: only the configured web app may call back. The page is
		// served from WebApp, the callback hits 127.0.0.1. Any other
		// site the user has open is refused before it can submit a
		// payload (the state check would stop it too).
		w.Header().Set("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" && origin != allowedOrigin {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		// Chrome's Private Network Access requires this header on requests
		// from a public/secure origin (https://...) to a private network
		// resource (http://127.0.0.1) — without it the preflight is blocked.
		// See https://developer.chrome.com/blog/private-network-access-preflight
		w.Header().Set("Access-Control-Allow-Private-Network", "true")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload CallbackPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			errCh <- fmt.Errorf("decode callback: %w", err)
			return
		}
		if payload.State != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- errors.New("callback state did not match — possible CSRF, aborting")
			return
		}
		if payload.RefreshToken == "" || payload.AccessToken == "" {
			http.Error(w, "tokens missing", http.StatusBadRequest)
			errCh <- errors.New("callback payload missing tokens")
			return
		}

		w.WriteHeader(http.StatusNoContent)
		resultCh <- &payload
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	loginURL := buildLoginURL(opts.WebApp, port, state)
	if opts.OnURLReady != nil {
		opts.OnURLReady(loginURL)
	}
	// Open failure is not fatal — the user can copy/paste the URL manually.
	if !opts.NoBrowser {
		_ = opts.BrowserOpenURL(loginURL)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	select {
	case payload := <-resultCh:
		return payload, nil
	case err := <-errCh:
		return nil, err
	case <-timeoutCtx.Done():
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("login timed out after %s — re-run `norcube login`", opts.Timeout)
		}
		return nil, timeoutCtx.Err()
	}
}

func buildLoginURL(webApp string, port int, state string) string {
	u, _ := url.Parse(webApp)
	u.Path = "/cli-login"
	q := u.Query()
	q.Set("port", fmt.Sprintf("%d", port))
	q.Set("state", state)
	// The sign-in page shows the device before you authorize it and
	// records it on the session, so you can tell your sessions apart
	// under Account › CLI sessions. Both are optional.
	if host := deviceName(); host != "" {
		q.Set("host", host)
	}
	q.Set("client", truncate(transport.UserAgent()))
	u.RawQuery = q.Encode()
	return u.String()
}

// maxLoginParam matches what the sign-in page keeps of host and client.
const maxLoginParam = 256

// deviceName is this machine's hostname, or "" when it can't be read.
func deviceName() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return truncate(strings.TrimSpace(host))
}

// truncate keeps at most maxLoginParam bytes of s without splitting a
// character.
func truncate(s string) string {
	if len(s) <= maxLoginParam {
		return s
	}
	return strings.ToValidUTF8(s[:maxLoginParam], "")
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// originOf returns the browser Origin ("scheme://host[:port]") of the web
// app URL, which is what its pages send in the Origin header.
func originOf(webApp string) (string, error) {
	u, err := url.Parse(webApp)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("web app URL %q is not an absolute URL", webApp)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}
