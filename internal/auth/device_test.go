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
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// deviceServer plays Norcube Auth's device endpoints. answers are the
// poll responses in order ("approved" ends with a session).
type deviceServer struct {
	mu       sync.Mutex
	answers  []string
	polls    int
	hostname string
	clientID string
	ua       string
}

func (s *deviceServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = r.ParseForm()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/auth/cli/device/code":
		s.hostname, s.clientID, s.ua = r.Form.Get("hostname"), r.Form.Get("client_id"), r.UserAgent()
		_ = json.NewEncoder(w).Encode(DeviceCode{
			DeviceCode: "dc-1", UserCode: "WDJB-MJHT",
			VerificationURI:         "https://app.example/device",
			VerificationURIComplete: "https://app.example/device?user_code=WDJB-MJHT",
			ExpiresIn:               600, Interval: 5,
		})
	case "/auth/cli/device/token":
		if r.Form.Get("device_code") != "dc-1" || r.Form.Get("grant_type") != deviceGrantType {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		answer := "authorization_pending"
		if s.polls < len(s.answers) {
			answer = s.answers[s.polls]
		}
		s.polls++
		if answer == "approved" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"accessToken": "at", "refreshToken": "rt", "accessExpiresIn": 300,
				"user":                map[string]any{"id": "u1", "email": "ada@acme.dev", "name": "Ada"},
				"defaultOrganization": map[string]any{"id": "o1", "slug": "acme", "name": "Acme"},
			})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":%q,"error_description":"…"}`, answer)
	default:
		http.NotFound(w, r)
	}
}

// fakeClock advances only when the flow sleeps, and records the sleeps.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d > 0 {
		c.sleeps = append(c.sleeps, d)
		c.now = c.now.Add(d)
	}
	return nil
}

func runDevice(t *testing.T, s *deviceServer, ctx context.Context) (*CallbackPayload, *fakeClock, error) {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	var shown DeviceCode
	p, err := DeviceLogin(ctx, DeviceLoginOptions{
		AuthURL:  srv.URL,
		HTTP:     srv.Client(),
		Hostname: "build-box",
		OnCode:   func(c DeviceCode) { shown = c },
		sleep:    clock.sleep,
		now:      func() time.Time { return clock.now },
	})
	if err == nil && shown.UserCode != "WDJB-MJHT" {
		t.Fatalf("the code was not shown: %+v", shown)
	}
	return p, clock, err
}

func TestDeviceLoginPendingSlowDownApproved(t *testing.T) {
	s := &deviceServer{answers: []string{"authorization_pending", "slow_down", "authorization_pending", "approved"}}
	p, clock, err := runDevice(t, s, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.RefreshToken != "rt" || p.User.Email != "ada@acme.dev" || p.DefaultOrg.Slug != "acme" {
		t.Fatalf("payload = %+v", p)
	}
	// 5 s between polls, then 10 s for good after the slow_down.
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
	if fmt.Sprint(clock.sleeps) != fmt.Sprint(want) {
		t.Fatalf("poll waits = %v, want %v", clock.sleeps, want)
	}
	if s.hostname != "build-box" || s.clientID != DeviceClientID {
		t.Fatalf("request labels: hostname %q client_id %q", s.hostname, s.clientID)
	}
}

func TestDeviceLoginEndings(t *testing.T) {
	for _, tt := range []struct {
		answer string
		want   error
	}{
		{"access_denied", ErrDeviceDenied},
		{"expired_token", ErrDeviceExpired},
	} {
		t.Run(tt.answer, func(t *testing.T) {
			_, _, err := runDevice(t, &deviceServer{answers: []string{"authorization_pending", tt.answer}}, context.Background())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var ce *clierr.Error
			if !errors.As(err, &ce) || ce.ExitCode() != clierr.ExitAuth {
				t.Fatalf("want exit 4, got %v", err)
			}
		})
	}
}

func TestDeviceLoginStopsAtDeadline(t *testing.T) {
	// Never approved: the flow gives up once the code's 10 minutes pass,
	// without polling forever.
	s := &deviceServer{}
	_, clock, err := runDevice(t, s, context.Background())
	if !errors.Is(err, ErrDeviceExpired) {
		t.Fatalf("err = %v", err)
	}
	if s.polls > 600/5+1 || len(clock.sleeps) == 0 {
		t.Fatalf("polled %d times", s.polls)
	}
}

func TestDeviceLoginCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := runDevice(t, &deviceServer{}, ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (exit 130)", err)
	}
}
