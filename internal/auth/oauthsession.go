package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/norcubeplatform/cli/internal/clierr"
)

// Tokens of an OAuth sign-in (credentials format 2):
//
//   - The refresh token and the 5-minute CLI access token live in the
//     profile's credentials. Refreshing rotates the refresh token, so it
//     happens under the in-process mutex and the per-profile lock file,
//     and the rotated token is saved before the new access token is used.
//   - The CLI access token is exchanged (RFC 8693) for a token per
//     (service, organization). Exchanges use nothing up, so they run in
//     parallel without the lock, and their tokens are cached in memory
//     only: writing them back to the credential store without the lock
//     could overwrite a refresh token another process just rotated.

// memCache holds the exchanged tokens of this process.
var memCache = struct {
	sync.Mutex
	m map[string]cachedAccess
}{m: map[string]cachedAccess{}}

func memPrefix(profile, api string) string { return profileName(profile) + "\x00" + api + "\x00" }

func memKey(profile, api, audience, orgID string) string {
	return memPrefix(profile, api) + audience + "\x00" + orgID
}

func memGet(key string) (cachedAccess, bool) {
	memCache.Lock()
	defer memCache.Unlock()
	a, ok := memCache.m[key]
	return a, ok
}

func memPut(key string, a cachedAccess) {
	memCache.Lock()
	defer memCache.Unlock()
	memCache.m[key] = a
}

// forgetMemory drops the profile's exchanged tokens (sign-in, sign-out).
func forgetMemory(profile, api string) {
	prefix := memPrefix(profile, api)
	memCache.Lock()
	defer memCache.Unlock()
	for k := range memCache.m {
		if strings.HasPrefix(k, prefix) {
			delete(memCache.m, k)
		}
	}
}

// flights coalesces concurrent exchanges for the same key into one.
var flights = struct {
	sync.Mutex
	m map[string]*flight
}{m: map[string]*flight{}}

type flight struct {
	done chan struct{}
	tok  string
	err  error
}

