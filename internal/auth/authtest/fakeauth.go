// Package authtest is a fake Norcube Auth for tests: the OAuth server the
// CLI signs in with (metadata, authorization redirect, code exchange,
// refresh with rotation and reuse detection, token exchange, device flow,
// session upgrade, revocation) plus the few account routes commands call
// right after a login. It keeps everything in memory and is safe for
// concurrent use. Only tests import it.
package authtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Wire constants of the OAuth server.
//
// #nosec G101 -- OAuth grant and token type identifiers, not credentials.
const (
	ClientID            = "norcube-cli"
	GrantTokenExchange  = "urn:ietf:params:oauth:grant-type:token-exchange"
	GrantDeviceCode     = "urn:ietf:params:oauth:grant-type:device_code"
	TokenTypeAccess     = "urn:ietf:params:oauth:token-type:access_token"
	TokenTypeCLISession = "urn:norcube:params:oauth:token-type:cli-session"
	// ReuseGrace: a refresh token rotated out less than this long ago is
	// refused without ending the sign-in.
	ReuseGrace = 10 * time.Second
)

// Organizations the fake account belongs to.
const (
	HomeOrgID = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	AcmeOrgID = "0b9d2b8e-1c1f-4c3e-9a51-6d1c2f9a7e01"
	// OtherOrgID exists, but the account isn't a member.
	OtherOrgID = "6f1e8a52-3d4b-4f7a-8c2e-9b0a1d2c3e4f"
)

// scopeAudiences mirrors the server's catalog: which audiences a scope
// lets a sign-in be exchanged for.
var scopeAudiences = map[string][]string{
	"full":              {"auth", "billing", "langsync-api", "snapdb-api", "prompthub", "domainradar", "cms-api"},
	"org.read":          {"auth"},
	"offline_access":    nil,
	"backup.read":       {"snapdb-api"},
	"backup.write":      {"snapdb-api"},
	"backup.verify":     {"snapdb-api"},
	"domainradar.check": {"domainradar"},
	"langsync.read":     {"langsync-api"},
	"langsync.sync":     {"langsync-api"},
	"langsync.write":    {"langsync-api"},
	"billing.read":      {"billing"},
	"cms.read":          {"cms-api"},
	"cms.write":         {"cms-api"},
}

// Grant is one sign-in (a CLI device).
type Grant struct {
	ID      string
	Scopes  []string
	Revoked bool
	// Refresh is the current refresh token; Access the current CLI
	// access token.
	Refresh string
	Access  string
	// rotated maps refresh tokens rotated out to when.
	rotated map[string]time.Time
}

// Server is the fake. Set the knobs before the first request (or under
// Lock for a running test).
type Server struct {
	*httptest.Server

	// OAuthOff makes the metadata 404 (an older Norcube Auth); NoExchange
	// leaves the token exchange out of grant_types_supported.
	OAuthOff   bool
	NoExchange bool
	// NoDevice leaves the device endpoint out of the metadata.
	NoDevice bool
	// IssOff stops sending iss on the redirect (and says so).
	IssOff bool
	// WrongIss sends another issuer on the redirect.
	WrongIss bool
	// Deny answers the authorization with access_denied.
	Deny bool
	// AccessTTL is the CLI access token's lifetime in seconds (299),
	// ProductTTL the exchanged tokens' (300).
	AccessTTL  int
	ProductTTL int
	// Delay slows down refreshes and exchanges to widen race windows.
	Delay time.Duration
	// DeviceAnswers are the device polls' answers in order; after them
	// the request is approved.
	DeviceAnswers []string
	// RevokeStatus, when set, is what /oauth/revoke answers.
	RevokeStatus int
	// NoDefaultOrg answers the sign-in with default_organization null.
	NoDefaultOrg bool
	// Clock replaces time.Now (grace checks).
	Clock func() time.Time

	mu       sync.Mutex
	nextID   int
	codes    map[string]*code
	grants   map[string]*Grant // by grant id
	byAccess map[string]*Grant
	// accessExp is when each CLI access token expires.
	accessExp map[string]time.Time
	byRT      map[string]*Grant // current and rotated refresh tokens
	product   map[string]productToken
	// legacy holds the older sign-in's refresh JWTs that may be upgraded
	// (they have a session); a used one is kept in legacyUsed.
	legacy     map[string]bool
	legacyUsed map[string]bool
	// LegacyRefresh is the one refresh token the older cookie refresh
	// accepts ("" for none).
	LegacyRefresh string
	device        *deviceReq

	// Counters.
	Authorizations  int
	CodeExchanges   int
	Refreshes       int
	Exchanges       int
	Upgrades        int
	UpgradeRefusals int
	Reuses          int
	Revocations     []url.Values
	SignOuts        []url.Values
	LegacyRefreshes int
	DevicePolls     int
	// AuthorizeQuery and DeviceCodeForm are the last requests' values.
	AuthorizeQuery url.Values
	DeviceCodeForm url.Values
	// RefreshTimes records when each refresh succeeded.
	RefreshTimes []time.Time

	inflight    atomic.Int32
	maxInflight atomic.Int32
}

