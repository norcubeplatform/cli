package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/transport"
)

// Device sign-in (OAuth 2.0 Device Authorization Grant, RFC 8628) for
// machines without a usable browser: the CLI shows a short code, the
// person approves it at <web app>/device on any device (a phone works),
// and the CLI receives the same session a browser login gives.

// DeviceClientID identifies the CLI to Norcube Auth's device endpoints.
const DeviceClientID = "norcube-cli"

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// DeviceCode is Norcube Auth's answer to a device sign-in request.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceLoginOptions configures DeviceLogin.
type DeviceLoginOptions struct {
	AuthURL string
	HTTP    *http.Client
	// Hostname labels the session under Account › CLI sessions.
	Hostname string
	// OnCode is called once with the code to show the person.
	OnCode func(DeviceCode)
	// OnWait is called before every poll with the time left.
	OnWait func(left time.Duration)

	// sleep and now are replaced in tests.
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
}

// Errors a device sign-in ends with. Both mean the person is still not
// signed in, so they map to exit code 4.
var (
	ErrDeviceDenied  = errors.New("the sign-in was denied on the approval page")
	ErrDeviceExpired = errors.New("the code expired before it was approved")
)

// DeviceLogin runs the whole device flow and returns the session, in the
// same shape a browser login delivers. Polling follows RFC 8628: never
// faster than the server's interval, 5 seconds slower after every
// slow_down, until the code expires. Network errors back off and retry
// within the code's lifetime. Cancelling ctx stops it (Ctrl-C); the
// pending request then simply expires on the server.
func DeviceLogin(ctx context.Context, opts DeviceLoginOptions) (*CallbackPayload, error) {
	if opts.HTTP == nil {
		opts.HTTP = transport.Default()
	}
	if opts.sleep == nil {
		opts.sleep = sleepCtx
	}
	if opts.now == nil {
		opts.now = time.Now
	}

	code, err := requestDeviceCode(ctx, opts)
	if err != nil {
		return nil, err
	}
	if opts.OnCode != nil {
		opts.OnCode(*code)
	}

	interval := time.Duration(max(code.Interval, 1)) * time.Second
	deadline := opts.now().Add(time.Duration(code.ExpiresIn) * time.Second)
	backoff := interval
	for {
		left := deadline.Sub(opts.now())
		if left <= 0 {
			return nil, deviceEnded(ErrDeviceExpired)
		}
		if opts.OnWait != nil {
			opts.OnWait(left)
		}
		if err := opts.sleep(ctx, interval); err != nil {
			return nil, err
		}
		payload, pollErr, err := pollDeviceToken(ctx, opts, code.DeviceCode)
		switch {
		case err != nil:
			// A dropped connection or a 5xx: keep trying, more slowly,
			// while the code is valid.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			backoff = min(backoff*2, time.Minute)
			if err := opts.sleep(ctx, backoff-interval); err != nil {
				return nil, err
			}
			continue
		case payload != nil:
			return payload, nil
		}
		backoff = interval
		switch pollErr {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
			backoff = interval
		case "access_denied":
			return nil, deviceEnded(ErrDeviceDenied)
		case "expired_token":
			return nil, deviceEnded(ErrDeviceExpired)
		default:
			return nil, fmt.Errorf("device sign-in failed: %s", pollErr)
		}
	}
}

// deviceEnded wraps a terminal outcome so it exits 4 with a useful hint.
func deviceEnded(err error) error {
	hint := "Run `norcube login --device` again."
	if errors.Is(err, ErrDeviceDenied) {
		hint = "If that wasn't you, nothing happened: the code is now void. Run `norcube login --device` again to sign in."
	}
	return &clierr.Error{Code: clierr.CodeLoginRequired, Message: err.Error(), Hint: hint, Err: err}
}

func requestDeviceCode(ctx context.Context, opts DeviceLoginOptions) (*DeviceCode, error) {
	form := url.Values{"client_id": {DeviceClientID}}
	host := opts.Hostname
	if host == "" {
		host = deviceName()
	}
	if host != "" {
		form.Set("hostname", truncate(host))
	}
	resp, raw, err := postForm(ctx, opts.HTTP, opts.AuthURL+"/auth/cli/device/code", form, true)
	if err != nil {
		return nil, fmt.Errorf("start device sign-in: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
			Msg         string `json:"msg"`
		}
		_ = json.Unmarshal(raw, &body)
		msg := strings.TrimSpace(firstNonEmpty(body.Description, body.Msg, body.Error, resp.Status))
		e := &clierr.Error{Code: clierr.CodeForStatus(resp.StatusCode, body.Error), Message: "start device sign-in: " + msg, HTTPStatus: resp.StatusCode, Service: "auth"}
		if resp.StatusCode == http.StatusTooManyRequests {
			e.Code = clierr.CodeRateLimited
			e.Hint = "Too many sign-in requests from this network; wait a few minutes."
		}
		return nil, e
	}
	var code DeviceCode
	if err := json.Unmarshal(raw, &code); err != nil {
		return nil, fmt.Errorf("decode device sign-in: %w", err)
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI == "" {
		return nil, errors.New("device sign-in: Norcube Auth returned an incomplete answer")
	}
	if code.ExpiresIn <= 0 {
		code.ExpiresIn = 600
	}
	return &code, nil
}

// pollDeviceToken asks once. It returns the session when approved, the
// RFC 8628 error code while not, or err for transport and server errors.
func pollDeviceToken(ctx context.Context, opts DeviceLoginOptions, deviceCode string) (*CallbackPayload, string, error) {
	form := url.Values{
		"grant_type":  {deviceGrantType},
		"device_code": {deviceCode},
		"client_id":   {DeviceClientID},
	}
	// Not retried by the transport: the loop owns the pacing, and a poll
	// repeated behind its back would earn a slow_down.
	resp, raw, err := postForm(ctx, opts.HTTP, opts.AuthURL+"/auth/cli/device/token", form, false)
	if err != nil {
		return nil, "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var payload CallbackPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, "", fmt.Errorf("decode device sign-in: %w", err)
		}
		if payload.RefreshToken == "" {
			return nil, "", errors.New("device sign-in: Norcube Auth returned no session")
		}
		return &payload, "", nil
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests:
		var body struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &body) == nil && body.Error != "" {
			return nil, body.Error, nil
		}
	}
	return nil, "", fmt.Errorf("device sign-in: %s", resp.Status)
}

func postForm(ctx context.Context, httpc *http.Client, endpoint string, form url.Values, retryable bool) (*http.Response, []byte, error) {
	// #nosec G704 -- the Norcube Auth URL the user configured.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if retryable {
		req.Header.Set(transport.IdempotencyKeyHeader, newIdempotencyKey())
	}
	resp, err := httpc.Do(req) // #nosec G704 -- see above
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, raw, nil
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