func coalesce(ctx context.Context, key string, fn func() (string, error)) (string, error) {
	flights.Lock()
	if f, ok := flights.m[key]; ok {
		flights.Unlock()
		select {
		case <-f.done:
			return f.tok, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	flights.m[key] = f
	flights.Unlock()

	f.tok, f.err = fn()
	flights.Lock()
	delete(flights.m, key)
	flights.Unlock()
	close(f.done)
	return f.tok, f.err
}

// refreshSems serializes this process's refreshes per profile; the lock
// file does it across processes.
var refreshSems sync.Map

func refreshSem(profile, api string) chan struct{} {
	s, _ := refreshSems.LoadOrStore(memPrefix(profile, api), make(chan struct{}, 1))
	return s.(chan struct{})
}

// ScopeError: the sign-in is limited by `login --scopes` and its scopes
// don't cover the service. Match it with errors.As.
type ScopeError struct {
	Audience string
	// Scopes are the sign-in's granted scopes.
	Scopes []string
}

func (e *ScopeError) Error() string {
	return fmt.Sprintf("the scopes of this login (%s) don't cover %s", DescribeScopes(e.Scopes), ServiceName(e.Audience))
}

// ServiceName names an audience for messages.
func ServiceName(audience string) string {
	switch audience {
	case AudienceAuth:
		return "Norcube Auth"
	case AudienceBilling:
		return "Billing"
	case AudienceSnapDB:
		return "Backup"
	case AudienceLangsync:
		return "LangSync"
	case AudienceDomainradar:
		return "DomainRadar"
	case AudiencePrompthub:
		return "PromptHub"
	case AudienceCMS:
		return "CMS"
	}
	return audience
}

// errSubjectRejected: the token exchange refused the CLI access token
// (expired early, or the sign-in ended). A refresh tells which.
var errSubjectRejected = errors.New("the CLI access token was refused")

// errSessionReplaced: the profile's stored sign-in changed kind while
// this command waited for the lock (a login of the older kind).
var errSessionReplaced = errors.New("the profile's sign-in changed while this command ran; run it again")

// endedSignIn is the answer to a refresh token Norcube Auth no longer
// accepts.
func endedSignIn() error {
	return fmt.Errorf("%w: Norcube Auth ended this sign-in (revoked, signed out elsewhere, or unused for 30 days)", ErrLoginRequired)
}

func loadCreds(profile, api string) (*credentials, error) {
	credsMu.RLock()
	defer credsMu.RUnlock()
	return loadCredentials(profile, api)
}

func saveCreds(profile string, c *credentials) error {
	credsMu.Lock()
	defer credsMu.Unlock()
	return saveCredentials(profile, c)
}

func (t *TokenSource) memKey() string { return memKey(t.Profile, t.APIBase, t.Audience, t.OrgID) }

// memCached returns this process's exchanged token for (audience, org)
// when it has more than expiryMargin of life left.
func (t *TokenSource) memCached() (string, bool) {
	a, ok := memGet(t.memKey())
	if !ok || a.Token == "" || a.ExpiresAt.Sub(t.clock()) < expiryMargin {
		return "", false
	}
	return a.Token, true
}

// oauthToken returns a token for (audience, org) of an OAuth sign-in.
func (t *TokenSource) oauthToken(ctx context.Context) (string, error) {
	if t.OrgID == "" {
		return "", clierr.Usage(fmt.Errorf("a token for %s needs an organization. Run `norcube org use <slug>` or pass --org <slug>", ServiceName(t.Audience)))
	}
	return coalesce(ctx, t.memKey(), func() (string, error) {
		if tok, ok := t.memCached(); ok {
			return tok, nil
		}
		access, err := t.cliAccess(ctx, "")
		if err != nil {
			return "", err
		}
		tok, err := t.exchange(ctx, access)
		if errors.Is(err, errSubjectRejected) {
			// Expired in flight, or the sign-in ended: a refresh
			// tells which.
			if access, err = t.cliAccess(ctx, access); err != nil {
				return "", err
			}
			if tok, err = t.exchange(ctx, access); errors.Is(err, errSubjectRejected) {
				return "", endedSignIn()
			}
		}
		return tok, err
	})
}

// validCLIAccess returns c's CLI access token when it has more than
// expiryMargin of life left and isn't stale (the one just refused).
func validCLIAccess(c *credentials, stale string, now time.Time) (string, bool) {
	if c.format() != FormatOAuth || c.CLIAccess == nil || c.CLIAccess.Token == "" || c.CLIAccess.Token == stale {
		return "", false
	}
	if c.CLIAccess.ExpiresAt.Sub(now) < expiryMargin {
		return "", false
	}
	return c.CLIAccess.Token, true
}

func (t *TokenSource) storedCLIAccess(stale string) (string, bool) {
	c, err := loadCreds(t.Profile, t.APIBase)
	if err != nil {
		return "", false
	}
	return validCLIAccess(c, stale, t.clock())
}

// cliAccess returns a CLI access token, refreshing the sign-in when the
// stored one is about to expire or is stale.
func (t *TokenSource) cliAccess(ctx context.Context, stale string) (string, error) {
	if tok, ok := t.storedCLIAccess(stale); ok {
		return tok, nil
	}
	sem := refreshSem(t.Profile, t.APIBase)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-sem }()
	// Another goroutine may have refreshed while we waited.
	if tok, ok := t.storedCLIAccess(stale); ok {
		return tok, nil
	}
	unlock, err := LockProfile(ctx, t.LockDir, t.Profile, t.APIBase, t.LockTimeout)
	if err != nil {
		return "", err
	}
	defer unlock()
	// Read the refresh token only now, under the lock: a value read
	// before it may already have been rotated away by another process.
	c, err := loadCreds(t.Profile, t.APIBase)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLoginRequired, err)
	}
	switch {
	case c.RefreshToken == "":
		return "", ErrNotLoggedIn
	case c.format() != FormatOAuth:
		return "", errSessionReplaced
	}
	if tok, ok := validCLIAccess(c, stale, t.clock()); ok {
		return tok, nil
	}
	return t.refreshCLIAccess(ctx, c, true)
}

// tokenEndpoint is the OAuth server's token endpoint: from the (cached)
// metadata, else the path every Norcube Auth serves it at.
func (t *TokenSource) tokenEndpoint(ctx context.Context) string {
	if m, err := Discover(ctx, t.HTTP, t.APIBase); err == nil {
		return m.TokenEndpoint
	}
	return strings.TrimRight(t.APIBase, "/") + "/oauth/token"
}

// refreshCLIAccess rotates the refresh token. Must be called under the
// profile's lock. On invalid_grant it reads the stored refresh token once
// more: if it changed meanwhile (another process rotated it), that one is
// used instead of declaring the sign-in ended. Norcube Auth refuses a
// token rotated out less than 10 seconds ago without ending the session,
// so presenting the old one by mistake is harmless within that grace.
func (t *TokenSource) refreshCLIAccess(ctx context.Context, c *credentials, retry bool) (string, error) {
	form := url.Values{
		"grant_type":    {grantRefreshToken},
		"refresh_token": {c.RefreshToken},
		"client_id":     {ClientID},
	}
	received := t.clock()
	// Not retried by the transport: a repeated refresh would present a
	// token the first attempt may already have rotated.
	tr, oe, err := postToken(ctx, t.HTTP, t.tokenEndpoint(ctx), form, false)
	if err != nil {
		return "", fmt.Errorf("refresh the sign-in: %w", err)
	}
	if oe != nil {
		if oe.Code != oauthInvalidGrant {
			return "", oauthFailure("refresh the sign-in", oe)
		}
		if retry {
			if now, err := loadCreds(t.Profile, t.APIBase); err == nil && now.format() == FormatOAuth &&
				now.RefreshToken != "" && now.RefreshToken != c.RefreshToken {
				if tok, ok := validCLIAccess(now, "", t.clock()); ok {
					return tok, nil
				}
				return t.refreshCLIAccess(ctx, now, false)
			}
		}
		return "", endedSignIn()
	}
	next := *c
	if tr.RefreshToken != "" {
		next.RefreshToken = tr.RefreshToken
	}
	next.CLIAccess = &cachedAccess{Token: tr.AccessToken, ExpiresAt: expiresAt(received, tr.ExpiresIn, tr.AccessToken)}
	if scopes := parseScope(tr.Scope); len(scopes) > 0 {
		next.Scopes = scopes
	}
	if err := saveCreds(t.Profile, &next); err != nil {
		// The presented refresh token is used up: without the new one
		// the next command can't refresh. Say so now.
		return "", fmt.Errorf("persist rotated refresh token: %w", err)
	}
	return tr.AccessToken, nil
}

