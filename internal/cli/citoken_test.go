package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/config"
)

// ciSecret builds a well-formed CI token (the checksum Norcube Auth
// issues: CRC32 IEEE of the 32 random characters, base62, 6 characters).
func ciSecret(body string) string {
	const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	n := crc32.ChecksumIEEE([]byte(body))
	sum := make([]byte, 6)
	for i := len(sum) - 1; i >= 0; i-- {
		sum[i] = base62[n%62]
		n /= 62
	}
	return auth.CITokenPrefix + body + string(sum)
}

var testCISecret = ciSecret("8Jd2kQ9mZr4XcVb7NtLw3Hs6Pf1Ya5Ue")

// ciBackend plays Norcube Auth's CI token exchange plus Backup.
type ciBackend struct {
	mu        sync.Mutex
	revoked   bool
	exchanges map[string]int
	backups   int
}

func (b *ciBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/auth/ci/token":
		_ = r.ParseForm()
		aud := r.Form.Get("audience")
		if b.exchanges == nil {
			b.exchanges = map[string]int{}
		}
		b.exchanges[aud]++
		if r.Header.Get("Authorization") != "Bearer "+testCISecret || b.revoked {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"type":"INVALID_TOKEN","msg":"invalid, expired or revoked CI token"}`)
			return
		}
		if aud != auth.AudienceSnapDB {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"type":"INSUFFICIENT_SCOPE","msg":"the token has no scope for %s"}`, aud) // #nosec G705 -- test server, JSON consumer
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{ // #nosec G101 -- test fixture
			"access_token": testJWT(aud, acmeOrgID), "token_type": "Bearer", "expires_in": 300,
			"scope": "backup.read", "audience": aud,
			"organization_id": acmeOrgID, "organization_slug": "acme", "organization_name": "Acme Inc",
			"ci_token_id": "tok-1", "ci_token_name": "GitHub Actions deploy", "ci_token_expires_at": "2027-01-27T09:00:00Z",
		})
	case "/datasources":
		// IAM refuses what no CI token scope grants.
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"type":"FORBIDDEN","msg":"Insufficient permissions"}`)
	case "/backups":
		b.backups++
		fmt.Fprint(w, `{"list":[],"cursors":{},"meta":{}}`)
	default:
		http.NotFound(w, r)
	}
}

// ciEnv runs commands with NORCUBE_TOKEN set to token against a fresh
// ciBackend, in a config with no session at all (as on a CI runner).
func ciEnv(t *testing.T, token string) *ciBackend {
	t.Helper()
	b := &ciBackend{}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	isolatedConfig(t, func(*config.File) {})
	t.Setenv("NORCUBE_AUTH_URL", srv.URL)
	t.Setenv("NORCUBE_SNAPDB_URL", srv.URL)
	t.Setenv("NORCUBE_TOKEN", token)
	return b
}

func TestCITokenRunsProductCommands(t *testing.T) {
	b := ciEnv(t, testCISecret)
	for range 2 {
		if _, stderr, err := run(t, "backup", "list", "-o", "json"); err != nil {
			t.Fatalf("backup list with a CI token: %v\n%s", err, stderr)
		}
	}
	// One exchange per process for the service; the CLI never stores the
	// access token, so a second process exchanges again.
	if b.exchanges[auth.AudienceSnapDB] != 2 || b.backups != 2 {
		t.Fatalf("exchanges %v, backup calls %d", b.exchanges, b.backups)
	}
	if auth.HasCredentials(config.DefaultProfileName, "") {
		t.Fatal("a CI token run stored credentials")
	}
}

func TestCITokenOrganization(t *testing.T) {
	tests := []struct {
		name string
		org  string
		ok   bool
	}{
		{"its own slug", "acme", true},
		{"its own id", acmeOrgID, true},
		{"another organization", "home", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ciEnv(t, testCISecret)
			_, _, err := run(t, "--org", tt.org, "backup", "list", "-o", "json")
			if tt.ok && err != nil {
				t.Fatal(err)
			}
			if !tt.ok && (!clierr.IsUsage(err) || !strings.Contains(err.Error(), "belongs to organization acme")) {
				t.Fatalf("want a usage error naming the token's organization, got %v", err)
			}
		})
	}
}

func TestMalformedCITokenIsCaughtLocally(t *testing.T) {
	b := ciEnv(t, testCISecret[:len(testCISecret)-1]+"x")
	_, _, err := run(t, "backup", "list")
	if !clierr.IsUsage(err) || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want a usage error about the checksum, got %v", err)
	}
	if len(b.exchanges) != 0 {
		t.Fatal("a mistyped token was sent to Norcube Auth")
	}
}

func TestRevokedCITokenExits4(t *testing.T) {
	b := ciEnv(t, testCISecret)
	b.revoked = true
	_, _, err := run(t, "backup", "list")
	e := Describe(err)
	if e.ExitCode() != clierr.ExitAuth || !strings.Contains(e.Hint, "ci-token create") {
		t.Fatalf("got exit %d, hint %q (%v)", e.ExitCode(), e.Hint, err)
	}
}

func TestCITokenMissingScopeExits5(t *testing.T) {
	ciEnv(t, testCISecret)
	t.Setenv("NORCUBE_LANGSYNC_URL", "http://127.0.0.1:1")
	_, _, err := run(t, "langsync", "namespace", "list")
	e := Describe(err)
	if e.ExitCode() != clierr.ExitAccess || !strings.Contains(e.Message, "no scope for LangSync") {
		t.Fatalf("got exit %d message %q", e.ExitCode(), e.Message)
	}
}

// A refusal under a CI token says that some actions always need a person,
// and the same refusal for a signed-in person doesn't.
func TestCITokenRefusalExplains(t *testing.T) {
	ciEnv(t, testCISecret)
	_, _, err := run(t, "api", "backup", "POST", "/datasources", "-f", "name=x")
	e := Describe(err)
	if e.Code != clierr.CodePermissionDenied || !strings.Contains(e.Hint, "always need a person") {
		t.Fatalf("code %q hint %q", e.Code, e.Hint)
	}

	usingCIToken = false
	if h := Describe(&clierr.Error{Code: clierr.CodePermissionDenied, Message: "denied"}).Hint; h != "" {
		t.Fatalf("hint without a CI token: %q", h)
	}
}

func TestCITokenUserCommandsExplain(t *testing.T) {
	ciEnv(t, testCISecret)
	for _, args := range [][]string{{"whoami"}, {"org", "list"}, {"auth", "sessions", "list"}} {
		_, _, err := run(t, args...)
		if !errors.Is(err, clictx.ErrNeedsUserLogin) {
			t.Fatalf("%v: want ErrNeedsUserLogin, got %v", args, err)
		}
	}
}

func TestAuthStatusWithCIToken(t *testing.T) {
	ciEnv(t, testCISecret)
	out, stderr, err := run(t, "auth", "status", "-o", "json")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	var rows []authStatus
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s := rows[0]
	if !s.SignedIn || s.Session != auth.SessionActive || s.Organization != "acme" || s.CIToken == nil ||
		s.CIToken.Name != "GitHub Actions deploy" || strings.Join(s.CIToken.Scopes, ",") != "backup.read" {
		t.Fatalf("status = %+v ci=%+v", s, s.CIToken)
	}
	// The endpoints come from NORCUBE_*_URL, not the production set, and the
	// status says so.
	if s.Environment != "custom" {
		t.Fatalf("environment = %q, want custom (endpoints overridden)", s.Environment)
	}
}

func TestAuthStatusWithRevokedCITokenExits4(t *testing.T) {
	b := ciEnv(t, testCISecret)
	b.revoked = true
	_, _, err := run(t, "auth", "status")
	if e := Describe(err); e.ExitCode() != clierr.ExitAuth {
		t.Fatalf("exit %d (%v)", e.ExitCode(), err)
	}
}

func TestAuthTokenWithCIToken(t *testing.T) {
	ciEnv(t, testCISecret)
	out, _, err := run(t, "auth", "token", "--audience", "backup", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var res tokenResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Organization != acmeOrgID || res.AccessToken == "" {
		t.Fatalf("token result %+v (%v)", res, err)
	}
	if _, _, err := run(t, "auth", "token", "--audience", "auth"); !clierr.IsUsage(err) {
		t.Fatalf("a CI token has no auth-audience token; got %v", err)
	}
}

func TestCITokenCreate(t *testing.T) {
	url := goldenEnv(t)
	var gotBody map[string]any
	goldenHandlers(t, map[string]http.HandlerFunc{
		"POST /iam/ci-tokens": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":{"id":"tok-9","name":"deploy","preview":"%s","scopes":["langsync.sync","backup.read"],"created_at":"2026-10-29T09:00:00Z","expires_at":"2027-01-27T09:00:00Z","status":"active"},"secret":%q}`,
				testCISecret[len(testCISecret)-4:], testCISecret)
		},
	})
	out, _, err := run(t, "auth", "ci-token", "create", "--name", "deploy", "--scopes", "langsync.sync, backup.read,langsync.sync", "--expires", "90d", "-o", "text")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != testCISecret {
		t.Fatalf("-o text should print only the secret, got %q", out)
	}
	if fmt.Sprint(gotBody["scopes"]) != "[langsync.sync backup.read]" || gotBody["name"] != "deploy" {
		t.Fatalf("request body %v", gotBody)
	}
	exp, _ := time.Parse(time.RFC3339, fmt.Sprint(gotBody["expires_at"]))
	if d := time.Until(exp); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("expires_at %v is not ~90 days away", exp)
	}

	table, _, err := run(t, "auth", "ci-token", "create", "--name", "deploy", "--scopes", "langsync.sync")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table, testCISecret) || !strings.Contains(table, "only time the token is shown") || !strings.Contains(table, "NORCUBE_TOKEN") {
		t.Fatalf("create output:\n%s", strings.ReplaceAll(table, url, "http://backend"))
	}
}

