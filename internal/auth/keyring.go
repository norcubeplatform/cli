package auth

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/norcubeplatform/cli/internal/config"
	"github.com/norcubeplatform/cli/internal/credstore"
)

// Each profile's secrets are one item in the credential store, key
// "profile:<name>", holding a JSON credentials blob: the refresh token
// and the cached access tokens. One item per profile means logout removes
// everything at once.
//
// The blob records the auth URL it belongs to. A blob for another auth
// URL (the profile's environment changed) counts as signed out, so a
// session for one endpoint is never sent to another.

// Store is where credentials are kept. The OS keyring by default.
type Store = credstore.Store

// ErrSecretNotFound is returned by Store.Get for a missing key.
var ErrSecretNotFound = credstore.ErrNotFound

var store Store = credstore.Keyring{}

// SetStore replaces the default credential store and returns the previous
// one.
func SetStore(s Store) Store {
	prev := store
	store = s
	return prev
}

var (
	profileStoresMu sync.Mutex
	profileStores   = map[string]Store{}
)

// UseStore makes profile keep its credentials in s instead of the default
// store (a profile with credential_store = "file"). nil restores the
// default.
func UseStore(profile string, s Store) {
	profileStoresMu.Lock()
	defer profileStoresMu.Unlock()
	if s == nil {
		delete(profileStores, profileName(profile))
		return
	}
	profileStores[profileName(profile)] = s
}

func storeFor(profile string) Store {
	profileStoresMu.Lock()
	defer profileStoresMu.Unlock()
	if s, ok := profileStores[profileName(profile)]; ok {
		return s
	}
	return store
}

// Credential formats. Format 1 (no "format" field) is the older sign-in:
// a refresh JWT of Norcube Auth's own sessions, refreshed straight into
// per-(audience, organization) tokens, which are cached in the blob too.
// Format 2 is the OAuth sign-in: the grant's refresh token, the cached
// 5-minute CLI access token and the granted scopes. Its per-service tokens
// are only cached in memory (see oauthsession.go), so the blob changes
// only on a refresh, under the profile's lock.
const (
	FormatLegacy = 1
	FormatOAuth  = 2
)

