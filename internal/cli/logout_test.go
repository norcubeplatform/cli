package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/config"
)

// signOutServer plays Norcube Auth's POST /auth/cli/sign-out and records
// what the CLI sent.
type signOutServer struct {
	srv    *httptest.Server
	status int

	mu         sync.Mutex
	calls      int
	form       url.Values
	ctype      string
	cookie     string
	lockedHeld bool
}

func (s *signOutServer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newSignOutServer(t *testing.T, profile string, status int) *signOutServer {
	t.Helper()
	s := &signOutServer{status: status}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth/cli/sign-out" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		// The CLI must hold the profile's refresh lock during the call,
		// so a concurrent refresh can't rotate the token being ended.
		unlock, err := auth.LockProfile(r.Context(), "", profile, s.srv.URL, 50*time.Millisecond)
		held := err != nil
		if err == nil {
			unlock()
		}
		s.mu.Lock()
		s.calls++
		s.form, s.ctype, s.cookie, s.lockedHeld = form, r.Header.Get("Content-Type"), r.Header.Get("Cookie"), held
		s.mu.Unlock()
		w.WriteHeader(s.status)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// signOutProfile writes a profile whose Norcube Auth is s and stores a
// session for it.
func signOutProfile(t *testing.T, name, authURL string) {
	t.Helper()
	isolatedConfig(t, func(f *config.File) {
		f.DefaultProfile = name
		p := f.Profile(name)
		p.Environment = config.EnvCustom
		p.Endpoints = map[string]string{config.ServiceAuth: authURL}
		p.User = config.User{ID: "user-" + name, Email: name + "@example.com"}
	})
	if err := auth.SaveRefreshToken(name, authURL, "refresh-live"); err != nil {
		t.Fatal(err)
	}
}

func logoutJSON(t *testing.T, stdout string) logoutResult {
	t.Helper()
	var res logoutResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("logout -o json: %v\n%s", err, stdout)
	}
	return res
}

// Logging out ends the session on Norcube Auth: the refresh token in a
// form body (never a cookie, which would end a browser session), under
// the profile's refresh lock, then forgets it locally.
func TestLogoutEndsTheServerSession(t *testing.T) {
	s := newSignOutServer(t, "so-live", http.StatusNoContent)
	signOutProfile(t, "so-live", s.srv.URL)

	stdout, stderr, err := run(t, "logout", "-o", "json")
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, stderr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls != 1 {
		t.Fatalf("sign-out calls = %d, want 1", s.calls)
	}
	if got := s.form.Get("refresh_token"); got != "refresh-live" {
		t.Errorf("refresh_token = %q, want the stored token", got)
	}
	if !strings.HasPrefix(s.ctype, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q", s.ctype)
	}
	if s.cookie != "" {
		t.Errorf("sent a cookie: %q", s.cookie)
	}
	if !s.lockedHeld {
		t.Error("the profile's refresh lock was not held during the sign-out call")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("unexpected stderr: %q", stderr)
	}
	if auth.HasCredentials("so-live", s.srv.URL) {
		t.Error("credentials kept after logout")
	}
	if res := logoutJSON(t, stdout); res.ServerSessionEnded == nil || !*res.ServerSessionEnded {
		t.Errorf("serverSessionEnded = %v, want true", res.ServerSessionEnded)
	}
}

// When the server can't end the session, logging out still succeeds
// locally and says what happened, on stderr.
func TestLogoutWhenTheServerFails(t *testing.T) {
	failing := newSignOutServer(t, "so-500", http.StatusInternalServerError)
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	for name, authURL := range map[string]string{"so-500": failing.srv.URL, "so-down": downURL} {
		t.Run(name, func(t *testing.T) {
			signOutProfile(t, name, authURL)
			stdout, stderr, err := run(t, "logout", "-o", "json")
			if err != nil {
				t.Fatalf("logout failed: %v", err)
			}
			if !strings.Contains(stderr, "couldn't end the session on the server") ||
				!strings.Contains(stderr, "Account › CLI sessions") {
				t.Errorf("missing warning, stderr: %q", stderr)
			}
			if auth.HasCredentials(name, authURL) {
				t.Error("credentials kept after a failed server sign-out")
			}
			if res := logoutJSON(t, stdout); res.ServerSessionEnded == nil || *res.ServerSessionEnded {
				t.Errorf("serverSessionEnded = %v, want false", res.ServerSessionEnded)
			}
		})
	}
}

// A profile without a stored session has nothing to end on the server.
func TestLogoutWithoutSessionCallsNothing(t *testing.T) {
	s := newSignOutServer(t, "so-none", http.StatusNoContent)
	signOutProfile(t, "so-none", s.srv.URL)
	if err := auth.DeleteCredentials("so-none", s.srv.URL); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run(t, "logout", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if n := s.callCount(); n != 0 {
		t.Errorf("sign-out called %d times without a session", n)
	}
	if res := logoutJSON(t, stdout); res.ServerSessionEnded != nil {
		t.Errorf("serverSessionEnded = %v, want absent", *res.ServerSessionEnded)
	}
}

// --all ends every profile's session once, even with two credential stores.
func TestLogoutAllEndsEverySession(t *testing.T) {
	one := newSignOutServer(t, "so-one", http.StatusNoContent)
	two := newSignOutServer(t, "so-two", http.StatusNoContent)
	isolatedConfig(t, func(f *config.File) {
		for name, s := range map[string]*signOutServer{"so-one": one, "so-two": two} {
			p := f.Profile(name)
			p.Environment = config.EnvCustom
			p.Endpoints = map[string]string{config.ServiceAuth: s.srv.URL}
		}
	})
	for name, s := range map[string]*signOutServer{"so-one": one, "so-two": two} {
		if err := auth.SaveRefreshToken(name, s.srv.URL, "refresh-live"); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, err := run(t, "logout", "--all", "-o", "json")
	if err != nil {
		t.Fatalf("logout --all: %v\n%s", err, stderr)
	}
	for name, s := range map[string]*signOutServer{"so-one": one, "so-two": two} {
		if n := s.callCount(); n != 1 {
			t.Errorf("%s: sign-out calls = %d, want 1", name, n)
		}
	}
	if res := logoutJSON(t, stdout); res.ServerSessionEnded == nil || !*res.ServerSessionEnded {
		t.Errorf("serverSessionEnded = %v, want true", res.ServerSessionEnded)
	}
}
