package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/norcubeplatform/cli/internal/clierr"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/buildinfo"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/credstore"
	"github.com/norcubeplatform/cli/internal/output"
	"github.com/norcubeplatform/cli/internal/registry"
	"github.com/norcubeplatform/cli/internal/resolve"
	"github.com/norcubeplatform/cli/internal/transport"
)

// Doctor check outcomes.
const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
	checkSkip = "skip"
)

// doctorCheck is one line of `nrc doctor`.
type doctorCheck struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// doctorProbeTimeout bounds one network probe; doctor never retries.
const doctorProbeTimeout = 5 * time.Second

// clockSkewWarn and clockSkewFail: tokens carry issue and expiry times, so
// a clock this far off makes them look not yet valid or already expired.
const (
	clockSkewWarn = 30 * time.Second
	clockSkewFail = 5 * time.Minute
)

func newDoctorCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the CLI's configuration, credentials and connectivity",
		Long: `Runs every check that explains why a command fails: the config file and
its permissions, which profile and organization apply and why, the
credential store, the session, the clock, DNS, TLS and latency of every
service endpoint, a token for every service, proxy and CA settings, and
whether a newer CLI exists.

Exits 1 when a check fails. Attach ` + "`norcube doctor -o json`" + ` to a support
request: it holds no secrets.`,
		Example: `  norcube doctor
  norcube doctor --profile ci -o json
  norcube doctor --offline`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.CheckQuery(cmd); err != nil {
				return err
			}
			checks := runDoctor(cmd.Context(), clictx.Get(cmd), offline)
			if err := cmdutil.Print(cmd, output.Table[doctorCheck]{
				Headers: []string{"CHECK", "STATUS", "DETAIL"},
				Rows: func(c doctorCheck) []string {
					return []string{c.Check, c.Status, c.Detail}
				},
				Items: checks,
			}); err != nil {
				return err
			}
			failed := 0
			for _, c := range checks {
				if c.Status == checkFail {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "Skip the checks that need the network")
	return cmd
}

func runDoctor(ctx context.Context, flags *clictx.Flags, offline bool) []doctorCheck {
	var checks []doctorCheck
	add := func(check, status, detail string) {
		checks = append(checks, doctorCheck{Check: check, Status: status, Detail: detail})
	}

	add("cli version", checkOK, fmt.Sprintf("%s (%s/%s, %s)", buildinfo.Version, runtime.GOOS, runtime.GOARCH, runtime.Version()))
	if latest := latestKnownVersion(); latest != "" && semverGreater(latest, buildinfo.Version) {
		checks[len(checks)-1] = doctorCheck{Check: "cli version", Status: checkWarn,
			Detail: fmt.Sprintf("%s; %s is available, run `norcube upgrade`", buildinfo.Version, latest)}
	}

	checks = append(checks, configChecks()...)

	res, err := flags.Resolve()
	if err != nil {
		add("profile", checkFail, err.Error())
		return checks
	}
	add("profile", checkOK, fmt.Sprintf("%s (from %s), environment %s", res.Profile.Value, res.Profile.Source, res.Environment.Value))
	cfg, err := flags.Config()
	if err != nil {
		add("config", checkFail, err.Error())
		return checks
	}
	if org := res.Org.Value; org != "" {
		add("organization", checkOK, fmt.Sprintf("%s (from %s)", org, res.Org.Source))
	} else {
		add("organization", checkWarn, "none set; run `norcube org use <slug>` or pass --org")
	}
	checks = append(checks, credentialCheck(flags, res, cfg))
	checks = append(checks, proxyChecks()...)

	if offline {
		add("network", checkSkip, "--offline")
		return checks
	}

	probes := probeEndpoints(ctx, flags, cfg)
	checks = append(checks, probes.checks...)
	if probes.authDate.IsZero() {
		add("clock", checkSkip, "Norcube Auth didn't answer with a Date header")
	} else {
		skew := time.Since(probes.authDate).Round(time.Second)
		abs := skew
		if abs < 0 {
			abs = -abs
		}
		switch {
		case abs >= clockSkewFail:
			add("clock", checkFail, fmt.Sprintf("off by %s from Norcube Auth; fix the system clock, tokens will look expired", skew))
		case abs >= clockSkewWarn:
			add("clock", checkWarn, fmt.Sprintf("off by %s from Norcube Auth", skew))
		default:
			add("clock", checkOK, fmt.Sprintf("within %s of Norcube Auth", clockSkewWarn))
		}
	}

	checks = append(checks, sessionAndTokenChecks(ctx, flags, cfg, res)...)
	return checks
}

