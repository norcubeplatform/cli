package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient builds an API client against srv with no real sleeping, and
// records the waits it would have done.
func testClient(t *testing.T, cfg Config) (*http.Client, *[]time.Duration) {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	rt := c.Transport.(*retryTransport)
	rt.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	return c, &waits
}

func statusServer(t *testing.T, codes []int, hits *atomic.Int32, header http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(hits.Add(1)) - 1
		code := codes[min(n, len(codes)-1)]
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, "body")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRetryCounts(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		idemKey  bool
		codes    []int
		attempts int
		wantHits int32
		wantCode int
	}{
		{"GET 503 then OK", http.MethodGet, false, []int{503, 200}, 3, 2, 200},
		{"GET 429 exhausts attempts", http.MethodGet, false, []int{429}, 3, 3, 429},
		{"GET 502 504 OK", http.MethodGet, false, []int{502, 504, 200}, 3, 3, 200},
		{"GET 500 is not retried", http.MethodGet, false, []int{500, 200}, 3, 1, 500},
		{"GET 404 is not retried", http.MethodGet, false, []int{404, 200}, 3, 1, 404},
		{"DELETE 503 retried", http.MethodDelete, false, []int{503, 200}, 3, 2, 200},
		{"POST without key is never retried", http.MethodPost, false, []int{503, 200}, 3, 1, 503},
		{"POST 429 without key is never retried", http.MethodPost, false, []int{429, 200}, 3, 1, 429},
		{"POST with Idempotency-Key retried", http.MethodPost, true, []int{503, 200}, 3, 2, 200},
		{"max attempts 1 disables retries", http.MethodGet, false, []int{503, 200}, 1, 1, 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := statusServer(t, tt.codes, &hits, nil)
			cfg := Defaults()
			cfg.MaxAttempts = tt.attempts
			c, _ := testClient(t, cfg)

			req, _ := http.NewRequest(tt.method, srv.URL, strings.NewReader(`{"a":1}`))
			if tt.idemKey {
				req.Header.Set(IdempotencyKeyHeader, "k1")
			}
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tt.wantCode || string(b) != "body" {
				t.Fatalf("got %d %q, want %d", resp.StatusCode, b, tt.wantCode)
			}
			if got := hits.Load(); got != tt.wantHits {
				t.Fatalf("server saw %d attempts, want %d", got, tt.wantHits)
			}
		})
	}
}

func TestRetryReplaysBody(t *testing.T) {
	var bodies []string
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := testClient(t, Defaults())
	req, _ := http.NewRequest(http.MethodPut, srv.URL, strings.NewReader("payload"))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(bodies) != 2 || bodies[0] != "payload" || bodies[1] != "payload" {
		t.Fatalf("bodies = %q", bodies)
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	var hits atomic.Int32
	srv := statusServer(t, []int{429, 200}, &hits, http.Header{"Retry-After": {"7"}})
	c, waits := testClient(t, Defaults())
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Fatalf("waits = %v, want [7s]", *waits)
	}
}

func TestRetryAfterTooLongReturnsResponse(t *testing.T) {
	var hits atomic.Int32
	srv := statusServer(t, []int{429, 200}, &hits, http.Header{"Retry-After": {"3600"}})
	c, waits := testClient(t, Defaults())
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 429 || hits.Load() != 1 || len(*waits) != 0 {
		t.Fatalf("status %d, hits %d, waits %v", resp.StatusCode, hits.Load(), *waits)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"0", 0, true},
		{"12", 12 * time.Second, true},
		{"-3", 0, false},
		{now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"soon", 0, false},
	}
	for _, tt := range tests {
		got, ok := retryAfter(tt.in, now)
		if got != tt.want || ok != tt.ok {
			t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestBackoffIsBoundedFullJitter(t *testing.T) {
	for attempt := 1; attempt <= 30; attempt++ {
		ceiling := min(backoffBase<<min(attempt-1, 16), backoffCap)
		for range 50 {
			if d := backoff(attempt); d < 0 || d >= ceiling {
				t.Fatalf("attempt %d: backoff %v outside [0, %v)", attempt, d, ceiling)
			}
		}
	}
}

func TestReadTimeoutFailsSlowServerAndRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	cfg := Defaults()
	cfg.ReadTimeout = 50 * time.Millisecond
	cfg.MaxAttempts = 2
	c, _ := testClient(t, cfg)
	start := time.Now()
	_, err := c.Get(srv.URL)
	var te *TimeoutError
	if !errors.As(err, &te) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want TimeoutError, got %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2 (timeouts are retried for GET)", hits.Load())
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v; the read timeout did not apply", time.Since(start))
	}
}

func TestReadTimeoutCoversBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	cfg := Defaults()
	cfg.ReadTimeout = 80 * time.Millisecond
	c, _ := testClient(t, cfg)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError reading a stalled body, got %v", err)
	}
}

func TestDownloadIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Keep trickling for longer than the idle timeout in total, but
		// never pause longer than it: the download must survive.
		for range 6 {
			_, _ = io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
		}
		// Then stall.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	cfg := Defaults()
	cfg.IdleTimeout = 100 * time.Millisecond
	c, err := NewDownload(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if !errors.Is(err, ErrIdleTimeout) {
		t.Fatalf("want ErrIdleTimeout, got %v", err)
	}
	if string(b) != "xxxxxx" {
		t.Fatalf("got %q before the stall", b)
	}
}

func TestCancelStopsMidRetry(t *testing.T) {
	var hits atomic.Int32
	srv := statusServer(t, []int{503}, &hits, http.Header{"Retry-After": {"30"}})
	c, err := New(Defaults()) // real sleep
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	start := time.Now()
	_, err = c.Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancel took %v", time.Since(start))
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

func TestUserAgent(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("User-Agent"))
	}))
	defer srv.Close()
	c, _ := testClient(t, Defaults())
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("User-Agent", "custom/1")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.HasPrefix(got[0], "norcube/") || !strings.Contains(got[0], " go/") {
		t.Fatalf("default UA = %q", got[0])
	}
	if got[1] != "custom/1" {
		t.Fatalf("explicit UA overwritten: %q", got[1])
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvReadTimeout, "90")
	t.Setenv(EnvConnectTimeout, "2s")
	t.Setenv(EnvMaxAttempts, "5")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReadTimeout != 90*time.Second || cfg.ConnectTimeout != 2*time.Second || cfg.MaxAttempts != 5 {
		t.Fatalf("cfg = %+v", cfg)
	}
	t.Setenv(EnvMaxAttempts, "0")
	if _, err := FromEnv(); err == nil {
		t.Fatal("NORCUBE_MAX_ATTEMPTS=0 accepted")
	}
	t.Setenv(EnvMaxAttempts, "")
	t.Setenv(EnvReadTimeout, "fast")
	if _, err := FromEnv(); err == nil {
		t.Fatal("bad timeout accepted")
	}
}

func TestBadCABundle(t *testing.T) {
	cfg := Defaults()
	cfg.CABundle = t.TempDir() + "/missing.pem"
	if _, err := New(cfg); err == nil {
		t.Fatal("missing CA bundle accepted")
	}
}
