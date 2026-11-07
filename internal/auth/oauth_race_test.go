package auth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/norcubeplatform/cli/internal/auth/authtest"
)

// hammerOAuth asks for tokens of several services from n goroutines until
// the deadline, like hammer does for the older sign-in.
func hammerOAuth(api, lockDir string, n int, d time.Duration) (calls, failures int64, firstErr error) {
	auds := []string{AudienceSnapDB, AudienceLangsync, AudienceDomainradar, AudienceAuth}
	var c, f atomic.Int64
	var once sync.Once
	deadline := time.Now().Add(d)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ts := NewTokenSource(api, auds[i%len(auds)], authtest.HomeOrgID)
			ts.LockDir = lockDir
			for time.Now().Before(deadline) {
				c.Add(1)
				if _, err := ts.Token(context.Background()); err != nil {
					f.Add(1)
					once.Do(func() { firstErr = err })
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	wg.Wait()
	return c.Load(), f.Load(), firstErr
}

const oauthHelperEnv = "NORCUBE_OAUTH_TOKEN_HELPER"

// TestOAuthHelperProcess is the child side of TestOAuthRefreshAcrossProcesses.
func TestOAuthHelperProcess(t *testing.T) {
	if os.Getenv(oauthHelperEnv) != "1" {
		t.Skip("helper for TestOAuthRefreshAcrossProcesses")
	}
	SetStore(fileStore{dir: os.Getenv("STORE_DIR")})
	d, _ := time.ParseDuration(os.Getenv("DURATION"))
	n, _ := strconv.Atoi(os.Getenv("GOROUTINES"))
	calls, failures, err := hammerOAuth(os.Getenv("API"), os.Getenv("LOCK_DIR"), n, d)
	fmt.Printf("RESULT calls=%d failures=%d err=%v\n", calls, failures, err)
}

// Six processes with twenty goroutines each keep asking for tokens of
// four services while the CLI access token and the service tokens expire
// every second. Only the refresh rotates anything: exactly one refresh
// per expiry across all processes, and no refresh token presented twice.
func TestOAuthRefreshAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test")
	}
	s := authtest.New()
	defer s.Close()
	// 31 s with the 30 s margin: each token is good for about a second.
	s.AccessTTL, s.ProductTTL = 31, 31
	s.Delay = 20 * time.Millisecond
	_ = discover(t, s)

	storeDir, lockDir := t.TempDir(), t.TempDir()
	prev := SetStore(fileStore{dir: storeDir})
	defer SetStore(prev)
	forgetMemory("", s.URL)
	g := s.NewGrant()
	if err := SaveOAuthSession("", s.URL, s.CurrentRefresh(g), "", time.Time{}, g.Scopes); err != nil {
		t.Fatal(err)
	}

	const (
		children   = 5
		goroutines = 20
		duration   = 3 * time.Second
	)
	type result struct {
		out []byte
		err error
	}
	results := make(chan result, children)
	for range children {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOAuthHelperProcess$", "-test.count=1", "-test.v") // #nosec G204 G702 -- re-runs this test binary
		cmd.Env = append(os.Environ(),
			oauthHelperEnv+"=1",
			"API="+s.URL,
			"STORE_DIR="+storeDir,
			"LOCK_DIR="+lockDir,
			"DURATION="+duration.String(),
			"GOROUTINES="+strconv.Itoa(goroutines),
		)
		go func() {
			out, err := cmd.CombinedOutput()
			results <- result{out, err}
		}()
	}
	calls, failures, firstErr := hammerOAuth(s.URL, lockDir, goroutines, duration)
	if failures != 0 {
		t.Errorf("parent: %d of %d calls failed, first: %v", failures, calls, firstErr)
	}
	for range children {
		r := <-results
		if r.err != nil {
			t.Fatalf("child failed: %v\n%s", r.err, r.out)
		}
		line := ""
		for l := range strings.SplitSeq(string(r.out), "\n") {
			if strings.HasPrefix(l, "RESULT ") {
				line = l
			}
		}
		if line == "" {
			t.Fatalf("child printed no result:\n%s", r.out)
		}
		if !strings.Contains(line, " failures=0 ") {
			t.Errorf("child: %s", line)
		}
	}

	st := s.Stats()
	// GraceHits counts a stale token presented within the server's grace
	// window, which a reuse check alone would not see.
	if st.Reuses != 0 || st.GraceHits != 0 || s.Revoked(g) {
		t.Fatalf("a rotated refresh token was presented again (%d reuses, %d within the grace window)", st.Reuses, st.GraceHits)
	}
	if n := st.Refreshes; n < 2 || n > 5 {
		t.Fatalf("%d refreshes in %s with a ~1s token life; want one per expiry", n, duration)
	}
	for i := 1; i < len(st.RefreshTimes); i++ {
		if gap := st.RefreshTimes[i].Sub(st.RefreshTimes[i-1]); gap < 800*time.Millisecond {
			t.Fatalf("refreshes %d and %d only %v apart: a still-valid token was refreshed again", i-1, i, gap)
		}
	}
	// Exchanges are per process and service, in parallel.
	if s.MaxConcurrentExchanges() < 2 {
		t.Fatalf("exchanges never overlapped (max %d in flight)", s.MaxConcurrentExchanges())
	}
}

// Two processes upgrading the same older sign-in at once: one upgrade,
// the legacy token presented once.
func TestLegacyUpgradeOnceAcrossGoroutines(t *testing.T) {
	s := authtest.New()
	defer s.Close()
	_ = discover(t, s)
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	forgetMemory("", s.URL)
	s.AddLegacySession("legacy-refresh")
	if err := SaveRefreshToken("", s.URL, "legacy-refresh"); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	_, failures, err := hammerOAuth(s.URL, lockDir, 30, 300*time.Millisecond)
	if failures != 0 {
		t.Fatalf("%d failures, first: %v", failures, err)
	}
	if st := s.Stats(); st.Upgrades != 1 || st.UpgradeRefusals != 0 || st.LegacyRefreshes != 0 {
		t.Fatalf("upgrades %d refusals %d legacy refreshes %d", st.Upgrades, st.UpgradeRefusals, st.LegacyRefreshes)
	}
}