func expiresAt(received time.Time, expiresIn int, token string) time.Time {
	if expiresIn > 0 {
		return received.Add(time.Duration(expiresIn) * time.Second)
	}
	return jwtExpiry(token)
}

// exchange trades the CLI access token for a token for (audience, org)
// and caches it in memory.
func (t *TokenSource) exchange(ctx context.Context, access string) (string, error) {
	form := url.Values{
		"grant_type":         {grantTokenExchange},
		"client_id":          {ClientID},
		"subject_token":      {access},
		"subject_token_type": {tokenTypeAccessToken},
		"audience":           {t.Audience},
		"organization_id":    {t.OrgID},
	}
	received := t.clock()
	// An exchange uses nothing up, so the transport may retry it.
	tr, oe, err := postToken(ctx, t.HTTP, t.tokenEndpoint(ctx), form, true)
	if err != nil {
		return "", fmt.Errorf("get a token for %s: %w", ServiceName(t.Audience), err)
	}
	if oe != nil {
		switch oe.Code {
		case oauthInvalidGrant:
			return "", errSubjectRejected
		case oauthOrgAccessDenied:
			return "", &OrgAccessError{Org: t.orgLabel()}
		case oauthInsufficientScope:
			var scopes []string
			if c, err := loadCreds(t.Profile, t.APIBase); err == nil {
				scopes = c.Scopes
			}
			return "", &ScopeError{Audience: t.Audience, Scopes: scopes}
		case oauthInvalidTarget:
			return "", fmt.Errorf("no tokens for %q from Norcube Auth (%s); is this norcube version too new for it?", t.Audience, oe.Error())
		}
		return "", oauthFailure("get a token for "+ServiceName(t.Audience), oe)
	}
	memPut(t.memKey(), cachedAccess{Token: tr.AccessToken, ExpiresAt: expiresAt(received, tr.ExpiresIn, tr.AccessToken)})
	return tr.AccessToken, nil
}

// upgradeLegacy moves an older sign-in (format 1) to OAuth, once: Norcube
// Auth uses up its refresh JWT and links a full grant to the same CLI
// session, so nobody signs in again. Must be called under the profile's
// lock. A token Norcube Auth won't move (the session ended, or the token
// predates recorded CLI sessions) asks for one new sign-in.
func (t *TokenSource) upgradeLegacy(ctx context.Context, m *Metadata, refresh string) error {
	form := url.Values{
		"grant_type":         {grantTokenExchange},
		"client_id":          {ClientID},
		"subject_token":      {refresh},
		"subject_token_type": {tokenTypeCLISession},
	}
	received := t.clock()
	// Not retried: the refresh JWT is used up by the first attempt.
	tr, oe, err := postToken(ctx, t.HTTP, m.TokenEndpoint, form, false)
	if err != nil {
		return fmt.Errorf("move this sign-in to the OAuth sign-in: %w", err)
	}
	if oe != nil {
		if oe.Code == oauthInvalidGrant {
			return fmt.Errorf("%w: Norcube Auth couldn't move this sign-in to the new sign-in (the session ended, or it was made before Norcube recorded CLI sessions); sign in once more", ErrLoginRequired)
		}
		return oauthFailure("move this sign-in to the OAuth sign-in", oe)
	}
	if tr.RefreshToken == "" {
		return errors.New("move this sign-in to the OAuth sign-in: the response has no refresh token")
	}
	if err := SaveOAuthSession(t.Profile, t.APIBase, tr.RefreshToken, tr.AccessToken,
		expiresAt(received, tr.ExpiresIn, tr.AccessToken), parseScope(tr.Scope)); err != nil {
		return fmt.Errorf("persist the upgraded sign-in: %w", err)
	}
	return nil
}

// isLimitedScope reports whether err is a ScopeError.
func isLimitedScope(err error) bool {
	var se *ScopeError
	return errors.As(err, &se)
}