func TestCITokenCreateValidation(t *testing.T) {
	goldenEnv(t)
	for _, args := range [][]string{
		{"--scopes", "langsync.sync"},
		{"--name", "x"},
		{"--name", "x", "--scopes", "langsync.sync", "--expires", "400d"},
		{"--name", "x", "--scopes", "langsync.sync", "--expires", "10m"},
		{"--name", "x", "--scopes", "langsync.sync", "--expires", "soon"},
	} {
		if _, _, err := run(t, append([]string{"auth", "ci-token", "create"}, args...)...); !clierr.IsUsage(err) {
			t.Errorf("%v: want a usage error, got %v", args, err)
		}
	}
}

func TestCITokenRevokeByName(t *testing.T) {
	goldenEnv(t)
	var revoked string
	goldenHandlers(t, map[string]http.HandlerFunc{
		"DELETE /iam/ci-tokens/0b7a4c8e-2d3f-4a1b-9c6d-5e4f3a2b1c0d": func(w http.ResponseWriter, r *http.Request) {
			revoked = "0b7a4c8e-2d3f-4a1b-9c6d-5e4f3a2b1c0d"
			fmt.Fprint(w, `{"id":"0b7a4c8e-2d3f-4a1b-9c6d-5e4f3a2b1c0d","name":"GitHub Actions deploy","status":"revoked","scopes":[],"preview":"x7Kq","created_at":"2026-10-01T09:00:00Z","expires_at":"2026-12-30T09:00:00Z"}`)
		},
	})
	out, _, err := run(t, "auth", "ci-token", "revoke", "github actions deploy")
	if err != nil || revoked == "" || !strings.Contains(out, `Revoked CI token "GitHub Actions deploy"`) {
		t.Fatalf("out %q err %v", out, err)
	}
	// An expired token's name doesn't match: only active tokens do.
	if _, _, err := run(t, "auth", "ci-token", "revoke", "nightly backup check"); Describe(err).ExitCode() != clierr.ExitNotFound {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"12h": 12 * time.Hour, "90d": 90 * 24 * time.Hour, "4w": 28 * 24 * time.Hour, "": ciTokenDefaultLifetime,
	} {
		got, err := parseExpiry(in, now)
		if err != nil || got.Sub(now) != want {
			t.Errorf("%q: %v, %v", in, got.Sub(now), err)
		}
	}
	if got, err := parseExpiry("2027-01-31", now); err != nil || got.Format("2006-01-02") != "2027-01-31" {
		t.Errorf("date: %v %v", got, err)
	}
}

