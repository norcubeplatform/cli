package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

// stubBrowserLogin replaces the browser handshake: it plays a successful
// sign-in that hands the CLI a new session, and counts its calls.
func stubBrowserLogin(t *testing.T, b *fakeBackend) *int {
	t.Helper()
	calls := 0
	prev := browserLogin
	browserLogin = func(context.Context, auth.BrowserLoginOptions) (*auth.CallbackPayload, error) {
		calls++
		b.mu.Lock()
		b.refresh = "refresh-new"
		b.mu.Unlock()
		return &auth.CallbackPayload{
			AccessToken:     testJWT(auth.AudienceAuth, ""),
			RefreshToken:    "refresh-new",
			AccessExpiresIn: 300,
			User:            auth.User{ID: "user-1", Name: "Dev", Email: "dev@example.com"},
			DefaultOrg:      auth.Organization{ID: homeOrgID, Slug: "home", Name: "Home"},
		}, nil
	}
	t.Cleanup(func() { browserLogin = prev })
	return &calls
}

func endSession() {
	backend.mu.Lock()
	backend.refresh = ""
	backend.mu.Unlock()
}

// loginProfile is a profile whose endpoints are a fresh fake Norcube Auth
// with a live stored session, so `login` targets it without an env override.
func loginProfile(t *testing.T) *fakeBackend {
	t.Helper()
	b := newBackend(t)
	isolatedConfig(t, func(f *config.File) {
		f.DefaultProfile = "p-login"
		customProfile(f, "p-login", b, config.Organization{ID: homeOrgID, Slug: "home"})
	})
	if err := auth.SaveRefreshToken("p-login", b.srv.URL, "refresh-0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auth.DeleteCredentials("p-login", b.srv.URL) })
	return b
}

// `login` on a profile whose stored session the server ended signs in
// again instead of claiming the profile is signed in.
func TestLoginReplacesEndedSession(t *testing.T) {
	b := loginProfile(t)
	b.mu.Lock()
	b.refresh = ""
	b.mu.Unlock()
	calls := stubBrowserLogin(t, b)

	_, stderr, err := run(t, "login")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	if *calls != 1 {
		t.Fatalf("browser sign-in ran %d times, want 1", *calls)
	}
	if !strings.Contains(stderr, "Your previous session ended (revoked or expired); signing in again.") {
		t.Errorf("stderr doesn't explain the new sign-in:\n%s", stderr)
	}
	if got, _ := auth.LoadRefreshToken("p-login", b.srv.URL); got != "refresh-new" {
		t.Errorf("stored refresh token %q, want the new session's", got)
	}
}

// A live session keeps the "already signed in" answer and no browser opens.
func TestLoginKeepsLiveSession(t *testing.T) {
	b := loginProfile(t)
	calls := stubBrowserLogin(t, b)

	stdout, _, err := run(t, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if *calls != 0 {
		t.Errorf("browser sign-in ran for a live session")
	}
	if !strings.Contains(stdout, "Already signed in") {
		t.Errorf("stdout: %q", stdout)
	}
	if strings.Contains(stdout, "Couldn't verify") {
		t.Errorf("a verified session must not carry the warning: %q", stdout)
	}
}

// When Norcube Auth can't answer, login keeps the stored session and says
// it couldn't verify it.
func TestLoginUnverifiedSession(t *testing.T) {
	b := loginProfile(t)
	b.mu.Lock()
	b.tokenStatus = http.StatusInternalServerError
	b.mu.Unlock()
	calls := stubBrowserLogin(t, b)

	stdout, _, err := run(t, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if *calls != 0 {
		t.Errorf("browser sign-in ran although the session wasn't known to be dead")
	}
	if !strings.Contains(stdout, "Already signed in") || !strings.Contains(stdout, "Couldn't verify the session") {
		t.Errorf("stdout: %q", stdout)
	}
	if got, _ := auth.LoadRefreshToken("p-login", b.srv.URL); got != "refresh-0" {
		t.Errorf("stored session was dropped on a server error")
	}
}

func TestAuthStatusChecksTheSession(t *testing.T) {
	tests := []struct {
		name        string
		setup       func()
		args        []string
		wantSession string
		wantSigned  bool
		wantExit    int
		wantLabel   string
	}{
		{"live session", func() {}, nil, "active", true, 0, "yes"},
		{"session ended on the server", endSession, nil, "ended", false, clierr.ExitAuth, "revoked/expired"},
		{"nothing stored", func() { _ = auth.DeleteCredentials("", backend.srv.URL) }, nil, "none", false, clierr.ExitAuth, "no"},
		{"server unreachable", func() {
			backend.mu.Lock()
			backend.tokenStatus = http.StatusInternalServerError
			backend.mu.Unlock()
		}, nil, "unknown", false, 0, "unknown"},
		{"offline skips the check", endSession, []string{"--offline"}, "", true, 0, "yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signIn(t)
			tt.setup()

			stdout, _, err := run(t, append([]string{"auth", "status", "-o", "json"}, tt.args...)...)
			if got := exitCode(err); got != tt.wantExit {
				t.Fatalf("exit %d (%v), want %d", got, err, tt.wantExit)
			}
			var rows []struct {
				Session  string `json:"session"`
				SignedIn bool   `json:"signedIn"`
			}
			if err := json.Unmarshal([]byte(stdout), &rows); err != nil || len(rows) != 1 {
				t.Fatalf("json: %v\n%s", err, stdout)
			}
			if rows[0].Session != tt.wantSession || rows[0].SignedIn != tt.wantSigned {
				t.Errorf("session %q signedIn %v, want %q %v", rows[0].Session, rows[0].SignedIn, tt.wantSession, tt.wantSigned)
			}

			signIn(t)
			tt.setup()
			table, _, _ := run(t, append([]string{"auth", "status"}, tt.args...)...)
			if !strings.Contains(table, "  "+tt.wantLabel+"  ") {
				t.Errorf("table doesn't show %q:\n%s", tt.wantLabel, table)
			}
		})
	}
}

// A cached access token proves the session within its lifetime: no
// refresh, so checking doesn't rotate the refresh token for nothing.
func TestAuthStatusReusesCachedToken(t *testing.T) {
	signIn(t)
	if _, _, err := run(t, "auth", "status"); err != nil {
		t.Fatal(err)
	}
	before := len(backend.mintedTokens())
	if _, _, err := run(t, "auth", "status"); err != nil {
		t.Fatal(err)
	}
	if after := len(backend.mintedTokens()); after != before {
		t.Errorf("second status exchanged %d more tokens, want 0", after-before)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ce *clierr.Error
	if errors.As(err, &ce) {
		return ce.ExitCode()
	}
	return Describe(err).ExitCode()
}
