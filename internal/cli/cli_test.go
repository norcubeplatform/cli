package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/config"
)

// Command tests run the real command tree in-process against one fake
// backend that plays Norcube Auth (/oauth/token, /organizations) and
// Backup (/backups). Config, cache and keyring are throwaway.

const (
	homeOrgID  = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	acmeOrgID  = "0b9d2b8e-1c1f-4c3e-9a51-6d1c2f9a7e01"
	otherOrgID = "6f1e8a52-3d4b-4f7a-8c2e-9b0a1d2c3e4f" // exists, user is not a member
)

type fakeBackend struct {
	srv *httptest.Server

	mu sync.Mutex
	// refresh is the one refresh token the server accepts; "" means the
	// session is dead.
	refresh string
	// minted records "audience/org" for every successful exchange.
	minted []string
	lists  int
	// tokenStatus, when set, is the status /oauth/token fails with (a
	// broken or unreachable Norcube Auth).
	tokenStatus int
}

var backend = &fakeBackend{}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "norcube-cli-test-")
	if err != nil {
		panic(err)
	}
	// Point every user directory at the temp dir: config, org cache, locks.
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "AppData", "LocalAppData"} {
		_ = os.Setenv(k, dir)
	}
	_ = os.Setenv("NORCUBE_NO_UPDATE_CHECK", "1")
	keyring.MockInit()
	// Tables show local time. A fixed zone two hours east of UTC keeps the
	// golden files the same on every machine and proves the conversion.
	time.Local = time.FixedZone("UTC+2", 2*60*60)

	backend.srv = httptest.NewServer(backend)
	_ = os.Setenv("NORCUBE_AUTH_URL", backend.srv.URL)
	_ = os.Setenv("NORCUBE_SNAPDB_URL", backend.srv.URL)

	if _, err := config.Update(func(f *config.File) error {
		p := f.Profile(config.DefaultProfileName)
		p.User = config.User{ID: "user-1", Email: "dev@example.com"}
		p.SetActiveOrg(config.Organization{ID: homeOrgID, Slug: "home", Name: "Home"})
		return nil
	}); err != nil {
		panic(err)
	}

	code := m.Run()
	backend.srv.Close()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// signIn resets the backend and stores a live session.
func signIn(t *testing.T) {
	t.Helper()
	backend.mu.Lock()
	backend.refresh = "refresh-0"
	backend.tokenStatus = 0
	backend.minted = nil
	backend.lists = 0
	backend.mu.Unlock()
	// A fresh session: no cached access tokens.
	if err := auth.DeleteCredentials("", backend.srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveRefreshToken("", backend.srv.URL, "refresh-0"); err != nil {
		t.Fatal(err)
	}
	cache, _ := os.UserCacheDir()
	_ = os.Remove(filepath.Join(cache, "norcube", "orgs.json"))
}

func (b *fakeBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/oauth/token":
		if b.tokenStatus != 0 {
			w.WriteHeader(b.tokenStatus)
			return
		}
		_ = r.ParseForm()
		c, err := r.Cookie("jds_refresh_token")
		if err != nil || b.refresh == "" || c.Value != b.refresh {
			// The real service answers every failure with the same 401.
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"type":"unauthorized","msg":"Unauthorized."}`)
			return
		}
		org := r.Form.Get("organization_id")
		if org != homeOrgID && org != acmeOrgID {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"type":"unauthorized","msg":"Unauthorized."}`)
			return
		}
		b.minted = append(b.minted, r.Form.Get("audience")+"/"+org)
		b.refresh = fmt.Sprintf("refresh-%d", len(b.minted))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  testJWT(r.Form.Get("audience"), org),
			"refresh_token": b.refresh,
			"token_type":    "Bearer",
			"expires_in":    300,
		})
	case "/organizations":
		b.lists++
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": homeOrgID, "slug": "home", "name": "Home", "is_default": true},
			{"id": acmeOrgID, "slug": "acme", "name": "Acme Inc"},
		})
	case "/backups":
		fmt.Fprint(w, `{"list":[],"cursors":{},"meta":{}}`)
	default:
		http.NotFound(w, r)
	}
}

func testJWT(aud, org string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p, _ := json.Marshal(map[string]any{"aud": aud, "org": org, "exp": time.Now().Add(5 * time.Minute).Unix()})
	return h + "." + base64.RawURLEncoding.EncodeToString(p) + ".sig"
}

// run executes the CLI with args and returns stdout, stderr and the error.
func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

func (b *fakeBackend) mintedTokens() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.minted...)
}
