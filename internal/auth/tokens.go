package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/transport"
)

// Audience names recognized by the auth service. These match the constants in
// apps/auth/internal/handler/user/user.go.
const (
	AudienceAuth        = "auth"
	AudienceBilling     = "billing"
	AudienceSnapDB      = "snapdb-api"
	AudienceLangsync    = "langsync-api"
	AudienceDomainradar = "domainradar"
	AudiencePrompthub   = "prompthub"
	AudienceCMS         = "cms-api"
)

// TokenSource returns a valid Bearer access token for a (audience, organization)
// pair, refreshing it via /oauth/token when necessary. It is the single place
// the rest of the CLI should ask for "give me a token I can use right now."
//
// It is safe for concurrent use, and refreshes are serialized across
// processes too: the server rotates the refresh token on every exchange
// and rejects the old one, so two exchanges racing with the same refresh
// token would sign the user out.
type TokenSource struct {
	// Profile names whose credentials are used ("" is the default profile).
	Profile  string
	APIBase  string
	Audience string
	OrgID    string
	HTTP     *http.Client

	// Static, when set, is returned as the token for every request and
	// nothing is exchanged or stored (NORCUBE_TOKEN, --token-file).
	Static string
	// CI, when set, mints the token from a CI token instead: one exchange
	// per service, cached in memory (NORCUBE_TOKEN=nrc_ci_…).
	CI *CIExchanger

	// ProbeOrgID is an organization the user is known to belong to (the
	// active org). When an exchange for OrgID is refused, one exchange for
	// ProbeOrgID tells "signed out" apart from "not a member of OrgID",
	// which Norcube Auth answers with the same 401.
	ProbeOrgID string
	// OrgLabel names OrgID in error messages (its slug); defaults to the id.
	OrgLabel string

	// LockDir holds the cross-process refresh lock files. Empty means
	// locks/ next to the config file.
	LockDir string
	// LockTimeout bounds the wait for another process's refresh.
	LockTimeout time.Duration

	semOnce sync.Once
	sem     chan struct{}
	now     func() time.Time
}

// DefaultLockTimeout is how long a command waits for another norcube
// process to finish refreshing the session.
const DefaultLockTimeout = 30 * time.Second

// expiryMargin: a cached token with less life left than this is refreshed
// first, so it can't expire in flight.
const expiryMargin = 30 * time.Second

func NewTokenSource(apiBase, audience, orgID string) *TokenSource {
	return &TokenSource{
		APIBase:     apiBase,
		Audience:    audience,
		OrgID:       orgID,
		HTTP:        transport.Default(),
		LockTimeout: DefaultLockTimeout,
	}
}

// SessionState is what Norcube Auth says about a profile's stored session.
type SessionState string

const (
	// SessionActive: the session refreshes, or a token minted from it a
	// few minutes ago is still cached.
	SessionActive SessionState = "active"
	// SessionEnded: Norcube Auth refused the session: revoked (from the
	// dashboard or another `logout`), replayed, or unused for too long.
	SessionEnded SessionState = "ended"
	// SessionNone: nothing is stored for the profile.
	SessionNone SessionState = "none"
	// SessionUnknown: Norcube Auth couldn't be asked (network, timeout).
	SessionUnknown SessionState = "unknown"
)

// CheckSession reports whether the profile's stored session still works,
// by getting a token for t.Audience and t.OrgID. A cached access token that
// hasn't expired counts as active without a request: it was minted from
// this session within the last few minutes, and asking again would rotate
// the refresh token for nothing. Otherwise it costs one refresh, under the
// usual locks. The error explains SessionUnknown and is nil otherwise.
func (t *TokenSource) CheckSession(ctx context.Context) (SessionState, error) {
	if !HasCredentials(t.Profile, t.APIBase) {
		return SessionNone, nil
	}
	_, err := t.Token(ctx)
	switch {
	case err == nil:
		return SessionActive, nil
	case errors.Is(err, ErrOrgAccessDenied):
		// Norcube Auth checks the session before the organization, so a
		// refused organization means the session itself is alive.
		return SessionActive, nil
	case errors.Is(err, ErrLoginRequired):
		return SessionEnded, nil
	default:
		return SessionUnknown, err
	}
}

// ErrLoginRequired marks auth failures that a fresh `norcube login` fixes:
// no refresh token in the keyring, or the server rejecting the one we have
// (revoked / fully expired). main() detects it with errors.Is and prints
// the re-login hint once, instead of every command growing its own.
var ErrLoginRequired = errors.New("not signed in or session expired")

// ErrOrgAccessDenied: the session is fine, but the user may not act in the
// requested organization (not a member, or no such organization). Match
// it with errors.Is; the error itself is an *OrgAccessError.
var ErrOrgAccessDenied = errors.New("organization access denied")

// OrgAccessError reports which organization was refused.
type OrgAccessError struct{ Org string }

func (e *OrgAccessError) Error() string {
	return fmt.Sprintf("you're not a member of organization %s (or it doesn't exist). Run `norcube org list` to see yours", e.Org)
}

