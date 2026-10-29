package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
)

const ciBody = "8Jd2kQ9mZr4XcVb7NtLw3Hs6Pf1Ya5Ue"

// goodCIToken is a well-formed CI token (a test value, not a credential).
var goodCIToken = CITokenPrefix + ciBody + ciTokenChecksum(ciBody)

func TestValidateCIToken(t *testing.T) {
	good := goodCIToken
	tests := []struct {
		name  string
		token string
		ok    bool
	}{
		{"well-formed", good, true},
		{"typo in the random part", strings.Replace(good, "8Jd2", "8Jd3", 1), false},
		{"typo in the checksum", good[:len(good)-1] + "x", false},
		{"truncated", good[:len(good)-3], false},
		{"extra character", good + "a", false},
		{"not base62", CITokenPrefix + strings.Repeat("-", 38), false},
		{"wrong prefix", "nrc_xx_" + ciBody + ciTokenChecksum(ciBody), false},
	}
	for _, tt := range tests {
		err := ValidateCIToken(tt.token)
		if (err == nil) != tt.ok {
			t.Errorf("%s: ValidateCIToken = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
	// The checksum matches the format Norcube Auth issues: 6 base62
	// characters, zero-padded.
	if sum := ciTokenChecksum(ciBody); len(sum) != 6 {
		t.Fatalf("checksum %q is not 6 characters", sum)
	}
}

// ciServer plays POST /auth/ci/token.
type ciServer struct {
	mu        sync.Mutex
	secret    string
	scopes    map[string]string // audience -> scope; missing = no scope
	revoked   bool
	status    int // forced failure status
	exchanges map[string]int
	expiresIn int
}

func (s *ciServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path != "/auth/ci/token" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	aud := r.Form.Get("audience")
	if s.exchanges == nil {
		s.exchanges = map[string]int{}
	}
	s.exchanges[aud]++
	w.Header().Set("Content-Type", "application/json")
	if s.status != 0 {
		if s.status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(s.status)
		fmt.Fprint(w, `{"type":"TOO_MANY_REQUESTS","msg":"slow down"}`)
		return
	}
	if r.Form.Get("subject_token") != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"INVALID_REQUEST","msg":"the secret must not be in the body"}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.secret || s.revoked {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"INVALID_TOKEN","msg":"invalid, expired or revoked CI token"}`)
		return
	}
	scope, ok := s.scopes[aud]
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"type":"INSUFFICIENT_SCOPE","msg":"no scope for %s"}`, aud) // #nosec G705 -- test server, JSON consumer
		return
	}
	exp := s.expiresIn
	if exp == 0 {
		exp = 300
	}
	_ = json.NewEncoder(w).Encode(map[string]any{ // #nosec G101 -- test fixture
		"access_token": "at-" + aud, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"token_type": "Bearer", "expires_in": exp, "scope": scope, "audience": aud,
		"organization_id": "org-1", "organization_slug": "acme", "organization_name": "Acme",
		"ci_token_id": "tok-1", "ci_token_name": "deploy", "ci_token_expires_at": "2027-01-27T09:00:00Z",
	})
}

func newCIExchangerFor(t *testing.T, s *ciServer) *CIExchanger {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return NewCIExchanger(srv.URL, s.secret)
}

func TestCIExchangerCachesPerAudience(t *testing.T) {
	s := &ciServer{secret: goodCIToken, scopes: map[string]string{AudienceSnapDB: "backup.read", AudienceLangsync: "langsync.sync"}}
	ex := newCIExchangerFor(t, s)
	now := time.Now()
	ex.now = func() time.Time { return now }

	g, err := ex.Grant(context.Background(), AudienceSnapDB)
	if err != nil {
		t.Fatal(err)
	}
	if g.AccessToken != "at-snapdb-api" || g.OrgSlug != "acme" || g.TokenName != "deploy" || strings.Join(g.Scopes, ",") != "backup.read" {
		t.Fatalf("grant = %+v", g)
	}
	// Concurrent callers share one exchange per service.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ex.Token(context.Background(), AudienceSnapDB); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := ex.Token(context.Background(), AudienceLangsync); err != nil {
		t.Fatal(err)
	}
	if s.exchanges[AudienceSnapDB] != 1 || s.exchanges[AudienceLangsync] != 1 {
		t.Fatalf("exchanges = %v, want one per service", s.exchanges)
	}
	// Within 30 s of expiry the token is replaced first.
	now = now.Add(5*time.Minute - 20*time.Second)
	if _, err := ex.Token(context.Background(), AudienceSnapDB); err != nil {
		t.Fatal(err)
	}
	if s.exchanges[AudienceSnapDB] != 2 {
		t.Fatalf("near-expiry token was reused: %v", s.exchanges)
	}
}

func TestCIExchangerErrors(t *testing.T) {
	tests := []struct {
		name     string
		revoked  bool
		scopes   map[string]string
		status   int
		wantCode string
		wantExit int
		inMsg    string
	}{
		{"revoked token", true, map[string]string{AudienceSnapDB: "backup.read"}, 0, clierr.CodeLoginRequired, clierr.ExitAuth, "invalid, expired or revoked"},
		{"no scope for the service", false, map[string]string{}, 0, clierr.CodeInsufficientScope, clierr.ExitAccess, "no scope for Norcube Backup"},
		{"rate limited", false, nil, http.StatusTooManyRequests, clierr.CodeRateLimited, clierr.ExitRateLimited, "rate limited"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &ciServer{secret: goodCIToken, revoked: tt.revoked, scopes: tt.scopes, status: tt.status}
			ex := newCIExchangerFor(t, s)
			_, err := ex.Grant(context.Background(), AudienceSnapDB)
			var ce *clierr.Error
			if !errors.As(err, &ce) {
				t.Fatalf("want *clierr.Error, got %v", err)
			}
			if ce.Code != tt.wantCode || ce.ExitCode() != tt.wantExit || !strings.Contains(ce.Message, tt.inMsg) {
				t.Fatalf("got code %s exit %d message %q", ce.Code, ce.ExitCode(), ce.Message)
			}
			// One sentence: the server's own wording isn't appended to ours.
			if strings.Contains(ce.Message, ": ") {
				t.Fatalf("message repeats the server's text: %q", ce.Message)
			}
			if tt.wantCode == clierr.CodeLoginRequired && !strings.Contains(ce.Hint, "ci-token create") {
				t.Fatalf("a refused token should say how to get a new one, hint %q", ce.Hint)
			}
		})
	}
}

func TestTokenSourceUsesCIExchanger(t *testing.T) {
	s := &ciServer{secret: goodCIToken, scopes: map[string]string{AudienceLangsync: "langsync.sync"}}
	ex := newCIExchangerFor(t, s)
	ts := NewTokenSource("http://unused.invalid", AudienceLangsync, "")
	ts.CI = ex
	tok, err := ts.Token(context.Background())
	if err != nil || tok != "at-langsync-api" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
}
