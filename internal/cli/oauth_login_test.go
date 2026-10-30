package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/auth/authtest"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

const oauthProfile = "p-oauth"

// oauthEnv starts a fake Norcube Auth with the OAuth sign-in, a profile
// using it (not signed in), and a "browser" that approves the sign-in by
// following the authorization redirect back to the CLI.
func oauthEnv(t *testing.T) *authtest.Server {
	t.Helper()
	s := authtest.New()
	t.Cleanup(s.Close)
	isolatedConfig(t, func(f *config.File) {
		f.DefaultProfile = oauthProfile
		p := f.Profile(oauthProfile)
		p.Environment = config.EnvCustom
		p.Endpoints = map[string]string{config.ServiceAuth: s.URL}
		p.SetActiveOrg(config.Organization{ID: authtest.HomeOrgID, Slug: "home", Name: "Home"})
	})
	auth.ForgetMetadata(s.URL)
	t.Cleanup(func() { _ = auth.DeleteCredentials(oauthProfile, s.URL) })
	prev := openBrowser
	openBrowser = func(u string) error {
		go func() {
			resp, err := http.Get(u) // #nosec G107 -- the test's fake server
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	t.Cleanup(func() { openBrowser = prev })
	return s
}

func TestOAuthLogin(t *testing.T) {
	s := oauthEnv(t)
	stdout, stderr, err := run(t, "login", "-o", "json")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	var res loginResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if res.User.Email != "dev@example.com" || res.Organization == nil || res.Organization.Slug != "home" ||
		strings.Join(res.Scopes, " ") != "full org.read offline_access" {
		t.Fatalf("login result: %+v", res)
	}
	if !strings.Contains(stderr, "Opening the Norcube sign-in page") || !strings.Contains(stderr, s.URL+"/oauth/authorize?") {
		t.Errorf("stderr:\n%s", stderr)
	}
	sess, _ := auth.LoadSession(oauthProfile, s.URL)
	if sess.Format != auth.FormatOAuth {
		t.Fatalf("stored format %d", sess.Format)
	}

	out, _, err := run(t, "whoami")
	if err != nil || !strings.Contains(out, "Access:      full") || !strings.Contains(out, "dev@example.com") {
		t.Fatalf("whoami: %v\n%s", err, out)
	}
	out, _, err = run(t, "auth", "status", "-o", "json")
	if err != nil || !strings.Contains(out, `"credentialFormat": 2`) || !strings.Contains(out, `"session": "active"`) {
		t.Fatalf("auth status: %v\n%s", err, out)
	}

	// Signed in already: no second sign-in.
	out, _, err = run(t, "login")
	if err != nil || !strings.Contains(out, "Already signed in") {
		t.Fatalf("second login: %v %q", err, out)
	}
	if n := s.Stats().CodeExchanges; n != 1 {
		t.Fatalf("%d code exchanges", n)
	}
}

func TestOAuthLoginWithScopes(t *testing.T) {
	s := oauthEnv(t)
	out, stderr, err := run(t, "login", "--scopes", "backup.read")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "Access: limited to backup.read.") {
		t.Errorf("login output:\n%s", out)
	}
	if got := s.Stats().AuthorizeQuery.Get("scope"); got != "backup.read offline_access" {
		t.Errorf("asked for %q", got)
	}

	// In scope: a Backup token. Out of scope: exit 5 and how to fix it.
	if _, _, err := run(t, "auth", "token", "--audience", "backup"); err != nil {
		t.Fatalf("token in scope: %v", err)
	}
	_, _, err = run(t, "auth", "token", "--audience", "langsync")
	e := Describe(err)
	if e.Code != clierr.CodeInsufficientScope || e.ExitCode() != clierr.ExitAccess ||
		e.Hint != "This login is limited to backup.read; run `norcube login` for full access." {
		t.Fatalf("out of scope: %+v (%v)", e, err)
	}

	out, _, err = run(t, "auth", "status")
	if err != nil || !strings.Contains(out, "SCOPES") || !strings.Contains(out, "backup.read") {
		t.Fatalf("auth status: %v\n%s", err, out)
	}
	out, _, err = run(t, "whoami", "-o", "json")
	if err != nil || !strings.Contains(out, `"backup.read"`) {
		t.Fatalf("whoami: %v\n%s", err, out)
	}
}

// A product's own scope refusal under a limited login gets the same hint.
func TestScopeRefusalHintUnderLimitedLogin(t *testing.T) {
	s := oauthEnv(t)
	if _, stderr, err := run(t, "login", "--scopes", "backup.read,langsync.sync"); err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	selectedProfile, selectedAuthURL = oauthProfile, s.URL
	t.Cleanup(func() { selectedAuthURL = "" })
	e := Describe(&clierr.Error{Code: clierr.CodeInsufficientScope, Message: "insufficient scope", HTTPStatus: http.StatusForbidden})
	if e.Hint != "This login is limited to backup.read, langsync.sync; run `norcube login` for full access." {
		t.Fatalf("hint %q", e.Hint)
	}
}

func TestLoginScopesValidation(t *testing.T) {
	s := oauthEnv(t)
	_, _, err := run(t, "login", "--scopes", "backup.read,backup.delete")
	if !clierr.IsUsage(err) || !strings.Contains(err.Error(), `unknown scope "backup.delete"`) || !strings.Contains(err.Error(), "langsync.sync") {
		t.Fatalf("got %v", err)
	}
	if s.Stats().Authorizations != 0 {
		t.Fatal("a sign-in started despite the bad scope")
	}
}

func TestLoginScopesCompletion(t *testing.T) {
	got, _ := completeLoginScopes(nil, nil, "backup.read,")
	if len(got) == 0 || !strings.HasPrefix(got[0], "backup.read,full\t") {
		t.Fatalf("got %v", got)
	}
	for _, c := range got {
		if strings.HasPrefix(c, "backup.read,backup.read") {
			t.Fatalf("offered a scope already given: %v", got)
		}
	}
}

// Against a Norcube Auth without the OAuth sign-in, login uses the older
// one, and --scopes is refused.
func TestLoginFallsBackToTheOlderSignIn(t *testing.T) {
	s := oauthEnv(t)
	s.Lock()
	s.NoExchange = true
	s.Unlock()
	calls := 0
	prev := browserLogin
	browserLogin = func(context.Context, auth.BrowserLoginOptions) (*auth.CallbackPayload, error) {
		calls++
		return &auth.CallbackPayload{
			AccessToken: testJWT(auth.AudienceAuth, ""), RefreshToken: "legacy-refresh", AccessExpiresIn: 300,
			User:       auth.User{ID: "user-1", Name: "Dev", Email: "dev@example.com"},
			DefaultOrg: auth.Organization{ID: authtest.HomeOrgID, Slug: "home", Name: "Home"},
		}, nil
	}
	t.Cleanup(func() { browserLogin = prev })

	if _, _, err := run(t, "login", "--scopes", "backup.read"); !clierr.IsUsage(err) || !strings.Contains(err.Error(), "needs the OAuth sign-in") {
		t.Fatalf("--scopes on an older server: %v", err)
	}
	out, stderr, err := run(t, "login")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	if calls != 1 || strings.Contains(out, "Access:") || s.Stats().Authorizations != 0 {
		t.Fatalf("calls %d, authorizations %d, output:\n%s", calls, s.Stats().Authorizations, out)
	}
	if sess, _ := auth.LoadSession(oauthProfile, s.URL); sess.Format != auth.FormatLegacy {
		t.Fatalf("format %d", sess.Format)
	}
}

func TestOAuthDeviceLoginPassesScopes(t *testing.T) {
	oauthEnv(t)
	var got auth.DeviceLoginOptions
	prev := deviceLogin
	deviceLogin = func(ctx context.Context, opts auth.DeviceLoginOptions) (*auth.CallbackPayload, error) {
		got = opts
		return nil, &clierr.Error{Code: clierr.CodeLoginRequired, Message: auth.ErrDeviceDenied.Error(), Err: auth.ErrDeviceDenied}
	}
	t.Cleanup(func() { deviceLogin = prev })
	_, _, err := run(t, "login", "--device", "--scopes", "langsync.sync")
	if !errors.Is(err, auth.ErrDeviceDenied) {
		t.Fatalf("got %v", err)
	}
	if got.Metadata == nil || !got.Metadata.SupportsDevice() || strings.Join(got.Scopes, ",") != "langsync.sync" {
		t.Fatalf("device options: %+v", got)
	}
}

// logout revokes an OAuth sign-in's refresh token (RFC 7009) instead of
// the older sign-out endpoint, then forgets it; a failed revocation is
// reported, and the local sign-out happens anyway.
func TestOAuthLogout(t *testing.T) {
	s := oauthEnv(t)
	if _, stderr, err := run(t, "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	rt, _ := auth.LoadRefreshToken(oauthProfile, s.URL)
	g := s.Grant(rt)
	out, _, err := run(t, "logout", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if res := logoutJSON(t, out); res.ServerSessionEnded == nil || !*res.ServerSessionEnded {
		t.Fatalf("logout result: %s", out)
	}
	st := s.Stats()
	if len(st.Revocations) != 1 || st.Revocations[0].Get("token") != rt || len(st.SignOuts) != 0 || !s.Revoked(g) {
		t.Fatalf("revocations %v, sign-outs %v", st.Revocations, st.SignOuts)
	}
	if auth.HasCredentials(oauthProfile, s.URL) {
		t.Fatal("credentials kept")
	}

	if _, stderr, err := run(t, "login", "--force"); err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	s.Lock()
	s.RevokeStatus = http.StatusServiceUnavailable
	s.Unlock()
	out, stderr, err := run(t, "logout", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if res := logoutJSON(t, out); res.ServerSessionEnded == nil || *res.ServerSessionEnded {
		t.Fatalf("logout result after a failed revocation: %s", out)
	}
	if !strings.Contains(stderr, "couldn't end the session on the server") {
		t.Errorf("stderr: %s", stderr)
	}
	if auth.HasCredentials(oauthProfile, s.URL) {
		t.Fatal("credentials kept after a failed revocation")
	}
}

// An older sign-in moves to OAuth on its first command, once, and doctor
// reports the format before and after.
func TestOlderSignInUpgradesOnce(t *testing.T) {
	s := oauthEnv(t)
	s.AddLegacySession("legacy-refresh")
	if err := auth.SaveRefreshToken(oauthProfile, s.URL, "legacy-refresh"); err != nil {
		t.Fatal(err)
	}
	checks, _ := doctorReport(t, "--offline")
	if c := checks["credentials"]; c.Status != checkWarn || !strings.Contains(c.Detail, "older sign-in (format 1)") {
		t.Fatalf("doctor before: %+v", c)
	}
	for range 2 {
		if out, stderr, err := run(t, "whoami"); err != nil || !strings.Contains(out, "Access:      full") {
			t.Fatalf("whoami: %v\n%s%s", err, out, stderr)
		}
	}
	st := s.Stats()
	if st.Upgrades != 1 || st.UpgradeRefusals != 0 || st.LegacyRefreshes != 0 || st.Refreshes != 0 {
		t.Fatalf("upgrades %d refusals %d legacy refreshes %d refreshes %d", st.Upgrades, st.UpgradeRefusals, st.LegacyRefreshes, st.Refreshes)
	}
	checks, _ = doctorReport(t, "--offline")
	if c := checks["credentials"]; c.Status != checkOK || !strings.Contains(c.Detail, "OAuth sign-in (format 2), scopes full") {
		t.Fatalf("doctor after: %+v", c)
	}
}

// A sign-in Norcube Auth won't move (no recorded session) asks for one
// login, exit 4.
func TestOlderSignInThatCantUpgradeAsksForLogin(t *testing.T) {
	s := oauthEnv(t)
	if err := auth.SaveRefreshToken(oauthProfile, s.URL, "sid-less"); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "whoami")
	if e := Describe(err); e.ExitCode() != clierr.ExitAuth || !strings.Contains(err.Error(), "sign in once more") {
		t.Fatalf("got %v (exit %d)", err, e.ExitCode())
	}
}

func TestDoctorWithLimitedLogin(t *testing.T) {
	s := oauthEnv(t)
	if _, stderr, err := run(t, "login", "--scopes", "backup.read"); err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	for _, key := range []string{config.ServiceSnapDB, config.ServiceLangsync, config.ServiceDomainradar, config.ServiceBilling} {
		t.Setenv(config.ServiceEnvVar(key), s.URL)
	}
	checks, _ := doctorReport(t)
	if c := checks["credentials"]; c.Status != checkOK || !strings.Contains(c.Detail, "scopes backup.read") {
		t.Errorf("credentials: %+v", c)
	}
	if c := checks["token backup"]; c.Status != checkOK {
		t.Errorf("token backup: %+v", c)
	}
	if c := checks["token langsync"]; c.Status != checkSkip || !strings.Contains(c.Detail, "don't cover") {
		t.Errorf("token langsync: %+v", c)
	}
}

func TestGoldenLoginHelp(t *testing.T) {
	out, _, err := run(t, "login", "--help")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "help-login.txt", out, "http://no-server.invalid")
}

// An account without an organization still signs in: /auth/me and the
// organization list need no organization; product commands do.
func TestOAuthLoginWithoutOrganization(t *testing.T) {
	s := oauthEnv(t)
	s.Lock()
	s.NoDefaultOrg = true
	s.Unlock()
	isolatedConfig(t, func(f *config.File) {
		f.DefaultProfile = oauthProfile
		p := f.Profile(oauthProfile)
		p.Environment = config.EnvCustom
		p.Endpoints = map[string]string{config.ServiceAuth: s.URL}
	})
	out, stderr, err := run(t, "login")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "Signed in as Dev Eloper (dev@example.com)") || !strings.Contains(out, "norcube org create") {
		t.Fatalf("login output:\n%s", out)
	}
	if out, _, err := run(t, "whoami"); err != nil || !strings.Contains(out, "dev@example.com") {
		t.Fatalf("whoami: %v\n%s", err, out)
	}
	if out, _, err := run(t, "org", "list"); err != nil || !strings.Contains(out, "acme") {
		t.Fatalf("org list: %v\n%s", err, out)
	}
	_, _, err = run(t, "auth", "token", "--audience", "backup")
	if !clierr.IsUsage(err) {
		t.Fatalf("product token without an organization: %v", err)
	}
}

// login --force ends the session it replaces on the server, the way
// logout does: revocation for an OAuth sign-in, the older sign-out for an
// older one. A failure warns and the new login goes ahead.
func TestLoginForceEndsThePreviousSession(t *testing.T) {
	t.Run("oauth", func(t *testing.T) {
		s := oauthEnv(t)
		if _, stderr, err := run(t, "login"); err != nil {
			t.Fatalf("login: %v\n%s", err, stderr)
		}
		old, _ := auth.LoadRefreshToken(oauthProfile, s.URL)
		g := s.Grant(old)
		if _, stderr, err := run(t, "login", "--force"); err != nil {
			t.Fatalf("login --force: %v\n%s", err, stderr)
		}
		st := s.Stats()
		if len(st.Revocations) != 1 || st.Revocations[0].Get("token") != old || !s.Revoked(g) || len(st.SignOuts) != 0 {
			t.Fatalf("revocations %v, sign-outs %v", st.Revocations, st.SignOuts)
		}
		if now, _ := auth.LoadRefreshToken(oauthProfile, s.URL); now == old || now == "" {
			t.Fatal("the new session was not stored")
		}

		// The server fails: a warning, and the new session anyway.
		s.Lock()
		s.RevokeStatus = http.StatusServiceUnavailable
		s.Unlock()
		prev, _ := auth.LoadRefreshToken(oauthProfile, s.URL)
		_, stderr, err := run(t, "login", "--force")
		if err != nil {
			t.Fatalf("login --force with a failing server: %v\n%s", err, stderr)
		}
		if !strings.Contains(stderr, "couldn't end the previous session") {
			t.Errorf("stderr: %s", stderr)
		}
		if now, _ := auth.LoadRefreshToken(oauthProfile, s.URL); now == prev || now == "" {
			t.Fatal("the new session was not stored after a failed revocation")
		}
	})
	t.Run("older sign-in", func(t *testing.T) {
		s := oauthEnv(t)
		s.AddLegacySession("legacy-refresh")
		if err := auth.SaveRefreshToken(oauthProfile, s.URL, "legacy-refresh"); err != nil {
			t.Fatal(err)
		}
		if _, stderr, err := run(t, "login", "--force"); err != nil {
			t.Fatalf("login --force: %v\n%s", err, stderr)
		}
		st := s.Stats()
		if len(st.SignOuts) != 1 || st.SignOuts[0].Get("refresh_token") != "legacy-refresh" || len(st.Revocations) != 0 {
			t.Fatalf("sign-outs %v, revocations %v", st.SignOuts, st.Revocations)
		}
		if sess, _ := auth.LoadSession(oauthProfile, s.URL); sess.Format != auth.FormatOAuth {
			t.Fatalf("format %d", sess.Format)
		}
	})
}