type code struct {
	challenge, redirect string
	scopes              []string
}

type productToken struct {
	audience, org string
	scopes        []string
}

type deviceReq struct {
	scopes []string
	polls  int
	done   bool
}

// New starts a fake Norcube Auth. Close it when done.
func New() *Server {
	s := &Server{
		AccessTTL:  299,
		ProductTTL: 300,
		codes:      map[string]*code{},
		grants:     map[string]*Grant{},
		byAccess:   map[string]*Grant{},
		accessExp:  map[string]time.Time{},
		byRT:       map[string]*Grant{},
		product:    map[string]productToken{},
		legacy:     map[string]bool{}, legacyUsed: map[string]bool{},
	}
	s.Server = httptest.NewServer(s)
	return s
}

// Lock and Unlock guard the knobs and counters while requests run.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

func (s *Server) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Server) id(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s-%d", prefix, s.nextID)
}

// jwt makes an unsigned token whose payload the CLI can read (exp).
func jwt(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	p, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(p) + ".sig"
}

// NewGrant creates a sign-in directly (as if a login had happened) and
// returns it. No scopes means full.
func (s *Server) NewGrant(scopes ...string) *Grant {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(scopes) == 0 {
		scopes = []string{"full"}
	}
	return s.newGrant(normalize(append(scopes, "offline_access")))
}

func (s *Server) newGrant(scopes []string) *Grant {
	g := &Grant{ID: s.id("grant"), Scopes: scopes, rotated: map[string]time.Time{}}
	s.grants[g.ID] = g
	s.issue(g)
	return g
}

// issue gives g a new refresh token and CLI access token.
func (s *Server) issue(g *Grant) {
	if g.Refresh != "" {
		g.rotated[g.Refresh] = s.now()
	}
	g.Refresh = "ory_rt_" + s.id("rt")
	g.Access = jwt(map[string]any{
		"token_type": "cli_access", "grant_id": g.ID, "jti": s.id("at"),
		"exp": s.now().Add(time.Duration(s.AccessTTL) * time.Second).Unix(),
	})
	s.byRT[g.Refresh] = g
	s.byAccess[g.Access] = g
	s.accessExp[g.Access] = s.now().Add(time.Duration(s.AccessTTL) * time.Second)
}

// AddLegacySession registers a refresh JWT of the older sign-in that has
// a session, so it can be upgraded (or refreshed the old way).
func (s *Server) AddLegacySession(refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legacy[refresh] = true
	s.LegacyRefresh = refresh
}

// Grant returns the grant a refresh token (current or rotated) belongs to.
func (s *Server) Grant(refresh string) *Grant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byRT[refresh]
}