func TestLoginDevice(t *testing.T) {
	b := loginProfile(t)
	_ = auth.DeleteCredentials("p-login", b.srv.URL)
	prev := deviceLogin
	t.Cleanup(func() { deviceLogin = prev })
	deviceLogin = func(ctx context.Context, opts auth.DeviceLoginOptions) (*auth.CallbackPayload, error) {
		opts.OnCode(auth.DeviceCode{UserCode: "WDJB-MJHT", VerificationURI: "https://app.example/device"})
		return &auth.CallbackPayload{
			RefreshToken: "refresh-0", AccessToken: testJWT(auth.AudienceAuth, ""), AccessExpiresIn: 300,
			User:       auth.User{ID: "user-1", Email: "dev@example.com", Name: "Dev"},
			DefaultOrg: auth.Organization{ID: homeOrgID, Slug: "home", Name: "Home"},
		}, nil
	}
	_, stderr, err := run(t, "login", "--device")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "WDJB-MJHT") || !strings.Contains(stderr, "https://app.example/device") || !strings.Contains(stderr, "Norcube never asks you for this code") {
		t.Fatalf("stderr:\n%s", stderr)
	}
	if !auth.HasCredentials("p-login", b.srv.URL) {
		t.Fatal("the device sign-in session was not stored")
	}
	if _, _, err := run(t, "login", "--device", "--no-browser"); !clierr.IsUsage(err) {
		t.Fatalf("--device with --no-browser: %v", err)
	}
}

func TestLoginDeviceDenied(t *testing.T) {
	b := loginProfile(t)
	_ = auth.DeleteCredentials("p-login", b.srv.URL)
	prev := deviceLogin
	t.Cleanup(func() { deviceLogin = prev })
	deviceLogin = func(ctx context.Context, opts auth.DeviceLoginOptions) (*auth.CallbackPayload, error) {
		return nil, &clierr.Error{Code: clierr.CodeLoginRequired, Message: auth.ErrDeviceDenied.Error(), Err: auth.ErrDeviceDenied}
	}
	_, _, err := run(t, "login", "--device")
	if !errors.Is(err, auth.ErrDeviceDenied) || Describe(err).ExitCode() != clierr.ExitAuth {
		t.Fatalf("got %v", err)
	}
	if auth.HasCredentials("p-login", b.srv.URL) {
		t.Fatal("a denied sign-in stored credentials")
	}
}
