package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/norcubeplatform/cli/internal/api/monitor"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// What `monitors create --type` accepts: the API's types plus keyword, an
// HTTP monitor that looks for a keyword (the API's "variant").
var createTypes = []string{"http", "keyword", "ssl", "domain"}

var (
	httpMethods  = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	keywordModes = []string{"present", "absent"}
)

// Defaults for settings the API requires on create; the dashboard's create
// form starts from the same.
const (
	defaultHTTPInterval   = time.Minute
	defaultSSLInterval    = time.Hour
	defaultDomainInterval = 12 * time.Hour
	defaultTimeout        = 10 * time.Second
	defaultMaxRedirects   = 5
)

var defaultExpiryDays = []int{30, 14, 7}

// Flags by monitor type, so a flag of another type is a usage error.
var (
	httpFlagNames = []string{"url", "method", "expect", "timeout", "slow", "no-follow-redirects", "max-redirects",
		"insecure", "keyword", "keyword-mode", "keyword-case-sensitive", "no-keyword", "header", "secret-headers-file",
		"clear-headers", "body-file", "body-content-type", "cert-expiry-days", "no-cert-watch", "domain-expiry-days", "no-domain-watch"}
	sslFlagNames    = []string{"host", "port", "server-name", "alert-days"}
	domainFlagNames = []string{"domain", "alert-days"}
)

// monitorFlags are the settings create and update share.
type monitorFlags struct {
	name, description string
	interval          time.Duration
	fromFile          string

	// HTTP
	url, method, keyword, keywordMode     string
	expect, headers                       []string
	timeout, slow                         time.Duration
	noFollow, insecure, caseSensitive     bool
	noKeyword, clearHeaders               bool
	maxRedirects                          int
	secretHeadersFile, bodyFile, bodyType string
	certDays, domainDays                  []int
	noCertWatch, noDomainWatch            bool
	host, serverName, domain              string
	port                                  int
	alertDays                             []int
}

