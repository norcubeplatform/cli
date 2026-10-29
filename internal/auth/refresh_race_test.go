package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rotatingServer mimics /oauth/token: every exchange rotates the refresh
// token, and presenting an already-rotated one is treated as token theft
// and ends the session. Any refresh race therefore signs the user out.
type rotatingServer struct {
	expiresIn int

	mu        sync.Mutex
	current   string
	exchanges []time.Time
	reuses    int
}

func (s *rotatingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Widen the race window: a slow exchange is when concurrent callers
	// pile up.
	time.Sleep(20 * time.Millisecond)
	c, err := r.Cookie("jds_refresh_token")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil || s.current == "" || c.Value != s.current {
		s.reuses++
		s.current = ""
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"msg":"Unauthorized."}`)
		return
	}
	s.exchanges = append(s.exchanges, time.Now())
	s.current = fmt.Sprintf("refresh-%d", len(s.exchanges))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  fmt.Sprintf("access-%d", len(s.exchanges)), // opaque: expiry must come from expires_in
		"refresh_token": s.current,
		"expires_in":    s.expiresIn,
	})
}

// fileStore is a Store several processes can share: one file per key,
// replaced atomically.
type fileStore struct{ dir string }

func (f fileStore) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(f.dir, hex.EncodeToString(sum[:]))
}

func (f fileStore) Get(key string) (string, error) {
	b, err := os.ReadFile(f.path(key)) // #nosec G703 -- hashed name in a temp dir
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrSecretNotFound
	}
	return string(b), err
}

func (f fileStore) Set(key, value string) error {
	tmp, err := os.CreateTemp(f.dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(value); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path(key)) // #nosec G703 -- hashed name in a temp dir
}

func (f fileStore) Delete(key string) error {
	err := os.Remove(f.path(key)) // #nosec G703 -- a test store under t.TempDir()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// hammer calls Token from n goroutines until the deadline: half share one
// TokenSource (like `langsync sync`), half build their own (like separate
// commands). It returns the number of calls and of failures.
func hammer(api, lockDir string, n int, d time.Duration) (calls, failures int64, firstErr error) {
	var c, f atomic.Int64
	var once sync.Once
	shared := NewTokenSource(api, "aud", "org-1")
	shared.LockDir = lockDir
	deadline := time.Now().Add(d)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ts := shared
			if i%2 == 1 {
				ts = NewTokenSource(api, "aud", "org-1")
				ts.LockDir = lockDir
			}
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

const helperEnv = "NORCUBE_TOKEN_HELPER"

// TestHelperProcess is the child side of TestRefreshAcrossProcesses.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper for TestRefreshAcrossProcesses")
	}
	SetStore(fileStore{dir: os.Getenv("STORE_DIR")})
	d, _ := time.ParseDuration(os.Getenv("DURATION"))
	n, _ := strconv.Atoi(os.Getenv("GOROUTINES"))
	calls, failures, err := hammer(os.Getenv("API"), os.Getenv("LOCK_DIR"), n, d)
	fmt.Printf("RESULT calls=%d failures=%d err=%v\n", calls, failures, err)
}

// Twenty goroutines in each of six processes (five children plus this
// one) keep asking for a token while it expires every second. The
// refresh lock must leave exactly one exchange per expiry, with no
// refresh token ever presented twice.
func TestRefreshAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test")
	}
	// expires_in 31 with the 30s margin: each token is good for ~1s.
	srv := &rotatingServer{expiresIn: 31, current: "refresh-0"}
	hs := httptest.NewServer(srv)
	defer hs.Close()

	storeDir, lockDir := t.TempDir(), t.TempDir()
	prev := SetStore(fileStore{dir: storeDir})
	defer SetStore(prev)
	if err := SaveRefreshToken("", hs.URL, "refresh-0"); err != nil {
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
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1", "-test.v") // #nosec G204 G702 -- re-runs this test binary
		cmd.Env = append(os.Environ(),
			helperEnv+"=1",
			"API="+hs.URL,
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
	calls, failures, firstErr := hammer(hs.URL, lockDir, goroutines, duration)
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

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.reuses != 0 {
		t.Fatalf("a rotated refresh token was presented again %d times", srv.reuses)
	}
	if n := len(srv.exchanges); n < 2 || n > 5 {
		t.Fatalf("%d exchanges in %s with a ~1s token life; want one per expiry", n, duration)
	}
	for i := 1; i < len(srv.exchanges); i++ {
		if gap := srv.exchanges[i].Sub(srv.exchanges[i-1]); gap < 800*time.Millisecond {
			t.Fatalf("exchanges %d and %d only %v apart: a still-valid token was refreshed again", i-1, i, gap)
		}
	}
}

// Many goroutines asking at once for a token nobody has yet cause one
// exchange, not one each.
func TestConcurrentFirstTokenExchangesOnce(t *testing.T) {
	srv := &rotatingServer{expiresIn: 300, current: "refresh-0"}
	hs := httptest.NewServer(srv)
	defer hs.Close()
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	if err := SaveRefreshToken("", hs.URL, "refresh-0"); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	_, failures, err := hammer(hs.URL, lockDir, 50, 200*time.Millisecond)
	if failures != 0 {
		t.Fatalf("%d failures, first: %v", failures, err)
	}
	if len(srv.exchanges) != 1 || srv.reuses != 0 {
		t.Fatalf("exchanges %d, reuses %d; want 1, 0", len(srv.exchanges), srv.reuses)
	}
}

// The cached expiry comes from expires_in on the local clock, so a token
// whose JWT exp looks past (a local clock running fast) is still reused.
func TestExpiryUsesExpiresInNotLocalClockVsJWT(t *testing.T) {
	var exchanges atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		exchanges.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			// exp claim already in the past by the local clock
			"access_token": fakeJWT(t, time.Now().Add(-time.Hour)),
			"expires_in":   300,
		})
	}))
	defer hs.Close()
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	_ = SaveRefreshToken("", hs.URL, "r")
	ts := newTestSource(t, hs.URL)
	for range 3 {
		if _, err := ts.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if exchanges.Load() != 1 {
		t.Fatalf("%d exchanges; the expires_in-based cache was not used", exchanges.Load())
	}

	// And it does refresh once expires_in has run out.
	ts.now = func() time.Time { return time.Now().Add(300*time.Second - expiryMargin + time.Second) }
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 2 {
		t.Fatalf("%d exchanges; an expiring token was reused", exchanges.Load())
	}
}

// The default profile uses the refresh token an older CLI stored per auth
// URL; other profiles don't. Reading never writes, so a stale copy can't
// appear while an older CLI keeps rotating the old item; the first save
// moves the session into the profile's item; logout removes both.
func TestLegacyRefreshTokenAdoption(t *testing.T) {
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	const api = "https://legacy.test"
	if err := store.Set(legacyRefreshKey(api), "old-refresh"); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadRefreshToken("work", api); err == nil {
		t.Fatalf("named profile adopted the legacy token: %q", got)
	}
	if got, err := LoadRefreshToken("", api); err != nil || got != "old-refresh" {
		t.Fatalf("default profile: %q %v", got, err)
	}
	if !HasCredentials("", api) {
		t.Fatal("HasCredentials misses the legacy session")
	}
	if _, err := store.Get(profileKey("")); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("reading wrote the profile item: %v", err)
	}

	// An older CLI rotates the old item meanwhile: the next read sees it.
	if err := store.Set(legacyRefreshKey(api), "old-refresh-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadRefreshToken("", api); got != "old-refresh-2" {
		t.Fatalf("stale legacy token: %q", got)
	}

	// The first rotation moves the session.
	if err := SaveRefreshToken("", api, "rotated"); err != nil {
		t.Fatal(err)
	}
	if v, err := store.Get(profileKey("")); err != nil || !strings.Contains(v, "rotated") {
		t.Fatalf("not saved into the profile: %q %v", v, err)
	}
	if _, err := store.Get(legacyRefreshKey(api)); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("legacy item kept after the move: %v", err)
	}
	if got, _ := LoadRefreshToken("", api); got != "rotated" {
		t.Fatalf("after the move: %q", got)
	}

	if err := DeleteCredentials("", api); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadRefreshToken("", api); err == nil {
		t.Fatalf("still signed in after logout: %q", got)
	}
}

// A profile's session belongs to one auth URL: a blob for another one
// (the profile's environment changed) is never sent.
func TestCredentialsBoundToAuthURL(t *testing.T) {
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	if err := SaveRefreshToken("work", "https://other.test", "other-refresh"); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadRefreshToken("work", "https://prod.test"); err == nil {
		t.Fatalf("another endpoint's session used for this one: %q", got)
	}
	if got, _ := LoadRefreshToken("work", "https://other.test"); got != "other-refresh" {
		t.Fatalf("got %q", got)
	}
}

// Profiles keep separate sessions and separate refresh locks.
func TestProfilesAreIndependent(t *testing.T) {
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	const api = "https://prod.test"
	_ = SaveRefreshToken("a", api, "refresh-a")
	_ = SaveRefreshToken("b", api, "refresh-b")
	if err := DeleteCredentials("a", api); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRefreshToken("a", api); err == nil {
		t.Fatal("profile a still signed in")
	}
	if got, _ := LoadRefreshToken("b", api); got != "refresh-b" {
		t.Fatalf("logout of a touched b: %q", got)
	}

	dir := t.TempDir()
	unlockA, err := LockProfile(context.Background(), dir, "a", api, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockA()
	unlockB, err := LockProfile(context.Background(), dir, "b", api, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("profile b waited for profile a's lock: %v", err)
	}
	unlockB()
}

// A refresh stuck behind another process's lock gives up with a clear
// error after LockTimeout.
func TestRefreshLockTimeout(t *testing.T) {
	prev := SetStore(fileStore{dir: t.TempDir()})
	defer SetStore(prev)
	_ = SaveRefreshToken("", "https://locked.test", "r")
	holder := newTestSource(t, "https://locked.test")
	unlock, err := LockProfile(context.Background(), holder.LockDir, holder.Profile, holder.APIBase, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	waiter := NewTokenSource("https://locked.test", "aud", "org-2")
	waiter.LockDir = holder.LockDir
	waiter.LockTimeout = 150 * time.Millisecond
	_, err = waiter.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "waiting for another norcube process") {
		t.Fatalf("want a lock timeout error, got %v", err)
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Fatal("a lock timeout is not a login problem")
	}

	// Cancelling the command while waiting returns at once.
	waiter.LockTimeout = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := waiter.Token(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context error, got %v", err)
	}
}
