package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/norcubeplatform/cli/internal/auth/authtest"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/credstore"
	"github.com/norcubeplatform/cli/internal/registry"
)

// browserFollows plays the browser: it opens the authorization URL and
// follows the redirect back to the CLI's loopback callback.
func browserFollows(t *testing.T) func(string) error {
	t.Helper()
	return func(u string) error {
		go func() {
			resp, err := http.Get(u) // #nosec G107 -- the test's own fake server
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
		return nil
	}
}

func discover(t *testing.T, s *authtest.Server) *Metadata {
	t.Helper()
	ForgetMetadata(s.URL)
	m, err := Discover(context.Background(), s.Client(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func oauthLogin(t *testing.T, s *authtest.Server, opts OAuthLoginOptions) (*CallbackPayload, error) {
	t.Helper()
	opts.Metadata = discover(t, s)
	opts.HTTP = s.Client()
	if opts.BrowserOpenURL == nil {
		opts.BrowserOpenURL = browserFollows(t)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	return OAuthLogin(context.Background(), opts)
}

func TestDiscover(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	m := discover(t, s)
	if !m.SupportsCLISignIn() || !m.SupportsDevice() || !m.IssParameterSupported || m.RevocationEndpoint != s.URL+"/oauth/revoke" {
		t.Fatalf("metadata: %+v", m)
	}

	// Cached: the server going away doesn't matter.
	s.Close()
	if _, err := Discover(context.Background(), http.DefaultClient, s.URL); err != nil {
		t.Fatalf("cached metadata not used: %v", err)
	}
}

func TestDiscoverWithoutOAuth(t *testing.T) {
	for name, set := range map[string]func(*authtest.Server){
		"no metadata":       func(s *authtest.Server) { s.OAuthOff = true },
		"no token exchange": func(s *authtest.Server) { s.NoExchange = true },
	} {
		t.Run(name, func(t *testing.T) {
			s := authtest.New()
			defer s.Close()
			set(s)
			ForgetMetadata(s.URL)
			if _, err := Discover(context.Background(), s.Client(), s.URL); !errors.Is(err, ErrOAuthUnavailable) {
				t.Fatalf("got %v, want ErrOAuthUnavailable", err)
			}
			// The answer is cached too, for a shorter time.
			s.Close()
			if _, err := Discover(context.Background(), http.DefaultClient, s.URL); !errors.Is(err, ErrOAuthUnavailable) {
				t.Fatalf("not cached: %v", err)
			}
		})
	}
}

// RFC 8414 §3.3: metadata naming another issuer is refused, and network
// errors are not cached.
func TestDiscoverRejectsAnotherIssuer(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	ForgetMetadata(s.URL + "/")
	other := "http://" + strings.Replace(strings.TrimPrefix(s.URL, "http://"), "127.0.0.1", "localhost", 1)
	ForgetMetadata(other)
	if _, err := Discover(context.Background(), s.Client(), other); err == nil || !strings.Contains(err.Error(), "names issuer") {
		t.Fatalf("got %v", err)
	}
	if _, ok := cachedMetadata(other); ok {
		t.Fatal("a refused answer was cached")
	}
}

func TestOAuthLogin(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	var shown string
	p, err := oauthLogin(t, s, OAuthLoginOptions{OnURLReady: func(u string) { shown = u }})
	if err != nil {
		t.Fatal(err)
	}
	if !p.OAuth || p.RefreshToken == "" || p.AccessToken == "" || p.AccessExpiresIn != 299 {
		t.Fatalf("payload: %+v", p)
	}
	if strings.Join(p.Scopes, " ") != "full org.read offline_access" || p.DefaultOrg.Slug != "home" {
		t.Fatalf("scopes %v, org %+v", p.Scopes, p.DefaultOrg)
	}
	q := s.Stats().AuthorizeQuery
	if !strings.HasPrefix(shown, s.URL+"/oauth/authorize?") {
		t.Errorf("printed URL %q", shown)
	}
	if q.Get("scope") != "full offline_access" || q.Get("client_id") != ClientID || q.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize query: %v", q)
	}
	if q.Get("client_user_agent") == "" || !strings.HasPrefix(q.Get("client_user_agent"), "norcube/") || q.Get("device_name") != deviceName() {
		t.Errorf("device details: %v", q)
	}
	if len(q.Get("state")) != 64 || len(q.Get("code_challenge")) != 43 {
		t.Errorf("state %q, challenge %q", q.Get("state"), q.Get("code_challenge"))
	}
	if n := s.Stats().CodeExchanges; n != 1 {
		t.Fatalf("%d code exchanges", n)
	}
}

func TestOAuthLoginScopes(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	p, err := oauthLogin(t, s, OAuthLoginOptions{Scopes: []string{"backup.read", "langsync.sync"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Stats().AuthorizeQuery.Get("scope"); got != "backup.read langsync.sync offline_access" {
		t.Errorf("scope %q", got)
	}
	if DescribeScopes(p.Scopes) != "backup.read, langsync.sync" || ScopesFull(p.Scopes) {
		t.Errorf("granted %v", p.Scopes)
	}
}

func TestPKCE(t *testing.T) {
	v, c, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 43 || strings.ContainsAny(v, "+/=") {
		t.Errorf("verifier %q", v)
	}
	// RFC 7636 appendix B.
	if got := pkceChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("S256 challenge %q", got)
	}
	if c != pkceChallenge(v) {
		t.Error("challenge doesn't match the verifier")
	}
}

// The callback must carry our state and the server's issuer; a browser
// sent there by anyone else, or a response from another server, is
// refused before any code is exchanged.
func TestOAuthLoginCallbackChecks(t *testing.T) {
	cases := map[string]struct {
		set  func(*authtest.Server)
		open func(t *testing.T) func(string) error
		want string
	}{
		"wrong issuer":   {set: func(s *authtest.Server) { s.WrongIss = true }, want: "came from issuer"},
		"missing issuer": {set: func(s *authtest.Server) { s.IssOff = true }, want: ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := authtest.New()
			defer s.Close()
			if c.set != nil {
				c.set(s)
			}
			opts := OAuthLoginOptions{}
			if c.open != nil {
				opts.BrowserOpenURL = c.open(t)
			}
			_, err := oauthLogin(t, s, opts)
			if c.want == "" {
				// A server that doesn't send iss (and says so) is fine.
				if err != nil {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if s.Stats().CodeExchanges != 0 {
				t.Fatal("a code was exchanged after a failed check")
			}
		})
	}
}

// A request to the callback without this sign-in's state (another local
// process, a page probing the port) is refused, and the login carries on
// with the real browser; a second answer after that is refused too.
func TestOAuthLoginIgnoresForeignCallbacks(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	follow := browserFollows(t)
	var forged, late int
	opts := OAuthLoginOptions{BrowserOpenURL: func(u string) error {
		q, _ := url.Parse(u)
		redirect := q.Query().Get("redirect_uri")
		resp, err := http.Get(redirect + "?code=stolen&state=forged") // #nosec G107 -- loopback in a test
		if err != nil {
			return err
		}
		forged = resp.StatusCode
		_ = resp.Body.Close()
		if err := follow(u); err != nil {
			return err
		}
		resp, err = http.Get(redirect + "?code=again&state=forged") // #nosec G107 -- loopback in a test
		if err == nil {
			late = resp.StatusCode
			_ = resp.Body.Close()
		}
		return nil
	}}
	p, err := oauthLogin(t, s, opts)
	if err != nil || p == nil {
		t.Fatalf("login aborted by a foreign callback: %v", err)
	}
	if forged != http.StatusBadRequest {
		t.Errorf("forged callback answered %d, want 400", forged)
	}
	if late != 0 && late != http.StatusBadRequest {
		t.Errorf("late forged callback answered %d", late)
	}
	if n := s.Stats().CodeExchanges; n != 1 {
		t.Errorf("%d code exchanges, want 1 (the real code)", n)
	}
}

// A server that advertises iss but leaves it out is refused.
func TestCheckCallbackRequiresAdvertisedIssuer(t *testing.T) {
	m := &Metadata{Issuer: "https://auth.example", IssParameterSupported: true}
	if _, err := checkCallback(url.Values{"state": {"s"}, "code": {"c"}}, "s", m); err == nil {
		t.Fatal("missing iss accepted")
	}
	if code, err := checkCallback(url.Values{"state": {"s"}, "code": {"c"}, "iss": {"https://auth.example/"}}, "s", m); err != nil || code != "c" {
		t.Fatalf("%q %v", code, err)
	}
}

func TestOAuthLoginErrors(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	s.Deny = true
	_, err := oauthLogin(t, s, OAuthLoginOptions{})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != clierr.CodeLoginRequired || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied: %v", err)
	}

	s.Lock()
	s.Deny = false
	s.Unlock()
	_, err = oauthLogin(t, s, OAuthLoginOptions{Scopes: []string{"nope.read"}})
	if !errors.As(err, &ce) || ce.Code != clierr.CodeUsage || !strings.Contains(err.Error(), "invalid_scope") {
		t.Fatalf("invalid scope: %v", err)
	}
}

func TestOAuthLoginTimesOut(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	_, err := oauthLogin(t, s, OAuthLoginOptions{NoBrowser: true, Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
}

func TestParseLoginScopes(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "full",
		"full":                      "full",
		"langsync.sync,backup.read": "backup.read langsync.sync",
		"backup.read,full":          "full",
		"org.read,backup.read":      "backup.read",
		" backup.read , ":           "backup.read",
		// Unknown here but well-formed: sent, Norcube Auth decides.
		"backup.delete,backup.read": "backup.read backup.delete",
		"vault,backup.read,vault":   "backup.read vault",
		"backup.delete,full":        "full",
	} {
		got, err := ParseLoginScopes(strings.Split(in, ","))
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%q: %v %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"Backup.Read", "backup.read.all", "backup read", "backup_read", "org.read", "offline_access,org.read"} {
		if _, err := ParseLoginScopes(strings.Split(bad, ",")); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDeviceLoginOAuth(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	s.DeviceAnswers = []string{"authorization_pending", "slow_down", "authorization_pending"}
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	p, err := DeviceLogin(context.Background(), DeviceLoginOptions{
		AuthURL: s.URL, HTTP: s.Client(), Metadata: discover(t, s), Scopes: []string{"backup.read"},
		Hostname: "build-box", sleep: clock.sleep, now: func() time.Time { return clock.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.OAuth || p.RefreshToken == "" || DescribeScopes(p.Scopes) != "backup.read" {
		t.Fatalf("payload %+v", p)
	}
	f := s.Stats().DeviceCodeForm
	if f.Get("scope") != "backup.read offline_access" || f.Get("device_name") != "build-box" || !strings.HasPrefix(f.Get("client_user_agent"), "norcube/") || f.Get("hostname") != "" {
		t.Errorf("device code form: %v", f)
	}
	// 5s, 5s, then 10s after slow_down, 10s.
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("sleeps %v, want %v", clock.sleeps, want)
	}
	for i := range want {
		if clock.sleeps[i] != want[i] {
			t.Fatalf("sleeps %v, want %v", clock.sleeps, want)
		}
	}
}

func TestDeviceLoginOAuthEndings(t *testing.T) {
	for answer, want := range map[string]error{"access_denied": ErrDeviceDenied, "expired_token": ErrDeviceExpired} {
		t.Run(answer, func(t *testing.T) {
			s := authtest.New()
			defer s.Close()
			s.DeviceAnswers = []string{answer}
			clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
			_, err := DeviceLogin(context.Background(), DeviceLoginOptions{
				AuthURL: s.URL, HTTP: s.Client(), Metadata: discover(t, s),
				sleep: clock.sleep, now: func() time.Time { return clock.now },
			})
			var ce *clierr.Error
			if !errors.Is(err, want) || !errors.As(err, &ce) || ce.ExitCode() != clierr.ExitAuth {
				t.Fatalf("got %v", err)
			}
		})
	}
	t.Run("invalid_scope", func(t *testing.T) {
		s := authtest.New()
		defer s.Close()
		_, err := DeviceLogin(context.Background(), DeviceLoginOptions{
			AuthURL: s.URL, HTTP: s.Client(), Metadata: discover(t, s), Scopes: []string{"nope"},
		})
		var ce *clierr.Error
		if !errors.As(err, &ce) || ce.Code != clierr.CodeUsage {
			t.Fatalf("got %v", err)
		}
	})
}

// signedInOAuth stores an OAuth sign-in for the fake's new grant.
func signedInOAuth(t *testing.T, s *authtest.Server, scopes ...string) *authtest.Grant {
	t.Helper()
	keyring.MockInit()
	forgetMemory("", s.URL)
	g := s.NewGrant(scopes...)
	if err := SaveOAuthSession("", s.URL, s.CurrentRefresh(g), "", time.Time{}, g.Scopes); err != nil {
		t.Fatal(err)
	}
	return g
}

func oauthSource(t *testing.T, s *authtest.Server, audience, org string) *TokenSource {
	t.Helper()
	ts := NewTokenSource(s.URL, audience, org)
	ts.HTTP = s.Client()
	ts.LockDir = t.TempDir()
	return ts
}

// One refresh, then one exchange per (audience, org); repeated calls
// come from memory, and the rotated refresh token is stored.
func TestOAuthTokenSource(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	g := signedInOAuth(t, s)
	first := s.CurrentRefresh(g)
	_ = discover(t, s)

	for range 3 {
		for _, aud := range []string{AudienceSnapDB, AudienceLangsync, AudienceAuth} {
			tok, err := oauthSource(t, s, aud, authtest.HomeOrgID).Token(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if a, org, _, ok := s.ProductToken(tok); !ok || a != aud || org != authtest.HomeOrgID {
				t.Fatalf("token for %s: %v %s %s", aud, ok, a, org)
			}
		}
	}
	if r, e, u := s.Counts(); r != 1 || e != 3 || u != 0 {
		t.Fatalf("refreshes %d exchanges %d upgrades %d, want 1 3 0", r, e, u)
	}
	stored, _ := LoadRefreshToken("", s.URL)
	if stored == first || stored != s.CurrentRefresh(g) {
		t.Fatalf("stored refresh %q, server has %q (first %q)", stored, s.CurrentRefresh(g), first)
	}
	sess, _ := LoadSession("", s.URL)
	if sess.Format != FormatOAuth || sess.CLIAccessExpiresAt.IsZero() || strings.Join(sess.Scopes, " ") != "full org.read offline_access" {
		t.Fatalf("session %+v", sess)
	}
	if exp := AccessExpiry("", s.URL, AudienceSnapDB, authtest.HomeOrgID); exp.IsZero() {
		t.Fatal("AccessExpiry doesn't see the in-memory token")
	}
}

func TestOAuthTokenErrors(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)

	t.Run("not a member", func(t *testing.T) {
		signedInOAuth(t, s)
		_, err := oauthSource(t, s, AudienceSnapDB, authtest.OtherOrgID).Token(context.Background())
		if !errors.Is(err, ErrOrgAccessDenied) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("insufficient scope", func(t *testing.T) {
		signedInOAuth(t, s, "backup.read")
		_, err := oauthSource(t, s, AudienceLangsync, authtest.HomeOrgID).Token(context.Background())
		var se *ScopeError
		if !errors.As(err, &se) || se.Audience != AudienceLangsync || DescribeScopes(se.Scopes) != "backup.read" {
			t.Fatalf("got %v", err)
		}
		if _, err := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID).Token(context.Background()); err != nil {
			t.Fatalf("in scope: %v", err)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		g := signedInOAuth(t, s)
		s.RevokeGrant(g)
		_, err := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID).Token(context.Background())
		if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "ended this sign-in") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no organization", func(t *testing.T) {
		signedInOAuth(t, s)
		// Norcube Auth's own token needs none (organization_id omitted;
		// the fake refuses an empty one)...
		tok, err := oauthSource(t, s, AudienceAuth, "").Token(context.Background())
		if err != nil {
			t.Fatalf("auth token without an organization: %v", err)
		}
		if aud, org, _, ok := s.ProductToken(tok); !ok || aud != AudienceAuth || org != "" {
			t.Fatalf("token: %v %s %q", ok, aud, org)
		}
		// ...a product's does, and that's caught before any request.
		_, err = oauthSource(t, s, AudienceSnapDB, "").Token(context.Background())
		if !clierr.IsUsage(err) {
			t.Fatalf("product token without an organization: %v", err)
		}
	})
}

// A CLI access token the exchange refuses (expired early) costs one
// refresh, then the exchange is repeated.
func TestOAuthExchangeRetriesAfterRefresh(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)
	g := signedInOAuth(t, s)
	if _, err := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.ExpireAccess(g)
	if _, err := oauthSource(t, s, AudienceLangsync, authtest.HomeOrgID).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, e, _ := s.Counts(); r != 2 || e != 2 {
		t.Fatalf("refreshes %d exchanges %d, want 2 2", r, e)
	}
}

// The refresh token this process read was rotated by another process
// meanwhile: the refused refresh reads the store again and uses the new one.
func TestOAuthRefreshReadsTheStoredTokenAgain(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)
	g := signedInOAuth(t, s)
	stale := s.CurrentRefresh(g)
	// Another process refreshed: the server rotated, and its new token
	// reaches the store only after this process read the old one.
	fresh := s.Rotate(g)
	c, _ := loadCreds("", s.URL)
	if c.RefreshToken != stale {
		t.Fatal("setup")
	}
	if err := SaveOAuthSession("", s.URL, fresh, "", time.Time{}, g.Scopes); err != nil {
		t.Fatal(err)
	}
	ts := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID)
	tok, err := ts.refreshCLIAccess(context.Background(), c, true)
	if err != nil || tok == "" {
		t.Fatalf("got %q %v", tok, err)
	}
	if s.Stats().Reuses != 0 {
		t.Fatal("the stale token counted as reuse")
	}
	if now, _ := LoadRefreshToken("", s.URL); now != s.CurrentRefresh(g) {
		t.Fatal("the rotated token was not stored")
	}
}

// An older sign-in is moved to OAuth once; later calls use the OAuth
// sign-in and never present the legacy token again.
func TestLegacyUpgradeOnce(t *testing.T) {
	keyring.MockInit()
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)
	s.AddLegacySession("legacy-refresh")
	if err := SaveRefreshToken("", s.URL, "legacy-refresh"); err != nil {
		t.Fatal(err)
	}
	for _, aud := range []string{AudienceSnapDB, AudienceLangsync, AudienceSnapDB} {
		if _, err := oauthSource(t, s, aud, authtest.HomeOrgID).Token(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if st := s.Stats(); st.Upgrades != 1 || st.UpgradeRefusals != 0 || st.LegacyRefreshes != 0 {
		t.Fatalf("upgrades %d refusals %d legacy refreshes %d", st.Upgrades, st.UpgradeRefusals, st.LegacyRefreshes)
	}
	sess, _ := LoadSession("", s.URL)
	if sess.Format != FormatOAuth || !ScopesFull(sess.Scopes) {
		t.Fatalf("after upgrade: %+v", sess)
	}
}

// A token Norcube Auth won't upgrade (no session recorded, or ended) asks
// for one login; nothing is stored over it.
func TestLegacyUpgradeRefused(t *testing.T) {
	keyring.MockInit()
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)
	if err := SaveRefreshToken("", s.URL, "sid-less"); err != nil {
		t.Fatal(err)
	}
	_, err := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID).Token(context.Background())
	if !errors.Is(err, ErrLoginRequired) || !strings.Contains(err.Error(), "sign in once more") {
		t.Fatalf("got %v", err)
	}
	if sess, _ := LoadSession("", s.URL); sess.Format != FormatLegacy {
		t.Fatalf("format %d", sess.Format)
	}
}

// Against a Norcube Auth without the OAuth sign-in the older refresh
// keeps working and nothing is upgraded.
func TestLegacyFallback(t *testing.T) {
	keyring.MockInit()
	s := authtest.New()
	defer s.Close()
	s.OAuthOff = true
	ForgetMetadata(s.URL)
	s.AddLegacySession("legacy-refresh")
	if err := SaveRefreshToken("", s.URL, "legacy-refresh"); err != nil {
		t.Fatal(err)
	}
	if _, err := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.Upgrades != 0 || st.LegacyRefreshes != 1 {
		t.Fatalf("upgrades %d legacy refreshes %d", st.Upgrades, st.LegacyRefreshes)
	}
	if sess, _ := LoadSession("", s.URL); sess.Format != FormatLegacy {
		t.Fatalf("format %d", sess.Format)
	}
}

// Exchanges take no lock: with another process holding the profile's
// lock (a refresh in progress), a token is still minted from the cached
// CLI access token.
func TestExchangeNeedsNoLock(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)
	signedInOAuth(t, s)
	ts := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID)
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	unlock, err := LockProfile(context.Background(), ts.LockDir, "", s.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	other := oauthSource(t, s, AudienceLangsync, authtest.HomeOrgID)
	other.LockDir, other.LockTimeout = ts.LockDir, 100*time.Millisecond
	if _, err := other.Token(context.Background()); err != nil {
		t.Fatalf("the exchange waited for the lock: %v", err)
	}
}

// Many goroutines asking for tokens of several services at once: one
// refresh, exchanges in parallel, one per (service, org).
func TestConcurrentOAuthTokens(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	s.Delay = 30 * time.Millisecond
	_ = discover(t, s)
	signedInOAuth(t, s)
	lockDir := t.TempDir()
	auds := []string{AudienceSnapDB, AudienceLangsync, AudienceDomainradar, AudienceBilling, AudienceAuth}
	errs := make(chan error, 50)
	for i := range 50 {
		go func() {
			ts := NewTokenSource(s.URL, auds[i%len(auds)], authtest.HomeOrgID)
			ts.HTTP, ts.LockDir = s.Client(), lockDir
			_, err := ts.Token(context.Background())
			errs <- err
		}()
	}
	for range 50 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	st := s.Stats()
	if st.Refreshes != 1 || st.Exchanges != len(auds) || st.Reuses != 0 || st.GraceHits != 0 {
		t.Fatalf("refreshes %d exchanges %d reuses %d grace hits %d; want 1 %d 0 0", st.Refreshes, st.Exchanges, st.Reuses, st.GraceHits, len(auds))
	}
	if s.MaxConcurrentExchanges() < 2 {
		t.Fatalf("exchanges ran one at a time (max %d in flight)", s.MaxConcurrentExchanges())
	}
}

// limitedStore caps values like the Windows Credential Manager.
type limitedStore struct {
	fileStore
	max int
}

func (l limitedStore) Set(key, value string) error {
	if len(value) > l.max {
		return credstore.ErrTooBig
	}
	return l.fileStore.Set(key, value)
}

// A format 2 blob with tokens of realistic size fits the 2560-byte
// Windows limit; one that doesn't keeps the refresh token and scopes.
func TestOAuthCredentialsFitTheWindowsKeyring(t *testing.T) {
	st := limitedStore{fileStore{dir: t.TempDir()}, 2560}
	prev := SetStore(st)
	defer SetStore(prev)
	const api = "https://auth.api.example.com"
	refresh := "ory_rt_" + strings.Repeat("r", 43) + "." + strings.Repeat("s", 43)
	// An RS256 JWT with the cli_access claims is about 1.1 KB.
	access := strings.Repeat("a", 36) + "." + strings.Repeat("p", 520) + "." + strings.Repeat("s", 342)
	scopes := []string{"backup.read", "backup.write", "backup.verify", "domainradar.check", "langsync.read", "langsync.sync",
		"langsync.write", "billing.read", "cms.read", "cms.write", "org.read", "offline_access"}
	if err := SaveOAuthSession("", api, refresh, access, time.Now().Add(5*time.Minute), scopes); err != nil {
		t.Fatal(err)
	}
	c, _ := loadCreds("", api)
	if c.CLIAccess == nil || c.CLIAccess.Token != access || c.RefreshToken != refresh || len(c.Scopes) != len(scopes) {
		t.Fatalf("not stored whole: %+v", c)
	}
	raw, _ := st.Get(profileKey(""))
	var probe map[string]any
	if json.Unmarshal([]byte(raw), &probe) != nil || probe["format"] != float64(FormatOAuth) {
		t.Fatalf("format marker missing: %s", raw)
	}

	huge := strings.Repeat("x", 3000)
	if err := SaveOAuthSession("", api, refresh, huge, time.Now().Add(5*time.Minute), scopes); err != nil {
		t.Fatal(err)
	}
	c, _ = loadCreds("", api)
	if c.CLIAccess != nil || c.RefreshToken != refresh || c.format() != FormatOAuth || len(c.Scopes) != len(scopes) {
		t.Fatalf("slimmed blob: %+v", c)
	}
}

func TestRevokeRefreshToken(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)
	g := s.NewGrant("full")
	rt := s.CurrentRefresh(g)
	if err := RevokeRefreshToken(context.Background(), s.Client(), s.URL, rt); err != nil {
		t.Fatal(err)
	}
	f := s.Stats().Revocations[0]
	if f.Get("token") != rt || f.Get("client_id") != ClientID || f.Get("token_type_hint") != "refresh_token" || !s.Revoked(g) {
		t.Fatalf("revocation %v, revoked %v", f, s.Revoked(g))
	}
	s.Lock()
	s.RevokeStatus = http.StatusServiceUnavailable
	s.Unlock()
	if err := RevokeRefreshToken(context.Background(), s.Client(), s.URL, rt); err == nil {
		t.Fatal("a failed revocation reported success")
	}
}

// Metadata naming an endpoint outside the issuer's origin is refused, so a
// tampered answer can't send the refresh token elsewhere.
func TestDiscoverRejectsForeignEndpoint(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	s.TokenEndpoint = "https://elsewhere.example/oauth/token"
	ForgetMetadata(s.URL)
	if _, err := Discover(context.Background(), s.Client(), s.URL); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("got %v", err)
	}
}

// A cached token endpoint that has moved answers without OAuth: the
// metadata is fetched again and the refresh goes to the new endpoint.
func TestOAuthTokenSourceRediscoversMovedEndpoint(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	signedInOAuth(t, s)
	s.TokenEndpoint = s.URL + "/moved/token"
	_ = discover(t, s)
	s.Lock()
	s.TokenEndpoint = ""
	s.Unlock()

	if _, err := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, e, _ := s.Counts(); r != 1 || e != 1 {
		t.Fatalf("refreshes %d exchanges %d, want 1 1", r, e)
	}
	if e, ok := cachedMetadata(s.URL); !ok || e.Metadata.TokenEndpoint != s.URL+"/oauth/token" {
		t.Fatal("the moved endpoint is still cached")
	}
}

// A full sign-in reaches every service the CLI can call. A product added
// to the registry without its audience on the fake (or the server) fails
// here.
func TestFullSignInCoversEveryService(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	signedInOAuth(t, s)
	_ = discover(t, s)
	services := registry.Callable()
	if len(services) == 0 {
		t.Fatal("no callable services")
	}
	for _, svc := range services {
		tok, err := oauthSource(t, s, svc.Audience, authtest.HomeOrgID).Token(context.Background())
		if err != nil {
			t.Errorf("%s: %v", svc.Command, err)
			continue
		}
		if a, _, _, ok := s.ProductToken(tok); !ok || a != svc.Audience {
			t.Errorf("%s: token for %q (valid %v), want %q", svc.Command, a, ok, svc.Audience)
		}
	}
}

// Every scope `login --scopes` offers is one the server knows, and a
// Monitor scope gets a token for Monitor only.
func TestLoginScopesAreKnownAndLimit(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	for _, ls := range LoginScopes {
		if !s.KnowsScope(ls.Name) {
			t.Errorf("--scopes offers %s, which the fake's catalog doesn't have", ls.Name)
		}
	}

	signedInOAuth(t, s, "monitor.read")
	_ = discover(t, s)
	tok, err := oauthSource(t, s, AudienceMonitor, authtest.HomeOrgID).Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a, _, _, ok := s.ProductToken(tok); !ok || a != AudienceMonitor {
		t.Fatalf("token for %q (valid %v)", a, ok)
	}
	if _, err := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID).Token(context.Background()); err == nil {
		t.Fatal("a monitor.read sign-in got a Backup token")
	}
}

// With KeepLegacy an older sign-in stays as it is even when Norcube Auth
// offers the OAuth sign-in: the older refresh is used instead.
func TestKeepLegacyDoesNotUpgrade(t *testing.T) {
	keyring.MockInit()
	s := authtest.New()
	defer s.Close()
	ForgetMetadata(s.URL)
	s.AddLegacySession("legacy-refresh")
	if err := SaveRefreshToken("", s.URL, "legacy-refresh"); err != nil {
		t.Fatal(err)
	}
	ts := oauthSource(t, s, AudienceSnapDB, authtest.HomeOrgID)
	ts.KeepLegacy = true
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.Upgrades != 0 || st.LegacyRefreshes != 1 {
		t.Fatalf("upgrades %d legacy refreshes %d", st.Upgrades, st.LegacyRefreshes)
	}
	if sess, _ := LoadSession("", s.URL); sess.Format != FormatLegacy {
		t.Fatalf("format %d", sess.Format)
	}
}