func addMonitorFlags(cmd *cobra.Command, f *monitorFlags, update bool) {
	fl := cmd.Flags()
	if update {
		fl.StringVar(&f.name, "name", "", "New name")
	}
	fl.StringVar(&f.description, "description", "", "A note on what the monitor is for")
	fl.DurationVar(&f.interval, "interval", 0, "How often to check, e.g. 30s, 5m, 1h (default 1m for HTTP, 1h for SSL, 12h for domains; your plan sets the minimum)")
	fl.StringVar(&f.fromFile, "from-file", "", "Read the request body as JSON from this file (- for standard input), in the API's shape; no other setting flags then")

	fl.StringVar(&f.url, "url", "", "HTTP: the URL to request")
	fl.StringVar(&f.method, "method", "", "HTTP: request method (default GET)")
	fl.StringSliceVar(&f.expect, "expect", nil, "HTTP: accepted status codes or ranges, e.g. 2xx, 301, 400-404 (repeat or comma-separate; default 2xx)")
	fl.DurationVar(&f.timeout, "timeout", 0, "HTTP: give up on a request after this long, 1s to 30s (default 10s)")
	fl.DurationVar(&f.slow, "slow", 0, "HTTP: warn when a response takes longer than this")
	fl.BoolVar(&f.noFollow, "no-follow-redirects", false, "HTTP: judge the first response instead of following redirects")
	fl.IntVar(&f.maxRedirects, "max-redirects", 0, "HTTP: redirects to follow at most (default 5)")
	fl.BoolVar(&f.insecure, "insecure", false, "HTTP: don't verify the TLS certificate (it is still reported)")
	fl.StringVar(&f.keyword, "keyword", "", "HTTP: text to look for in the response body (the first 256 KiB)")
	fl.StringVar(&f.keywordMode, "keyword-mode", "", "HTTP: whether the keyword must be present (default) or absent")
	fl.BoolVar(&f.caseSensitive, "keyword-case-sensitive", false, "HTTP: match the keyword's case")
	fl.StringArrayVar(&f.headers, "header", nil, `HTTP: a request header "Name: value" (repeatable; replaces the plain headers on update)`)
	fl.StringVar(&f.secretHeadersFile, "secret-headers-file", "", `HTTP: read secret headers ("Name: value" per line) from this file (- for standard input); stored encrypted, never shown again`)
	fl.StringVar(&f.bodyFile, "body-file", "", "HTTP: send the request body read from this file (- for standard input)")
	fl.StringVar(&f.bodyType, "body-content-type", "", "HTTP: Content-Type of --body-file")
	fl.IntSliceVar(&f.certDays, "cert-expiry-days", nil, "HTTP: also watch the certificate, alerting this many days before it expires (https URLs; default on with 30,14,7)")
	fl.BoolVar(&f.noCertWatch, "no-cert-watch", false, "HTTP: don't watch the certificate")
	fl.IntSliceVar(&f.domainDays, "domain-expiry-days", nil, "HTTP: also watch the domain's registration, alerting this many days before it expires (default on with 30,14,7 for host names)")
	fl.BoolVar(&f.noDomainWatch, "no-domain-watch", false, "HTTP: don't watch the domain's registration")

	fl.StringVar(&f.host, "host", "", "SSL: host whose certificate to check")
	fl.IntVar(&f.port, "port", 0, "SSL: port (default 443)")
	fl.StringVar(&f.serverName, "server-name", "", "SSL: SNI name to send, when it differs from --host")
	fl.StringVar(&f.domain, "domain", "", "Domain: the registered domain, e.g. example.com (not www.example.com)")
	fl.IntSliceVar(&f.alertDays, "alert-days", nil, "SSL and domain: alert this many days before expiry, up to 5 values (default 30,14,7)")

	if update {
		fl.BoolVar(&f.noKeyword, "no-keyword", false, "HTTP: stop looking for a keyword")
		fl.BoolVar(&f.clearHeaders, "clear-headers", false, "HTTP: remove every request header, secret ones too")
		cmd.MarkFlagsMutuallyExclusive("keyword", "no-keyword")
		cmd.MarkFlagsMutuallyExclusive("header", "clear-headers")
		cmd.MarkFlagsMutuallyExclusive("secret-headers-file", "clear-headers")
	}
	cmd.MarkFlagsMutuallyExclusive("cert-expiry-days", "no-cert-watch")
	cmd.MarkFlagsMutuallyExclusive("domain-expiry-days", "no-domain-watch")
	cmdutil.FlagEnum(cmd, "method", httpMethods...)
	cmdutil.FlagEnum(cmd, "keyword-mode", keywordModes...)
}

// changedSettings lists the setting flags given on the command line.
func changedSettings(cmd *cobra.Command) []string {
	var out []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if slices.Contains(httpFlagNames, f.Name) || slices.Contains(sslFlagNames, f.Name) || slices.Contains(domainFlagNames, f.Name) {
			out = append(out, f.Name)
		}
	})
	return out
}

// checkFlagsFit refuses setting flags of another monitor type.
func checkFlagsFit(changed []string, monitorType string) error {
	own := map[string][]string{"http": httpFlagNames, "ssl": sslFlagNames, "domain": domainFlagNames}[monitorType]
	for _, n := range changed {
		if !slices.Contains(own, n) {
			return clierr.Usage(fmt.Errorf("--%s doesn't apply to %s monitors", n, monitorType))
		}
	}
	return nil
}