// CurrentRefresh is the grant's current refresh token.
func (s *Server) CurrentRefresh(g *Grant) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return g.Refresh
}

// ExpireAccess makes the server refuse g's CLI access tokens issued so
// far, as if they had expired early.
func (s *Server) ExpireAccess(g *Grant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok, owner := range s.byAccess {
		if owner == g {
			delete(s.byAccess, tok)
		}
	}
}

// RevokeGrant ends a sign-in (session revoked in the dashboard).
func (s *Server) RevokeGrant(g *Grant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g.Revoked = true
}

// Rotate rotates g's refresh token as another process would.
func (s *Server) Rotate(g *Grant) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issue(g)
	return g.Refresh
}

// Stats is a copy of the counters and the last requests' values.
type Stats struct {
	Authorizations, CodeExchanges, Refreshes, Exchanges, Upgrades, UpgradeRefusals int
	Reuses, LegacyRefreshes, DevicePolls                                           int
	Revocations, SignOuts                                                          []url.Values
	AuthorizeQuery, DeviceCodeForm                                                 url.Values
	RefreshTimes                                                                   []time.Time
}

// Stats returns the counters so far.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Authorizations: s.Authorizations, CodeExchanges: s.CodeExchanges, Refreshes: s.Refreshes,
		Exchanges: s.Exchanges, Upgrades: s.Upgrades, UpgradeRefusals: s.UpgradeRefusals,
		Reuses: s.Reuses, LegacyRefreshes: s.LegacyRefreshes, DevicePolls: s.DevicePolls,
		Revocations: slices.Clone(s.Revocations), SignOuts: slices.Clone(s.SignOuts),
		AuthorizeQuery: s.AuthorizeQuery, DeviceCodeForm: s.DeviceCodeForm,
		RefreshTimes: slices.Clone(s.RefreshTimes),
	}
}

// Revoked reports whether g ended.
func (s *Server) Revoked(g *Grant) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return g.Revoked
}

// MaxConcurrentExchanges is the most token exchanges seen in flight at
// once.
func (s *Server) MaxConcurrentExchanges() int { return int(s.maxInflight.Load()) }

// Counts returns refreshes, exchanges and upgrades so far.
func (s *Server) Counts() (refreshes, exchanges, upgrades int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Refreshes, s.Exchanges, s.Upgrades
}

func normalize(scopes []string) []string {
	want := map[string]bool{"org.read": true}
	for _, sc := range scopes {
		want[sc] = true
	}
	var out []string
	for _, sc := range []string{"full", "org.read", "offline_access", "backup.read", "backup.write", "backup.verify",
		"domainradar.check", "langsync.read", "langsync.sync", "langsync.write", "billing.read", "cms.read", "cms.write"} {
		if !want[sc] || (want["full"] && sc != "full" && sc != "org.read" && sc != "offline_access") {
			continue
		}
		out = append(out, sc)
	}
	return out
}

func knownScopes(scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, sc := range scopes {
		if _, ok := scopeAudiences[sc]; !ok {
			return false
		}
	}
	return true
}

func allows(scopes []string, audience string) bool {
	for _, sc := range scopes {
		if slices.Contains(scopeAudiences[sc], audience) {
			return true
		}
	}
	return false
}

func oauthErr(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server":
		s.metadata(w, r)
	case "/oauth/authorize":
		s.authorize(w, r)
	case "/oauth/token":
		s.token(w, r)
	case "/oauth/device/code":
		s.deviceCode(w, r)
	case "/oauth/revoke":
		s.revoke(w, r)
	case "/auth/cli/sign-out":
		_ = r.ParseForm()
		s.mu.Lock()
		s.SignOuts = append(s.SignOuts, r.PostForm)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "/auth/me":
		if s.bearer(w, r, "auth") {
			fmt.Fprint(w, `{"id":"user-1","name":"Dev Eloper","email":"dev@example.com","avatarUrl":""}`)
		}
	case "/organizations":
		if s.bearer(w, r, "auth") {
			fmt.Fprintf(w, `[{"id":%q,"slug":"home","name":"Home","is_default":true},{"id":%q,"slug":"acme","name":"Acme Inc"}]`, HomeOrgID, AcmeOrgID)
		}
	case "/auth/me/sessions":
		if s.bearer(w, r, "auth") {
			fmt.Fprint(w, `[]`)
		}
	default:
		http.NotFound(w, r)
	}
}

