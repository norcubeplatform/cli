// Package sse reads server-sent event streams (text/event-stream), as the
// HTML standard defines them, and keeps a stream going across dropped
// connections.
package sse

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Event is one dispatched event. Name is "message" when the stream gave
// no event field.
type Event struct {
	Name string
	Data string
	ID   string
}

// Read parses r and calls handle for every event until r ends, handle
// returns stop=true or an error, or ctx ends. It returns nil when the
// stream ended or handle stopped it.
func Read(ctx context.Context, r io.Reader, handle func(Event) (stop bool, err error)) error {
	sc := bufio.NewScanner(r)
	// A single event carries one JSON object; allow large ones.
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var (
		name, id string
		data     []string
	)
	dispatch := func() (bool, error) {
		if len(data) == 0 {
			name = ""
			return false, nil
		}
		ev := Event{Name: name, Data: strings.Join(data, "\n"), ID: id}
		if ev.Name == "" {
			ev.Name = "message"
		}
		name, data = "", nil
		return handle(ev)
	}
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Text()
		if line == "" {
			stop, err := dispatch()
			if err != nil || stop {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment, used as a keep-alive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			data = append(data, value)
		case "id":
			id = value
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	_, err := dispatch()
	return err
}

// ErrStreamFailed wraps the error that ended a stream after its last
// allowed reconnect.
var ErrStreamFailed = errors.New("event stream failed")

// FollowOptions controls Follow.
type FollowOptions struct {
	// MaxReconnects is how many times a dropped connection is reopened
	// before giving up (default 5).
	MaxReconnects int
	// Backoff is the first delay before reconnecting; it doubles up to
	// 10 s (default 1 s).
	Backoff time.Duration
	// Sleep waits between attempts; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Follow opens a stream with open and reads it with Read. When the
// connection drops before handle stops it, Follow reopens it after a
// backoff, up to MaxReconnects times in a row (a stream that delivers
// events resets the count). Servers that replay earlier events on a new
// connection make handle see them again; handle should be idempotent.
// open returns the response; a non-2xx status is returned as is, without
// a reconnect.
func Follow(ctx context.Context, opts FollowOptions, open func(ctx context.Context) (*http.Response, error), handle func(Event) (stop bool, err error)) error {
	maxReconnects := opts.MaxReconnects
	if maxReconnects <= 0 {
		maxReconnects = 5
	}
	backoff := opts.Backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	failures := 0
	delay := backoff
	for {
		resp, err := open(ctx)
		if err == nil && (resp.StatusCode < 200 || resp.StatusCode > 299) {
			return &StatusError{Response: resp}
		}
		var stopped, delivered bool
		if err == nil {
			err = Read(ctx, resp.Body, func(ev Event) (bool, error) {
				delivered = true
				stop, herr := handle(ev)
				stopped = stop
				return stop, herr
			})
			_ = resp.Body.Close()
			if err == nil && stopped {
				return nil
			}
			if err != nil && !isConnectionError(err) {
				return err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if delivered {
			failures, delay = 0, backoff
		}
		failures++
		if failures > maxReconnects {
			if err == nil {
				err = errors.New("the server closed the stream")
			}
			return fmt.Errorf("%w after %d reconnects: %w", ErrStreamFailed, maxReconnects, err)
		}
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		delay *= 2
		if delay > 10*time.Second {
			delay = 10 * time.Second
		}
	}
}

// StatusError is a stream that answered with a non-2xx status. The caller
// owns Response (its body is unread) and turns it into an API error.
type StatusError struct{ Response *http.Response }

func (e *StatusError) Error() string {
	return fmt.Sprintf("event stream: status %d", e.Response.StatusCode)
}

// isConnectionError reports errors worth a reconnect: the body ended
// abruptly or the network failed, as opposed to a handler error.
func isConnectionError(err error) bool {
	var handlerErr *handlerError
	return !errors.As(err, &handlerErr)
}

// handlerError marks errors returned by the caller's handler so Follow
// doesn't reconnect after them.
type handlerError struct{ err error }

func (e *handlerError) Error() string { return e.err.Error() }
func (e *handlerError) Unwrap() error { return e.err }

// HandlerError wraps err so Follow returns it instead of reconnecting.
// Handlers return it for errors in their own logic (bad JSON, a write to
// stdout failing).
func HandlerError(err error) error {
	if err == nil {
		return nil
	}
	return &handlerError{err: err}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
