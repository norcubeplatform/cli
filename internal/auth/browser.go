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
	"time"

	"github.com/pkg/browser"
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

	// Bind to :0 so the OS picks a free port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	resultCh := make(chan *CallbackPayload, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// CORS — the page is served from WebApp, the callback hits 127.0.0.1.
		w.Header().Set("Access-Control-Allow-Origin", originHeader(r, opts.WebApp))
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
	if err := opts.BrowserOpenURL(loginURL); err != nil {
		// Not fatal — the user can copy/paste the URL manually.
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
	u.RawQuery = q.Encode()
	return u.String()
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// originHeader echoes back the request's Origin if it matches the configured
// web app, otherwise falls back to the configured web app. Echoing is required
// because Access-Control-Allow-Origin: * is rejected when credentials are used.
func originHeader(r *http.Request, webApp string) string {
	origin := r.Header.Get("Origin")
	if origin != "" {
		return origin
	}
	return webApp
}