// bearer checks a product token for audience. Scoped tokens get 403
// INSUFFICIENT_SCOPE when they're for another audience, like the
// products' own checks.
func (s *Server) bearer(w http.ResponseWriter, r *http.Request, audience string) bool {
	w.Header().Set("Content-Type", "application/json")
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	p, ok := s.product[tok]
	s.mu.Unlock()
	if !ok || p.audience != audience {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"UNAUTHORIZED","msg":"Unauthorized."}`)
		return false
	}
	return true
}

// ProductToken describes a token the fake minted, for assertions.
func (s *Server) ProductToken(tok string) (audience, org string, scopes []string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.product[tok]
	return p.audience, p.org, p.scopes, ok
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.OAuthOff {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	grants := []string{"authorization_code", "refresh_token"}
	if !s.NoDevice {
		grants = append(grants, GrantDeviceCode)
	}
	if !s.NoExchange {
		grants = append(grants, GrantTokenExchange)
	}
	m := map[string]any{
		"issuer":                                         s.URL,
		"authorization_endpoint":                         s.URL + "/oauth/authorize",
		"token_endpoint":                                 s.URL + "/oauth/token",
		"registration_endpoint":                          s.URL + "/oauth/register",
		"revocation_endpoint":                            s.URL + "/oauth/revoke",
		"scopes_supported":                               []string{"org.read", "backup.read", "offline_access"},
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          grants,
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"authorization_response_iss_parameter_supported": !s.IssOff,
	}
	if !s.NoDevice {
		m["device_authorization_endpoint"] = s.URL + "/oauth/device/code"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

// authorize plays the authorization endpoint and the consent page at
// once: it approves (or denies) and redirects back to the loopback
// callback.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Authorizations++
	s.AuthorizeQuery = q
	redirect := q.Get("redirect_uri")
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/callback" || u.Port() == "" {
		http.Error(w, "redirect_uri must be http://127.0.0.1:<port>/callback", http.StatusBadRequest)
		return
	}
	if q.Get("client_id") != ClientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {q.Get("state")}}
	switch {
	case s.WrongIss:
		back.Set("iss", "https://evil.example")
	case !s.IssOff:
		back.Set("iss", s.URL)
	}
	scopes := strings.Fields(q.Get("scope"))
	switch {
	case !knownScopes(scopes):
		back.Set("error", "invalid_scope")
		back.Set("error_description", "Unknown or missing scope.")
	case s.Deny:
		back.Set("error", "access_denied")
		back.Set("error_description", "The person denied the sign-in.")
	default:
		c := s.id("code")
		s.codes[c] = &code{challenge: q.Get("code_challenge"), redirect: redirect, scopes: normalize(scopes)}
		back.Set("code", c)
	}
	u.RawQuery = back.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound) // #nosec G710 -- checked above: the loopback callback
}

func (s *Server) signInResponse(w http.ResponseWriter, g *Grant) {
	resp := map[string]any{
		"access_token": g.Access, "token_type": "bearer", "expires_in": s.AccessTTL,
		"refresh_token": g.Refresh, "scope": strings.Join(g.Scopes, " "),
		"default_organization": map[string]any{"id": HomeOrgID, "name": "Home", "slug": "home", "logo_url": nil},
	}
	if s.NoDefaultOrg {
		resp["default_organization"] = nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f := r.PostForm
	if f.Get("client_id") == "" && f.Get("grant_type") == "refresh_token" {
		s.legacyRefresh(w, r)
		return
	}
	if f.Get("client_id") != ClientID {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "Unknown client.")
		return
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		s.codeExchange(w, f)
	case "refresh_token":
		s.refresh(w, f)
	case GrantTokenExchange:
		if f.Get("subject_token_type") == TokenTypeCLISession {
			s.upgrade(w, f)
			return
		}
		s.exchange(w, f)
	case GrantDeviceCode:
		s.devicePoll(w, f)
	default:
		oauthErr(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) codeExchange(w http.ResponseWriter, f url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[f.Get("code")]
	delete(s.codes, f.Get("code"))
	if !ok || c.redirect != f.Get("redirect_uri") {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "Unknown code or redirect_uri mismatch.")
		return
	}
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "The PKCE code_verifier doesn't match the challenge.")
		return
	}
	s.CodeExchanges++
	s.signInResponse(w, s.newGrant(c.scopes))
}

func (s *Server) refresh(w http.ResponseWriter, f url.Values) {
	if s.Delay > 0 {
		time.Sleep(s.Delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rt := f.Get("refresh_token")
	g, ok := s.byRT[rt]
	switch {
	case !ok || g.Revoked:
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "The refresh token is invalid.")
		return
	case rt != g.Refresh:
		if at, ok := g.rotated[rt]; ok && s.now().Sub(at) < ReuseGrace {
			oauthErr(w, http.StatusBadRequest, "invalid_grant", "The refresh token was just rotated.")
			return
		}
		// Reuse: two parties hold the sign-in. End it.
		s.Reuses++
		g.Revoked = true
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "Refresh token reuse.")
		return
	}
	s.Refreshes++
	s.RefreshTimes = append(s.RefreshTimes, s.now())
	s.issue(g)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": g.Access, "token_type": "bearer", "expires_in": s.AccessTTL,
		"refresh_token": g.Refresh, "scope": strings.Join(g.Scopes, " "),
	})
}

func (s *Server) exchange(w http.ResponseWriter, f url.Values) {
	n := s.inflight.Add(1)
	defer s.inflight.Add(-1)
	for {
		m := s.maxInflight.Load()
		if n <= m || s.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	if s.Delay > 0 {
		time.Sleep(s.Delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Get("subject_token_type") != TokenTypeAccess || f.Get("audience") == "" || f.Get("scope") != "" {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "Bad exchange.")
		return
	}
	if !slices.Contains(scopeAudiences["full"], f.Get("audience")) {
		oauthErr(w, http.StatusBadRequest, "invalid_target", "Unknown audience.")
		return
	}
	// Only Norcube Auth's own tokens may come without an organization.
	org, hasOrg := f.Get("organization_id"), f.Has("organization_id")
	if (org == "" || !hasOrg) && (hasOrg || f.Get("audience") != "auth") {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "organization_id must be an organization id.")
		return
	}
	// Like the real JWTs, every CLI access token of a live sign-in works
	// until it expires.
	g, ok := s.byAccess[f.Get("subject_token")]
	if !ok || g.Revoked || !s.now().Before(s.accessExp[f.Get("subject_token")]) {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "The access token is invalid or expired, or the sign-in was revoked.")
		return
	}
	aud := f.Get("audience")
	if !allows(g.Scopes, aud) {
		oauthErr(w, http.StatusForbidden, "insufficient_scope", "This sign-in's scopes don't cover "+aud+".")
		return
	}
	if org != "" && org != HomeOrgID && org != AcmeOrgID {
		oauthErr(w, http.StatusForbidden, "org_access_denied", "You are not a member of the organization.")
		return
	}
	s.Exchanges++
	tok := jwt(map[string]any{"aud": aud, "org": org, "jti": s.id("pt"), "exp": s.now().Add(time.Duration(s.ProductTTL) * time.Second).Unix()})
	var scopes []string
	if !slices.Contains(g.Scopes, "full") {
		for _, sc := range g.Scopes {
			if slices.Contains(scopeAudiences[sc], aud) {
				scopes = append(scopes, sc)
			}
		}
	}
	s.product[tok] = productToken{audience: aud, org: org, scopes: scopes}
	resp := map[string]any{"access_token": tok, "issued_token_type": TokenTypeAccess, "token_type": "Bearer", "expires_in": s.ProductTTL}
	if scopes != nil {
		resp["scope"] = strings.Join(scopes, " ")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) upgrade(w http.ResponseWriter, f url.Values) {
	if s.Delay > 0 {
		time.Sleep(s.Delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Get("audience") != "" || f.Get("scope") != "" {
		oauthErr(w, http.StatusBadRequest, "invalid_request", "No audience or scope on an upgrade.")
		return
	}
	tok := f.Get("subject_token")
	if !s.legacy[tok] || s.legacyUsed[tok] {
		s.UpgradeRefusals++
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "The session can't be upgraded.")
		return
	}
	s.legacyUsed[tok] = true
	s.LegacyRefresh = ""
	s.Upgrades++
	s.signInResponse(w, s.newGrant(normalize([]string{"full", "offline_access"})))
}

// legacyRefresh is the older cookie refresh (an older Norcube Auth).
func (s *Server) legacyRefresh(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := r.Cookie("jds_refresh_token")
	if err != nil || s.LegacyRefresh == "" || c.Value != s.LegacyRefresh {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"unauthorized","msg":"Unauthorized."}`)
		return
	}
	s.LegacyRefreshes++
	delete(s.legacy, s.LegacyRefresh)
	s.LegacyRefresh = jwt(map[string]any{"sid": "sess-1", "jti": s.id("legacy")})
	s.legacy[s.LegacyRefresh] = true
	aud, org := r.PostForm.Get("audience"), r.PostForm.Get("organization_id")
	tok := jwt(map[string]any{"aud": aud, "org": org, "jti": s.id("pt"), "exp": s.now().Add(5 * time.Minute).Unix()})
	s.product[tok] = productToken{audience: aud, org: org}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "refresh_token": s.LegacyRefresh, "token_type": "Bearer", "expires_in": 300})
}

