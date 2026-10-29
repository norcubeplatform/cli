package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// fakeJWT builds an unsigned JWT with the given exp claim — enough for
// isExpiringSoon, which decodes without verifying.
func fakeJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestTokenUsesCachedAccessToken(t *testing.T) {
	keyring.MockInit()
	api := "https://cache-hit.test"
	ts := newTestSource(t, api)

	cached := fakeJWT(t, time.Now().Add(time.Hour))
	if err := SaveAccessTokenUntil("", api, "aud", "org-1", cached, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// No HTTP server exists for this APIBase: a network call would fail,
	// proving the cache path never leaves the process.
	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != cached {
		t.Fatalf("expected cached token, got %q", got)
	}
}

func TestTokenRefreshesAndPersistsRotation(t *testing.T) {
	keyring.MockInit()
	access := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		c, err := r.Cookie("jds_refresh_token")
		if err != nil || c.Value != "old-refresh" {
			t.Errorf("expected old refresh token cookie, got %v", c)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token":  access,
			"refresh_token": "rotated-refresh",
		})
	}))
	defer srv.Close()

	access = fakeJWT(t, time.Now().Add(time.Hour))
	if err := SaveRefreshToken("", srv.URL, "old-refresh"); err != nil {
		t.Fatal(err)
	}
	ts := newTestSource(t, srv.URL)
	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != access {
		t.Fatalf("wrong access token: %q", got)
	}
	// The rotated refresh token must be persisted, or the next exchange
	// presents a revoked token and the session dies.
	stored, err := LoadRefreshToken("", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if stored != "rotated-refresh" {
		t.Fatalf("rotation not persisted: %q", stored)
	}
	// And the fresh access token is cached for the next invocation.
	if cached, _, _ := loadAccess("", srv.URL, "aud", "org-1"); cached != access {
		t.Fatalf("access token not cached: %q", cached)
	}
}

func TestTokenMissingRefreshIsLoginRequired(t *testing.T) {
	keyring.MockInit()
	ts := newTestSource(t, "https://nothing-stored.test")
	_, err := ts.Token(context.Background())
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expected ErrLoginRequired, got %v", err)
	}
}

func TestTokenRejectedRefreshIsLoginRequired(t *testing.T) {
	keyring.MockInit()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"msg":"refresh token revoked"}`)
	}))
	defer srv.Close()

	if err := SaveRefreshToken("", srv.URL, "revoked"); err != nil {
		t.Fatal(err)
	}
	ts := newTestSource(t, srv.URL)
	_, err := ts.Token(context.Background())
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expected ErrLoginRequired on 401, got %v", err)
	}
	// A transient 500 must NOT read as login-required.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()
	if err := SaveRefreshToken("", srv2.URL, "fine"); err != nil {
		t.Fatal(err)
	}
	ts2 := newTestSource(t, srv2.URL)
	_, err = ts2.Token(context.Background())
	if err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("a 500 must be a plain error, got %v", err)
	}
}

func TestJWTExpiry(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	if got := jwtExpiry(fakeJWT(t, exp)); !got.Equal(exp) {
		t.Errorf("jwtExpiry = %v, want %v", got, exp)
	}
	if !jwtExpiry("garbage.token.here").IsZero() {
		t.Error("undecodable token must have no expiry (reads as expired)")
	}
}

// newTestSource keeps lock files out of the real config directory.
func newTestSource(t *testing.T, api string) *TokenSource {
	t.Helper()
	ts := NewTokenSource(api, "aud", "org-1")
	ts.LockDir = t.TempDir()
	return ts
}