func newMonitorCreateCmd(d deps) *cobra.Command {
	var (
		f   monitorFlags
		typ string
	)
	cmd := &cobra.Command{
		Use:         "create <name>",
		Annotations: cmdutil.Writes(),
		Short:       "Create a monitor; its first check runs right away",
		Long: `Creates an enabled monitor of one type:

  http     requests --url and expects --expect (default 2xx) within --timeout;
           for https URLs it also watches the certificate and the domain's
           registration unless --no-cert-watch / --no-domain-watch
  keyword  an HTTP monitor that also looks for --keyword in the body
           (--keyword-mode absent: alerts when it appears)
  ssl      checks the certificate of --host:--port and alerts --alert-days
           before it expires
  domain   checks the registration of --domain and alerts --alert-days
           before it expires

Settings not given start from the dashboard's defaults. For anything the
flags don't cover, pass the API's JSON body with --from-file (see "nrc api
monitor POST /monitors"). Secret header values come from a file or
standard input only (--secret-headers-file), never from arguments. The
first check runs at once and is billed like a scheduled one.`,
		Example: `  norcube monitor monitors create "Shop" --type http --url https://shop.example.com/health
  norcube monitor monitors create "Login page" --type keyword --url https://example.com/login --keyword "Sign in"
  norcube monitor monitors create "API cert" --type ssl --host api.example.com --alert-days 30,7
  norcube monitor monitors create "example.com" --type domain --domain example.com --interval 24h
  printf 'Authorization: Bearer %s\n' "$TOKEN" | norcube monitor monitors create "Admin API" --type http --url https://example.com/admin/health --secret-headers-file -
  norcube monitor monitors create --from-file monitor.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			body, err := createBody(cmd, f, typ, args)
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.CreateMonitorWithResponse(cmd.Context(), body)
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			m := *res.JSON201
			return output.Done{
				Message: fmt.Sprintf("Created monitor %s (%s); its first check is running.", m.Name, m.Id),
				Value:   m,
				Fields:  []output.Field{{Value: m.Id.String()}, {Value: m.Name}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&typ, "type", "", "Monitor type: http, keyword, ssl or domain")
	cmdutil.FlagEnum(cmd, "type", createTypes...)
	addMonitorFlags(cmd, &f, false)
	return cmd
}

// createBody builds and validates POST /monitors before any request.
func createBody(cmd *cobra.Command, f monitorFlags, typ string, args []string) (monitor.DtoMonitorCreate, error) {
	var body monitor.DtoMonitorCreate
	changed := changedSettings(cmd)
	if f.fromFile != "" {
		if typ != "" || len(changed) > 0 || cmd.Flags().Changed("interval") || cmd.Flags().Changed("description") {
			return body, clierr.Usage(fmt.Errorf("--from-file can't be combined with --type or setting flags; put them in the file"))
		}
		if err := readJSON(cmd, f.fromFile, "the monitor", &body); err != nil {
			return body, err
		}
		if len(args) == 1 {
			body.Name = args[0]
		}
		return body, nil
	}
	if len(args) == 0 {
		return body, clierr.Usage(fmt.Errorf("name the monitor: norcube monitor monitors create <name> --type <type>"))
	}
	if typ == "" {
		return body, clierr.Usage(fmt.Errorf("--type is required: one of %s", strings.Join(createTypes, ", ")))
	}
	if err := oneOf("--type", typ, createTypes); err != nil {
		return body, err
	}
	apiType := typ
	if typ == "keyword" {
		apiType = "http"
		if f.keyword == "" {
			return body, clierr.Usage(fmt.Errorf("--type keyword needs --keyword"))
		}
	}
	if err := checkFlagsFit(changed, apiType); err != nil {
		return body, err
	}
	body.Name = strings.TrimSpace(args[0])
	body.Type = monitor.DtoMonitorCreateType(apiType)
	if f.description != "" {
		body.Description = &f.description
	}
	iv := map[string]time.Duration{"http": defaultHTTPInterval, "ssl": defaultSSLInterval, "domain": defaultDomainInterval}[apiType]
	if cmd.Flags().Changed("interval") {
		iv = f.interval
	}
	secs, err := intervalSeconds(iv)
	if err != nil {
		return body, err
	}
	body.IntervalSeconds = secs
	set := cmd.Flags().Changed
	switch apiType {
	case "http":
		if f.url == "" {
			return body, clierr.Usage(fmt.Errorf("--url is required for %s monitors", typ))
		}
		h, err := httpSettings(cmd, f, nil, set)
		if err != nil {
			return body, err
		}
		body.Http = h
	case "ssl":
		if f.host == "" {
			return body, clierr.Usage(fmt.Errorf("--host is required for ssl monitors"))
		}
		body.Ssl = sslSettings(f, nil, set)
	case "domain":
		if f.domain == "" {
			return body, clierr.Usage(fmt.Errorf("--domain is required for domain monitors"))
		}
		body.Domain = domainSettings(f, nil, set)
	}
	return body, nil
}

func newMonitorUpdateCmd(d deps) *cobra.Command {
	var f monitorFlags
	cmd := &cobra.Command{
		Use:         "update <monitor>",
		Annotations: cmdutil.Writes(),
		Short:       "Change a monitor's name, interval or check settings",
		Long: `Changes only what you pass. Check settings start from the monitor's
current ones, so "--timeout 5s" keeps everything else; secret headers keep
their stored values unless --secret-headers-file replaces them (and only
while the URL keeps its origin). The type can't change. --from-file sends
the API's PATCH body as it is (a settings object there replaces the
settings as a whole).`,
		Example: `  norcube monitor monitors update "Shop" --interval 30s --slow 1500ms
  norcube monitor monitors update "Shop" --name "Shop (EU)" --expect 2xx,301
  norcube monitor monitors update "API cert" --alert-days 21,7
  norcube monitor monitors update 7d1c2f7e-6a1f-4f5e-9b8a-1f7c2b3d4e5f --from-file patch.json`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			changed := changedSettings(cmd)
			others := cmd.Flags().Changed("name") || cmd.Flags().Changed("description") || cmd.Flags().Changed("interval")
			var body monitor.DtoMonitorUpdate
			if f.fromFile != "" {
				if len(changed) > 0 || others {
					return nil, clierr.Usage(fmt.Errorf("--from-file can't be combined with setting flags; put them in the file"))
				}
				if err := readJSON(cmd, f.fromFile, "the changes", &body); err != nil {
					return nil, err
				}
			} else if len(changed) == 0 && !others {
				return nil, clierr.Usage(fmt.Errorf("nothing to change: pass a setting flag (see --help) or --from-file"))
			}
			if cmd.Flags().Changed("interval") {
				secs, err := intervalSeconds(f.interval)
				if err != nil {
					return nil, err
				}
				body.IntervalSeconds = &secs
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			m, err := getMonitor(cmd.Context(), c, args[0])
			if err != nil {
				return nil, err
			}
			if err := checkFlagsFit(changed, string(m.Type)); err != nil {
				return nil, err
			}
			if cmd.Flags().Changed("name") {
				n := strings.TrimSpace(f.name)
				body.Name = &n
			}
			if cmd.Flags().Changed("description") {
				body.Description = &f.description
			}
			if len(changed) > 0 {
				set := cmd.Flags().Changed
				switch string(m.Type) {
				case "http":
					h, err := httpSettings(cmd, f, m.Http, set)
					if err != nil {
						return nil, err
					}
					body.Http = h
				case "ssl":
					body.Ssl = sslSettings(f, m.Ssl, set)
				case "domain":
					body.Domain = domainSettings(f, m.Domain, set)
				}
			}
			res, err := c.API.UpdateMonitorWithResponse(cmd.Context(), m.Id, body)
			if err != nil {
				return nil, err
			}
			if res.JSON200 == nil {
				return nil, apiError(res.HTTPResponse, res.Body)
			}
			u := *res.JSON200
			return output.Done{
				Message: fmt.Sprintf("Updated monitor %s (%s).", u.Name, u.Id),
				Value:   u,
				Fields:  []output.Field{{Value: u.Id.String()}, {Value: u.Name}},
			}, nil
		}),
	}
	addMonitorFlags(cmd, &f, true)
	return cmd
}

func intervalSeconds(d time.Duration) (int, error) {
	if d < time.Second || d%time.Second != 0 {
		return 0, clierr.Usage(fmt.Errorf("--interval %s: use whole seconds, e.g. 30s, 5m or 1h", d))
	}
	return int(d / time.Second), nil
}

// httpSettings builds the HTTP settings object: from base (the monitor's
// current settings) or the defaults, with the flags given applied.
func httpSettings(cmd *cobra.Command, f monitorFlags, base *monitor.DtoHTTPSettings, set func(string) bool) (*monitor.DtoHTTPSettingsInput, error) {
	in := &monitor.DtoHTTPSettingsInput{
		Method: "GET", ExpectedStatus: []string{"2xx"}, TimeoutMs: int(defaultTimeout / time.Millisecond),
		FollowRedirects: true, MaxRedirects: defaultMaxRedirects, VerifyTls: true,
	}
	if base != nil {
		in.Url, in.Method, in.ExpectedStatus = base.Url, monitor.DtoHTTPSettingsInputMethod(base.Method), base.ExpectedStatus
		in.TimeoutMs, in.SlowThresholdMs = base.TimeoutMs, base.SlowThresholdMs
		in.FollowRedirects, in.MaxRedirects, in.VerifyTls = base.FollowRedirects, base.MaxRedirects, base.VerifyTls
		in.Keyword, in.RequestBody, in.RequestBodyContentType = base.Keyword, base.RequestBody, base.RequestBodyContentType
		if base.Keyword != nil {
			cs := base.KeywordCaseSensitive
			in.KeywordCaseSensitive = &cs
			if base.KeywordMode != nil {
				km := monitor.DtoHTTPSettingsInputKeywordMode(*base.KeywordMode)
				in.KeywordMode = &km
			}
		}
		headers := make([]monitor.DtoHTTPHeaderInput, 0, len(base.Headers))
		for _, h := range base.Headers {
			secret := h.Secret
			// A secret header without a value keeps its stored one.
			headers = append(headers, monitor.DtoHTTPHeaderInput{Name: h.Name, Secret: &secret, Value: h.Value})
		}
		in.Headers = &headers
		if base.CertificateExpiry != nil {
			in.CertificateExpiry = &monitor.DtoExpiryWatchInput{ThresholdsDays: base.CertificateExpiry.ThresholdsDays}
		}
		if base.DomainExpiry != nil {
			in.DomainExpiry = &monitor.DtoExpiryWatchInput{ThresholdsDays: base.DomainExpiry.ThresholdsDays}
		}
	}
	if set("url") {
		in.Url = f.url
	}
	if set("method") {
		m := strings.ToUpper(f.method)
		if err := oneOf("--method", m, httpMethods); err != nil {
			return nil, err
		}
		in.Method = monitor.DtoHTTPSettingsInputMethod(m)
	}
	if set("expect") {
		in.ExpectedStatus = f.expect
	}
	if set("timeout") {
		in.TimeoutMs = int(f.timeout / time.Millisecond)
	}
	if set("slow") {
		v := int(f.slow / time.Millisecond)
		in.SlowThresholdMs = &v
	}
	if set("no-follow-redirects") {
		in.FollowRedirects = !f.noFollow
	}
	if set("max-redirects") {
		in.MaxRedirects = f.maxRedirects
	}
	if set("insecure") {
		in.VerifyTls = !f.insecure
	}
	if set("keyword") {
		in.Keyword = &f.keyword
	}
	if set("no-keyword") && f.noKeyword {
		in.Keyword, in.KeywordMode, in.KeywordCaseSensitive = nil, nil, nil
	}
	if set("keyword-mode") {
		if err := oneOf("--keyword-mode", f.keywordMode, keywordModes); err != nil {
			return nil, err
		}
		km := monitor.DtoHTTPSettingsInputKeywordMode(f.keywordMode)
		in.KeywordMode = &km
	}
	if set("keyword-case-sensitive") {
		in.KeywordCaseSensitive = &f.caseSensitive
	}
	if (set("keyword-mode") || set("keyword-case-sensitive")) && in.Keyword == nil {
		return nil, clierr.Usage(fmt.Errorf("--keyword-mode and --keyword-case-sensitive need a keyword (--keyword)"))
	}
	if err := applyHeaders(cmd, f, in, set); err != nil {
		return nil, err
	}
	if set("body-file") {
		b, err := cmdutil.ReadText(cmd.InOrStdin(), "", f.bodyFile, "the request body")
		if err != nil {
			return nil, err
		}
		in.RequestBody = &b
	}
	if set("body-content-type") {
		in.RequestBodyContentType = &f.bodyType
	}
	if base == nil {
		// New monitors watch an https URL's certificate and a host name's
		// domain, like the dashboard's create form.
		if u, err := url.Parse(in.Url); err == nil && u.Scheme == "https" {
			if !f.noCertWatch {
				in.CertificateExpiry = &monitor.DtoExpiryWatchInput{ThresholdsDays: defaultExpiryDays}
			}
			if !f.noDomainWatch && net.ParseIP(u.Hostname()) == nil && strings.Contains(u.Hostname(), ".") {
				in.DomainExpiry = &monitor.DtoExpiryWatchInput{ThresholdsDays: defaultExpiryDays}
			}
		}
	}
	if set("cert-expiry-days") {
		in.CertificateExpiry = &monitor.DtoExpiryWatchInput{ThresholdsDays: f.certDays}
	}
	if f.noCertWatch {
		in.CertificateExpiry = nil
	}
	if set("domain-expiry-days") {
		in.DomainExpiry = &monitor.DtoExpiryWatchInput{ThresholdsDays: f.domainDays}
	}
	if f.noDomainWatch {
		in.DomainExpiry = nil
	}
	return in, nil
}

// applyHeaders sets the request headers: --header replaces the plain ones,
// --secret-headers-file the secret ones, --clear-headers removes all.
func applyHeaders(cmd *cobra.Command, f monitorFlags, in *monitor.DtoHTTPSettingsInput, set func(string) bool) error {
	current := []monitor.DtoHTTPHeaderInput{}
	if in.Headers != nil {
		current = *in.Headers
	}
	if f.clearHeaders {
		current = nil
	}
	split := func(secret bool) (keep []monitor.DtoHTTPHeaderInput) {
		for _, h := range current {
			if cmdutil.Deref(h.Secret) != secret {
				keep = append(keep, h)
			}
		}
		return keep
	}
	if set("header") {
		plain := make([]monitor.DtoHTTPHeaderInput, 0, len(f.headers))
		for _, raw := range f.headers {
			h, err := parseHeader(raw, "--header")
			if err != nil {
				return err
			}
			plain = append(plain, h)
		}
		current = append(split(false), plain...)
	}
	if set("secret-headers-file") {
		text, err := cmdutil.ReadText(cmd.InOrStdin(), "", f.secretHeadersFile, "the secret headers")
		if err != nil {
			return err
		}
		var secrets []monitor.DtoHTTPHeaderInput
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			h, err := parseHeader(line, "--secret-headers-file")
			if err != nil {
				return err
			}
			t := true
			h.Secret = &t
			secrets = append(secrets, h)
		}
		current = append(split(true), secrets...)
	}
	in.Headers = &current
	return nil
}

// parseHeader reads "Name: value". The error never repeats the value: it
// may be a secret.
func parseHeader(raw, flag string) (monitor.DtoHTTPHeaderInput, error) {
	name, value, ok := strings.Cut(raw, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" || strings.ContainsAny(name, " \t") {
		return monitor.DtoHTTPHeaderInput{}, clierr.Usage(fmt.Errorf(`%s: write each header as "Name: value"`, flag))
	}
	return monitor.DtoHTTPHeaderInput{Name: name, Value: &value}, nil
}

func sslSettings(f monitorFlags, base *monitor.DtoSSLSettings, set func(string) bool) *monitor.DtoSSLSettingsInput {
	in := &monitor.DtoSSLSettingsInput{Port: 443, ExpiryThresholdsDays: defaultExpiryDays}
	if base != nil {
		in.Host, in.Port, in.ServerName, in.ExpiryThresholdsDays = base.Host, base.Port, base.ServerName, base.ExpiryThresholdsDays
	}
	if set("host") {
		in.Host = f.host
	}
	if set("port") {
		in.Port = f.port
	}
	if set("server-name") {
		in.ServerName = &f.serverName
	}
	if set("alert-days") {
		in.ExpiryThresholdsDays = f.alertDays
	}
	return in
}

func domainSettings(f monitorFlags, base *monitor.DtoDomainSettings, set func(string) bool) *monitor.DtoDomainSettingsInput {
	in := &monitor.DtoDomainSettingsInput{ExpiryThresholdsDays: defaultExpiryDays}
	if base != nil {
		in.Domain, in.ExpiryThresholdsDays = base.Domain, base.ExpiryThresholdsDays
	}
	if set("domain") {
		in.Domain = f.domain
	}
	if set("alert-days") {
		in.ExpiryThresholdsDays = f.alertDays
	}
	return in
}

// readJSON decodes a JSON request body from a file or standard input,
// refusing members the API doesn't have (as the API does) before any
// request.
func readJSON(cmd *cobra.Command, path, what string, v any) error {
	text, err := cmdutil.ReadText(cmd.InOrStdin(), "", path, what)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return clierr.Usage(fmt.Errorf("%s in %s is not valid: %w", what, inputName(path), err))
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return clierr.Usage(fmt.Errorf("%s in %s has more than one JSON value", what, inputName(path)))
	}
	return nil
}

func inputName(path string) string {
	if path == "-" {
		return "standard input"
	}
	return path
}
