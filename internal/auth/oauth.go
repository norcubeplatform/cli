package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pkg/browser"

	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/transport"
)

// The CLI as an OAuth 2.0 client of Norcube Auth: a public, first-party
// client ("norcube-cli") that signs in with the authorization code flow
// and PKCE over a loopback redirect (RFC 8252), or with the device flow
// (RFC 8628). The sign-in yields a refresh token and a short-lived CLI
// access token, which is exchanged (RFC 8693) for a token per service and
// organization. Only a one-time, PKCE-bound code passes through the
// browser.

// ClientID is the CLI's OAuth client id.
const ClientID = DeviceClientID

// OAuth grant and token type identifiers.
//
// #nosec G101 -- identifiers, not credentials.
const (
	grantAuthorizationCode = "authorization_code"
	grantRefreshToken      = "refresh_token"
	grantTokenExchange     = "urn:ietf:params:oauth:grant-type:token-exchange"

	tokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"
	// tokenTypeCLISession is a refresh JWT of the older CLI sign-in,
	// exchanged once for an OAuth sign-in of the same session.
	tokenTypeCLISession = "urn:norcube:params:oauth:token-type:cli-session"
)

// Metadata is the part of Norcube Auth's RFC 8414 metadata the CLI uses.
type Metadata struct {
	Issuer                      string   `json:"issuer"`
	AuthorizationEndpoint       string   `json:"authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	RevocationEndpoint          string   `json:"revocation_endpoint,omitempty"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint,omitempty"`
	GrantTypesSupported         []string `json:"grant_types_supported,omitempty"`
	CodeChallengeMethods        []string `json:"code_challenge_methods_supported,omitempty"`
	IssParameterSupported       bool     `json:"authorization_response_iss_parameter_supported,omitempty"`
}

// SupportsCLISignIn reports whether the server signs the CLI in through
// OAuth: it advertises the token exchange the CLI needs for every
// command. Older servers don't, and the CLI keeps their older sign-in.
func (m *Metadata) SupportsCLISignIn() bool {
	return m != nil && m.AuthorizationEndpoint != "" && m.TokenEndpoint != "" &&
		slices.Contains(m.GrantTypesSupported, grantTokenExchange)
}

// SupportsDevice reports whether the server takes device sign-ins through
// OAuth.
func (m *Metadata) SupportsDevice() bool {
	return m.SupportsCLISignIn() && m.DeviceAuthorizationEndpoint != "" &&
		slices.Contains(m.GrantTypesSupported, deviceGrantType)
}

// ErrOAuthUnavailable: Norcube Auth has no OAuth sign-in for the CLI (an
// older server, or the OAuth server is switched off). The CLI then uses
// the older sign-in.
var ErrOAuthUnavailable = errors.New("the OAuth sign-in for the CLI is not available on this Norcube Auth")

// metadataCacheTTL is how long a discovery answer is reused, on disk and
// in memory. A server without OAuth is asked again sooner, so a backend
// upgrade is picked up within the hour.
const (
	metadataCacheTTL         = 24 * time.Hour
	metadataNegativeCacheTTL = time.Hour
)