type credentials struct {
	// Format is FormatOAuth for an OAuth sign-in; absent (0) means
	// FormatLegacy, which is what CLI versions before it wrote.
	Format       int    `json:"format,omitempty"`
	AuthURL      string `json:"auth_url"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// Access caches access tokens by "<audience>|<organization id>"
	// (format 1 only).
	Access map[string]cachedAccess `json:"access,omitempty"`
	// CLIAccess is the cached CLI access token (format 2), only good for
	// the token exchange.
	CLIAccess *cachedAccess `json:"cli_access,omitempty"`
	// Scopes are the scopes Norcube Auth granted the sign-in (format 2),
	// e.g. ["full", "org.read", "offline_access"].
	Scopes []string `json:"scopes,omitempty"`

	// legacy: read from an older CLI's per-auth-URL item, not yet saved
	// into the profile's own item.
	legacy bool
}

// cachedAccess is a cached access token. ExpiresAt is computed from the
// exchange's expires_in against the local clock at receipt, so a skewed
// local clock can't make a valid token look expired or the reverse.
type cachedAccess struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func profileName(profile string) string {
	if profile == "" {
		return config.DefaultProfileName
	}
	return profile
}

func profileKey(profile string) string { return "profile:" + profileName(profile) }

// legacyRefreshKey is where CLI versions before profiles kept the refresh
// token: one item per auth URL.
func legacyRefreshKey(api string) string { return api + "|refresh" }

// loadCredentials returns the profile's credentials for api. Missing or
// belonging to another auth URL means empty.
//
// The default profile falls back to the refresh token an older CLI
// version stored per auth URL. Loading never writes: the old item is read
// fresh every time until the session is actually used, so an older CLI
// that keeps rotating it can't leave a stale copy behind (presenting a
// refresh token twice ends the whole session). saveCredentials then moves
// it into the profile's item.
// credsMu serializes this process's access to the credential store. A
// profile's session is one record that SaveRefreshToken and
// SaveAccessTokenUntil read, modify and write back, so concurrent requests
// (to several services, or many to one) would otherwise lose each other's
// updates; stores aren't required to be safe for concurrent use either.
// Other processes are kept out by the refresh lock file (see
// TokenSource.lock). loadCredentials and saveCredentials expect the
// caller to hold credsMu.
var credsMu sync.RWMutex

func loadCredentials(profile, api string) (*credentials, error) {
	st := storeFor(profile)
	v, err := st.Get(profileKey(profile))
	switch {
	case err == nil:
		var c credentials
		if json.Unmarshal([]byte(v), &c) == nil && c.AuthURL == api {
			return &c, nil
		}
		return &credentials{AuthURL: api}, nil
	case !errors.Is(err, ErrSecretNotFound):
		return nil, err
	}
	if profileName(profile) == config.DefaultProfileName {
		if old, err := st.Get(legacyRefreshKey(api)); err == nil && old != "" {
			return &credentials{AuthURL: api, RefreshToken: old, legacy: true}, nil
		}
	}
	return &credentials{AuthURL: api}, nil
}

func (c *credentials) format() int {
	if c.Format == FormatOAuth {
		return FormatOAuth
	}
	return FormatLegacy
}

// saveCredentials writes the blob. A store that can't hold the cached
// access tokens (Windows caps items at 2.5 KB) gets the refresh token and
// scopes only; the next command mints new access tokens.
func saveCredentials(profile string, c *credentials) error {
	b, err := json.Marshal(c) // #nosec G117 -- written to the credential store, which is where secrets belong
	if err != nil {
		return err
	}
	st := storeFor(profile)
	err = st.Set(profileKey(profile), string(b))
	if errors.Is(err, credstore.ErrTooBig) && (len(c.Access) > 0 || c.CLIAccess != nil) {
		slim := *c
		slim.Access = nil
		slim.CLIAccess = nil
		b, _ = json.Marshal(slim) // #nosec G117 -- see above
		err = st.Set(profileKey(profile), string(b))
	}
	if err != nil {
		return err
	}
	if c.legacy {
		// Moved, not copied: the session now lives in the profile's
		// item only. Its old token was just rotated away, so an older
		// CLI still on this machine asks to sign in again instead of
		// replaying it.
		if derr := st.Delete(legacyRefreshKey(c.AuthURL)); derr != nil && !errors.Is(derr, ErrSecretNotFound) {
			return derr
		}
		c.legacy = false
	}
	return nil
}

// SaveRefreshToken stores the profile's refresh token for api as an older
// (format 1) sign-in. Cached access tokens of another auth URL are
// dropped, and so is an OAuth sign-in it replaces.
func SaveRefreshToken(profile, api, token string) error {
	credsMu.Lock()
	defer credsMu.Unlock()
	c, err := loadCredentials(profile, api)
	if err != nil {
		return err
	}
	if c.format() == FormatOAuth {
		c = &credentials{AuthURL: api, legacy: c.legacy}
		forgetMemory(profile, api)
	}
	c.RefreshToken = token
	return saveCredentials(profile, c)
}

// SaveOAuthSession stores an OAuth sign-in (format 2) for the profile:
// its refresh token, the CLI access token that came with it and the
// granted scopes. It replaces whatever the profile had, older sign-in
// included. Callers hold the profile's lock.
func SaveOAuthSession(profile, api, refresh, cliAccess string, accessExpiresAt time.Time, scopes []string) error {
	credsMu.Lock()
	defer credsMu.Unlock()
	old, err := loadCredentials(profile, api)
	if err != nil {
		return err
	}
	c := &credentials{
		Format:       FormatOAuth,
		AuthURL:      api,
		RefreshToken: refresh,
		Scopes:       scopes,
		legacy:       old.legacy,
	}
	if cliAccess != "" {
		if accessExpiresAt.IsZero() {
			accessExpiresAt = jwtExpiry(cliAccess)
		}
		c.CLIAccess = &cachedAccess{Token: cliAccess, ExpiresAt: accessExpiresAt}
	}
	forgetMemory(profile, api)
	return saveCredentials(profile, c)
}

// StoredSession describes what a profile has stored, without secrets.
type StoredSession struct {
	// Format is FormatLegacy or FormatOAuth; 0 when nothing is stored.
	Format int
	// Scopes are the granted scopes of an OAuth sign-in (nil for the
	// older sign-in, which always has full access).
	Scopes []string
	// CLIAccessExpiresAt is when the cached CLI access token expires
	// (format 2; zero when none is cached).
	CLIAccessExpiresAt time.Time
}

// LoadSession describes the profile's stored session for api.
func LoadSession(profile, api string) (StoredSession, error) {
	credsMu.RLock()
	defer credsMu.RUnlock()
	c, err := loadCredentials(profile, api)
	if err != nil || c.RefreshToken == "" {
		return StoredSession{}, err
	}
	s := StoredSession{Format: c.format(), Scopes: c.Scopes}
	if c.CLIAccess != nil {
		s.CLIAccessExpiresAt = c.CLIAccess.ExpiresAt
	}
	return s, nil
}

// LoadRefreshToken returns the profile's refresh token for api, or
// ErrNotLoggedIn.
func LoadRefreshToken(profile, api string) (string, error) {
	credsMu.RLock()
	defer credsMu.RUnlock()
	c, err := loadCredentials(profile, api)
	if err != nil {
		return "", err
	}
	if c.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	return c.RefreshToken, nil
}

func accessKey(audience, orgID string) string { return audience + "|" + orgID }

// SaveAccessTokenUntil caches token as valid until expiresAt. A zero
// expiresAt falls back to the token's exp claim.
func SaveAccessTokenUntil(profile, api, audience, orgID, token string, expiresAt time.Time) error {
	credsMu.Lock()
	defer credsMu.Unlock()
	if expiresAt.IsZero() {
		expiresAt = jwtExpiry(token)
	}
	c, err := loadCredentials(profile, api)
	if err != nil {
		return err
	}
	if c.Access == nil {
		c.Access = map[string]cachedAccess{}
	}
	now := time.Now()
	for k, a := range c.Access {
		if a.ExpiresAt.Before(now) {
			delete(c.Access, k)
		}
	}
	c.Access[accessKey(audience, orgID)] = cachedAccess{Token: token, ExpiresAt: expiresAt}
	return saveCredentials(profile, c)
}

// loadAccess returns the cached token and its expiry, or "" when none is
// cached.
func loadAccess(profile, api, audience, orgID string) (string, time.Time, error) {
	credsMu.RLock()
	defer credsMu.RUnlock()
	c, err := loadCredentials(profile, api)
	if err != nil {
		return "", time.Time{}, err
	}
	a, ok := c.Access[accessKey(audience, orgID)]
	if !ok || a.Token == "" {
		return "", time.Time{}, nil
	}
	return a.Token, a.ExpiresAt, nil
}

// DeleteCredentials removes everything stored for the profile, including
// the default profile's pre-profiles refresh token for api (so it isn't
// adopted again).
func DeleteCredentials(profile, api string) error {
	return DeleteCredentialsFrom(storeFor(profile), profile, api)
}

// DeleteCredentialsFrom is DeleteCredentials on a given store.
func DeleteCredentialsFrom(s Store, profile, api string) error {
	credsMu.Lock()
	defer credsMu.Unlock()
	forgetMemory(profile, api)
	if err := s.Delete(profileKey(profile)); err != nil {
		return err
	}
	if profileName(profile) == config.DefaultProfileName && api != "" {
		return s.Delete(legacyRefreshKey(api))
	}
	return nil
}

// HasCredentials reports whether the profile has a session for api.
func HasCredentials(profile, api string) bool {
	t, err := LoadRefreshToken(profile, api)
	return err == nil && t != ""
}

// AccessExpiry returns when the profile's cached token for (audience,
// org) expires; zero when none is cached.
func AccessExpiry(profile, api, audience, orgID string) time.Time {
	if a, ok := memGet(memKey(profile, api, audience, orgID)); ok {
		return a.ExpiresAt
	}
	_, exp, _ := loadAccess(profile, api, audience, orgID)
	return exp
}

// ErrNotLoggedIn: the profile has no session at all. It also matches
// ErrLoginRequired, so callers that only care about "a login fixes it"
// need one check.
var ErrNotLoggedIn error = notLoggedIn{}

type notLoggedIn struct{}

func (notLoggedIn) Error() string        { return "not signed in" }
func (notLoggedIn) Is(target error) bool { return target == ErrLoginRequired }