func configChecks() []doctorCheck {
	path, err := config.Path()
	if err != nil {
		return []doctorCheck{{"config file", checkFail, err.Error()}}
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return []doctorCheck{{"config file", checkOK, path + " (not created yet; defaults apply)"}}
	case err != nil:
		return []doctorCheck{{"config file", checkFail, err.Error()}}
	}
	if _, err := config.Load(); err != nil {
		return []doctorCheck{{"config file", checkFail, fmt.Sprintf("%s: %v", path, err)}}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return []doctorCheck{{"config file", checkWarn, fmt.Sprintf("%s is %v; run `chmod 600 %s`", path, info.Mode().Perm(), path)}}
	}
	return []doctorCheck{{"config file", checkOK, path}}
}

func credentialCheck(flags *clictx.Flags, res *resolve.Result, cfg *config.Config) doctorCheck {
	c := doctorCheck{Check: "credentials"}
	if _, err := flags.CIToken(cfg); err != nil {
		c.Status, c.Detail = checkFail, err.Error()
		return c
	} else if static, _ := flags.StaticToken(); auth.IsCIToken(static) {
		c.Status, c.Detail = checkOK, "CI token from "+staticSource(res)
		return c
	}
	switch res.Credentials.Value {
	case resolve.CredentialTokenFile:
		c.Status, c.Detail = checkOK, "access token from --token-file"
	case config.CredentialEnv:
		if os.Getenv(resolve.EnvToken) == "" {
			c.Status, c.Detail = checkFail, "credential_store is env but "+resolve.EnvToken+" is empty"
		} else {
			c.Status, c.Detail = checkOK, "access token from "+resolve.EnvToken
		}
	case config.CredentialFile:
		c.Status, c.Detail = checkOK, "file store (0600 files in the config directory)"
	default:
		if credstore.Available() {
			c.Status, c.Detail = checkOK, "OS keyring"
		} else {
			c.Status, c.Detail = checkWarn, "no OS keyring here; sessions fall back to the file store"
		}
	}
	if !auth.HasCredentials(cfg.Profile, cfg.Auth) && c.Status == checkOK && res.Credentials.Value != resolve.CredentialTokenFile && res.Credentials.Value != config.CredentialEnv {
		c.Detail += "; no session stored"
	}
	return c
}

func proxyChecks() []doctorCheck {
	var out []doctorCheck
	var parts []string
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		if v := os.Getenv(k); v != "" {
			if u, err := url.Parse(v); err == nil && u.User != nil {
				v = transport.RedactURL(u)
			}
			parts = append(parts, k+"="+v)
		}
	}
	if len(parts) == 0 {
		out = append(out, doctorCheck{"proxy", checkOK, "none"})
	} else {
		out = append(out, doctorCheck{"proxy", checkOK, strings.Join(parts, " ")})
	}
	if ca := os.Getenv(transport.EnvCABundle); ca != "" {
		if _, err := os.Stat(ca); err != nil { // #nosec G703 -- a path the user configured
			out = append(out, doctorCheck{"ca bundle", checkFail, err.Error()})
		} else {
			out = append(out, doctorCheck{"ca bundle", checkOK, ca})
		}
	}
	return out
}

type probeResult struct {
	checks   []doctorCheck
	authDate time.Time
}

// probeEndpoints resolves and calls every service endpoint in parallel:
// any HTTP answer means the endpoint is reachable.
func probeEndpoints(ctx context.Context, flags *clictx.Flags, cfg *config.Config) probeResult {
	tc, err := flags.Transport()
	if err != nil {
		return probeResult{checks: []doctorCheck{{"network", checkFail, err.Error()}}}
	}
	tc.MaxAttempts = 1
	tc.ReadTimeout = doctorProbeTimeout
	client, err := transport.New(tc)
	if err != nil {
		return probeResult{checks: []doctorCheck{{"network", checkFail, err.Error()}}}
	}

	keys := registry.Keys()
	out := make([]doctorCheck, len(keys))
	var authDate time.Time
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, date := probeEndpoint(ctx, client, key, cfg.Endpoint(key))
			out[i] = c
			if key == config.ServiceAuth {
				mu.Lock()
				authDate = date
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return probeResult{checks: out, authDate: authDate}
}

func probeEndpoint(ctx context.Context, client *http.Client, key, endpoint string) (doctorCheck, time.Time) {
	c := doctorCheck{Check: "endpoint " + key}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		c.Status, c.Detail = checkFail, fmt.Sprintf("invalid URL %q", endpoint)
		return c, time.Time{}
	}
	ctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(ctx, u.Hostname()); err != nil {
		c.Status, c.Detail = checkFail, fmt.Sprintf("%s: DNS: %v", u.Host, err)
		return c, time.Time{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		c.Status, c.Detail = checkFail, err.Error()
		return c, time.Time{}
	}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		c.Status, c.Detail = checkFail, fmt.Sprintf("%s: %v", u.Host, err)
		return c, time.Time{}
	}
	_ = resp.Body.Close()
	detail := fmt.Sprintf("%s, %s", u.Host, elapsed)
	if resp.TLS != nil {
		detail += ", " + tls.VersionName(resp.TLS.Version)
	}
	c.Status, c.Detail = checkOK, detail
	if elapsed > 2*time.Second {
		c.Status = checkWarn
		c.Detail += " (slow)"
	}
	date, _ := http.ParseTime(resp.Header.Get("Date"))
	return c, date
}

