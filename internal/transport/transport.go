// Package transport builds the one HTTP client every part of the CLI uses:
// generated API clients, the auth client, the token exchange and artifact
// downloads. It owns timeouts, retries, proxy and CA settings and the
// User-Agent, so no command has to think about them.
package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/norcubeplatform/cli/internal/buildinfo"
)

// Defaults. ReadTimeout bounds one attempt of an API request, from sending
// it to reading the last byte of the response.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultReadTimeout    = 60 * time.Second
	DefaultMaxAttempts    = 3
	// DefaultIdleTimeout is how long a streaming download may go without
	// receiving a single byte before it is abandoned.
	DefaultIdleTimeout = 60 * time.Second

	tlsHandshakeTimeout = 10 * time.Second
)

// Environment variables read by FromEnv.
const (
	EnvConnectTimeout = "NORCUBE_CONNECT_TIMEOUT"
	EnvReadTimeout    = "NORCUBE_READ_TIMEOUT"
	EnvMaxAttempts    = "NORCUBE_MAX_ATTEMPTS"
	EnvCABundle       = "NORCUBE_CA_BUNDLE"
)

// Config describes the client. The zero value is not useful; start from
// Defaults() or FromEnv().
type Config struct {
	// ConnectTimeout bounds the TCP dial. Zero means no limit.
	ConnectTimeout time.Duration
	// ReadTimeout bounds one attempt of an API request including reading
	// its body. Zero means no limit. Downloads use IdleTimeout instead.
	ReadTimeout time.Duration
	// IdleTimeout bounds the silence between two reads of a streaming
	// download. Zero means no limit.
	IdleTimeout time.Duration
	// MaxAttempts is the total number of tries for a retryable request,
	// including the first one. Values below 1 are treated as 1.
	MaxAttempts int
	// CABundle is a PEM file whose certificates are trusted in addition
	// to the system roots (corporate TLS-inspecting proxies).
	CABundle string
	// UserAgent is sent on requests that don't set their own.
	UserAgent string
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		ConnectTimeout: DefaultConnectTimeout,
		ReadTimeout:    DefaultReadTimeout,
		IdleTimeout:    DefaultIdleTimeout,
		MaxAttempts:    DefaultMaxAttempts,
		UserAgent:      UserAgent(),
	}
}

// FromEnv returns Defaults() overridden by the NORCUBE_* environment
// variables. Timeouts accept whole seconds ("30") or Go durations ("1m30s").
func FromEnv() (Config, error) {
	return FromEnvOver(Defaults())
}

// FromEnvOver applies the NORCUBE_* variables that are set on top of cfg.
func FromEnvOver(cfg Config) (Config, error) {
	if v := os.Getenv(EnvConnectTimeout); v != "" {
		d, err := ParseTimeout(v)
		if err != nil {
			return cfg, fmt.Errorf("%s: %w", EnvConnectTimeout, err)
		}
		cfg.ConnectTimeout = d
	}
	if v := os.Getenv(EnvReadTimeout); v != "" {
		d, err := ParseTimeout(v)
		if err != nil {
			return cfg, fmt.Errorf("%s: %w", EnvReadTimeout, err)
		}
		cfg.ReadTimeout = d
	}
	if v := os.Getenv(EnvMaxAttempts); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("%s: want a whole number of at least 1, got %q", EnvMaxAttempts, v)
		}
		cfg.MaxAttempts = n
	}
	cfg.CABundle = os.Getenv(EnvCABundle)
	return cfg, nil
}

// ParseTimeout reads a timeout given as whole seconds or as a Go duration.
// "0" means no timeout.
func ParseTimeout(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("timeout must not be negative, got %q", v)
		}
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("want seconds or a duration like 90s, got %q", v)
	}
	if d < 0 {
		return 0, fmt.Errorf("timeout must not be negative, got %q", v)
	}
	return d, nil
}

// UserAgent identifies the CLI build to the server, e.g.
// "norcube/0.16.0 (darwin; arm64) go/go1.26.1".
func UserAgent() string {
	return fmt.Sprintf("norcube/%s (%s; %s) %s", buildinfo.Version, runtime.GOOS, runtime.GOARCH,
		strings.Replace(runtime.Version(), "go", "go/", 1))
}

// New returns the client for API requests: per-attempt ReadTimeout and
// retries on throttling and transient server or network failures.
func New(cfg Config) (*http.Client, error) {
	base, err := baseTransport(cfg)
	if err != nil {
		return nil, err
	}
	// No ResponseHeaderTimeout: the per-attempt timer already covers
	// waiting for headers, and reports it as a TimeoutError.
	return &http.Client{Transport: &retryTransport{
		base:        base,
		maxAttempts: cfg.MaxAttempts,
		attemptTTL:  cfg.ReadTimeout,
		userAgent:   cfg.UserAgent,
	}}, nil
}

// NewDownload returns the client for streaming large bodies (backup
// artifacts). A download may take as long as it needs, but the server
// must answer within ReadTimeout and then keep bytes flowing: a body that
// stalls for IdleTimeout fails with ErrIdleTimeout. Retries apply until
// the response headers arrive.
func NewDownload(cfg Config) (*http.Client, error) {
	base, err := baseTransport(cfg)
	if err != nil {
		return nil, err
	}
	base.ResponseHeaderTimeout = cfg.ReadTimeout
	return &http.Client{Transport: &retryTransport{
		base:        base,
		maxAttempts: cfg.MaxAttempts,
		idleTTL:     cfg.IdleTimeout,
		userAgent:   cfg.UserAgent,
	}}, nil
}

// Default returns an API client with the built-in settings, for code that
// runs without command-line context (tests, library defaults).
func Default() *http.Client {
	c, _ := New(Defaults()) // only a CA bundle can fail, and Defaults has none
	return c
}

func baseTransport(cfg Config) (*http.Transport, error) {
	dialer := &net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30 * time.Second}
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}
	if cfg.CABundle != "" {
		pool, err := loadCABundle(cfg.CABundle)
		if err != nil {
			return nil, err
		}
		t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return t, nil
}

func loadCABundle(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA bundle %s: %w", path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA bundle %s holds no PEM certificates", path)
	}
	return pool, nil
}

// ErrIdleTimeout is returned while reading a download body that received
// no data for the configured idle timeout.
var ErrIdleTimeout = errors.New("connection stalled: no data received within the idle timeout")
