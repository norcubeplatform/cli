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
	ts := NewTokenSource(api, "aud", "org-1")

	cached := fakeJWT(t, time.Now().Add(time.Hour))
	if err := SaveAccessToken(api, "aud", "org-1", cached); err != nil {
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
	if err := SaveRefreshToken(srv.URL, "old-refresh"); err != nil {
		t.Fatal(err)
	}
	ts := NewTokenSource(srv.URL, "aud", "org-1")
	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != access {
		t.Fatalf("wrong access token: %q", got)
	}
	// The rotated refresh token must be persisted, or the next exchange
	// presents a revoked token and the session dies.
	stored, err := LoadRefreshToken(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if stored != "rotated-refresh" {
		t.Fatalf("rotation not persisted: %q", stored)
	}
	// And the fresh access token is cached for the next invocation.
	if cached, _ := LoadAccessToken(srv.URL, "aud", "org-1"); cached != access {
		t.Fatalf("access token not cached: %q", cached)
	}
}

func TestTokenMissingRefreshIsLoginRequired(t *testing.T) {
	keyring.MockInit()
	ts := NewTokenSource("https://nothing-stored.test", "aud", "org-1")
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

	if err := SaveRefreshToken(srv.URL, "revoked"); err != nil {
		t.Fatal(err)
	}
	ts := NewTokenSource(srv.URL, "aud", "org-1")
	_, err := ts.Token(context.Background())
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expected ErrLoginRequired on 401, got %v", err)
	}
	// A transient 500 must NOT read as login-required.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()
	if err := SaveRefreshToken(srv2.URL, "fine"); err != nil {
		t.Fatal(err)
	}
	ts2 := NewTokenSource(srv2.URL, "aud", "org-1")
	_, err = ts2.Token(context.Background())
	if err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("a 500 must be a plain error, got %v", err)
	}
}

func TestIsExpiringSoon(t *testing.T) {
	if isExpiringSoon(fakeJWT(t, time.Now().Add(time.Hour))) {
		t.Error("hour-fresh token flagged as expiring")
	}
	if !isExpiringSoon(fakeJWT(t, time.Now().Add(10*time.Second))) {
		t.Error("10s-left token not flagged (30s buffer)")
	}
	if !isExpiringSoon("garbage.token.here") {
		t.Error("undecodable token must count as expiring")
	}
}