// sessionAndTokenChecks asks Norcube Auth about the stored session and,
// when it works and an organization is set, mints a token for every
// service the CLI calls.
func sessionAndTokenChecks(ctx context.Context, flags *clictx.Flags, cfg *config.Config, res *resolve.Result) []doctorCheck {
	if ci, err := flags.CIToken(cfg); err == nil && ci != nil {
		return ciTokenChecks(ctx, ci)
	}
	if res.Credentials.Value == resolve.CredentialTokenFile || res.Credentials.Value == config.CredentialEnv {
		return []doctorCheck{{"session", checkSkip, "a static access token is in use"}}
	}
	state, err := checkSession(ctx, flags, cfg)
	var out []doctorCheck
	switch state {
	case auth.SessionActive:
		out = append(out, doctorCheck{"session", checkOK, "active"})
	case auth.SessionNone:
		return append(out, doctorCheck{"session", checkFail, "not signed in; run `" + LoginCommand() + "`"})
	case auth.SessionEnded:
		return append(out, doctorCheck{"session", checkFail, "revoked or expired; run `" + LoginCommand() + "`"})
	default:
		detail := "couldn't reach Norcube Auth"
		if err != nil {
			detail += ": " + err.Error()
		}
		return append(out, doctorCheck{"session", checkWarn, detail})
	}

	org, err := flags.Org(ctx, cfg)
	if err != nil || org.ID == "" {
		return append(out, doctorCheck{"tokens", checkSkip, "no organization to mint tokens for"})
	}
	services := registry.Callable()
	// One at a time: every mint rotates the profile's one refresh token.
	for _, s := range services {
		c := doctorCheck{Check: "token " + s.Command}
		ts, err := flags.TokenSource(cfg, s.Audience, org.ID)
		if err == nil {
			tctx, cancel := context.WithTimeout(ctx, sessionCheckTimeout)
			_, err = ts.Token(tctx)
			cancel()
		}
		if err != nil {
			c.Status, c.Detail = checkFail, err.Error()
		} else {
			c.Status, c.Detail = checkOK, fmt.Sprintf("%s in %s", s.Audience, orgLabel(config.Organization{ID: org.ID, Slug: org.Slug}))
		}
		out = append(out, c)
	}
	return out
}

// latestKnownVersion is the newest release the update check has seen, or
// "" when it hasn't run.
func latestKnownVersion() string {
	c := loadUpdateCache()
	if c == nil {
		return ""
	}
	return c.LatestVersion
}

// staticSource names where a static token comes from.
func staticSource(res *resolve.Result) string {
	if res.Credentials.Value == resolve.CredentialTokenFile {
		return "--token-file"
	}
	return resolve.EnvToken
}

// ciTokenChecks exchanges the CI token for every service the CLI calls.
// A service the token has no scope for is expected (tokens are narrow on
// purpose) and is reported as skipped; a refused token fails.
func ciTokenChecks(ctx context.Context, ci *auth.CIExchanger) []doctorCheck {
	var out []doctorCheck
	for _, s := range registry.Callable() {
		if s.Audience == auth.AudienceAuth {
			continue
		}
		c := doctorCheck{Check: "token " + s.Command}
		tctx, cancel := context.WithTimeout(ctx, sessionCheckTimeout)
		g, err := ci.Grant(tctx, s.Audience)
		cancel()
		var ce *clierr.Error
		switch {
		case err == nil:
			c.Status = checkOK
			c.Detail = fmt.Sprintf("%s in %s (%s)", s.Audience, firstNonEmptyStr(g.OrgSlug, g.OrgID), strings.Join(g.Scopes, ", "))
		case errors.As(err, &ce) && ce.Code == clierr.CodeInsufficientScope:
			c.Status, c.Detail = checkSkip, "the CI token has no scope for this service"
		default:
			c.Status, c.Detail = checkFail, err.Error()
		}
		out = append(out, c)
	}
	return out
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
