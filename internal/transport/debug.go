package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// EnvDebug turns on the HTTP trace, like --debug.
const EnvDebug = "NORCUBE_DEBUG"

// Redacted replaces secrets in the trace.
const Redacted = "[REDACTED]"

// maxTracedBody is the largest request or response body the trace prints.
// Bigger bodies (and streams of unknown length) are summarized.
const maxTracedBody = 16 << 10

// debugTransport writes every attempt of every request to w: method, URL,
// headers, small JSON or form bodies, status, timing and request id, with
// credentials redacted. It sits under the retry layer, so retries show up
// as separate attempts.
type debugTransport struct {
	base http.RoundTripper
	w    io.Writer
	mu   sync.Mutex
	seq  int
}

func (t *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.seq++
	id := t.seq
	t.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "--> #%d %s %s\n", id, req.Method, RedactURL(req.URL))
	writeHeaders(&b, req.Header)
	if body, ok := peekRequestBody(req); ok {
		fmt.Fprintf(&b, "    body: %s\n", body)
	}
	t.write(b.String())

	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Millisecond)

	b.Reset()
	if err != nil {
		fmt.Fprintf(&b, "<-- #%d error after %s: %v\n", id, elapsed, err)
		t.write(b.String())
		return resp, err
	}
	fmt.Fprintf(&b, "<-- #%d %s (%s)", id, resp.Status, elapsed)
	if rid := requestID(resp.Header); rid != "" {
		fmt.Fprintf(&b, " request-id=%s", rid)
	}
	b.WriteString("\n")
	writeHeaders(&b, resp.Header)
	if body, ok := peekResponseBody(resp); ok {
		fmt.Fprintf(&b, "    body: %s\n", body)
	}
	t.write(b.String())
	return resp, nil
}

func (t *debugTransport) write(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = io.WriteString(t.w, s)
}

func requestID(h http.Header) string {
	for _, k := range []string{"X-Request-Id", "X-Amzn-Requestid", "Cf-Ray"} {
		if v := h.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// sensitiveHeaders are printed with their value replaced.
var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"Apikey":              true,
	"X-Api-Key":           true,
}

func writeHeaders(b *strings.Builder, h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintf(b, "    %s: %s\n", k, redactHeader(k, v))
		}
	}
}

func redactHeader(name, value string) string {
	if !sensitiveHeaders[http.CanonicalHeaderKey(name)] {
		return value
	}
	// Keep the scheme ("Bearer") so the trace still shows what kind of
	// credential was sent.
	if scheme, _, ok := strings.Cut(value, " "); ok && http.CanonicalHeaderKey(name) == "Authorization" {
		return scheme + " " + Redacted
	}
	return Redacted
}

// sensitiveKey reports whether a query parameter or body field carries a
// credential: tokens, secrets, passwords, API keys and presigned-URL
// signatures.
func sensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, part := range []string{"token", "secret", "password", "apikey", "api_key", "signature", "credential", "authorization", "cookie", "sig"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return k == "code" || k == "x-amz-security-token"
}

// RedactURL returns u as a string with credential query values replaced,
// e.g. the signature of a presigned storage URL.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	if c.RawQuery != "" {
		q := c.Query()
		for k := range q {
			if sensitiveKey(k) {
				q[k] = []string{Redacted}
			}
		}
		c.RawQuery = q.Encode()
	}
	if c.User != nil {
		c.User = url.User(Redacted)
	}
	return c.String()
}

func peekRequestBody(req *http.Request) (string, bool) {
	if req.GetBody == nil || req.ContentLength <= 0 || req.ContentLength > maxTracedBody {
		if req.ContentLength > maxTracedBody {
			return fmt.Sprintf("(%d bytes, not shown)", req.ContentLength), true
		}
		return "", false
	}
	rc, err := req.GetBody()
	if err != nil {
		return "", false
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxTracedBody))
	if err != nil {
		return "", false
	}
	return RedactBody(req.Header.Get("Content-Type"), raw), true
}

func peekResponseBody(resp *http.Response) (string, bool) {
	if resp.Body == nil || resp.ContentLength == 0 {
		return "", false
	}
	if resp.ContentLength < 0 || resp.ContentLength > maxTracedBody {
		// A stream or a download: reading it here would buffer it.
		return "", false
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	return RedactBody(resp.Header.Get("Content-Type"), raw), true
}

// RedactBody renders a JSON or form body with credential fields replaced.
// Other content types are summarized by size.
func RedactBody(contentType string, raw []byte) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch {
	case mt == "application/x-www-form-urlencoded":
		v, err := url.ParseQuery(string(raw))
		if err != nil {
			break
		}
		for k := range v {
			if sensitiveKey(k) {
				v[k] = []string{Redacted}
			}
		}
		return v.Encode()
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			break
		}
		out, err := json.Marshal(redactJSON(doc))
		if err != nil {
			break
		}
		return string(out)
	}
	return fmt.Sprintf("(%d bytes of %s, not shown)", len(raw), orUnknown(mt))
}

func redactJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if sensitiveKey(k) {
				x[k] = Redacted
				continue
			}
			x[k] = redactJSON(val)
		}
	case []any:
		for i := range x {
			x[i] = redactJSON(x[i])
		}
	case string:
		// Presigned download links and connection strings with a
		// password can come back inside JSON bodies.
		if u, err := url.Parse(x); err == nil && u.Scheme != "" && u.Host != "" && (u.RawQuery != "" || u.User != nil) {
			return RedactURL(u)
		}
	}
	return v
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown type"
	}
	return s
}