func (e *OrgAccessError) Is(target error) bool { return target == ErrOrgAccessDenied }

// BearerInjector returns a request editor that injects a fresh access
// token into each outgoing request. Every generated API client shares
// this, so token acquisition lives in exactly one place.
func (t *TokenSource) BearerInjector() func(ctx context.Context, req *http.Request) error {
	return func(ctx context.Context, req *http.Request) error {
		token, err := t.Token(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// Token returns a valid access token, minting one via /oauth/token if no
// cached token exists or the cached token is close to expiring.
//
// Side effect: when /oauth/token returns a rotated refresh token, the
// keyring-stored refresh token is updated in place. This keeps the CLI
// logged in indefinitely as long as the user runs `nrc` at least once
// per refresh-token TTL — the rotation pushes the expiry forward on
// each exchange. The user only sees `nrc login --force` again if they
// stay completely inactive for the full TTL window.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	if t.CI != nil {
		return t.CI.Token(ctx, t.Audience)
	}
	if t.Static != "" {
		return t.Static, nil
	}
	if tok, ok := t.cached(); ok {
		return tok, nil
	}

	// In-process: one refresh at a time; the others wait and then find
	// the fresh token in the cache.
	t.semOnce.Do(func() { t.sem = make(chan struct{}, 1) })
	select {
	case t.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-t.sem }()
	if tok, ok := t.cached(); ok {
		return tok, nil
	}

	// Cross-process: every token of this profile shares one refresh
	// token, so every refresh for the profile takes the same lock.
	unlock, err := LockProfile(ctx, t.LockDir, t.Profile, t.APIBase, t.LockTimeout)
	if err != nil {
		return "", err
	}
	defer unlock()
	// Another process may have refreshed while we waited for the lock.
	if tok, ok := t.cached(); ok {
		return tok, nil
	}

	// Read the refresh token only now, under the lock: a value read
	// before it may already have been rotated away by another process.
	refresh, err := LoadRefreshToken(t.Profile, t.APIBase)
	if err != nil {
		if errors.Is(err, ErrNotLoggedIn) {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", ErrLoginRequired, err)
	}

	received := t.clock()
	tr, err := t.exchangeRefresh(ctx, refresh, t.Audience, t.OrgID)
	if err != nil {
		if errors.Is(err, errOrgRefused) {
			return "", &OrgAccessError{Org: t.orgLabel()}
		}
		if errors.Is(err, ErrLoginRequired) {
			return "", t.diagnose(ctx, refresh, err)
		}
		return "", err
	}
	if err := t.saveExchange(tr, received, t.Audience, t.OrgID); err != nil {
		return "", err
	}
	return tr.AccessToken, nil
}

// diagnose runs after an exchange for OrgID was refused with a bare 401.
// Current Norcube Auth says ORG_ACCESS_DENIED for a foreign organization
// (handled in Token); older servers answer a dead session and a foreign
// organization with the same 401
// (membership is checked before the refresh token is rotated, so the
// refused exchange consumed nothing). One exchange for an organization the
// user belongs to decides which it was. Must be called under the lock.
func (t *TokenSource) diagnose(ctx context.Context, refresh string, refused error) error {
	if t.OrgID == "" || t.ProbeOrgID == "" || t.ProbeOrgID == t.OrgID {
		// Nothing to compare against. A fresh login also resets the
		// active org, so it is the right advice either way.
		return refused
	}
	received := t.clock()
	tr, err := t.exchangeRefresh(ctx, refresh, AudienceAuth, t.ProbeOrgID)
	switch {
	case errors.Is(err, ErrLoginRequired):
		return err
	case err != nil:
		return fmt.Errorf("a token for organization %s was refused, and checking the session failed: %w", t.orgLabel(), err)
	}
	// The probe rotated the refresh token: persist it or the session dies.
	if err := t.saveExchange(tr, received, AudienceAuth, t.ProbeOrgID); err != nil {
		return err
	}
	return &OrgAccessError{Org: t.orgLabel()}
}

func (t *TokenSource) orgLabel() string {
	if t.OrgLabel != "" {
		return t.OrgLabel
	}
	return t.OrgID
}

// saveExchange persists a rotated refresh token and caches the access
// token for (audience, orgID).
func (t *TokenSource) saveExchange(tr *tokenResponse, received time.Time, audience, orgID string) error {
	if tr.RefreshToken != "" {
		if err := SaveRefreshToken(t.Profile, t.APIBase, tr.RefreshToken); err != nil {
			// Failing to persist the rotated refresh token means the
			// next /oauth/token call will fail (the keyring still
			// has the now-revoked old value). Surface it loudly so
			// the user can re-login rather than chasing a phantom
			// 401 later.
			return fmt.Errorf("persist rotated refresh token: %w", err)
		}
	}
	var expiresAt time.Time
	if tr.ExpiresIn > 0 {
		expiresAt = received.Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	// Cache failure is non-fatal — we still got a usable token.
	_ = SaveAccessTokenUntil(t.Profile, t.APIBase, audience, orgID, tr.AccessToken, expiresAt)
	return nil
}

// cached returns the cached access token when it has more than
// expiryMargin of life left.
func (t *TokenSource) cached() (string, bool) {
	tok, exp, err := loadAccess(t.Profile, t.APIBase, t.Audience, t.OrgID)
	if err != nil || tok == "" || exp.Sub(t.clock()) < expiryMargin {
		return "", false
	}
	return tok, true
}

func (t *TokenSource) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// LockProfile takes the cross-process lock that serializes every change
// to a profile's credentials for api (refreshes, login, logout). dir
// empty means locks/ next to the config file; timeout <= 0 means
// DefaultLockTimeout. Call the returned function to release it.
func LockProfile(ctx context.Context, dir, profile, api string, timeout time.Duration) (func(), error) {
	if dir == "" {
		cfgDir, err := config.Dir()
		if err != nil {
			return nil, fmt.Errorf("locate lock directory: %w", err)
		}
		dir = filepath.Join(cfgDir, "locks")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // #nosec G703 -- the user's own config directory
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	sum := sha256.Sum256([]byte(profileName(profile) + "|" + api))
	path := filepath.Join(dir, "refresh-"+hex.EncodeToString(sum[:8])+".lock")

	if timeout <= 0 {
		timeout = DefaultLockTimeout
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fl := flock.New(path)
	ok, err := fl.TryLockContext(lctx, 25*time.Millisecond)
	if !ok || err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) || err == nil {
			return nil, fmt.Errorf("timed out after %s waiting for another norcube process to refresh the session (lock file %s)", timeout, path)
		}
		return nil, fmt.Errorf("take refresh lock %s: %w", path, err)
	}
	return func() { _ = fl.Unlock() }, nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	// RefreshToken is non-empty when the auth service rotated the
	// refresh token on this exchange. When present the CLI MUST
	// replace the keyring-stored refresh token with this value —
	// the previously-stored one has already been revoked
	// server-side and will fail on the next /oauth/token call.
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type apiError struct {
	Type string `json:"type"`
	Msg  string `json:"msg"`
}

// errOrgRefused: /oauth/token refused the requested organization (not a
// member, no such organization, or a malformed id) while the session itself
// is fine. Token turns it into an *OrgAccessError.
var errOrgRefused = errors.New("organization refused")

// Error types Norcube Auth returns for an organization it won't issue a
// token for. A refused organization consumes nothing, so the stored
// refresh token stays valid.
const (
	apiErrOrgAccessDenied     = "ORG_ACCESS_DENIED"
	apiErrInvalidOrganization = "INVALID_ORGANIZATION"
)

// exchangeRefresh calls POST /oauth/token (form-encoded). The auth service
// reads the refresh token from a cookie when called from the browser; for the
// CLI we send it in the form body via a Cookie header instead.
//
// When the auth service rotates the refresh token on this call the rotated
// value is in RefreshToken and the caller MUST persist it before the next
// /oauth/token call (the presented refresh token has been revoked
// server-side). Empty means the server didn't rotate (legacy server, or a
// flow that doesn't issue refresh tokens).
func (t *TokenSource) exchangeRefresh(ctx context.Context, refresh, audience, orgID string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("audience", audience)
	if orgID != "" {
		form.Set("organization_id", orgID)
	}

	// #nosec G704 -- APIBase is the Norcube Auth URL the user configured.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.APIBase+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The handler reads the refresh token from the configured cookie name.
	// Secure/HttpOnly/SameSite are response attributes; they don't apply to
	// a cookie attached to an outgoing request.
	req.AddCookie(&http.Cookie{Name: "jds_refresh_token", Value: refresh}) // #nosec G124

	resp, err := t.HTTP.Do(req) // #nosec G704 -- see above
	if err != nil {
		return nil, fmt.Errorf("oauth/token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var apiErr apiError
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Type == apiErrOrgAccessDenied || apiErr.Type == apiErrInvalidOrganization {
			return nil, errOrgRefused
		}
		// Any other 401/403: the session was refused. Older servers send
		// the same 401 for a foreign organization, so Token runs diagnose
		// to tell an expired session from one.
		base := fmt.Errorf("oauth/token failed (%d)", resp.StatusCode)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			base = fmt.Errorf("%w: oauth/token rejected the stored session (%d)", ErrLoginRequired, resp.StatusCode)
		}
		if apiErr.Msg != "" {
			return nil, fmt.Errorf("%w: %s", base, apiErr.Msg)
		}
		return nil, base
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode oauth/token: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("oauth/token returned empty access token")
	}
	return &tr, nil
}

// jwtExpiry decodes a JWT's exp claim without verifying the signature (the
// server validates it on the next request anyway). Undecodable tokens get
// the zero time, which reads as expired.
func jwtExpiry(jwt string) time.Time {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}