func (s *Server) deviceCode(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DeviceCodeForm = r.PostForm
	if r.PostForm.Get("client_id") != ClientID {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	scopes := strings.Fields(r.PostForm.Get("scope"))
	if !knownScopes(scopes) {
		oauthErr(w, http.StatusBadRequest, "invalid_scope", "Unknown or missing scope.")
		return
	}
	s.device = &deviceReq{scopes: normalize(scopes)}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device_code": "dc-oauth", "user_code": "WDJB-MJHT",
		"verification_uri":          "https://app.example/device",
		"verification_uri_complete": "https://app.example/device?user_code=WDJB-MJHT",
		"expires_in":                600, "interval": 5,
	})
}

func (s *Server) devicePoll(w http.ResponseWriter, f url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DevicePolls++
	d := s.device
	if d == nil || d.done || f.Get("device_code") != "dc-oauth" {
		oauthErr(w, http.StatusBadRequest, "invalid_grant", "Unknown or already collected code.")
		return
	}
	if d.polls < len(s.DeviceAnswers) {
		answer := s.DeviceAnswers[d.polls]
		d.polls++
		oauthErr(w, http.StatusBadRequest, answer, "")
		return
	}
	d.done = true
	s.signInResponse(w, s.newGrant(d.scopes))
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Revocations = append(s.Revocations, r.PostForm)
	if s.RevokeStatus != 0 {
		oauthErr(w, s.RevokeStatus, "temporarily_unavailable", "Revocation failed; retry.")
		return
	}
	if r.PostForm.Get("client_id") != ClientID {
		oauthErr(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	// Current and rotated tokens alike end the grant.
	if g, ok := s.byRT[r.PostForm.Get("token")]; ok {
		g.Revoked = true
	}
	w.WriteHeader(http.StatusOK)
}
