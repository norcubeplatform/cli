package transport

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Backoff between attempts: exponential with full jitter (the AWS SDK
// "standard" retry mode), so a fleet of CI jobs hitting a 503 at once
// spreads out instead of retrying in lockstep.
const (
	backoffBase = 200 * time.Millisecond
	backoffCap  = 20 * time.Second
	// maxRetryAfter is the longest server-requested wait we honor. A
	// longer Retry-After means "not soon": the response is returned so the
	// user sees it instead of a command that silently hangs.
	maxRetryAfter = 60 * time.Second
)

// IdempotencyKeyHeader marks a POST as safe to retry.
const IdempotencyKeyHeader = "Idempotency-Key"

type retryTransport struct {
	base        http.RoundTripper
	maxAttempts int
	// attemptTTL bounds each attempt, body included. Zero: no bound.
	attemptTTL time.Duration
	// idleTTL bounds the silence between body reads. Zero: no bound.
	idleTTL   time.Duration
	userAgent string

	// sleep is replaced in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.userAgent != "" && req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.userAgent)
	}
	attempts := max(t.maxAttempts, 1)
	if !canRetry(req) {
		attempts = 1
	}
	sleep := t.sleep
	if sleep == nil {
		sleep = sleepCtx
	}

	for attempt := 1; ; attempt++ {
		resp, err := t.attempt(req, attempt)
		if attempt >= attempts || !shouldRetry(req.Context(), resp, err) {
			return resp, err
		}
		wait := backoff(attempt)
		if resp != nil {
			if ra, ok := retryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
				if ra > maxRetryAfter {
					return resp, nil
				}
				wait = max(wait, ra)
			}
			// Drain a little so the connection can be reused, then close.
			_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
			_ = resp.Body.Close()
		}
		if err := sleep(req.Context(), wait); err != nil {
			return nil, err
		}
	}
}

// attempt sends one try. Its context carries the per-attempt deadline,
// which keeps running while the caller reads the body; closing the body
// releases it.
func (t *retryTransport) attempt(req *http.Request, n int) (*http.Response, error) {
	r := req
	if n > 1 {
		r = req.Clone(req.Context())
		if req.Body != nil && req.Body != http.NoBody {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r.Body = body
		}
	}
	if t.attemptTTL <= 0 && t.idleTTL <= 0 {
		return t.base.RoundTrip(r)
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	var stopTimer func() bool
	if t.attemptTTL > 0 {
		timer := time.AfterFunc(t.attemptTTL, func() { cancel(context.DeadlineExceeded) })
		stopTimer = timer.Stop
	}
	resp, err := t.base.RoundTrip(r.WithContext(ctx))
	if err != nil {
		if stopTimer != nil {
			stopTimer()
		}
		// Report the timeout, not a bare "context canceled".
		if cause := context.Cause(ctx); errors.Is(cause, context.DeadlineExceeded) && r.Context().Err() == nil {
			err = &TimeoutError{After: t.attemptTTL}
		}
		cancel(nil)
		return nil, err
	}
	b := &body{rc: resp.Body, cancel: cancel, stopTimer: stopTimer, ctx: ctx, parent: r.Context(), ttl: t.attemptTTL}
	if t.idleTTL > 0 {
		b.idle = t.idleTTL
		b.idleTimer = time.AfterFunc(t.idleTTL, func() { cancel(ErrIdleTimeout) })
	}
	resp.Body = b
	return resp, nil
}

// body releases the attempt's context on Close and, for downloads, pushes
// the idle deadline forward on every read that returns data.
type body struct {
	rc        io.ReadCloser
	cancel    context.CancelCauseFunc
	stopTimer func() bool
	ctx       context.Context
	parent    context.Context
	ttl       time.Duration
	idle      time.Duration
	idleTimer *time.Timer
	once      sync.Once
}

func (b *body) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 && b.idleTimer != nil {
		b.idleTimer.Reset(b.idle)
	}
	if err != nil && !errors.Is(err, io.EOF) && b.parent.Err() == nil {
		switch cause := context.Cause(b.ctx); {
		case errors.Is(cause, ErrIdleTimeout):
			err = cause
		case errors.Is(cause, context.DeadlineExceeded):
			err = &TimeoutError{After: b.ttl}
		}
	}
	return n, err
}

// TimeoutError reports an attempt that ran past the read timeout.
type TimeoutError struct{ After time.Duration }

func (e *TimeoutError) Error() string {
	return "no complete response within " + e.After.String() + " (raise it with --cli-read-timeout or NORCUBE_READ_TIMEOUT)"
}

// Timeout lets net.Error-style checks recognize it.
func (e *TimeoutError) Timeout() bool { return true }

// Unwrap makes errors.Is(err, context.DeadlineExceeded) hold.
func (e *TimeoutError) Unwrap() error { return context.DeadlineExceeded }

func (b *body) Close() error {
	err := b.rc.Close()
	b.once.Do(func() {
		if b.stopTimer != nil {
			b.stopTimer()
		}
		if b.idleTimer != nil {
			b.idleTimer.Stop()
		}
		b.cancel(nil)
	})
	return err
}

// canRetry reports whether repeating req can't apply a change twice:
// idempotent methods, or a POST/PATCH carrying an Idempotency-Key. The
// body must also be replayable.
func canRetry(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
	default:
		if req.Header.Get(IdempotencyKeyHeader) == "" {
			return false
		}
	}
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// shouldRetry: throttling (429), gateway and availability errors
// (502/503/504) and network failures. Never other 4xx, never a request
// the user cancelled.
func shouldRetry(ctx context.Context, resp *http.Response, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoff returns a random wait in [0, min(cap, base*2^(attempt-1))).
func backoff(attempt int) time.Duration {
	ceiling := backoffBase << min(attempt-1, 16)
	if ceiling <= 0 || ceiling > backoffCap {
		ceiling = backoffCap
	}
	return time.Duration(rand.Int64N(int64(ceiling))) // #nosec G404 -- jitter, not security
}

// retryAfter parses a Retry-After header: delay-seconds or an HTTP date.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if s, err := strconv.Atoi(v); err == nil {
		if s < 0 {
			return 0, false
		}
		return time.Duration(s) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