type metadataEntry struct {
	Metadata  *Metadata `json:"metadata,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

var (
	metadataMu    sync.Mutex
	metadataCache = map[string]metadataEntry{}
	// metadataCacheDir is where discovery answers are kept between runs;
	// "" means the user cache directory. Tests set it.
	metadataCacheDir string
)

// Discover returns Norcube Auth's metadata for authURL, or
// ErrOAuthUnavailable when the server has no OAuth sign-in for the CLI.
// Answers are cached (24 hours, one hour for "unavailable") in memory and
// in the user cache directory, so commands don't pay a request for it.
// Network failures are returned as they are and never cached.
func Discover(ctx context.Context, httpc *http.Client, authURL string) (*Metadata, error) {
	authURL = strings.TrimRight(authURL, "/")
	if e, ok := cachedMetadata(authURL); ok {
		if !e.Metadata.SupportsCLISignIn() {
			return nil, ErrOAuthUnavailable
		}
		return e.Metadata, nil
	}
	m, err := fetchMetadata(ctx, httpc, authURL)
	if err != nil && !errors.Is(err, ErrOAuthUnavailable) {
		return nil, err
	}
	storeMetadata(authURL, metadataEntry{Metadata: m, FetchedAt: time.Now()})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ForgetMetadata drops the cached discovery answer for authURL.
func ForgetMetadata(authURL string) {
	authURL = strings.TrimRight(authURL, "/")
	metadataMu.Lock()
	defer metadataMu.Unlock()
	delete(metadataCache, authURL)
	if p := metadataPath(authURL); p != "" {
		_ = os.Remove(p) // #nosec G703 -- hashed name in the user's cache directory
	}
}

func fetchMetadata(ctx context.Context, httpc *http.Client, authURL string) (*Metadata, error) {
	if httpc == nil {
		httpc = transport.Default()
	}
	// #nosec G704 -- the Norcube Auth URL the user configured.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req) // #nosec G704 -- see above
	if err != nil {
		return nil, fmt.Errorf("read the OAuth metadata of Norcube Auth: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrOAuthUnavailable
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("read the OAuth metadata of Norcube Auth: status %d", resp.StatusCode)
	}
	var m Metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("read the OAuth metadata of Norcube Auth: %w", err)
	}
	// RFC 8414 §3.3: the issuer must be the URL the metadata was
	// fetched for, or a server could pass off another's endpoints.
	if !sameURL(m.Issuer, authURL) {
		return nil, fmt.Errorf("the OAuth metadata of Norcube Auth names issuer %q, not %q; check the auth URL of this profile", m.Issuer, authURL)
	}
	if !m.SupportsCLISignIn() {
		return &m, ErrOAuthUnavailable
	}
	if len(m.CodeChallengeMethods) > 0 && !slices.Contains(m.CodeChallengeMethods, "S256") {
		return nil, errors.New("the OAuth server of Norcube Auth doesn't support PKCE S256")
	}
	return &m, nil
}

func sameURL(a, b string) bool {
	norm := func(s string) string {
		u, err := url.Parse(strings.TrimRight(s, "/"))
		if err != nil {
			return s
		}
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		return u.String()
	}
	return a != "" && norm(a) == norm(b)
}

func cachedMetadata(authURL string) (metadataEntry, bool) {
	metadataMu.Lock()
	defer metadataMu.Unlock()
	fresh := func(e metadataEntry) bool {
		ttl := metadataCacheTTL
		if !e.Metadata.SupportsCLISignIn() {
			ttl = metadataNegativeCacheTTL
		}
		age := time.Since(e.FetchedAt)
		return age >= 0 && age < ttl
	}
	if e, ok := metadataCache[authURL]; ok && fresh(e) {
		return e, true
	}
	p := metadataPath(authURL)
	if p == "" {
		return metadataEntry{}, false
	}
	b, err := os.ReadFile(p) // #nosec G304 -- hashed name in the user's cache directory
	if err != nil {
		return metadataEntry{}, false
	}
	var e metadataEntry
	if json.Unmarshal(b, &e) != nil || !fresh(e) {
		return metadataEntry{}, false
	}
	if e.Metadata != nil && !sameURL(e.Metadata.Issuer, authURL) {
		return metadataEntry{}, false
	}
	metadataCache[authURL] = e
	return e, true
}

func storeMetadata(authURL string, e metadataEntry) {
	metadataMu.Lock()
	defer metadataMu.Unlock()
	metadataCache[authURL] = e
	p := metadataPath(authURL)
	if p == "" {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	// Best effort: the cache only saves a request.
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil { // #nosec G703 -- the user's cache directory
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".oauth-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil { // #nosec G703 -- see above
		_ = os.Remove(tmp.Name()) // #nosec G703 -- see above
	}
}

func metadataPath(authURL string) string {
	dir := metadataCacheDir
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(cache, "norcube")
	}
	sum := sha256.Sum256([]byte(authURL))
	return filepath.Join(dir, "oauth-metadata-"+hex.EncodeToString(sum[:8])+".json")
}

// --- token endpoint ---

// tokenEndpointResponse is a token endpoint answer: the code exchange,
// device poll, refresh and session upgrade (cli_access token and refresh
// token), or a token exchange (a service token).
type tokenEndpointResponse struct {
	AccessToken     string `json:"access_token"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int    `json:"expires_in"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	Scope           string `json:"scope,omitempty"`
	IssuedTokenType string `json:"issued_token_type,omitempty"`
	// DefaultOrganization comes with a sign-in (code exchange, device
	// poll, upgrade); null when the person belongs to none.
	DefaultOrganization *Organization `json:"default_organization,omitempty"`
}

// OAuthError is an RFC 6749 §5.2 error answer of Norcube Auth's OAuth
// server.
type OAuthError struct {
	Status      int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("%s (status %d)", e.Code, e.Status)
}

// OAuth error codes the CLI acts on.
const (
	oauthInvalidGrant       = "invalid_grant"
	oauthInsufficientScope  = "insufficient_scope"
	oauthOrgAccessDenied    = "org_access_denied"
	oauthInvalidScope       = "invalid_scope"
	oauthInvalidTarget      = "invalid_target"
	oauthAccessDenied       = "access_denied"
	oauthTemporarilyUnavail = "temporarily_unavailable"
	oauthServerError        = "server_error"
)

// postToken posts form to the token endpoint. It returns the answer, or
// the OAuth error the server sent, or err for transport failures and
// answers that aren't OAuth errors. retryable marks requests the
// transport may repeat (they use nothing up: token exchanges).
func postToken(ctx context.Context, httpc *http.Client, endpoint string, form url.Values, retryable bool) (*tokenEndpointResponse, *OAuthError, error) {
	if httpc == nil {
		httpc = transport.Default()
	}
	resp, raw, err := postForm(ctx, httpc, endpoint, form, retryable)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusOK {
		var tr tokenEndpointResponse
		if err := json.Unmarshal(raw, &tr); err != nil {
			return nil, nil, fmt.Errorf("decode the token response: %w", err)
		}
		if tr.AccessToken == "" {
			return nil, nil, errors.New("the token response has no access token")
		}
		return &tr, nil, nil
	}
	if oe := parseOAuthError(resp.StatusCode, raw); oe != nil {
		return nil, oe, nil
	}
	e := &clierr.Error{
		Code:       clierr.CodeForStatus(resp.StatusCode, ""),
		Message:    fmt.Sprintf("the token endpoint of Norcube Auth answered %s", resp.Status),
		HTTPStatus: resp.StatusCode,
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Not an OAuth answer (a proxy, or no OAuth server behind
		// /oauth/token): don't claim the session ended.
		e.Code = clierr.CodeError
	}
	return nil, nil, e
}

func parseOAuthError(status int, raw []byte) *OAuthError {
	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Error == "" {
		return nil
	}
	return &OAuthError{Status: status, Code: body.Error, Description: body.Description}
}

// oauthFailure turns an OAuth error of a sign-in step into a CLI error.
func oauthFailure(step string, oe *OAuthError) error {
	e := &clierr.Error{
		Code:       clierr.CodeError,
		Message:    fmt.Sprintf("%s: %s", step, oe.Error()),
		HTTPStatus: oe.Status,
		Err:        oe,
	}
	if oe.Status >= 500 {
		e.Code = clierr.CodeServerError
	}
	switch oe.Code {
	case oauthInvalidScope:
		e.Code = clierr.CodeUsage
		e.Hint = "Valid scopes: " + strings.Join(LoginScopeNames(), ", ") + "."
	case oauthInvalidGrant, oauthAccessDenied:
		e.Code = clierr.CodeLoginRequired
	case oauthTemporarilyUnavail, oauthServerError:
		e.Code = clierr.CodeServerError
	}
	if oe.Status == http.StatusTooManyRequests {
		e.Code = clierr.CodeRateLimited
		e.Hint = "Too many sign-in requests from this network; wait a few minutes."
	}
	return e
}

// signInPayload turns a sign-in's token response into the session a
// login stores.
func signInPayload(tr *tokenEndpointResponse) (*CallbackPayload, error) {
	if tr.RefreshToken == "" {
		return nil, errors.New("the sign-in response has no refresh token")
	}
	p := &CallbackPayload{
		AccessToken:     tr.AccessToken,
		RefreshToken:    tr.RefreshToken,
		AccessExpiresIn: tr.ExpiresIn,
		Scopes:          parseScope(tr.Scope),
		OAuth:           true,
	}
	if tr.DefaultOrganization != nil {
		p.DefaultOrg = *tr.DefaultOrganization
	}
	return p, nil
}

// --- browser sign-in: authorization code + PKCE over a loopback redirect ---

// OAuthLoginOptions configures OAuthLogin.
type OAuthLoginOptions struct {
	Metadata *Metadata
	HTTP     *http.Client
	// Scopes to ask for (see ParseLoginScopes); empty means full.
	Scopes  []string
	Timeout time.Duration
	// BrowserOpenURL opens the authorization URL; the default browser
	// by default.
	BrowserOpenURL func(string) error
	// OnURLReady is called with the authorization URL before the browser
	// opens, for printing.
	OnURLReady func(string)
	// NoBrowser skips opening a browser: the caller prints the URL.
	NoBrowser bool
}

// OAuthLogin signs in through the browser:
//
//  1. Listen on 127.0.0.1 on a random port.
//  2. Open the authorization endpoint with PKCE (S256), a random state,
//     the scopes, and the device's name and User-Agent for the consent
//     screen and the session.
//  3. Norcube Auth redirects the browser to http://127.0.0.1:<port>/callback
//     with a code, the state and its issuer (RFC 9207), all checked.
//  4. Exchange the code with the PKCE verifier.
//
// The caller stores the returned session.
func OAuthLogin(ctx context.Context, opts OAuthLoginOptions) (*CallbackPayload, error) {
	m := opts.Metadata
	if !m.SupportsCLISignIn() {
		return nil, ErrOAuthUnavailable
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.BrowserOpenURL == nil {
		opts.BrowserOpenURL = browser.OpenURL
	}
	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	verifier, challenge, err := newPKCE()
	if err != nil {
		return nil, fmt.Errorf("generate PKCE verifier: %w", err)
	}

	// RFC 8252 §7.3: loopback IP literal, any port. IPv4 only, as the
	// registered redirect URI names 127.0.0.1.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("can't open a local port on 127.0.0.1 for the sign-in callback: %w. "+
			"Browser login needs IPv4 loopback; check that the loopback interface is up and that no firewall blocks local ports, or use `norcube login --device`", err)
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)
	deliver := func(r result) {
		select {
		case resultCh <- r:
		default: // the first answer wins
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		code, err := checkCallback(r.URL.Query(), state, m)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			// #nosec G705 -- callbackPage escapes everything it is given.
			fmt.Fprint(w, callbackPage("Sign-in failed", err.Error()+". Return to the terminal for details."))
			deliver(result{err: err})
			return
		}
		fmt.Fprint(w, callbackPage("Signed in to Norcube CLI", "You can close this tab and return to the terminal."))
		deliver(result{code: code})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			deliver(result{err: err})
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	authURL := authorizeURL(m.AuthorizationEndpoint, redirectURI, state, challenge, opts.Scopes)
	if opts.OnURLReady != nil {
		opts.OnURLReady(authURL)
	}
	// Open failure is not fatal: the user can copy the URL.
	if !opts.NoBrowser {
		_ = opts.BrowserOpenURL(authURL)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	var res result
	select {
	case res = <-resultCh:
	case <-timeoutCtx.Done():
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("login timed out after %s; re-run `norcube login`", opts.Timeout)
		}
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}

	form := url.Values{
		"grant_type":    {grantAuthorizationCode},
		"code":          {res.code},
		"redirect_uri":  {redirectURI},
		"client_id":     {ClientID},
		"code_verifier": {verifier},
	}
	// Not retried: a code is good for one exchange.
	tr, oe, err := postToken(ctx, opts.HTTP, m.TokenEndpoint, form, false)
	switch {
	case err != nil:
		return nil, fmt.Errorf("complete the sign-in: %w", err)
	case oe != nil:
		return nil, oauthFailure("complete the sign-in", oe)
	}
	return signInPayload(tr)
}

// authorizeURL is the authorization request (RFC 6749 §4.1.1, PKCE,
// device details for the consent screen).
func authorizeURL(endpoint, redirectURI, state, challenge string, scopes []string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", requestScope(scopes))
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if host := deviceName(); host != "" {
		q.Set("device_name", host)
	}
	q.Set("client_user_agent", truncate(transport.UserAgent()))
	u.RawQuery = q.Encode()
	return u.String()
}

// checkCallback validates the redirect: state first (anything without
// our state is refused, CSRF), then the issuer (RFC 9207, mix-up), then
// the outcome.
func checkCallback(q url.Values, state string, m *Metadata) (string, error) {
	got := q.Get("state")
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
		return "", errors.New("the sign-in callback's state did not match (possible CSRF), sign-in aborted")
	}
	iss := q.Get("iss")
	switch {
	case iss != "" && !sameURL(iss, m.Issuer):
		return "", fmt.Errorf("the sign-in callback came from issuer %q, not %q, sign-in aborted", iss, m.Issuer)
	case iss == "" && m.IssParameterSupported:
		return "", errors.New("the sign-in callback has no issuer although Norcube Auth sends one, sign-in aborted")
	}
	if e := q.Get("error"); e != "" {
		oe := &OAuthError{Status: http.StatusBadRequest, Code: e, Description: q.Get("error_description")}
		if e == oauthAccessDenied {
			return "", &clierr.Error{Code: clierr.CodeLoginRequired, Message: "the sign-in was denied in the browser", Err: oe}
		}
		return "", oauthFailure("sign-in", oe)
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("the sign-in callback has no code, sign-in aborted")
	}
	return code, nil
}

func callbackPage(title, body string) string {
	return "<!doctype html><html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) +
		"</title></head><body style=\"font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem\"><h1>" +
		html.EscapeString(title) + "</h1><p>" + html.EscapeString(body) + "</p></body></html>"
}

// newPKCE returns an RFC 7636 code verifier (43 characters from 32 random
// bytes) and its S256 challenge.
func newPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, pkceChallenge(verifier), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// --- revocation ---

// RevokeRefreshToken signs an OAuth sign-in out on Norcube Auth (RFC 7009
// revocation of its refresh token): the grant, its tokens and the CLI
// session end, also when the token was already rotated out. Norcube Auth
// answers 200 for unknown or already revoked tokens too; anything else is
// an error. Callers hold the profile's lock.
func RevokeRefreshToken(ctx context.Context, httpc *http.Client, authURL, refreshToken string) error {
	if httpc == nil {
		httpc = transport.Default()
	}
	ctx, cancel := context.WithTimeout(ctx, SignOutTimeout)
	defer cancel()
	endpoint := strings.TrimRight(authURL, "/") + "/oauth/revoke"
	if m, err := Discover(ctx, httpc, authURL); err == nil && m.RevocationEndpoint != "" {
		endpoint = m.RevocationEndpoint
	}
	form := url.Values{
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {ClientID},
	}
	// Revoking twice is harmless, so the transport may retry.
	resp, raw, err := postForm(ctx, httpc, endpoint, form, true)
	if err != nil {
		return fmt.Errorf("oauth/revoke: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		if oe := parseOAuthError(resp.StatusCode, raw); oe != nil {
			return fmt.Errorf("oauth/revoke: %w", oe)
		}
		return fmt.Errorf("oauth/revoke: status %d", resp.StatusCode)
	}
	return nil
}
