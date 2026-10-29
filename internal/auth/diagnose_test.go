package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// memberServer behaves like Norcube Auth's /oauth/token: one live refresh
// token, rotated on every successful exchange, and the same bare 401 for
// a dead session and for an organization the user isn't a member of.
type memberServer struct {
	mu      sync.Mutex
	current string // "" once the session is dead
	member  map[string]bool
	n       int
	status  int    // when set, every request fails with it
	typed   string // when set, a live session asking for a foreign org gets this error type (current Norcube Auth)
	calls   []string
}

func (s *memberServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	org := r.Form.Get("organization_id")
	s.calls = append(s.calls, r.Form.Get("audience")+"/"+org)
	if s.status != 0 {
		w.WriteHeader(s.status)
		return
	}
	c, err := r.Cookie("jds_refresh_token")
	if s.typed != "" && err == nil && s.current != "" && c.Value == s.current && !s.member[org] {
		status := http.StatusForbidden
		if s.typed == apiErrInvalidOrganization {
			status = http.StatusBadRequest
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"type":%q,"msg":"refused"}`, s.typed)
		return
	}
	if err != nil || s.current == "" || c.Value != s.current || !s.member[org] {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"unauthorized","msg":"Unauthorized."}`)
		return
	}
	s.n++
	s.current = fmt.Sprintf("refresh-%d", s.n)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": fmt.Sprintf("access-%d", s.n), "refresh_token": s.current, "expires_in": 300,
	})
}

func TestRefusedExchangeIsDiagnosed(t *testing.T) {
	tests := []struct {
		name       string
		session    bool   // refresh token still valid
		org, probe string // requested org, ProbeOrgID
		status     int
		typed      string // the server's error type for a foreign org
		wantAccess bool   // ErrOrgAccessDenied
		wantLogin  bool   // ErrLoginRequired
		wantCalls  int
	}{
		{"foreign org with a live session", true, "foreign", "home", 0, "", true, false, 2},
		{"dead session with a probe org", false, "foreign", "home", 0, "", false, true, 2},
		{"dead session for the active org", false, "home", "home", 0, "", false, true, 1},
		{"no probe org known", true, "foreign", "", 0, "", false, true, 1},
		{"server error is neither", true, "foreign", "home", http.StatusInternalServerError, "", false, false, 1},
		// Current servers name the reason, so no probe exchange is needed,
		// even for the active org or without a probe org.
		{"ORG_ACCESS_DENIED needs no probe", true, "foreign", "home", 0, apiErrOrgAccessDenied, true, false, 1},
		{"ORG_ACCESS_DENIED without a probe org", true, "foreign", "", 0, apiErrOrgAccessDenied, true, false, 1},
		{"INVALID_ORGANIZATION", true, "bogus", "home", 0, apiErrInvalidOrganization, true, false, 1},
		{"typed server, dead session", false, "foreign", "home", 0, apiErrOrgAccessDenied, false, true, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &memberServer{member: map[string]bool{"home": true}, status: tt.status, typed: tt.typed}
			if tt.session {
				srv.current = "refresh-0"
			}
			hs := httptest.NewServer(srv)
			defer hs.Close()
			prev := SetStore(fileStore{dir: t.TempDir()})
			defer SetStore(prev)
			_ = SaveRefreshToken("", hs.URL, "refresh-0")

			ts := newTestSource(t, hs.URL)
			ts.OrgID, ts.ProbeOrgID, ts.OrgLabel = tt.org, tt.probe, "acme"
			_, err := ts.Token(context.Background())
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrOrgAccessDenied); got != tt.wantAccess {
				t.Errorf("ErrOrgAccessDenied = %v, want %v (%v)", got, tt.wantAccess, err)
			}
			if got := errors.Is(err, ErrLoginRequired); got != tt.wantLogin {
				t.Errorf("ErrLoginRequired = %v, want %v (%v)", got, tt.wantLogin, err)
			}
			if len(srv.calls) != tt.wantCalls {
				t.Errorf("exchanges %v, want %d", srv.calls, tt.wantCalls)
			}
			if tt.wantAccess {
				var oa *OrgAccessError
				if !errors.As(err, &oa) || oa.Org != "acme" {
					t.Errorf("want OrgAccessError for acme, got %#v", err)
				}
				// The session must survive. When a probe ran, it rotated the
				// refresh token and its token for the probe org is cached.
				stored, _ := LoadRefreshToken("", hs.URL)
				if stored != srv.current {
					t.Fatalf("rotated refresh token not persisted: stored %q, server %q", stored, srv.current)
				}
				home := newTestSource(t, hs.URL)
				home.Audience, home.OrgID = AudienceAuth, "home"
				if _, err := home.Token(context.Background()); err != nil {
					t.Fatalf("session broken after the diagnosis: %v", err)
				}
				if tt.typed == "" && len(srv.calls) != tt.wantCalls {
					t.Errorf("the probe's token was not cached: %v", srv.calls)
				}
			}
		})
	}
}
